package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/dlukt/socialpost/internal/providers"
	"github.com/jackc/pgx/v5"
)

type accountRequest struct {
	Name        string         `json:"name"`
	Username    *string        `json:"username,omitempty"`
	Media       map[string]any `json:"media,omitempty"`
	Provider    string         `json:"provider"`
	ProviderID  string         `json:"provider_id"`
	Data        map[string]any `json:"data,omitempty"`
	Authorized  bool           `json:"authorized"`
	AccessToken map[string]any `json:"access_token,omitempty"`
}

type accountResponse struct {
	ID         int64          `json:"id"`
	UUID       string         `json:"uuid"`
	Name       string         `json:"name"`
	Username   *string        `json:"username,omitempty"`
	Media      map[string]any `json:"media,omitempty"`
	Provider   string         `json:"provider"`
	ProviderID string         `json:"provider_id"`
	Data       map[string]any `json:"data,omitempty"`
	Authorized bool           `json:"authorized"`
	CreatedAt  time.Time      `json:"created_at"`
	UpdatedAt  time.Time      `json:"updated_at"`
}

func (s *apiServer) handleListAccounts(w http.ResponseWriter, r *http.Request, _ authenticatedUser) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	rows, err := s.deps.Postgres.Query(ctx, `
SELECT id, uuid::text, name, username, media, provider, provider_id, data, authorized, created_at, updated_at
FROM accounts
ORDER BY id DESC
`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list accounts")
		return
	}
	defer rows.Close()

	response := make([]accountResponse, 0)
	for rows.Next() {
		item, err := scanAccount(rows)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to scan account")
			return
		}
		response = append(response, item)
	}

	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to iterate accounts")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"data": response})
}

func (s *apiServer) handleCreateAccount(w http.ResponseWriter, r *http.Request, _ authenticatedUser) {
	var req accountRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	req.Name = strings.TrimSpace(req.Name)
	req.Provider = strings.ToLower(strings.TrimSpace(req.Provider))
	req.ProviderID = strings.TrimSpace(req.ProviderID)

	if req.Name == "" || req.Provider == "" || req.ProviderID == "" {
		writeError(w, http.StatusBadRequest, "name, provider and provider_id are required")
		return
	}

	if !providers.KnownProvider(req.Provider) {
		writeError(w, http.StatusBadRequest, "unsupported provider")
		return
	}

	if req.Authorized && len(req.AccessToken) == 0 {
		writeError(w, http.StatusBadRequest, "access_token is required when authorized is true")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	if err := s.ensureProviderServiceReady(ctx, req.Provider); err != nil {
		switch {
		case errors.Is(err, providers.ErrServiceNotConfigured):
			writeError(w, http.StatusUnprocessableEntity, err.Error())
		case errors.Is(err, providers.ErrServiceDisabled):
			writeError(w, http.StatusUnprocessableEntity, err.Error())
		default:
			writeError(w, http.StatusInternalServerError, "failed to validate provider service")
		}
		return
	}

	mediaJSON, err := marshalJSONNullable(req.Media)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid media")
		return
	}
	dataJSON, err := marshalJSONNullable(req.Data)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid data")
		return
	}
	accessTokenJSON, err := json.Marshal(emptyMapIfNil(req.AccessToken))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid access_token")
		return
	}

	var item accountResponse
	var mediaRaw, dataRaw []byte
	err = s.deps.Postgres.QueryRow(ctx, `
INSERT INTO accounts (name, username, media, provider, provider_id, data, authorized, access_token)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING id, uuid::text, name, username, media, provider, provider_id, data, authorized, created_at, updated_at
`, req.Name, req.Username, mediaJSON, req.Provider, req.ProviderID, dataJSON, req.Authorized, accessTokenJSON).Scan(
		&item.ID,
		&item.UUID,
		&item.Name,
		&item.Username,
		&mediaRaw,
		&item.Provider,
		&item.ProviderID,
		&dataRaw,
		&item.Authorized,
		&item.CreatedAt,
		&item.UpdatedAt,
	)
	if err != nil {
		if isDuplicateError(err) {
			writeError(w, http.StatusConflict, "account already exists for this provider")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to create account")
		return
	}

	if err := unmarshalJSONNullable(mediaRaw, &item.Media); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to decode media")
		return
	}
	if err := unmarshalJSONNullable(dataRaw, &item.Data); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to decode data")
		return
	}

	writeJSON(w, http.StatusCreated, item)
}

func (s *apiServer) handleUpdateAccount(w http.ResponseWriter, r *http.Request, _ authenticatedUser) {
	id, err := parseIDFromPath(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid account id")
		return
	}

	var req accountRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	req.Name = strings.TrimSpace(req.Name)
	req.Provider = strings.ToLower(strings.TrimSpace(req.Provider))
	req.ProviderID = strings.TrimSpace(req.ProviderID)

	if req.Name == "" || req.Provider == "" || req.ProviderID == "" {
		writeError(w, http.StatusBadRequest, "name, provider and provider_id are required")
		return
	}

	if !providers.KnownProvider(req.Provider) {
		writeError(w, http.StatusBadRequest, "unsupported provider")
		return
	}

	if req.Authorized && len(req.AccessToken) == 0 {
		writeError(w, http.StatusBadRequest, "access_token is required when authorized is true")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	if err := s.ensureProviderServiceReady(ctx, req.Provider); err != nil {
		switch {
		case errors.Is(err, providers.ErrServiceNotConfigured):
			writeError(w, http.StatusUnprocessableEntity, err.Error())
		case errors.Is(err, providers.ErrServiceDisabled):
			writeError(w, http.StatusUnprocessableEntity, err.Error())
		default:
			writeError(w, http.StatusInternalServerError, "failed to validate provider service")
		}
		return
	}

	mediaJSON, err := marshalJSONNullable(req.Media)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid media")
		return
	}
	dataJSON, err := marshalJSONNullable(req.Data)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid data")
		return
	}
	accessTokenJSON, err := json.Marshal(emptyMapIfNil(req.AccessToken))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid access_token")
		return
	}

	var item accountResponse
	var mediaRaw, dataRaw []byte
	err = s.deps.Postgres.QueryRow(ctx, `
UPDATE accounts
SET name = $2,
    username = $3,
    media = $4,
    provider = $5,
    provider_id = $6,
    data = $7,
    authorized = $8,
    access_token = $9,
    updated_at = NOW()
WHERE id = $1
RETURNING id, uuid::text, name, username, media, provider, provider_id, data, authorized, created_at, updated_at
`, id, req.Name, req.Username, mediaJSON, req.Provider, req.ProviderID, dataJSON, req.Authorized, accessTokenJSON).Scan(
		&item.ID,
		&item.UUID,
		&item.Name,
		&item.Username,
		&mediaRaw,
		&item.Provider,
		&item.ProviderID,
		&dataRaw,
		&item.Authorized,
		&item.CreatedAt,
		&item.UpdatedAt,
	)
	if err != nil {
		if isDuplicateError(err) {
			writeError(w, http.StatusConflict, "account already exists for this provider")
			return
		}
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "account not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to update account")
		return
	}

	if err := unmarshalJSONNullable(mediaRaw, &item.Media); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to decode media")
		return
	}
	if err := unmarshalJSONNullable(dataRaw, &item.Data); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to decode data")
		return
	}

	writeJSON(w, http.StatusOK, item)
}

func (s *apiServer) handleDeleteAccount(w http.ResponseWriter, r *http.Request, _ authenticatedUser) {
	id, err := parseIDFromPath(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid account id")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	tag, err := s.deps.Postgres.Exec(ctx, `DELETE FROM accounts WHERE id = $1`, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete account")
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "account not found")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func scanAccount(row pgx.Row) (accountResponse, error) {
	var (
		item     accountResponse
		mediaRaw []byte
		dataRaw  []byte
	)

	if err := row.Scan(
		&item.ID,
		&item.UUID,
		&item.Name,
		&item.Username,
		&mediaRaw,
		&item.Provider,
		&item.ProviderID,
		&dataRaw,
		&item.Authorized,
		&item.CreatedAt,
		&item.UpdatedAt,
	); err != nil {
		return accountResponse{}, err
	}

	if err := unmarshalJSONNullable(mediaRaw, &item.Media); err != nil {
		return accountResponse{}, err
	}
	if err := unmarshalJSONNullable(dataRaw, &item.Data); err != nil {
		return accountResponse{}, err
	}

	return item, nil
}

func marshalJSONNullable(value map[string]any) ([]byte, error) {
	if value == nil {
		return nil, nil
	}
	return json.Marshal(value)
}

func unmarshalJSONNullable(raw []byte, dst *map[string]any) error {
	if len(raw) == 0 {
		*dst = nil
		return nil
	}
	return json.Unmarshal(raw, dst)
}

func (s *apiServer) ensureProviderServiceReady(ctx context.Context, provider string) error {
	binding, ok := providers.ServiceBindingForProvider(provider)
	if !ok || !binding.Required {
		return nil
	}

	var active bool
	err := s.deps.Postgres.QueryRow(ctx, `
SELECT active
FROM services
WHERE name = $1
LIMIT 1
`, binding.Service).Scan(&active)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return providers.ErrServiceNotConfigured
		}
		return err
	}

	if !active {
		return providers.ErrServiceDisabled
	}

	return nil
}
