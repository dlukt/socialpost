package httpserver

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"
)

type authUserResponse struct {
	ID    int64     `json:"id"`
	UUID  string    `json:"uuid"`
	Name  string    `json:"name"`
	Email string    `json:"email"`
	Since time.Time `json:"since"`
}

type registerRequest struct {
	Name     string `json:"name"`
	Email    string `json:"email"`
	Password string `json:"password"`
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type apiTokenRequest struct {
	Name          string `json:"name"`
	ExpiresInDays *int   `json:"expires_in_days,omitempty"`
}

type apiTokenResponse struct {
	ID         int64      `json:"id"`
	Name       string     `json:"name"`
	Token      string     `json:"token,omitempty"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
}

func (s *apiServer) handleRegister(w http.ResponseWriter, r *http.Request) {
	var req registerRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Name == "" || req.Email == "" || len(req.Password) < 8 {
		writeError(w, http.StatusBadRequest, "name, email and password (min 8 chars) are required")
		return
	}

	passwordHash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to hash password")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var user authenticatedUser
	err = s.deps.Postgres.QueryRow(ctx, `
INSERT INTO users (name, email, password_hash)
VALUES ($1, $2, $3)
RETURNING id, uuid::text, name, email, created_at
`, req.Name, req.Email, string(passwordHash)).Scan(
		&user.ID,
		&user.UUID,
		&user.Name,
		&user.Email,
		&user.Since,
	)
	if err != nil {
		if isDuplicateError(err) {
			writeError(w, http.StatusConflict, "email already exists")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to create user")
		return
	}

	if err := s.createSession(w, r, user.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create session")
		return
	}

	writeJSON(w, http.StatusCreated, authUserResponse(user))
}

func (s *apiServer) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Email == "" || req.Password == "" {
		writeError(w, http.StatusBadRequest, "email and password are required")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var (
		user         authenticatedUser
		passwordHash string
	)
	err := s.deps.Postgres.QueryRow(ctx, `
SELECT id, uuid::text, name, email, created_at, password_hash
FROM users
WHERE email = $1
`, req.Email).Scan(
		&user.ID,
		&user.UUID,
		&user.Name,
		&user.Email,
		&user.Since,
		&passwordHash,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusUnauthorized, "invalid credentials")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to authenticate")
		return
	}

	if err := bcrypt.CompareHashAndPassword([]byte(passwordHash), []byte(req.Password)); err != nil {
		writeError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}

	if err := s.createSession(w, r, user.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create session")
		return
	}

	writeJSON(w, http.StatusOK, authUserResponse(user))
}

func (s *apiServer) handleLogout(w http.ResponseWriter, r *http.Request, _ authenticatedUser) {
	cookie, err := r.Cookie(sessionCookieName)
	if err == nil {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		_, _ = s.deps.Postgres.Exec(ctx, `DELETE FROM user_sessions WHERE token_hash = $1`, hashToken(cookie.Value))
	}

	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		MaxAge:   -1,
		SameSite: http.SameSiteLaxMode,
	})

	w.WriteHeader(http.StatusNoContent)
}

func (s *apiServer) handleMe(w http.ResponseWriter, _ *http.Request, user authenticatedUser) {
	writeJSON(w, http.StatusOK, authUserResponse(user))
}

func (s *apiServer) handleCreateAPIToken(w http.ResponseWriter, r *http.Request, user authenticatedUser) {
	var req apiTokenRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}

	random, err := generateOpaqueToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to generate token")
		return
	}
	plainToken := tokenPrefix + random

	var expiresAt *time.Time
	if req.ExpiresInDays != nil {
		if *req.ExpiresInDays <= 0 {
			writeError(w, http.StatusBadRequest, "expires_in_days must be greater than 0")
			return
		}
		t := time.Now().UTC().Add(time.Duration(*req.ExpiresInDays) * 24 * time.Hour)
		expiresAt = &t
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var response apiTokenResponse
	err = s.deps.Postgres.QueryRow(ctx, `
INSERT INTO api_tokens (user_id, name, token_hash, expires_at)
VALUES ($1, $2, $3, $4)
RETURNING id, name, expires_at, created_at
`, user.ID, req.Name, hashToken(plainToken), expiresAt).Scan(
		&response.ID,
		&response.Name,
		&response.ExpiresAt,
		&response.CreatedAt,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create api token")
		return
	}

	response.Token = plainToken
	writeJSON(w, http.StatusCreated, response)
}

func (s *apiServer) handleListAPITokens(w http.ResponseWriter, r *http.Request, user authenticatedUser) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	rows, err := s.deps.Postgres.Query(ctx, `
SELECT id, name, last_used_at, expires_at, created_at
FROM api_tokens
WHERE user_id = $1
ORDER BY id DESC
`, user.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list api tokens")
		return
	}
	defer rows.Close()

	response := make([]apiTokenResponse, 0)
	for rows.Next() {
		var item apiTokenResponse
		if err := rows.Scan(&item.ID, &item.Name, &item.LastUsedAt, &item.ExpiresAt, &item.CreatedAt); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to scan api token")
			return
		}
		response = append(response, item)
	}

	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to iterate api tokens")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"data": response})
}

func (s *apiServer) handleDeleteAPIToken(w http.ResponseWriter, r *http.Request, user authenticatedUser) {
	id, err := parseIDFromPath(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid api token id")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	tag, err := s.deps.Postgres.Exec(ctx, `
DELETE FROM api_tokens
WHERE id = $1 AND user_id = $2
`, id, user.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete api token")
		return
	}

	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "api token not found")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (s *apiServer) createSession(w http.ResponseWriter, r *http.Request, userID int64) error {
	plainToken, err := generateOpaqueToken()
	if err != nil {
		return err
	}

	expiresAt := time.Now().UTC().Add(sessionTTL)
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	_, err = s.deps.Postgres.Exec(ctx, `
INSERT INTO user_sessions (user_id, token_hash, user_agent, ip, expires_at)
VALUES ($1, $2, $3, $4, $5)
`, userID, hashToken(plainToken), r.UserAgent(), requestIP(r), expiresAt)
	if err != nil {
		return err
	}

	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    plainToken,
		Path:     "/",
		HttpOnly: true,
		Secure:   false,
		SameSite: http.SameSiteLaxMode,
		Expires:  expiresAt,
	})

	return nil
}
