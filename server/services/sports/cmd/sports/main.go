// main.go — Roost Sports Service.
// P15-T02: Sports data sync, live score polling, and sports event API.
// Port: 8102 (env: SPORTS_PORT).
//
// Routes:
//   GET  /health                                    — service liveness
//   GET  /sports/leagues                            — list active leagues
//   GET  /sports/leagues/:id/teams                  — teams for a league
//   GET  /sports/events?league=NFL&week=1&season=2026 — filtered events
//   GET  /sports/events/:id                         — event detail with channel mappings
//   GET  /sports/live                               — all currently live events
//   POST /admin/sports/leagues                      — create league
//   POST /admin/sports/teams                        — create team
//   POST /admin/sports/events                       — create event
//   POST /admin/sports/events/:id/channel-mapping   — map event to channel
//   POST /admin/sports/sync                         — trigger manual sync
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/yourflock/roost/services/sports"
	"github.com/yourflock/roost/services/sports/data"
)

func main() {
	port := getEnv("SPORTS_PORT", "8102")
	dsn := getEnv("POSTGRES_URL", "postgres://roost:roost@localhost:5433/roost_dev?sslmode=disable")

	db, err := sports.ConnectDB(dsn)
	if err != nil {
		log.Fatalf("[sports] db connect: %v", err)
	}
	defer db.Close()
	log.Printf("[sports] database connected")

	// Seed NFL teams on startup (idempotent)
	ctx := context.Background()
	if err := data.SeedNFLTeams(ctx, db); err != nil {
		log.Printf("[sports] NFL seed warning: %v", err)
	}

	srv := sports.NewServer(db)

	mainCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Start background goroutines
	go srv.DailySync(mainCtx)
	go srv.GameDay30sPoller(mainCtx)
	// OSG.2.001 — channel matching cron (same 24h cadence as DailySync)
	go func() {
		srv.RunAllSourcesChannelMatch(mainCtx)
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-mainCtx.Done():
				return
			case <-ticker.C:
				srv.RunAllSourcesChannelMatch(mainCtx)
			}
		}
	}()
	// OSG.2.002 — stream source health check worker (every 5 minutes)
	go srv.StartHealthWorker(mainCtx)

	httpServer := &http.Server{
		Addr:         ":" + port,
		Handler:      srv.Routes(),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
	}

	// Graceful shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		log.Printf("[sports] starting on :%s", port)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("[sports] server error: %v", err)
		}
	}()

	<-quit
	log.Printf("[sports] shutting down...")
	cancel()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Printf("[sports] shutdown error: %v", err)
	}
	log.Printf("[sports] stopped")
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
