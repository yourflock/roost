// main.go — Content acquisition worker entrypoint.
// Acquires content from the queue into R2 object storage.
// Requires DATABASE_URL, REDIS_URL, and optionally R2_UPLOAD_URL.
package main

import (
	"context"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	content_acquirer "github.com/unyeco/roost/services/content_acquirer"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		log.Fatal("DATABASE_URL is required")
	}
	redisURL := os.Getenv("REDIS_URL")
	if redisURL == "" {
		log.Fatal("REDIS_URL is required")
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		log.Fatalf("db pool creation failed: %v", err)
	}
	defer pool.Close()

	redisOpt, err := redis.ParseURL(redisURL)
	if err != nil {
		log.Fatalf("redis URL parse failed: %v", err)
	}
	rdb := redis.NewClient(redisOpt)
	defer rdb.Close()

	logger.Info("content acquisition worker starting")
	content_acquirer.PollQueue(ctx, pool, rdb, logger)
	logger.Info("content acquisition worker stopped")
}
