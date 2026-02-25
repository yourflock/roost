// main.go — FTV Notifications service entrypoint.
package main

import (
	"context"
	"log"
	"log/slog"
	"os"

	ftv_notifications "github.com/yourflock/roost/services/ftv_notifications"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		log.Fatal("DATABASE_URL is required")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		log.Fatalf("DB pool creation failed: %v", err)
	}
	defer pool.Close()

	svc := ftv_notifications.NewNotificationService(pool, logger)
	if err := svc.Run(); err != nil {
		log.Fatalf("FTV Notifications error: %v", err)
	}
}
