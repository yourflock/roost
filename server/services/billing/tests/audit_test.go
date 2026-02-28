// audit_test.go — Integration tests for the audit log.
// P16-T01: Structured Logging & Audit Trail
//
// These tests require a running Postgres with the audit_log table.
// Run with: POSTGRES_PASSWORD=xxx go test ./services/billing/tests/... -run TestAudit -v
package tests

import (
	"context"
	"testing"
	"time"

	"github.com/yourflock/roost/pkg/audit"
)

// TestAuditLogInsert verifies that LogAction inserts a row into audit_log.
func TestAuditLogInsert(t *testing.T) {
	db := testDB(t)
	defer db.Close()

	ctx := context.Background()
	details := map[string]interface{}{
		"plan":   "premium",
		"period": "annual",
	}

	err := audit.LogAction(ctx, db,
		"subscriber", "00000000-0000-0000-0000-000000000001",
		"billing.checkout_complete",
		"subscription", "00000000-0000-0000-0000-000000000002",
		details,
	)
	if err != nil {
		t.Fatalf("LogAction failed: %v", err)
	}

	// Verify the row was inserted.
	var action string
	var detailsJSON string
	err = db.QueryRowContext(ctx, `
		SELECT action, details::text FROM audit_log
		WHERE actor_type = 'subscriber'
		  AND action = 'billing.checkout_complete'
		ORDER BY created_at DESC LIMIT 1
	`).Scan(&action, &detailsJSON)
	if err != nil {
		t.Fatalf("failed to query inserted audit row: %v", err)
	}
	if action != "billing.checkout_complete" {
		t.Errorf("expected action 'billing.checkout_complete', got %q", action)
	}
}

// TestAuditLogInsertSystemAction verifies system actions (no actor ID) are valid.
func TestAuditLogInsertSystemAction(t *testing.T) {
	db := testDB(t)
	defer db.Close()

	ctx := context.Background()
	err := audit.LogAction(ctx, db,
		"system", "", // empty actorID = system
		"abuse.detected.shared_token",
		"subscriber", "",
		map[string]interface{}{"ip": "1.2.3.4", "distinct_tokens": 5},
	)
	if err != nil {
		t.Fatalf("system action LogAction failed: %v", err)
	}
}

// TestAuditLogQuery verifies that QueryAuditLog returns rows with correct pagination.
func TestAuditLogQuery(t *testing.T) {
	db := testDB(t)
	defer db.Close()

	ctx := context.Background()

	// Insert 3 test entries with a unique action name for isolation.
	uniqueAction := "test.query_audit_" + time.Now().Format("150405")
	for i := 0; i < 3; i++ {
		if err := audit.LogAction(ctx, db, "admin", "", uniqueAction, "test", "", nil); err != nil {
			t.Fatalf("insert failed: %v", err)
		}
	}

	// Query with action filter.
	entries, total, err := audit.QueryAuditLog(ctx, db,
		map[string]string{"action": uniqueAction}, 10, 0)
	if err != nil {
		t.Fatalf("QueryAuditLog failed: %v", err)
	}
	if total < 3 {
		t.Errorf("expected at least 3 entries for action %q, got total=%d", uniqueAction, total)
	}
	if len(entries) < 3 {
		t.Errorf("expected at least 3 entries returned, got %d", len(entries))
	}
	for _, e := range entries {
		if e.Action != uniqueAction {
			t.Errorf("expected action %q, got %q", uniqueAction, e.Action)
		}
	}
}

// TestAuditLogPagination verifies that limit and offset work correctly.
func TestAuditLogPagination(t *testing.T) {
	db := testDB(t)
	defer db.Close()

	ctx := context.Background()
	uniqueAction := "test.pagination_" + time.Now().Format("150405")

	for i := 0; i < 5; i++ {
		if err := audit.LogAction(ctx, db, "admin", "", uniqueAction, "test", "", nil); err != nil {
			t.Fatalf("insert failed: %v", err)
		}
	}

	// Fetch first 2.
	page1, total, err := audit.QueryAuditLog(ctx, db,
		map[string]string{"action": uniqueAction}, 2, 0)
	if err != nil {
		t.Fatalf("QueryAuditLog page 1 failed: %v", err)
	}
	if len(page1) != 2 {
		t.Errorf("expected 2 entries on page 1, got %d", len(page1))
	}
	if total < 5 {
		t.Errorf("expected total >= 5, got %d", total)
	}

	// Fetch next 2.
	page2, _, err := audit.QueryAuditLog(ctx, db,
		map[string]string{"action": uniqueAction}, 2, 2)
	if err != nil {
		t.Fatalf("QueryAuditLog page 2 failed: %v", err)
	}
	if len(page2) != 2 {
		t.Errorf("expected 2 entries on page 2, got %d", len(page2))
	}

	// IDs should be different between pages.
	if len(page1) > 0 && len(page2) > 0 && page1[0].ID == page2[0].ID {
		t.Error("page 1 and page 2 returned the same first entry — pagination not working")
	}
}
