// audit_hooks.go — Audit log integration for billing service.
// P16-T01: Structured Logging & Audit Trail
//
// This file provides thin wrapper functions that call audit.LogAction after
// key billing events. They are invoked at the end of handler functions to
// avoid modifying core business logic.
//
// Pattern: call logBillingEvent() as the last non-error step of a handler.
// All calls are best-effort — failures are logged but never propagate.
package billing

import (
	"context"
	"log"
	"net/http"

	"github.com/yourflock/roost/pkg/audit"
)

// logBillingEvent records a billing event to the audit log.
// actorType is typically "subscriber" or "system".
// Non-blocking: any error is logged and discarded.
//lint:ignore U1000 pending route registration
func (s *Server) logBillingEvent(
	ctx context.Context,
	actorType, actorID, action, resourceType, resourceID string,
	details map[string]interface{},
) {
	if err := audit.LogAction(ctx, s.db, actorType, actorID, action, resourceType, resourceID, details); err != nil {
		log.Printf("[audit] WARN: failed to write audit event action=%s actor=%s: %v", action, actorID, err)
	}
}

// logBillingEventFromRequest records a billing event with IP and User-Agent from the request.
func (s *Server) logBillingEventFromRequest(
	r *http.Request,
	actorType, actorID, action, resourceType, resourceID string,
	details map[string]interface{},
) {
	if err := audit.LogActionWithRequest(r, s.db, actorType, actorID, action, resourceType, resourceID, details); err != nil {
		log.Printf("[audit] WARN: failed to write audit event action=%s actor=%s: %v", action, actorID, err)
	}
}

// logAdminAction is a convenience wrapper for superowner admin actions.
func (s *Server) logAdminAction(r *http.Request, adminID, action, resourceType, resourceID string, details map[string]interface{}) {
	s.logBillingEventFromRequest(r, "admin", adminID, action, resourceType, resourceID, details)
}

// logSubscriberAction is a convenience wrapper for subscriber-initiated actions.
func (s *Server) logSubscriberAction(r *http.Request, subscriberID, action, resourceType, resourceID string, details map[string]interface{}) {
	s.logBillingEventFromRequest(r, "subscriber", subscriberID, action, resourceType, resourceID, details)
}

// logSystemAction is a convenience wrapper for automated/system actions (e.g. Stripe webhooks).
//lint:ignore U1000 pending route registration
func (s *Server) logSystemAction(ctx context.Context, action, resourceType, resourceID string, details map[string]interface{}) {
	s.logBillingEvent(ctx, "system", "", action, resourceType, resourceID, details)
}
