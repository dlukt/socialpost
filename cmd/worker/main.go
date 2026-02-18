package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/dlukt/socialpost/internal/jobs/publish"
	"github.com/dlukt/socialpost/internal/platform/config"
	"github.com/dlukt/socialpost/internal/platform/db"
	"github.com/dlukt/socialpost/internal/platform/logging"
	"github.com/dlukt/socialpost/internal/platform/queue"
	"github.com/dlukt/socialpost/internal/providers"
	appRuntime "github.com/dlukt/socialpost/internal/runtime"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

const (
	workerPostStatusPublished        = 2
	workerPostStatusFailed           = 3
	workerPostScheduleStatusComplete = 2
)

func main() {
	ctx, stop := appRuntime.ContextWithSignals(context.Background())
	defer stop()

	cfg, err := config.Load("worker")
	if err != nil {
		slog.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	logger := logging.New(cfg.LogLevel).With(
		"service", cfg.ServiceName,
		"env", cfg.Environment,
	)

	postgresPool, err := db.Open(ctx, cfg.PostgresURL)
	if err != nil {
		logger.Error("failed to connect postgres", "error", err)
		os.Exit(1)
	}
	defer postgresPool.Close()

	redisClient, err := queue.Open(ctx, cfg.RedisAddr, cfg.RedisPassword, cfg.RedisDB)
	if err != nil {
		logger.Error("failed to connect redis", "error", err)
		os.Exit(1)
	}
	defer redisClient.Close()

	providerManager := providers.NewDefaultManager()

	logger.Info("worker started")
	workerLoop(ctx, logger, postgresPool, redisClient, providerManager)
	logger.Info("worker stopped")
}

func workerLoop(ctx context.Context, logger *slog.Logger, postgresPool *pgxpool.Pool, redisClient *redis.Client, providerManager *providers.Manager) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		result, err := redisClient.BRPop(ctx, 5*time.Second, publish.QueueKey).Result()
		if err != nil {
			if errors.Is(err, redis.Nil) || errors.Is(err, context.Canceled) {
				continue
			}
			logger.Error("failed to read queue", "error", err)
			continue
		}

		if len(result) != 2 {
			continue
		}

		var job publish.Job
		if err := json.Unmarshal([]byte(result[1]), &job); err != nil {
			logger.Error("invalid publish job payload", "error", err)
			continue
		}

		if err := processPublishJob(ctx, postgresPool, providerManager, job); err != nil {
			logger.Error("failed to process publish job", "post_id", job.PostID, "account_id", job.AccountID, "attempt", job.Attempt, "error", err)

			if !isPermanentPublishError(err) && job.Attempt < 3 {
				job.Attempt++
				payload, marshalErr := json.Marshal(job)
				if marshalErr != nil {
					logger.Error("failed to marshal retry job", "error", marshalErr)
					continue
				}
				if pushErr := redisClient.LPush(ctx, publish.QueueKey, payload).Err(); pushErr != nil {
					logger.Error("failed to requeue job", "error", pushErr)
				}
				continue
			}

			if markErr := markAccountJobError(ctx, postgresPool, job, err); markErr != nil {
				logger.Error("failed to mark account error", "error", markErr)
			}
			continue
		}

		logger.Info("publish job processed", "post_id", job.PostID, "account_id", job.AccountID)
	}
}

func processPublishJob(parent context.Context, postgresPool *pgxpool.Pool, providerManager *providers.Manager, job publish.Job) error {
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()

	tx, err := postgresPool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	account, err := fetchAccountForPostTx(ctx, tx, job.PostID, job.AccountID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	}

	if !account.Authorized {
		return providers.ErrUnauthorized
	}

	serviceCfg, err := fetchServiceConfigurationForProviderTx(ctx, tx, account.Provider)
	if err != nil {
		return err
	}

	text, err := fetchPublishTextTx(ctx, tx, job.PostID, job.AccountID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return providers.ErrInvalidContent
		}
		return err
	}

	result, err := providerManager.Publish(ctx, account.ProviderAccount(), providers.PublishRequest{
		Text:                 text,
		ServiceConfiguration: serviceCfg.Configuration,
	})
	if err != nil {
		return err
	}

	resultData, err := json.Marshal(result.Data)
	if err != nil {
		return err
	}

	tag, err := tx.Exec(ctx, `
UPDATE post_accounts
SET provider_post_id = $3,
    data = $4,
    errors = NULL,
    updated_at = NOW()
WHERE post_id = $1
  AND account_id = $2
`, job.PostID, job.AccountID, result.ProviderPostID, resultData)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return nil
	}

	if err := finalizePostIfDoneTx(ctx, tx, job.PostID); err != nil {
		return err
	}

	return tx.Commit(ctx)
}

func markAccountJobError(parent context.Context, postgresPool *pgxpool.Pool, job publish.Job, publishErr error) error {
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()

	tx, err := postgresPool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if errors.Is(publishErr, providers.ErrUnauthorized) {
		if _, err := tx.Exec(ctx, `
UPDATE accounts
SET authorized = FALSE,
    updated_at = NOW()
WHERE id = $1
`, job.AccountID); err != nil {
			return err
		}
	}

	if _, err := tx.Exec(ctx, `
UPDATE post_accounts
SET errors = jsonb_build_array($3::text),
    updated_at = NOW()
WHERE post_id = $1
  AND account_id = $2
`, job.PostID, job.AccountID, publishErr.Error()); err != nil {
		return err
	}

	if err := finalizePostIfDoneTx(ctx, tx, job.PostID); err != nil {
		return err
	}

	return tx.Commit(ctx)
}

func finalizePostIfDoneTx(ctx context.Context, tx pgx.Tx, postID int64) error {
	var pendingCount int64
	var failedCount int64
	if err := tx.QueryRow(ctx, `
SELECT
    COUNT(*) FILTER (WHERE provider_post_id IS NULL AND errors IS NULL) AS pending_count,
    COUNT(*) FILTER (WHERE errors IS NOT NULL) AS failed_count
FROM post_accounts
WHERE post_id = $1
`, postID).Scan(&pendingCount, &failedCount); err != nil {
		return err
	}

	if pendingCount > 0 {
		return nil
	}

	if failedCount > 0 {
		_, err := tx.Exec(ctx, `
UPDATE posts
SET status = $2,
    schedule_status = $3,
    updated_at = NOW()
WHERE id = $1
`, postID, workerPostStatusFailed, workerPostScheduleStatusComplete)
		return err
	}

	_, err := tx.Exec(ctx, `
UPDATE posts
SET status = $2,
    schedule_status = $3,
    published_at = NOW(),
    updated_at = NOW()
WHERE id = $1
`, postID, workerPostStatusPublished, workerPostScheduleStatusComplete)
	return err
}

type publishAccount struct {
	ID          int64
	Provider    string
	ProviderID  string
	Name        string
	Username    string
	Authorized  bool
	Data        map[string]any
	AccessToken map[string]any
}

func (a publishAccount) ProviderAccount() providers.Account {
	return providers.Account{
		ID:          a.ID,
		Provider:    a.Provider,
		ProviderID:  a.ProviderID,
		Name:        a.Name,
		Username:    a.Username,
		Data:        a.Data,
		AccessToken: a.AccessToken,
	}
}

func fetchAccountForPostTx(ctx context.Context, tx pgx.Tx, postID, accountID int64) (publishAccount, error) {
	var (
		item           publishAccount
		username       *string
		dataRaw        []byte
		accessTokenRaw []byte
	)

	err := tx.QueryRow(ctx, `
SELECT a.id, a.provider, a.provider_id, a.name, a.username, a.authorized, a.data, a.access_token
FROM post_accounts pa
JOIN accounts a ON a.id = pa.account_id
WHERE pa.post_id = $1
  AND pa.account_id = $2
`, postID, accountID).Scan(
		&item.ID,
		&item.Provider,
		&item.ProviderID,
		&item.Name,
		&username,
		&item.Authorized,
		&dataRaw,
		&accessTokenRaw,
	)
	if err != nil {
		return publishAccount{}, err
	}

	if username != nil {
		item.Username = *username
	}
	if len(dataRaw) > 0 {
		if err := json.Unmarshal(dataRaw, &item.Data); err != nil {
			return publishAccount{}, err
		}
	}
	if len(accessTokenRaw) > 0 {
		if err := json.Unmarshal(accessTokenRaw, &item.AccessToken); err != nil {
			return publishAccount{}, err
		}
	}

	if item.Data == nil {
		item.Data = map[string]any{}
	}
	if item.AccessToken == nil {
		item.AccessToken = map[string]any{}
	}

	return item, nil
}

type serviceConfig struct {
	Configuration map[string]any
}

func fetchServiceConfigurationForProviderTx(ctx context.Context, tx pgx.Tx, provider string) (serviceConfig, error) {
	binding, ok := providers.ServiceBindingForProvider(provider)
	if !ok || binding.Service == "" {
		return serviceConfig{Configuration: map[string]any{}}, nil
	}

	var (
		configurationRaw []byte
		active           bool
	)

	err := tx.QueryRow(ctx, `
SELECT configuration, active
FROM services
WHERE name = $1
LIMIT 1
`, binding.Service).Scan(&configurationRaw, &active)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			if binding.Required {
				return serviceConfig{}, providers.ErrServiceNotConfigured
			}
			return serviceConfig{Configuration: map[string]any{}}, nil
		}
		return serviceConfig{}, err
	}

	if !active {
		return serviceConfig{}, providers.ErrServiceDisabled
	}

	cfg := serviceConfig{Configuration: map[string]any{}}
	if len(configurationRaw) > 0 {
		if err := json.Unmarshal(configurationRaw, &cfg.Configuration); err != nil {
			return serviceConfig{}, err
		}
	}

	return cfg, nil
}

func fetchPublishTextTx(ctx context.Context, tx pgx.Tx, postID, accountID int64) (string, error) {
	var contentRaw []byte
	err := tx.QueryRow(ctx, `
SELECT content
FROM post_versions
WHERE post_id = $1
  AND account_id = $2
ORDER BY id ASC
LIMIT 1
`, postID, accountID).Scan(&contentRaw)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			err = tx.QueryRow(ctx, `
SELECT content
FROM post_versions
WHERE post_id = $1
  AND is_original = TRUE
ORDER BY id ASC
LIMIT 1
`, postID).Scan(&contentRaw)
		}
		if err != nil {
			return "", err
		}
	}

	return extractBodyFromVersionContent(contentRaw)
}

func extractBodyFromVersionContent(raw []byte) (string, error) {
	if len(raw) == 0 {
		return "", providers.ErrInvalidContent
	}

	var object map[string]any
	if err := json.Unmarshal(raw, &object); err == nil {
		if body, ok := object["body"].(string); ok {
			trimmed := strings.TrimSpace(body)
			if trimmed != "" {
				return trimmed, nil
			}
		}
	}

	var list []map[string]any
	if err := json.Unmarshal(raw, &list); err == nil {
		if len(list) > 0 {
			if body, ok := list[0]["body"].(string); ok {
				trimmed := strings.TrimSpace(body)
				if trimmed != "" {
					return trimmed, nil
				}
			}
		}
	}

	var plain string
	if err := json.Unmarshal(raw, &plain); err == nil {
		trimmed := strings.TrimSpace(plain)
		if trimmed != "" {
			return trimmed, nil
		}
	}

	return "", providers.ErrInvalidContent
}

func isPermanentPublishError(err error) bool {
	return errors.Is(err, providers.ErrUnauthorized) ||
		errors.Is(err, providers.ErrUnsupportedProvider) ||
		errors.Is(err, providers.ErrInvalidContent) ||
		errors.Is(err, providers.ErrServiceDisabled) ||
		errors.Is(err, providers.ErrServiceNotConfigured)
}
