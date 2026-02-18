package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/dlukt/socialpost/internal/providers"
	"github.com/jackc/pgx/v5"
)

const (
	facebookProviderName       = "facebook"
	facebookOAuthStateTTL      = 10 * time.Minute
	facebookOAuthDefaultScopes = "pages_show_list,pages_manage_posts,pages_read_engagement"
)

var (
	errOAuthStateInvalid = errors.New("invalid oauth state")
	errOAuthStateExpired = errors.New("oauth state expired")
)

type facebookImportPagesRequest struct {
	UserAccessToken      string `json:"user_access_token"`
	ExchangeForLongLived *bool  `json:"exchange_for_long_lived,omitempty"`
}

type facebookOAuthStartResponse struct {
	AuthorizationURL string    `json:"authorization_url"`
	State            string    `json:"state"`
	RedirectURI      string    `json:"redirect_uri"`
	ExpiresAt        time.Time `json:"expires_at"`
}

type facebookOAuthCallbackResponse struct {
	Accounts      []accountResponse `json:"accounts"`
	PagesFound    int               `json:"pages_found"`
	ImportedCount int               `json:"imported_count"`
	SkippedPages  []string          `json:"skipped_pages,omitempty"`
}

type facebookImportPagesResponse struct {
	Accounts       []accountResponse `json:"accounts"`
	PagesFound     int               `json:"pages_found"`
	ImportedCount  int               `json:"imported_count"`
	SkippedPages   []string          `json:"skipped_pages,omitempty"`
	ExchangedToken bool              `json:"exchanged_token"`
}

type oauthStateRecord struct {
	ID          int64
	UserID      int64
	RedirectURI string
	ExpiresAt   time.Time
}

func (s *apiServer) handleStartFacebookOAuth(w http.ResponseWriter, r *http.Request, user authenticatedUser) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	serviceConfig, err := s.loadActiveServiceConfiguration(ctx, facebookProviderName)
	if err != nil {
		switch {
		case errors.Is(err, providers.ErrServiceNotConfigured), errors.Is(err, providers.ErrServiceDisabled):
			writeError(w, http.StatusUnprocessableEntity, err.Error())
		default:
			writeError(w, http.StatusInternalServerError, "failed to load facebook service configuration")
		}
		return
	}

	if err := providers.ValidateServiceConfiguration(facebookProviderName, serviceConfig, true); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	clientID := lookupStringFromMap(serviceConfig, "client_id")
	if clientID == "" {
		writeError(w, http.StatusUnprocessableEntity, "facebook configuration invalid: client_id is required")
		return
	}

	apiVersion := providers.ResolveFacebookAPIVersion(serviceConfig)
	redirectURI := lookupStringFromMap(serviceConfig, "redirect_uri")
	if !isValidAbsoluteHTTPURL(redirectURI) {
		writeError(w, http.StatusUnprocessableEntity, "facebook configuration invalid: redirect_uri is required and must be an absolute http/https URL")
		return
	}

	state, expiresAt, err := s.createOAuthState(ctx, user.ID, facebookProviderName, redirectURI)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create oauth state")
		return
	}

	authorizationURL, err := buildFacebookAuthorizationURL(apiVersion, clientID, redirectURI, state, facebookOAuthDefaultScopes)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to build facebook authorization url")
		return
	}

	writeJSON(w, http.StatusOK, facebookOAuthStartResponse{
		AuthorizationURL: authorizationURL,
		State:            state,
		RedirectURI:      redirectURI,
		ExpiresAt:        expiresAt,
	})
}

func (s *apiServer) handleFacebookOAuthCallback(w http.ResponseWriter, r *http.Request) {
	facebookError := strings.TrimSpace(r.URL.Query().Get("error"))
	if facebookError != "" {
		description := strings.TrimSpace(r.URL.Query().Get("error_description"))
		if description == "" {
			description = facebookError
		}
		writeError(w, http.StatusBadRequest, "facebook authorization rejected: "+description)
		return
	}

	state := strings.TrimSpace(r.URL.Query().Get("state"))
	code := strings.TrimSpace(r.URL.Query().Get("code"))
	if state == "" || code == "" {
		writeError(w, http.StatusBadRequest, "state and code are required")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	stateRecord, err := s.consumeOAuthState(ctx, facebookProviderName, state)
	if err != nil {
		switch {
		case errors.Is(err, errOAuthStateInvalid), errors.Is(err, errOAuthStateExpired):
			writeError(w, http.StatusBadRequest, "oauth state is invalid or expired")
		default:
			writeError(w, http.StatusInternalServerError, "failed to validate oauth state")
		}
		return
	}

	serviceConfig, err := s.loadActiveServiceConfiguration(ctx, facebookProviderName)
	if err != nil {
		switch {
		case errors.Is(err, providers.ErrServiceNotConfigured), errors.Is(err, providers.ErrServiceDisabled):
			writeError(w, http.StatusUnprocessableEntity, err.Error())
		default:
			writeError(w, http.StatusInternalServerError, "failed to load facebook service configuration")
		}
		return
	}

	if err := providers.ValidateServiceConfiguration(facebookProviderName, serviceConfig, true); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	apiVersion := providers.ResolveFacebookAPIVersion(serviceConfig)
	clientID := lookupStringFromMap(serviceConfig, "client_id")
	clientSecret := lookupStringFromMap(serviceConfig, "client_secret")

	graphClient := providers.NewFacebookGraphClient()
	shortLivedToken, err := graphClient.ExchangeAuthorizationCode(ctx, apiVersion, clientID, clientSecret, stateRecord.RedirectURI, code)
	if err != nil {
		s.writeFacebookProviderError(w, err)
		return
	}

	userAccessToken, err := graphClient.ExchangeUserAccessToken(ctx, apiVersion, clientID, clientSecret, shortLivedToken)
	if err != nil {
		s.writeFacebookProviderError(w, err)
		return
	}

	pages, err := graphClient.ListManagedPages(ctx, apiVersion, userAccessToken)
	if err != nil {
		s.writeFacebookProviderError(w, err)
		return
	}

	tx, err := s.deps.Postgres.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start transaction")
		return
	}
	defer tx.Rollback(ctx)

	imported, skipped, err := importFacebookPagesTx(ctx, tx, pages, apiVersion)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to import facebook pages")
		return
	}

	if err := tx.Commit(ctx); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit facebook page import")
		return
	}

	writeJSON(w, http.StatusOK, facebookOAuthCallbackResponse{
		Accounts:      imported,
		PagesFound:    len(pages),
		ImportedCount: len(imported),
		SkippedPages:  skipped,
	})
}

func (s *apiServer) handleImportFacebookPages(w http.ResponseWriter, r *http.Request, _ authenticatedUser) {
	var req facebookImportPagesRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	req.UserAccessToken = strings.TrimSpace(req.UserAccessToken)
	if req.UserAccessToken == "" {
		writeError(w, http.StatusBadRequest, "user_access_token is required")
		return
	}

	exchangeForLongLived := true
	if req.ExchangeForLongLived != nil {
		exchangeForLongLived = *req.ExchangeForLongLived
	}

	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	serviceConfig, err := s.loadActiveServiceConfiguration(ctx, facebookProviderName)
	if err != nil {
		switch {
		case errors.Is(err, providers.ErrServiceNotConfigured), errors.Is(err, providers.ErrServiceDisabled):
			writeError(w, http.StatusUnprocessableEntity, err.Error())
		default:
			writeError(w, http.StatusInternalServerError, "failed to load facebook service configuration")
		}
		return
	}

	if err := providers.ValidateServiceConfiguration(facebookProviderName, serviceConfig, true); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	apiVersion := providers.ResolveFacebookAPIVersion(serviceConfig)
	clientID := lookupStringFromMap(serviceConfig, "client_id")
	clientSecret := lookupStringFromMap(serviceConfig, "client_secret")

	graphClient := providers.NewFacebookGraphClient()
	userToken := req.UserAccessToken
	exchanged := false

	if exchangeForLongLived {
		longLivedToken, err := graphClient.ExchangeUserAccessToken(ctx, apiVersion, clientID, clientSecret, userToken)
		if err != nil {
			s.writeFacebookProviderError(w, err)
			return
		}
		userToken = longLivedToken
		exchanged = true
	}

	pages, err := graphClient.ListManagedPages(ctx, apiVersion, userToken)
	if err != nil {
		s.writeFacebookProviderError(w, err)
		return
	}

	tx, err := s.deps.Postgres.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start transaction")
		return
	}
	defer tx.Rollback(ctx)

	imported, skipped, err := importFacebookPagesTx(ctx, tx, pages, apiVersion)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to import facebook pages")
		return
	}

	if err := tx.Commit(ctx); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit facebook page import")
		return
	}

	writeJSON(w, http.StatusOK, facebookImportPagesResponse{
		Accounts:       imported,
		PagesFound:     len(pages),
		ImportedCount:  len(imported),
		SkippedPages:   skipped,
		ExchangedToken: exchanged,
	})
}

func importFacebookPagesTx(ctx context.Context, tx pgx.Tx, pages []providers.FacebookPage, apiVersion string) ([]accountResponse, []string, error) {
	imported := make([]accountResponse, 0, len(pages))
	skipped := make([]string, 0)

	for _, page := range pages {
		pageID := strings.TrimSpace(page.ID)
		pageToken := strings.TrimSpace(page.AccessToken)
		if pageID == "" {
			skipped = append(skipped, strings.TrimSpace(page.Name))
			continue
		}
		if pageToken == "" {
			skipped = append(skipped, pageID)
			continue
		}

		item, err := upsertFacebookPageAccountTx(ctx, tx, page, apiVersion, pageToken)
		if err != nil {
			return nil, nil, err
		}
		imported = append(imported, item)
	}

	return imported, skipped, nil
}

func upsertFacebookPageAccountTx(ctx context.Context, tx pgx.Tx, page providers.FacebookPage, apiVersion, pageToken string) (accountResponse, error) {
	name := strings.TrimSpace(page.Name)
	if name == "" {
		name = strings.TrimSpace(page.ID)
	}

	dataMap := map[string]any{}
	if category := strings.TrimSpace(page.Category); category != "" {
		dataMap["category"] = category
	}
	if username := strings.TrimSpace(page.Username); username != "" {
		dataMap["username"] = username
	}
	dataJSON, err := json.Marshal(dataMap)
	if err != nil {
		return accountResponse{}, err
	}

	tokenMap := map[string]any{
		"page_access_token": pageToken,
		"api_version":       apiVersion,
	}
	tokenJSON, err := json.Marshal(tokenMap)
	if err != nil {
		return accountResponse{}, err
	}

	row := tx.QueryRow(ctx, `
INSERT INTO accounts (name, username, provider, provider_id, data, authorized, access_token)
VALUES ($1, $2, $3, $4, $5, TRUE, $6)
ON CONFLICT (provider, provider_id)
DO UPDATE SET
    name = EXCLUDED.name,
    username = EXCLUDED.username,
    data = EXCLUDED.data,
    authorized = TRUE,
    access_token = EXCLUDED.access_token,
    updated_at = NOW()
RETURNING id, uuid::text, name, username, media, provider, provider_id, data, authorized, created_at, updated_at
`, name, nullableString(page.Username), "facebook_page", page.ID, dataJSON, tokenJSON)

	return scanAccount(row)
}

func nullableString(value string) *string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

func (s *apiServer) loadActiveServiceConfiguration(ctx context.Context, serviceName string) (map[string]any, error) {
	var (
		configurationRaw []byte
		active           bool
	)

	err := s.deps.Postgres.QueryRow(ctx, `
SELECT configuration, active
FROM services
WHERE name = $1
LIMIT 1
`, strings.ToLower(strings.TrimSpace(serviceName))).Scan(&configurationRaw, &active)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("%w: %s", providers.ErrServiceNotConfigured, serviceName)
		}
		return nil, err
	}

	if !active {
		return nil, fmt.Errorf("%w: %s", providers.ErrServiceDisabled, serviceName)
	}

	configuration := map[string]any{}
	if len(configurationRaw) > 0 {
		if err := json.Unmarshal(configurationRaw, &configuration); err != nil {
			return nil, err
		}
	}

	return configuration, nil
}

func lookupStringFromMap(values map[string]any, key string) string {
	if values == nil {
		return ""
	}
	item, ok := values[key]
	if !ok {
		return ""
	}
	asString, ok := item.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(asString)
}

func (s *apiServer) writeFacebookProviderError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, providers.ErrUnauthorized):
		writeError(w, http.StatusUnauthorized, "facebook authorization failed")
	case errors.Is(err, context.DeadlineExceeded):
		writeError(w, http.StatusGatewayTimeout, "facebook request timed out")
	default:
		writeError(w, http.StatusBadGateway, "facebook request failed")
	}
}

func (s *apiServer) createOAuthState(ctx context.Context, userID int64, provider, redirectURI string) (string, time.Time, error) {
	expiresAt := time.Now().UTC().Add(facebookOAuthStateTTL)

	for i := 0; i < 3; i++ {
		state, err := generateOpaqueToken()
		if err != nil {
			return "", time.Time{}, err
		}

		_, err = s.deps.Postgres.Exec(ctx, `
INSERT INTO oauth_states (user_id, provider, state, redirect_uri, expires_at)
VALUES ($1, $2, $3, $4, $5)
`, userID, provider, state, redirectURI, expiresAt)
		if err == nil {
			return state, expiresAt, nil
		}
		if !isDuplicateError(err) {
			return "", time.Time{}, err
		}
	}

	return "", time.Time{}, fmt.Errorf("failed to generate unique oauth state")
}

func (s *apiServer) consumeOAuthState(ctx context.Context, provider, state string) (oauthStateRecord, error) {
	tx, err := s.deps.Postgres.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return oauthStateRecord{}, err
	}
	defer tx.Rollback(ctx)

	var record oauthStateRecord
	err = tx.QueryRow(ctx, `
SELECT id, user_id, redirect_uri, expires_at
FROM oauth_states
WHERE provider = $1
  AND state = $2
  AND consumed_at IS NULL
FOR UPDATE
`, provider, state).Scan(&record.ID, &record.UserID, &record.RedirectURI, &record.ExpiresAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return oauthStateRecord{}, errOAuthStateInvalid
		}
		return oauthStateRecord{}, err
	}

	if !record.ExpiresAt.After(time.Now().UTC()) {
		if _, err := tx.Exec(ctx, `
UPDATE oauth_states
SET consumed_at = NOW()
WHERE id = $1
`, record.ID); err != nil {
			return oauthStateRecord{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return oauthStateRecord{}, err
		}
		return oauthStateRecord{}, errOAuthStateExpired
	}

	if _, err := tx.Exec(ctx, `
UPDATE oauth_states
SET consumed_at = NOW()
WHERE id = $1
`, record.ID); err != nil {
		return oauthStateRecord{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return oauthStateRecord{}, err
	}

	return record, nil
}

func buildFacebookAuthorizationURL(apiVersion, clientID, redirectURI, state, scope string) (string, error) {
	version := strings.TrimSpace(apiVersion)
	if version == "" {
		version = "v21.0"
	}

	authURL := &url.URL{
		Scheme: "https",
		Host:   "www.facebook.com",
		Path:   "/" + version + "/dialog/oauth",
	}

	query := authURL.Query()
	query.Set("client_id", strings.TrimSpace(clientID))
	query.Set("redirect_uri", strings.TrimSpace(redirectURI))
	query.Set("state", strings.TrimSpace(state))
	query.Set("scope", strings.TrimSpace(scope))
	query.Set("response_type", "code")
	authURL.RawQuery = query.Encode()

	return authURL.String(), nil
}

func isValidAbsoluteHTTPURL(value string) bool {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil {
		return false
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return false
	}
	return parsed.Host != ""
}
