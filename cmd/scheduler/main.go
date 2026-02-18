package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"time"

	"github.com/dlukt/socialpost/internal/jobs/publish"
	"github.com/dlukt/socialpost/internal/platform/config"
	"github.com/dlukt/socialpost/internal/platform/db"
	"github.com/dlukt/socialpost/internal/platform/logging"
	"github.com/dlukt/socialpost/internal/platform/queue"
	appRuntime "github.com/dlukt/socialpost/internal/runtime"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

const (
	postStatusScheduled        = 1
	postStatusPublished        = 2
	postStatusFailed           = 3
	postScheduleStatusPending  = 0
	postScheduleStatusRunning  = 1
	postScheduleStatusComplete = 2
)

func main() {
	ctx, stop := appRuntime.ContextWithSignals(context.Background())
	defer stop()

	cfg, err := config.Load("scheduler")
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

	logger.Info("scheduler started")
	schedulerLoop(ctx, logger, postgresPool, redisClient)
	logger.Info("scheduler stopped")
}

func schedulerLoop(ctx context.Context, logger *slog.Logger, postgresPool *pgxpool.Pool, redisClient *redis.Client) {
	runOnce := func() {
		enqueued, failed, err := scanAndEnqueueDuePosts(ctx, postgresPool, redisClient)
		if err != nil {
			logger.Error("scheduler scan failed", "error", err)
			return
		}
		logger.Info("scheduler tick", "enqueued_jobs", enqueued, "failed_posts", failed)
	}

	runOnce()

	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			runOnce()
		}
	}
}

func scanAndEnqueueDuePosts(parent context.Context, postgresPool *pgxpool.Pool, redisClient *redis.Client) (int, int, error) {
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()

	rows, err := postgresPool.Query(ctx, `
SELECT id
FROM posts
WHERE status = $1
  AND schedule_status = $2
  AND deleted_at IS NULL
  AND scheduled_at IS NOT NULL
  AND scheduled_at <= NOW()
ORDER BY scheduled_at ASC
LIMIT 200
`, postStatusScheduled, postScheduleStatusPending)
	if err != nil {
		return 0, 0, err
	}
	defer rows.Close()

	var (
		enqueuedJobs int
		failedPosts  int
	)

	for rows.Next() {
		var postID int64
		if err := rows.Scan(&postID); err != nil {
			return enqueuedJobs, failedPosts, err
		}

		jobs, postFailed, err := enqueuePost(ctx, postgresPool, redisClient, postID)
		if err != nil {
			return enqueuedJobs, failedPosts, err
		}
		enqueuedJobs += jobs
		if postFailed {
			failedPosts++
		}
	}

	if err := rows.Err(); err != nil {
		return enqueuedJobs, failedPosts, err
	}

	if err := redisClient.Set(ctx, "mixpost:last-schedule-run", time.Now().UTC().Format(time.RFC3339), 0).Err(); err != nil {
		return enqueuedJobs, failedPosts, err
	}

	return enqueuedJobs, failedPosts, nil
}

func enqueuePost(ctx context.Context, postgresPool *pgxpool.Pool, redisClient *redis.Client, postID int64) (int, bool, error) {
	tx, err := postgresPool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return 0, false, err
	}
	defer tx.Rollback(ctx)

	var lockedPostID int64
	err = tx.QueryRow(ctx, `
UPDATE posts
SET schedule_status = $2,
    updated_at = NOW()
WHERE id = $1
  AND status = $3
  AND schedule_status = $4
  AND deleted_at IS NULL
RETURNING id
`, postID, postScheduleStatusRunning, postStatusScheduled, postScheduleStatusPending).Scan(&lockedPostID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, false, nil
		}
		return 0, false, err
	}

	rows, err := tx.Query(ctx, `
SELECT account_id
FROM post_accounts
WHERE post_id = $1
ORDER BY id
`, postID)
	if err != nil {
		return 0, false, err
	}

	accountIDs := make([]int64, 0)
	for rows.Next() {
		var accountID int64
		if err := rows.Scan(&accountID); err != nil {
			rows.Close()
			return 0, false, err
		}
		accountIDs = append(accountIDs, accountID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, false, err
	}
	rows.Close()

	if len(accountIDs) == 0 {
		if _, err := tx.Exec(ctx, `
UPDATE posts
SET status = $2,
    schedule_status = $3,
    updated_at = NOW()
WHERE id = $1
`, postID, postStatusFailed, postScheduleStatusComplete); err != nil {
			return 0, false, err
		}
		if err := tx.Commit(ctx); err != nil {
			return 0, false, err
		}
		return 0, true, nil
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, false, err
	}

	enqueued := 0
	for _, accountID := range accountIDs {
		payload, err := json.Marshal(publish.Job{
			PostID:     postID,
			AccountID:  accountID,
			Attempt:    1,
			EnqueuedAt: time.Now().UTC(),
		})
		if err != nil {
			return enqueued, false, err
		}
		if err := redisClient.LPush(ctx, publish.QueueKey, payload).Err(); err != nil {
			return enqueued, false, err
		}
		enqueued++
	}

	return enqueued, false, nil
}
