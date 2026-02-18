package httpserver

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

const (
	sessionCookieName = "mixpost_session"
	sessionTTL        = 30 * 24 * time.Hour
	tokenPrefix       = "mpat_"
)

type Dependencies struct {
	Postgres *pgxpool.Pool
	Redis    *redis.Client
}

type apiServer struct {
	serviceName string
	deps        Dependencies
}

type authenticatedUser struct {
	ID    int64     `json:"id"`
	UUID  string    `json:"uuid"`
	Name  string    `json:"name"`
	Email string    `json:"email"`
	Since time.Time `json:"since"`
}

type userHandler func(http.ResponseWriter, *http.Request, authenticatedUser)

type statusResponse struct {
	Status  string            `json:"status"`
	Checks  map[string]string `json:"checks,omitempty"`
	Service string            `json:"service,omitempty"`
}

func New(addr, serviceName string, deps Dependencies) *http.Server {
	s := &apiServer{serviceName: serviceName, deps: deps}

	mux := http.NewServeMux()
	s.registerHealthRoutes(mux)
	s.registerAuthRoutes(mux)
	s.registerAPIRoutes(mux)

	return &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
}

func (s *apiServer) registerHealthRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /health/liveness", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, statusResponse{Status: "ok", Service: s.serviceName})
	})

	mux.HandleFunc("GET /health/readiness", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()

		checks := map[string]string{}
		hasFailure := false

		if s.deps.Postgres != nil {
			if err := s.deps.Postgres.Ping(ctx); err != nil {
				checks["postgres"] = "down"
				hasFailure = true
			} else {
				checks["postgres"] = "up"
			}
		}

		if s.deps.Redis != nil {
			if err := s.deps.Redis.Ping(ctx).Err(); err != nil {
				checks["redis"] = "down"
				hasFailure = true
			} else {
				checks["redis"] = "up"
			}
		}

		if hasFailure {
			writeJSON(w, http.StatusServiceUnavailable, statusResponse{Status: "degraded", Checks: checks, Service: s.serviceName})
			return
		}

		writeJSON(w, http.StatusOK, statusResponse{Status: "ok", Checks: checks, Service: s.serviceName})
	})
}

func (s *apiServer) registerAuthRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /auth/register", s.handleRegister)
	mux.HandleFunc("POST /auth/login", s.handleLogin)
	mux.HandleFunc("POST /auth/logout", s.requireSession(s.handleLogout))
	mux.HandleFunc("GET /auth/me", s.requireSession(s.handleMe))
	mux.HandleFunc("GET /auth/api-tokens", s.requireSession(s.handleListAPITokens))
	mux.HandleFunc("POST /auth/api-tokens", s.requireSession(s.handleCreateAPIToken))
	mux.HandleFunc("DELETE /auth/api-tokens/{id}", s.requireSession(s.handleDeleteAPIToken))
}

func (s *apiServer) registerAPIRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/services", s.requireAPIToken(s.handleListServices))
	mux.HandleFunc("POST /api/v1/services", s.requireAPIToken(s.handleCreateService))
	mux.HandleFunc("PUT /api/v1/services/{id}", s.requireAPIToken(s.handleUpdateService))
	mux.HandleFunc("DELETE /api/v1/services/{id}", s.requireAPIToken(s.handleDeleteService))

	mux.HandleFunc("GET /api/v1/accounts", s.requireAPIToken(s.handleListAccounts))
	mux.HandleFunc("POST /api/v1/accounts", s.requireAPIToken(s.handleCreateAccount))
	mux.HandleFunc("PUT /api/v1/accounts/{id}", s.requireAPIToken(s.handleUpdateAccount))
	mux.HandleFunc("DELETE /api/v1/accounts/{id}", s.requireAPIToken(s.handleDeleteAccount))

	mux.HandleFunc("GET /api/v1/facebook/oauth/start", s.requireAPIToken(s.handleStartFacebookOAuth))
	mux.HandleFunc("GET /api/v1/facebook/oauth/callback", s.handleFacebookOAuthCallback)
	mux.HandleFunc("POST /api/v1/facebook/pages/import", s.requireAPIToken(s.handleImportFacebookPages))

	mux.HandleFunc("GET /api/v1/posts", s.requireAPIToken(s.handleListPosts))
	mux.HandleFunc("GET /api/v1/posts/{id}", s.requireAPIToken(s.handleGetPost))
	mux.HandleFunc("POST /api/v1/posts", s.requireAPIToken(s.handleCreatePost))
	mux.HandleFunc("PUT /api/v1/posts/{id}", s.requireAPIToken(s.handleUpdatePost))
	mux.HandleFunc("DELETE /api/v1/posts/{id}", s.requireAPIToken(s.handleDeletePost))
}

func (s *apiServer) requireSession(next userHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, err := s.userFromSession(r)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next(w, r, user)
	}
}

func (s *apiServer) requireAPIToken(next userHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		authHeader := strings.TrimSpace(r.Header.Get("Authorization"))
		if authHeader == "" {
			writeError(w, http.StatusUnauthorized, "missing Authorization header")
			return
		}

		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			writeError(w, http.StatusUnauthorized, "invalid Authorization header")
			return
		}

		tokenHash := hashToken(parts[1])
		user, err := s.userFromAPITokenHash(r.Context(), tokenHash)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}

		next(w, r, user)
	}
}

func (s *apiServer) userFromSession(r *http.Request) (authenticatedUser, error) {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil {
		return authenticatedUser{}, err
	}

	tokenHash := hashToken(cookie.Value)
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()

	var user authenticatedUser
	row := s.deps.Postgres.QueryRow(ctx, `
SELECT u.id, u.uuid::text, u.name, u.email, u.created_at
FROM user_sessions us
JOIN users u ON u.id = us.user_id
WHERE us.token_hash = $1
  AND us.expires_at > NOW()
`, tokenHash)

	if err := row.Scan(&user.ID, &user.UUID, &user.Name, &user.Email, &user.Since); err != nil {
		return authenticatedUser{}, err
	}

	_, _ = s.deps.Postgres.Exec(ctx, `
UPDATE user_sessions
SET last_seen_at = NOW()
WHERE token_hash = $1
`, tokenHash)

	return user, nil
}

func (s *apiServer) userFromAPITokenHash(ctx context.Context, tokenHash string) (authenticatedUser, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	var user authenticatedUser
	row := s.deps.Postgres.QueryRow(ctx, `
SELECT u.id, u.uuid::text, u.name, u.email, u.created_at
FROM api_tokens t
JOIN users u ON u.id = t.user_id
WHERE t.token_hash = $1
  AND (t.expires_at IS NULL OR t.expires_at > NOW())
`, tokenHash)

	if err := row.Scan(&user.ID, &user.UUID, &user.Name, &user.Email, &user.Since); err != nil {
		return authenticatedUser{}, err
	}

	_, _ = s.deps.Postgres.Exec(ctx, `
UPDATE api_tokens
SET last_used_at = NOW(), updated_at = NOW()
WHERE token_hash = $1
`, tokenHash)

	return user, nil
}

func writeJSON(w http.ResponseWriter, statusCode int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, statusCode int, message string) {
	writeJSON(w, statusCode, map[string]string{"error": message})
}

func decodeJSON(r *http.Request, dst any) error {
	defer r.Body.Close()
	decoder := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return err
	}
	if decoder.More() {
		return errors.New("request body must contain a single JSON object")
	}
	return nil
}

func parseIDFromPath(r *http.Request) (int64, error) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		return 0, errors.New("missing id")
	}
	parsed, err := strconv.ParseInt(id, 10, 64)
	if err != nil || parsed <= 0 {
		return 0, errors.New("invalid id")
	}
	return parsed, nil
}

func generateOpaqueToken() (string, error) {
	buffer := make([]byte, 32)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("generate random token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buffer), nil
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func requestIP(r *http.Request) string {
	if xff := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); xff != "" {
		parts := strings.Split(xff, ",")
		if len(parts) > 0 {
			return strings.TrimSpace(parts[0])
		}
	}

	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func isDuplicateError(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "23505"
	}
	return false
}
