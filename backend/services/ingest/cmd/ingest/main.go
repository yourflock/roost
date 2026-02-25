// main.go — Roost Ingest Service.
// Polls active channels from Postgres, manages FFmpeg pipelines, serves health endpoint.
// Port: 8094 (env: INGEST_PORT). Internal service — not exposed directly to subscribers.
//
// Endpoints:
//   GET /health              — JSON health (no auth)
//   GET /channels/health     — per-channel health map
//   GET /alerts              — active stream alerts
//   GET /metrics             — Prometheus metrics (no auth; firewall-protected)
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	_ "github.com/lib/pq"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/yourflock/roost/services/ingest/internal/config"
	"github.com/yourflock/roost/services/ingest/internal/pipeline"
)

// channelHealth is the health snapshot stored per channel.
type channelHealth struct {
	Status    string `json:"status"`
	LastCheck string `json:"last_check"`
}

func main() {
	cfg := config.Load()
	log.Printf("[ingest] starting on port %s, segments at %s", cfg.IngestPort, cfg.SegmentDir)

	// Connect to Postgres
	db, err := sql.Open("postgres", cfg.PostgresURL)
	if err != nil {
		log.Fatalf("[ingest] db open: %v", err)
	}
	defer db.Close()
	if err := db.Ping(); err != nil {
		log.Fatalf("[ingest] db ping: %v", err)
	}

	// Health state kept in-memory
	healthMap := make(map[string]channelHealth)
	var healthMu sync.RWMutex

	// Alerts: channels that went offline/stale
	alerts := make(map[string]time.Time) // slug -> when alert was raised
	var alertsMu sync.Mutex

	healthCallback := func(slug, status string) {
		healthMu.Lock()
		healthMap[slug] = channelHealth{Status: status, LastCheck: time.Now().UTC().Format(time.RFC3339)}
		healthMu.Unlock()

		// Update Prometheus metrics
		pipeline.MetricActiveChannels.Set(0) // will be recalculated below

		// Raise/clear alerts
		alertsMu.Lock()
		if status == "unhealthy" || status == "offline" || status == "stale" {
			if _, exists := alerts[slug]; !exists {
				alerts[slug] = time.Now()
				log.Printf("[ingest] ALERT raised for channel %q (status: %s)", slug, status)
			}
		} else if status == "healthy" {
			if _, exists := alerts[slug]; exists {
				delete(alerts, slug)
				log.Printf("[ingest] ALERT resolved for channel %q", slug)
			}
		}
		alertsMu.Unlock()
	}

	mgr := pipeline.NewManager(
		cfg.SegmentDir,
		cfg.MaxRestarts,
		cfg.RestartWindow,
		healthCallback,
	)

	// Start disk usage monitor (logs warning at 80% usage)
	pipeline.DiskUsageMonitor(cfg.SegmentDir)

	// Health file monitor goroutine — checks segment freshness every 30s
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			healthMu.RLock()
			slugs := make([]string, 0, len(healthMap))
			for slug := range healthMap {
				slugs = append(slugs, slug)
			}
			healthMu.RUnlock()

			for _, slug := range slugs {
				status := pipeline.HealthFileMonitor(cfg.SegmentDir, slug)
				healthCallback(slug, status)
			}

			// Update active channels gauge
			pipeline.MetricActiveChannels.Set(float64(mgr.ActiveCount()))
		}
	}()

	// Initial channel poll
	channels, err := fetchActiveChannels(db)
	if err != nil {
		log.Printf("[ingest] initial channel fetch failed: %v", err)
	} else {
		mgr.Sync(channels)
	}

	// Periodic poll goroutine
	go func() {
		ticker := time.NewTicker(cfg.ChannelPollInterval)
		defer ticker.Stop()
		for range ticker.C {
			chs, err := fetchActiveChannels(db)
			if err != nil {
				log.Printf("[ingest] channel poll error: %v", err)
				continue
			}
			mgr.Sync(chs)
			pipeline.MetricActiveChannels.Set(float64(mgr.ActiveCount()))
		}
	}()

	// HTTP server
	mux := http.NewServeMux()

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"status":"ok","service":"roost-ingest","active_channels":%d}`, mgr.ActiveCount())
	})

	mux.HandleFunc("GET /channels/health", func(w http.ResponseWriter, r *http.Request) {
		healthMu.RLock()
		snap := make(map[string]channelHealth, len(healthMap))
		for k, v := range healthMap {
			snap[k] = v
		}
		healthMu.RUnlock()

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(snap)
	})

	mux.HandleFunc("GET /alerts", func(w http.ResponseWriter, r *http.Request) {
		alertsMu.Lock()
		snap := make(map[string]string, len(alerts))
		for slug, t := range alerts {
			snap[slug] = t.UTC().Format(time.RFC3339)
		}
		alertsMu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(snap)
	})

	// Prometheus metrics endpoint — protected by firewall in production
	mux.Handle("GET /metrics", promhttp.Handler())

	srv := &http.Server{
		Addr:    ":" + cfg.IngestPort,
		Handler: mux,
	}

	// Graceful shutdown
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM, syscall.SIGINT)

	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("[ingest] http server: %v", err)
		}
	}()

	<-stop
	log.Println("[ingest] shutting down")

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	srv.Shutdown(ctx)

	mgr.StopAll()
	log.Println("[ingest] stopped")
}

// fetchActiveChannels queries all active channels from Postgres.
func fetchActiveChannels(db *sql.DB) ([]pipeline.Channel, error) {
	rows, err := db.QueryContext(context.Background(), `
		SELECT id, slug, source_url, source_type, bitrate_config, is_active
		FROM channels
		WHERE is_active = true
		ORDER BY sort_order, name
	`)
	if err != nil {
		return nil, fmt.Errorf("fetchActiveChannels: %w", err)
	}
	defer rows.Close()

	var channels []pipeline.Channel
	for rows.Next() {
		var ch pipeline.Channel
		var bitrateJSON []byte
		err := rows.Scan(&ch.ID, &ch.Slug, &ch.SourceURL, &ch.SourceType, &bitrateJSON, &ch.IsActive)
		if err != nil {
			return nil, fmt.Errorf("scan channel: %w", err)
		}
		if len(bitrateJSON) > 0 {
			json.Unmarshal(bitrateJSON, &ch.BitrateConfig)
		}
		if ch.BitrateConfig.Mode == "" {
			ch.BitrateConfig.Mode = "passthrough"
		}
		channels = append(channels, ch)
	}
	return channels, rows.Err()
}
