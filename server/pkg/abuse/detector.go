// detector.go — In-process abuse detection for Roost stream endpoints.
// P16-T05: Abuse Detection
//
// Primary detection: shared token / credential sharing.
// A single IP address using multiple different subscriber tokens within 1 hour
// is a strong signal that a token was shared publicly (e.g. on a piracy forum).
// When detected, an AbuseEvent is recorded and the subscriber is flagged for
// admin review.
package abuse

import (
	"context"
	"database/sql"
	"encoding/json"
	"log"
	"sync"
	"time"
)

// AbuseEvent represents a detected abuse incident.
type AbuseEvent struct {
	// Type classifies the detection:
	//   "shared_token"   — 1 IP, multiple subscriber tokens within 1 hour
	//   "rate_exceeded"  — sustained streams exceeding allowed concurrent count
	//   "geo_anomaly"    — streams from geographically impossible locations
	Type string

	// SubscriberID is the primary subscriber being flagged.
	SubscriberID string

	// IP is the source IP address that triggered detection.
	IP string

	// Details contains event-specific context for the admin review UI.
	Details map[string]interface{}

	DetectedAt time.Time
}

// ── Shared Token Detector ─────────────────────────────────────────────────────

// ipWindow tracks subscriber IDs seen from a given IP within a sliding window.
type ipWindow struct {
	mu          sync.Mutex
	subscribers map[string]time.Time // subscriberID → last seen
}

// sharedTokenDetector tracks {ip → subscriber_ids} in a 1-hour sliding window.
type sharedTokenDetector struct {
	mu        sync.Mutex
	windows   map[string]*ipWindow
	threshold int           // distinct tokens before flagging
	ttl       time.Duration // sliding window width
}

// defaultDetector is the global singleton used by DetectSharedToken.
// Shared across all goroutines. Thread-safe.
var defaultDetector = &sharedTokenDetector{
	windows:   make(map[string]*ipWindow),
	threshold: 3,        // >3 distinct subscriber IDs from 1 IP = shared token
	ttl:       time.Hour,
}

// DetectSharedToken checks whether the given (ip, subscriberID) pair indicates
// token sharing. Returns (true, event) if abuse is detected.
// Thread-safe. Call on every POST /owl/stream/:slug request.
func DetectSharedToken(ip, subscriberID string) (bool, *AbuseEvent) {
	return defaultDetector.check(ip, subscriberID)
}

func (d *sharedTokenDetector) check(ip, subscriberID string) (bool, *AbuseEvent) {
	d.mu.Lock()
	win, ok := d.windows[ip]
	if !ok {
		win = &ipWindow{subscribers: make(map[string]time.Time)}
		d.windows[ip] = win
	}
	d.mu.Unlock()

	win.mu.Lock()
	defer win.mu.Unlock()

	now := time.Now()
	cutoff := now.Add(-d.ttl)

	// Prune stale entries from the window.
	for id, t := range win.subscribers {
		if t.Before(cutoff) {
			delete(win.subscribers, id)
		}
	}

	// Record this subscriber for this IP.
	win.subscribers[subscriberID] = now

	// Check whether we've exceeded the threshold.
	if len(win.subscribers) > d.threshold {
		ids := make([]string, 0, len(win.subscribers))
		for id := range win.subscribers {
			ids = append(ids, id)
		}
		return true, &AbuseEvent{
			Type:         "shared_token",
			SubscriberID: subscriberID,
			IP:           ip,
			Details: map[string]interface{}{
				"distinct_subscriber_ids": len(win.subscribers),
				"subscriber_ids_sample":   ids,
				"window_hours":            int(d.ttl.Hours()),
				"threshold":               d.threshold,
			},
			DetectedAt: now,
		}
	}
	return false, nil
}

// ── Abuse Event Persistence ───────────────────────────────────────────────────

// RecordAbuseEvent writes an abuse event to audit_log and upserts a row into
// flagged_subscribers. Non-blocking: errors are logged but not propagated to
// callers — abuse recording must never cause a user-visible error.
func RecordAbuseEvent(ctx context.Context, db *sql.DB, event AbuseEvent) error {
	if db == nil {
		return nil
	}

	detailsJSON := "{}"
	if event.Details != nil {
		if b, err := json.Marshal(event.Details); err == nil {
			detailsJSON = string(b)
		}
	}

	// Write to audit_log.
	_, auditErr := db.ExecContext(ctx, `
		INSERT INTO audit_log (
			actor_type, action,
			resource_type, resource_id, details, ip_address
		) VALUES ('system', $1, 'subscriber', $2, $3::jsonb, $4::inet)
	`,
		"abuse.detected."+event.Type,
		nullUUID(event.SubscriberID),
		detailsJSON,
		nullStr(event.IP),
	)
	if auditErr != nil {
		log.Printf("[abuse] audit_log insert failed: %v", auditErr)
	}

	// Upsert flagged_subscribers.
	if event.SubscriberID != "" {
		_, upsertErr := db.ExecContext(ctx, `
			INSERT INTO flagged_subscribers (
				subscriber_id, flag_reason, flag_details, auto_flagged
			) VALUES ($1, $2, $3::jsonb, true)
			ON CONFLICT (subscriber_id) DO UPDATE SET
				flag_reason  = EXCLUDED.flag_reason,
				flag_details = flagged_subscribers.flag_details || EXCLUDED.flag_details,
				flagged_at   = NOW()
			WHERE flagged_subscribers.resolution IS NULL
		`, event.SubscriberID, event.Type, detailsJSON)
		if upsertErr != nil {
			log.Printf("[abuse] flagged_subscribers upsert failed: %v", upsertErr)
			return upsertErr
		}
	}

	return auditErr
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// nullUUID returns nil if the string is empty (for UUID columns that allow NULL).
func nullUUID(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

// nullStr returns nil if the string is empty.
func nullStr(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}
