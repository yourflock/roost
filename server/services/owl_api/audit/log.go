// Package audit provides an asynchronous, insert-only audit logger for Roost admin actions.
// Every admin handler that modifies state MUST call Log(). This is a code-review requirement.
package audit

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"time"
)

// Logger writes audit entries to the admin_audit_log table asynchronously.
// Use New() to create an instance, then pass it to admin handlers via dependency injection.
type Logger struct {
	db *sql.DB
}

// New creates an audit Logger using the provided database pool.
func New(db *sql.DB) *Logger { return &Logger{db: db} }

// Log inserts an audit entry for an admin write action asynchronously (fire-and-forget).
// The insert runs in a goroutine with a 5-second timeout independent of the request context
// (the request context may have been cancelled by the time the goroutine runs).
//
// Parameters:
//   - r:            the HTTP request (for IP address extraction)
//   - roostID:      the Roost server UUID this action applies to
//   - userID:  the user ID of the admin performing the action
//   - action:       dot-separated verb.noun, e.g. "storage.scan", "user.role_change"
//   - targetID:     optional: the ID of the affected resource (empty string = NULL)
//   - details:      optional: before/after values or extra context (nil = NULL)
//
// If the insert fails, the error is logged to stderr only — it does NOT propagate
// to the caller. The HTTP response will still be 200.
//
// NOTE: IP is extracted from r.RemoteAddr, not X-Forwarded-For. If Roost is behind a
// trusted reverse proxy (Nginx/Cloudflare), replace with the real client IP from
// X-Forwarded-For after validating the proxy is trusted.
func (l *Logger) Log(r *http.Request, roostID, userID, action, targetID string, details map[string]any) {
	var detailsJSON []byte
	if details != nil {
		detailsJSON, _ = json.Marshal(details)
	}
	// Extract IP from r.RemoteAddr — format is "IP:port"
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	if ip == "" {
		ip = r.RemoteAddr // fallback if no port
	}

	if l.db == nil {
		// No-op logger — used in tests without a real DB.
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err := l.db.ExecContext(ctx,
			`INSERT INTO admin_audit_log
			     (roost_id, user_id, action, target_id, details, ip_address)
			 VALUES ($1, $2, $3, NULLIF($4,''), $5, $6::inet)`,
			roostID, userID, action, targetID, detailsJSON, ip,
		)
		if err != nil {
			slog.Error("audit log insert failed",
				"action", action,
				"roost_id", roostID,
				"user_id", userID,
				"err", err,
			)
		}
	}()
}
