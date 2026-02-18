package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type postVersionInput struct {
	AccountID  int64           `json:"account_id"`
	IsOriginal bool            `json:"is_original"`
	Content    json.RawMessage `json:"content"`
}

type postRequest struct {
	Status         int16              `json:"status"`
	ScheduleStatus int16              `json:"schedule_status"`
	ScheduledAt    *time.Time         `json:"scheduled_at,omitempty"`
	PublishedAt    *time.Time         `json:"published_at,omitempty"`
	AccountIDs     []int64            `json:"account_ids,omitempty"`
	Versions       []postVersionInput `json:"versions,omitempty"`
}

type postVersionResponse struct {
	ID         int64           `json:"id"`
	AccountID  int64           `json:"account_id"`
	IsOriginal bool            `json:"is_original"`
	Content    json.RawMessage `json:"content,omitempty"`
}

type postResponse struct {
	ID             int64                 `json:"id"`
	UUID           string                `json:"uuid"`
	Status         int16                 `json:"status"`
	ScheduleStatus int16                 `json:"schedule_status"`
	ScheduledAt    *time.Time            `json:"scheduled_at,omitempty"`
	PublishedAt    *time.Time            `json:"published_at,omitempty"`
	DeletedAt      *time.Time            `json:"deleted_at,omitempty"`
	CreatedAt      time.Time             `json:"created_at"`
	UpdatedAt      time.Time             `json:"updated_at"`
	AccountIDs     []int64               `json:"account_ids"`
	Versions       []postVersionResponse `json:"versions,omitempty"`
}

func (s *apiServer) handleListPosts(w http.ResponseWriter, r *http.Request, _ authenticatedUser) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	rows, err := s.deps.Postgres.Query(ctx, `
SELECT
    p.id,
    p.uuid::text,
    p.status,
    p.schedule_status,
    p.scheduled_at,
    p.published_at,
    p.deleted_at,
    p.created_at,
    p.updated_at,
    COALESCE(array_agg(pa.account_id ORDER BY pa.id) FILTER (WHERE pa.account_id IS NOT NULL), '{}') AS account_ids
FROM posts p
LEFT JOIN post_accounts pa ON pa.post_id = p.id
WHERE p.deleted_at IS NULL
GROUP BY p.id
ORDER BY p.id DESC
`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list posts")
		return
	}
	defer rows.Close()

	response := make([]postResponse, 0)
	for rows.Next() {
		item, err := scanPost(rows)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to scan post")
			return
		}
		response = append(response, item)
	}

	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to iterate posts")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"data": response})
}

func (s *apiServer) handleGetPost(w http.ResponseWriter, r *http.Request, _ authenticatedUser) {
	id, err := parseIDFromPath(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid post id")
		return
	}

	item, err := s.fetchPostByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "post not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to get post")
		return
	}

	writeJSON(w, http.StatusOK, item)
}

func (s *apiServer) handleCreatePost(w http.ResponseWriter, r *http.Request, _ authenticatedUser) {
	var req postRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	tx, err := s.deps.Postgres.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start transaction")
		return
	}
	defer tx.Rollback(ctx)

	var created postResponse
	err = tx.QueryRow(ctx, `
INSERT INTO posts (status, schedule_status, scheduled_at, published_at)
VALUES ($1, $2, $3, $4)
RETURNING id, uuid::text, status, schedule_status, scheduled_at, published_at, deleted_at, created_at, updated_at
`, req.Status, req.ScheduleStatus, req.ScheduledAt, req.PublishedAt).Scan(
		&created.ID,
		&created.UUID,
		&created.Status,
		&created.ScheduleStatus,
		&created.ScheduledAt,
		&created.PublishedAt,
		&created.DeletedAt,
		&created.CreatedAt,
		&created.UpdatedAt,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create post")
		return
	}

	if err := replacePostAccountsTx(ctx, tx, created.ID, req.AccountIDs); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := replacePostVersionsTx(ctx, tx, created.ID, req.Versions); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	if err := tx.Commit(ctx); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit post")
		return
	}

	item, err := s.fetchPostByID(r.Context(), created.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "post created but failed to load response")
		return
	}

	writeJSON(w, http.StatusCreated, item)
}

func (s *apiServer) handleUpdatePost(w http.ResponseWriter, r *http.Request, _ authenticatedUser) {
	id, err := parseIDFromPath(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid post id")
		return
	}

	var req postRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	tx, err := s.deps.Postgres.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start transaction")
		return
	}
	defer tx.Rollback(ctx)

	var updated postResponse
	err = tx.QueryRow(ctx, `
UPDATE posts
SET status = $2,
    schedule_status = $3,
    scheduled_at = $4,
    published_at = $5,
    updated_at = NOW()
WHERE id = $1
  AND deleted_at IS NULL
RETURNING id, uuid::text, status, schedule_status, scheduled_at, published_at, deleted_at, created_at, updated_at
`, id, req.Status, req.ScheduleStatus, req.ScheduledAt, req.PublishedAt).Scan(
		&updated.ID,
		&updated.UUID,
		&updated.Status,
		&updated.ScheduleStatus,
		&updated.ScheduledAt,
		&updated.PublishedAt,
		&updated.DeletedAt,
		&updated.CreatedAt,
		&updated.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "post not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to update post")
		return
	}

	if err := replacePostAccountsTx(ctx, tx, id, req.AccountIDs); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := replacePostVersionsTx(ctx, tx, id, req.Versions); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	if err := tx.Commit(ctx); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit post")
		return
	}

	item, err := s.fetchPostByID(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "post updated but failed to load response")
		return
	}

	writeJSON(w, http.StatusOK, item)
}

func (s *apiServer) handleDeletePost(w http.ResponseWriter, r *http.Request, _ authenticatedUser) {
	id, err := parseIDFromPath(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid post id")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	tag, err := s.deps.Postgres.Exec(ctx, `
UPDATE posts
SET deleted_at = NOW(), updated_at = NOW()
WHERE id = $1
  AND deleted_at IS NULL
`, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete post")
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "post not found")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (s *apiServer) fetchPostByID(ctx context.Context, id int64) (postResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	var item postResponse
	err := s.deps.Postgres.QueryRow(ctx, `
SELECT
    p.id,
    p.uuid::text,
    p.status,
    p.schedule_status,
    p.scheduled_at,
    p.published_at,
    p.deleted_at,
    p.created_at,
    p.updated_at,
    COALESCE(array_agg(pa.account_id ORDER BY pa.id) FILTER (WHERE pa.account_id IS NOT NULL), '{}') AS account_ids
FROM posts p
LEFT JOIN post_accounts pa ON pa.post_id = p.id
WHERE p.id = $1
GROUP BY p.id
`, id).Scan(
		&item.ID,
		&item.UUID,
		&item.Status,
		&item.ScheduleStatus,
		&item.ScheduledAt,
		&item.PublishedAt,
		&item.DeletedAt,
		&item.CreatedAt,
		&item.UpdatedAt,
		&item.AccountIDs,
	)
	if err != nil {
		return postResponse{}, err
	}

	versions, err := fetchPostVersions(ctx, s.deps.Postgres, id)
	if err != nil {
		return postResponse{}, err
	}
	item.Versions = versions

	return item, nil
}

func fetchPostVersions(ctx context.Context, db *pgxpool.Pool, postID int64) ([]postVersionResponse, error) {
	rows, err := db.Query(ctx, `
SELECT id, account_id, is_original, content
FROM post_versions
WHERE post_id = $1
ORDER BY id
`, postID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	versions := make([]postVersionResponse, 0)
	for rows.Next() {
		var (
			item       postVersionResponse
			contentRaw []byte
		)
		if err := rows.Scan(&item.ID, &item.AccountID, &item.IsOriginal, &contentRaw); err != nil {
			return nil, err
		}
		if len(contentRaw) > 0 {
			item.Content = append([]byte(nil), contentRaw...)
		}
		versions = append(versions, item)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	return versions, nil
}

func replacePostAccountsTx(ctx context.Context, tx pgx.Tx, postID int64, accountIDs []int64) error {
	if _, err := tx.Exec(ctx, `DELETE FROM post_accounts WHERE post_id = $1`, postID); err != nil {
		return err
	}

	seen := map[int64]struct{}{}
	for _, accountID := range accountIDs {
		if accountID <= 0 {
			return errors.New("account_ids must contain positive integers")
		}
		if _, ok := seen[accountID]; ok {
			continue
		}
		seen[accountID] = struct{}{}

		if _, err := tx.Exec(ctx, `
INSERT INTO post_accounts (post_id, account_id)
VALUES ($1, $2)
`, postID, accountID); err != nil {
			return err
		}
	}

	return nil
}

func replacePostVersionsTx(ctx context.Context, tx pgx.Tx, postID int64, versions []postVersionInput) error {
	if _, err := tx.Exec(ctx, `DELETE FROM post_versions WHERE post_id = $1`, postID); err != nil {
		return err
	}

	for _, version := range versions {
		if version.AccountID < 0 {
			return errors.New("version account_id must be >= 0")
		}

		content := version.Content
		if len(content) == 0 {
			content = []byte("null")
		}

		if _, err := tx.Exec(ctx, `
INSERT INTO post_versions (post_id, account_id, is_original, content)
VALUES ($1, $2, $3, $4)
`, postID, version.AccountID, version.IsOriginal, content); err != nil {
			return err
		}
	}

	return nil
}

func scanPost(row pgx.Row) (postResponse, error) {
	var item postResponse
	if err := row.Scan(
		&item.ID,
		&item.UUID,
		&item.Status,
		&item.ScheduleStatus,
		&item.ScheduledAt,
		&item.PublishedAt,
		&item.DeletedAt,
		&item.CreatedAt,
		&item.UpdatedAt,
		&item.AccountIDs,
	); err != nil {
		return postResponse{}, err
	}
	if item.AccountIDs == nil {
		item.AccountIDs = []int64{}
	}
	return item, nil
}
