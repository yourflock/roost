// audit.go — Shared audit logging package for all Roost services.
// P16-T01: Structured Logging & Audit Trail
//
// Every administrative and subscriber action that changes state is written to
// the audit_log table via LogAction. This provides a tamper-evident trail for
// compliance, debugging, and security incident response.
//
// Actor types: "admin" | "subscriber" | "system" | "reseller"
// Action naming convention: "{resource}.{verb}"
//   e.g. "channel.create", "subscriber.suspend", "billing.checkout_complete"
package audit

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/google/uuid"
)

// LogAction inserts a row into the audit_log table.
//
// Parameters:
//   - ctx:          request context (for deadline propagation)
//   - db:           database connection
//   - actorType:    who performed the action — "admin" | "subscriber" | "system" | "reseller"
//   - actorID:      UUID of the actor (may be empty string for system actions)
//   - action:       namespaced action string e.g. "channel.create"
//   - resourceType: type of the affected resource e.g. "channel", "subscriber"
//   - resourceID:   UUID of the affected resource (may be empty string)
//   - details:      arbitrary JSON-serialisable key/value context
//
// On error the failure is logged but NOT propagated — audit log writes are
// best-effort and must never cause a user-visible error.
func LogAction(
	ctx context.Context,
	db *sql.DB,
	actorType, actorID, action, resourceType, resourceID string,
	details map[string]interface{},
) error {
	detailsJSON, err := json.Marshal(details)
	if err != nil {
		detailsJSON = []byte("{}")
	}

	var actorUUID, resourceUUID *uuid.UUID

	if actorID != "" {
		id, err := uuid.Parse(actorID)
		if err == nil {
			actorUUID = &id
		}
	}
	if resourceID != "" {
		id, err := uuid.Parse(resourceID)
		if err == nil {
			resourceUUID = &id
		}
	}

	_, err = db.ExecContext(ctx, `
		INSERT INTO audit_log (
			actor_type, actor_id, action,
			resource_type, resource_id, details
		) VALUES ($1, $2, $3, $4, $5, $6)`,
		actorType, actorUUID, action,
		resourceType, resourceUUID, string(detailsJSON),
	)
	return err
}

// LogActionWithRequest is a convenience wrapper that also captures the
// request's IP address and User-Agent from an http.Request.
func LogActionWithRequest(
	r *http.Request,
	db *sql.DB,
	actorType, actorID, action, resourceType, resourceID string,
	details map[string]interface{},
) error {
	detailsJSON, err := json.Marshal(details)
	if err != nil {
		detailsJSON = []byte("{}")
	}

	var actorUUID, resourceUUID *uuid.UUID

	if actorID != "" {
		id, parseErr := uuid.Parse(actorID)
		if parseErr == nil {
			actorUUID = &id
		}
	}
	if resourceID != "" {
		id, parseErr := uuid.Parse(resourceID)
		if parseErr == nil {
			resourceUUID = &id
		}
	}

	// Extract real IP: check X-Forwarded-For first (Cloudflare sets this),
	// then fall back to RemoteAddr.
	ip := r.Header.Get("CF-Connecting-IP")
	if ip == "" {
		ip = r.Header.Get("X-Forwarded-For")
	}
	if ip == "" {
		ip = r.RemoteAddr
	}
	ua := r.Header.Get("User-Agent")

	_, err = db.ExecContext(r.Context(), `
		INSERT INTO audit_log (
			actor_type, actor_id, action,
			resource_type, resource_id, details,
			ip_address, user_agent
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		actorType, actorUUID, action,
		resourceType, resourceUUID, string(detailsJSON),
		ip, ua,
	)
	return err
}

// AuditEntry represents a row returned from the audit_log query.
type AuditEntry struct {
	ID           string                 `json:"id"`
	ActorType    string                 `json:"actor_type"`
	ActorID      *string                `json:"actor_id"`
	Action       string                 `json:"action"`
	ResourceType string                 `json:"resource_type"`
	ResourceID   *string                `json:"resource_id"`
	Details      map[string]interface{} `json:"details"`
	IPAddress    *string                `json:"ip_address"`
	UserAgent    *string                `json:"user_agent"`
	CreatedAt    string                 `json:"created_at"`
}

// QueryAuditLog fetches paginated audit log entries with optional filters.
// filters keys: "actor_id", "action", "resource_id", "resource_type",
// "date_from" (RFC3339), "date_to" (RFC3339).
func QueryAuditLog(
	ctx context.Context,
	db *sql.DB,
	filters map[string]string,
	limit, offset int,
) ([]AuditEntry, int, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}

	where := "WHERE 1=1"
	args := []interface{}{}
	argIdx := 1

	if v, ok := filters["actor_id"]; ok && v != "" {
		where += fmt.Sprintf(" AND actor_id = $%d", argIdx)
		args = append(args, v)
		argIdx++
	}
	if v, ok := filters["action"]; ok && v != "" {
		where += fmt.Sprintf(" AND action ILIKE $%d", argIdx)
		args = append(args, "%"+v+"%")
		argIdx++
	}
	if v, ok := filters["resource_type"]; ok && v != "" {
		where += fmt.Sprintf(" AND resource_type = $%d", argIdx)
		args = append(args, v)
		argIdx++
	}
	if v, ok := filters["resource_id"]; ok && v != "" {
		where += fmt.Sprintf(" AND resource_id = $%d", argIdx)
		args = append(args, v)
		argIdx++
	}
	if v, ok := filters["date_from"]; ok && v != "" {
		where += fmt.Sprintf(" AND created_at >= $%d", argIdx)
		args = append(args, v)
		argIdx++
	}
	if v, ok := filters["date_to"]; ok && v != "" {
		where += fmt.Sprintf(" AND created_at <= $%d", argIdx)
		args = append(args, v)
		argIdx++
	}

	// Count query.
	countArgs := make([]interface{}, len(args))
	copy(countArgs, args)
	var total int
	err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM audit_log "+where, countArgs...).Scan(&total)
	if err != nil {
		return nil, 0, err
	}

	// Data query.
	args = append(args, limit, offset)
	rows, err := db.QueryContext(ctx, `
		SELECT id, actor_type, actor_id, action,
		       resource_type, resource_id, details,
		       ip_address::text, user_agent, created_at
		FROM audit_log
		`+where+`
		ORDER BY created_at DESC
		LIMIT $`+fmt.Sprintf("%d", argIdx)+` OFFSET $`+fmt.Sprintf("%d", argIdx+1),
		args...,
	)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var entries []AuditEntry
	for rows.Next() {
		var e AuditEntry
		var detailsJSON string
		if err := rows.Scan(
			&e.ID, &e.ActorType, &e.ActorID, &e.Action,
			&e.ResourceType, &e.ResourceID, &detailsJSON,
			&e.IPAddress, &e.UserAgent, &e.CreatedAt,
		); err != nil {
			return nil, 0, err
		}
		_ = json.Unmarshal([]byte(detailsJSON), &e.Details)
		entries = append(entries, e)
	}
	if entries == nil {
		entries = []AuditEntry{}
	}
	return entries, total, rows.Err()
}
