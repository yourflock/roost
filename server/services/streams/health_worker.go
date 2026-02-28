// health_worker.go — Background health-check worker for family IPTV channels.
//
// Runs a periodic HEAD request against each channel's stream URL to determine
// whether the source is reachable. Updates health_status in family_iptv_channels.
//
// Check interval: 5 minutes (env: IPTV_HEALTH_INTERVAL_SEC).
// Per-channel timeout: 5 seconds.
// Concurrency: up to 20 goroutines in parallel (env: IPTV_HEALTH_CONCURRENCY).
//
// Health logic:
//   - HTTP 2xx / 3xx: "healthy"
//   - HTTP 4xx / 5xx or timeout: "down"
//   - Any network error: "down"
//
// The worker degrades gracefully: if the DB query fails it logs and continues
// on the next tick — it never panics or crashes the parent service.
package streams

import (
	"context"
	"log"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"
)

// channelRecord holds the minimal data needed for a health check.
type channelRecord struct {
	ID        string
	StreamURL string
}

// StartHealthWorker starts the background IPTV channel health-check loop.
// It runs until ctx is cancelled (e.g., on service shutdown).
// Pass the same DB used by the IPTV handler.
func StartHealthWorker(ctx context.Context, db DB) {
	interval := healthInterval()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	log.Printf("[streams/health] worker started (interval=%s, concurrency=%d)",
		interval, healthConcurrency())

	// Run one initial check immediately at startup.
	runHealthChecks(ctx, db)

	for {
		select {
		case <-ctx.Done():
			log.Println("[streams/health] worker stopped")
			return
		case <-ticker.C:
			runHealthChecks(ctx, db)
		}
	}
}

// runHealthChecks fetches all channels and checks each concurrently.
func runHealthChecks(ctx context.Context, db DB) {
	channels, err := listAllChannels(ctx, db)
	if err != nil {
		log.Printf("[streams/health] list channels error: %v", err)
		return
	}
	if len(channels) == 0 {
		return
	}

	maxConcurrent := healthConcurrency()
	sem := make(chan struct{}, maxConcurrent)
	var wg sync.WaitGroup

	for _, ch := range channels {
		wg.Add(1)
		sem <- struct{}{}
		go func(c channelRecord) {
			defer wg.Done()
			defer func() { <-sem }()
			status := checkChannelHealth(ctx, c.StreamURL)
			if err := updateChannelHealth(ctx, db, c.ID, status); err != nil {
				log.Printf("[streams/health] update channel %s: %v", c.ID, err)
			}
		}(ch)
	}
	wg.Wait()
}

// listAllChannels fetches all channel IDs and stream URLs from the DB.
func listAllChannels(ctx context.Context, db DB) ([]channelRecord, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT id, stream_url
		FROM family_iptv_channels
		ORDER BY id
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var channels []channelRecord
	for rows.Next() {
		var c channelRecord
		if err := rows.Scan(&c.ID, &c.StreamURL); err != nil {
			continue
		}
		channels = append(channels, c)
	}
	return channels, rows.Err()
}

// checkChannelHealth issues a HEAD request to the stream URL with a 5-second timeout.
// Returns "healthy" or "down".
func checkChannelHealth(ctx context.Context, streamURL string) string {
	reqCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	client := &http.Client{
		// Do not follow redirects — a 3xx itself is a healthy signal.
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
		Timeout: 5 * time.Second,
	}

	req, err := http.NewRequestWithContext(reqCtx, http.MethodHead, streamURL, nil)
	if err != nil {
		return "down"
	}

	resp, err := client.Do(req)
	if err != nil {
		return "down"
	}
	resp.Body.Close()

	if resp.StatusCode < 400 {
		return "healthy"
	}
	return "down"
}

// updateChannelHealth writes the health status and timestamp to the DB.
func updateChannelHealth(ctx context.Context, db DB, channelID, status string) error {
	_, err := db.ExecContext(ctx, `
		UPDATE family_iptv_channels
		SET health_status = $1, last_health_check = NOW()
		WHERE id = $2
	`, status, channelID)
	return err
}

// ── Configuration helpers ─────────────────────────────────────────────────────

// healthInterval returns the health-check ticker interval.
// Default: 5 minutes. Override with IPTV_HEALTH_INTERVAL_SEC.
func healthInterval() time.Duration {
	if s := os.Getenv("IPTV_HEALTH_INTERVAL_SEC"); s != "" {
		if secs, err := strconv.Atoi(s); err == nil && secs > 0 {
			return time.Duration(secs) * time.Second
		}
	}
	return 5 * time.Minute
}

// healthConcurrency returns the max goroutines for concurrent health checks.
// Default: 20. Override with IPTV_HEALTH_CONCURRENCY.
func healthConcurrency() int {
	if s := os.Getenv("IPTV_HEALTH_CONCURRENCY"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			return n
		}
	}
	return 20
}
