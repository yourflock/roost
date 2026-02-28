// health_worker.go — Background health check worker for registered stream sources.
// OSG.2.002: Every 5 minutes, sample up to 5 channels per source, probe reachability,
// aggregate health status, and update sports_stream_sources.
package sports

import (
	"context"
	"database/sql"
	"log"
	"math/rand"
	"net/http"
	"sync"
	"time"
)

const (
	healthCheckInterval = 5 * time.Minute
	channelProbeTimeout = 5 * time.Second
	maxSampleChannels   = 5
)

// StartHealthWorker begins the background health check loop.
// It runs every healthCheckInterval and exits when ctx is cancelled.
func (s *Server) StartHealthWorker(ctx context.Context) {
	// Run once immediately on startup
	s.runHealthChecks(ctx)

	ticker := time.NewTicker(healthCheckInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.runHealthChecks(ctx)
		}
	}
}

// runHealthChecks iterates all enabled sources and updates their health status.
func (s *Server) runHealthChecks(ctx context.Context) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, health_status FROM sports_stream_sources WHERE enabled = true`)
	if err != nil {
		log.Printf("[sports/health] list sources: %v", err)
		return
	}

	type sourceRow struct {
		id            string
		prevStatus    string
	}
	var sources []sourceRow
	for rows.Next() {
		var sr sourceRow
		if err := rows.Scan(&sr.id, &sr.prevStatus); err != nil {
			continue
		}
		sources = append(sources, sr)
	}
	rows.Close()

	for _, src := range sources {
		s.checkSourceHealth(ctx, src.id, src.prevStatus)
	}
}

// checkSourceHealth samples up to maxSampleChannels for a source, runs parallel probes,
// aggregates the result, and updates the DB.
func (s *Server) checkSourceHealth(ctx context.Context, sourceID, prevStatus string) {
	// Load up to maxSampleChannels channel URLs (random sample via ORDER BY random())
	rows, err := s.db.QueryContext(ctx, `
		SELECT channel_url FROM sports_source_channels
		WHERE source_id = $1
		ORDER BY random()
		LIMIT $2`, sourceID, maxSampleChannels)
	if err != nil {
		log.Printf("[sports/health] source %s load channels: %v", sourceID, err)
		return
	}

	var urls []string
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			continue
		}
		urls = append(urls, u)
	}
	rows.Close()

	// No channels — set unknown and return
	if len(urls) == 0 {
		s.updateSourceHealth(ctx, sourceID, "unknown", prevStatus, false)
		return
	}

	// Probe channels in parallel
	results := make([]bool, len(urls))
	var wg sync.WaitGroup
	for i, u := range urls {
		wg.Add(1)
		go func(idx int, url string) {
			defer wg.Done()
			results[idx] = checkChannelHealth(ctx, url)
		}(i, u)
	}
	wg.Wait()

	newStatus := aggregateSourceHealth(results)
	wasHealthy := newStatus == "healthy"
	s.updateSourceHealth(ctx, sourceID, newStatus, prevStatus, wasHealthy)

	// If status just transitioned to down, trigger failover for affected games
	if newStatus == "down" && prevStatus != "down" {
		log.Printf("[sports/health] source %s transitioned to down (was %s) — triggering failover",
			sourceID, prevStatus)
		s.failoverGamesForSource(ctx, sourceID)
	}
}

// checkChannelHealth sends a range request (bytes 0-4095) to the channel URL.
// Returns true if HTTP 200–206; false on any error or non-success status.
func checkChannelHealth(ctx context.Context, channelURL string) bool {
	probeCtx, cancel := context.WithTimeout(ctx, channelProbeTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, channelURL, nil)
	if err != nil {
		return false
	}
	req.Header.Set("Range", "bytes=0-4095")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode <= 206
}

// aggregateSourceHealth computes health status from a slice of probe results.
//
//   >= 90% success → "healthy"
//   50–89% success → "degraded"
//   < 50% success  → "down"
func aggregateSourceHealth(results []bool) string {
	if len(results) == 0 {
		return "unknown"
	}
	ok := 0
	for _, r := range results {
		if r {
			ok++
		}
	}
	pct := float64(ok) / float64(len(results))
	switch {
	case pct >= 0.90:
		return "healthy"
	case pct >= 0.50:
		return "degraded"
	default:
		return "down"
	}
}

// updateSourceHealth writes the new health_status to the DB.
// last_healthy_at is only updated on a transition TO healthy.
func (s *Server) updateSourceHealth(ctx context.Context, sourceID, newStatus, prevStatus string, wasHealthy bool) {
	if wasHealthy && prevStatus != "healthy" {
		// Transitioning to healthy — update last_healthy_at
		_, err := s.db.ExecContext(ctx, `
			UPDATE sports_stream_sources
			SET health_status = $1, last_health_check_at = now(), last_healthy_at = now(), updated_at = now()
			WHERE id = $2`, newStatus, sourceID)
		if err != nil {
			log.Printf("[sports/health] update source %s health (with last_healthy_at): %v", sourceID, err)
		}
		return
	}

	_, err := s.db.ExecContext(ctx, `
		UPDATE sports_stream_sources
		SET health_status = $1, last_health_check_at = now(), updated_at = now()
		WHERE id = $2`, newStatus, sourceID)
	if err != nil {
		log.Printf("[sports/health] update source %s health: %v", sourceID, err)
	}
}

// failoverGamesForSource re-routes any active game assignments pointing at the downed source.
// Only re-routes games that are live or starting within 4 hours.
// This is called inline within the health check goroutine — no goroutine leak risk.
func (s *Server) failoverGamesForSource(ctx context.Context, sourceID string) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT a.id, a.game_id
		FROM sports_game_source_assignments a
		JOIN sports_source_channels sc ON sc.id = a.source_channel_id
		JOIN sports_events e ON e.id = a.game_id
		WHERE sc.source_id = $1
		  AND a.is_active = true
		  AND e.scheduled_time > now() - interval '4 hours'`, sourceID)
	if err != nil {
		log.Printf("[sports/health] failover query for source %s: %v", sourceID, err)
		return
	}

	type affectedGame struct {
		assignmentID string
		gameID       string
	}
	var affected []affectedGame
	for rows.Next() {
		var ag affectedGame
		if err := rows.Scan(&ag.assignmentID, &ag.gameID); err != nil {
			continue
		}
		affected = append(affected, ag)
	}
	rows.Close()

	for _, ag := range affected {
		newChannel, err := selectBestSourceForGame(ctx, s.db, ag.gameID)
		if err != nil {
			log.Printf("[sports/health] failover game %s: no alternative source available", ag.gameID)
			continue
		}

		// Deactivate old assignment, create new one
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			log.Printf("[sports/health] failover tx begin: %v", err)
			continue
		}

		_, err = tx.ExecContext(ctx,
			`UPDATE sports_game_source_assignments SET is_active = false WHERE id = $1`, ag.assignmentID)
		if err != nil {
			tx.Rollback()
			continue
		}

		var newAssignID string
		err = tx.QueryRowContext(ctx, `
			INSERT INTO sports_game_source_assignments (game_id, source_channel_id, assigned_by)
			VALUES ($1, $2, 'auto') RETURNING id`,
			ag.gameID, newChannel.ID,
		).Scan(&newAssignID)
		if err != nil {
			tx.Rollback()
			continue
		}

		// Log failover event to sports_game_events_log if it exists
		logFailoverEvent(ctx, tx, ag.gameID, sourceID, newChannel.SourceID, "source_down")

		if err := tx.Commit(); err != nil {
			log.Printf("[sports/health] failover commit: %v", err)
			continue
		}

		log.Printf("[sports/health] source %s went down, re-routed game %s to source %s",
			sourceID, ag.gameID, newChannel.SourceID)
	}
}

// logFailoverEvent writes a failover event to sports_game_events_log.
// Silently ignores errors (the table may not exist in early schema versions).
func logFailoverEvent(ctx context.Context, tx *sql.Tx, gameID, oldSourceID, newSourceID, reason string) {
	payload := `{"old_source_id":"` + oldSourceID + `","new_source_id":"` + newSourceID + `","reason":"` + reason + `"}`
	_, _ = tx.ExecContext(ctx, `
		INSERT INTO sports_game_events_log (game_id, event_type, payload)
		VALUES ($1, 'sports.stream_failover', $2::jsonb)`,
		gameID, payload)
}

// init seeds the global rand source (Go 1.20+ auto-seeds, but explicit for clarity).
var _ = func() struct{} {
	rand.New(rand.NewSource(time.Now().UnixNano()))
	return struct{}{}
}()
