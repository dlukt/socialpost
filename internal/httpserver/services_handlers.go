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

type serviceRequest struct {
	Name          string         `json:"name"`
	Configuration map[string]any `json:"configuration"`
	Active        bool           `json:"active"`
}

type serviceResponse struct {
	ID            int64          `json:"id"`
	Name          string         `json:"name"`
	Configuration map[string]any `json:"configuration"`
	Active        bool           `json:"active"`
	CreatedAt     time.Time      `json:"created_at"`
	UpdatedAt     time.Time      `json:"updated_at"`
}

func (s *apiServer) handleListServices(w http.ResponseWriter, r *http.Request, _ authenticatedUser) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	rows, err := s.deps.Postgres.Query(ctx, `
SELECT id, name, configuration, active, created_at, updated_at
FROM services
ORDER BY id DESC
`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list services")
		return
	}
	defer rows.Close()

	response := make([]serviceResponse, 0)
	for rows.Next() {
		item, err := scanService(rows)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to scan service")
			return
		}
		response = append(response, item)
	}

	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to iterate services")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"data": response})
}

func (s *apiServer) handleCreateService(w http.ResponseWriter, r *http.Request, _ authenticatedUser) {
	var req serviceRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	req.Name = strings.ToLower(strings.TrimSpace(req.Name))
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}

	if err := providers.ValidateServiceConfiguration(req.Name, req.Configuration, req.Active); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	configJSON, err := json.Marshal(emptyMapIfNil(req.Configuration))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid configuration")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var item serviceResponse
	err = s.deps.Postgres.QueryRow(ctx, `
INSERT INTO services (name, configuration, active)
VALUES ($1, $2, $3)
RETURNING id, name, configuration, active, created_at, updated_at
`, req.Name, configJSON, req.Active).Scan(
		&item.ID,
		&item.Name,
		&configJSON,
		&item.Active,
		&item.CreatedAt,
		&item.UpdatedAt,
	)
	if err != nil {
		if isDuplicateError(err) {
			writeError(w, http.StatusConflict, "service already exists")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to create service")
		return
	}

	if err := json.Unmarshal(configJSON, &item.Configuration); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to decode configuration")
		return
	}

	writeJSON(w, http.StatusCreated, item)
}

func (s *apiServer) handleUpdateService(w http.ResponseWriter, r *http.Request, _ authenticatedUser) {
	id, err := parseIDFromPath(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid service id")
		return
	}

	var req serviceRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	req.Name = strings.ToLower(strings.TrimSpace(req.Name))
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}

	if err := providers.ValidateServiceConfiguration(req.Name, req.Configuration, req.Active); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	configJSON, err := json.Marshal(emptyMapIfNil(req.Configuration))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid configuration")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var item serviceResponse
	err = s.deps.Postgres.QueryRow(ctx, `
UPDATE services
SET name = $2,
    configuration = $3,
    active = $4,
    updated_at = NOW()
WHERE id = $1
RETURNING id, name, configuration, active, created_at, updated_at
`, id, req.Name, configJSON, req.Active).Scan(
		&item.ID,
		&item.Name,
		&configJSON,
		&item.Active,
		&item.CreatedAt,
		&item.UpdatedAt,
	)
	if err != nil {
		if isDuplicateError(err) {
			writeError(w, http.StatusConflict, "service already exists")
			return
		}
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "service not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to update service")
		return
	}

	if err := json.Unmarshal(configJSON, &item.Configuration); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to decode configuration")
		return
	}

	writeJSON(w, http.StatusOK, item)
}

func (s *apiServer) handleDeleteService(w http.ResponseWriter, r *http.Request, _ authenticatedUser) {
	id, err := parseIDFromPath(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid service id")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	tag, err := s.deps.Postgres.Exec(ctx, `DELETE FROM services WHERE id = $1`, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete service")
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "service not found")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func scanService(row pgx.Row) (serviceResponse, error) {
	var (
		item       serviceResponse
		configJSON []byte
	)
	if err := row.Scan(
		&item.ID,
		&item.Name,
		&configJSON,
		&item.Active,
		&item.CreatedAt,
		&item.UpdatedAt,
	); err != nil {
		return serviceResponse{}, err
	}
	if err := json.Unmarshal(configJSON, &item.Configuration); err != nil {
		return serviceResponse{}, err
	}
	return item, nil
}

func emptyMapIfNil(value map[string]any) map[string]any {
	if value == nil {
		return map[string]any{}
	}
	return value
}
