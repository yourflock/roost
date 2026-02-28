// coppa_cleanup.go — COPPA data minimization cleanup job (P22.4.002).
//
// COPPA requires that data collected from children under 13 be retained only
// as long as reasonably necessary. Roost retains kid subscriber stream events
// for a maximum of 24 hours (vs 90 days for adult subscribers).
//
// This file contains:
//   - StartKidDataCleanupCron: launches the background cleanup goroutine
//   - PurgeKidStreamEvents: deletes stream_sessions older than 24h for kid profiles
//
// Additionally, analytics events are never collected for kid profiles —
// the analytics_collector.go checks IsKidProfile() before recording any event.
package billing

import (
	"context"
	"database/sql"
	"log"
	"time"
)

// StartKidDataCleanupCron launches a background goroutine that runs the
// COPPA data minimization job once per day. Stops when ctx is cancelled.
func StartKidDataCleanupCron(ctx context.Context, db *sql.DB) {
	go func() {
		// Run immediately on startup to catch any backlog.
		if err := PurgeKidStreamEvents(ctx, db); err != nil {
			log.Printf("COPPA cleanup: initial run failed: %v", err)
		}

		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := PurgeKidStreamEvents(ctx, db); err != nil {
					log.Printf("COPPA cleanup: daily run failed: %v", err)
				}
			}
		}
	}()
}

// PurgeKidStreamEvents deletes stream_sessions older than 24 hours for kid profiles.
// Returns the number of rows deleted and any error.
func PurgeKidStreamEvents(ctx context.Context, db *sql.DB) error {
	if db == nil {
		return nil
	}

	// Delete stream_sessions for kid subscribers older than 24 hours.
	result, err := db.ExecContext(ctx, `
		DELETE FROM stream_sessions
		WHERE subscriber_id IN (
			SELECT id FROM subscribers WHERE is_kid_profile = true
		)
		AND started_at < now() - INTERVAL '24 hours'
	`)
	if err != nil {
		return err
	}

	deleted, _ := result.RowsAffected()
	if deleted > 0 {
		log.Printf("COPPA cleanup: purged %d stream_session rows for kid profiles", deleted)
	}

	// Also clean up any analytics events for kid profiles (belt-and-suspenders:
	// analytics should never be written for kids, but purge as a safety net).
	db.ExecContext(ctx, `
		DELETE FROM analytics_events
		WHERE subscriber_id IN (
			SELECT id FROM subscribers WHERE is_kid_profile = true
		)
		AND created_at < now() - INTERVAL '24 hours'
	`)

	return nil
}
