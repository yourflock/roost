// main.go — FTV Gateway service entrypoint.
package main

import (
	"context"
	"log"
	"os"

	ftv_gateway "github.com/yourflock/roost/services/ftv_gateway"
	"github.com/redis/go-redis/v9"
)

func main() {
	ctx := context.Background()

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		log.Fatal("DATABASE_URL is required")
	}

	db, err := ftv_gateway.ConnectCentralDB(ctx, dbURL)
	if err != nil {
		log.Fatalf("central DB connection failed: %v", err)
	}
	defer db.Close()

	var rdb *redis.Client
	if redisURL := os.Getenv("REDIS_URL"); redisURL != "" {
		opt, _ := redis.ParseURL(redisURL)
		rdb = redis.NewClient(opt)
	}

	srv := ftv_gateway.NewServer(db, rdb)
	if err := srv.Run(); err != nil {
		log.Fatalf("FTV Gateway error: %v", err)
	}
}
