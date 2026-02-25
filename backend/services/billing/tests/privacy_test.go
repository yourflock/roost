// privacy_test.go — Integration tests for GDPR privacy endpoints.
// P16-T04: Privacy & GDPR Compliance
//
// These tests require a running Postgres with the data_deletion_requests table.
// Run with: POSTGRES_PASSWORD=xxx go test ./services/billing/tests/... -run TestPrivacy -v
package tests

import (
	"bytes"
	"io"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	_ "github.com/lib/pq"
	billingsvc "github.com/yourflock/roost/services/billing"
	internalauth "github.com/yourflock/roost/internal/auth"
)

// setupPrivacyTestServer creates a test billing server and HTTP test server for privacy tests.
func setupPrivacyTestServer(t *testing.T) (*httptest.Server, *billingsvc.Server, func()) {
	t.Helper()
	db := testDB(t)
	srv := billingsvc.NewServer(db, nil, nil)
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)
	ts := httptest.NewServer(mux)
	return ts, srv, func() {
		ts.Close()
		db.Close()
	}
}

// makeTestSubscriberToken creates a subscriber in the DB and returns (id, jwt).
func makeTestSubscriberToken(t *testing.T, ts *httptest.Server) (string, string) {
	t.Helper()
	db := testDB(t)
	defer db.Close()

	email := fmt.Sprintf("privacy-test-%s@example.com", uuid.New().String()[:8])
	subscriberID := createTestSubscriber(t, db, email)

	token, err := internalauth.GenerateAccessToken(mustParseUUID(t, subscriberID), true)
	if err != nil {
		t.Fatalf("GenerateAccessToken: %v", err)
	}
	return subscriberID, token
}

func mustParseUUID(t *testing.T, s string) uuid.UUID {
	t.Helper()
	id, err := uuid.Parse(s)
	if err != nil {
		t.Fatalf("uuid.Parse(%q): %v", s, err)
	}
	return id
}

// TestDeletionRequestCreation verifies POST /account/delete creates a pending request.
func TestDeletionRequestCreation(t *testing.T) {
	setupTestEnv()
	ts, _, cleanup := setupPrivacyTestServer(t)
	defer cleanup()

	subscriberID, token := makeTestSubscriberToken(t, ts)
	_ = subscriberID

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/account/delete", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /account/delete: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("expected 202, got %d: %s", resp.StatusCode, string(body))
		return
	}

	var body struct {
		RequestID           string `json:"request_id"`
		Status              string `json:"status"`
		ScheduledDeletionAt string `json:"scheduled_deletion_at"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Status != "pending" {
		t.Errorf("expected status 'pending', got %q", body.Status)
	}
	if body.RequestID == "" {
		t.Error("expected non-empty request_id")
	}

	// Verify scheduled deletion is ~30 days out.
	scheduled, err := time.Parse(time.RFC3339, body.ScheduledDeletionAt)
	if err != nil {
		t.Fatalf("parse scheduled_deletion_at: %v", err)
	}
	daysOut := time.Until(scheduled).Hours() / 24
	if daysOut < 29 || daysOut > 31 {
		t.Errorf("expected ~30 days out, got %.1f days", daysOut)
	}
}

// TestDeletionRequestDuplicate verifies that a second POST returns 409.
func TestDeletionRequestDuplicate(t *testing.T) {
	setupTestEnv()
	ts, _, cleanup := setupPrivacyTestServer(t)
	defer cleanup()

	_, token := makeTestSubscriberToken(t, ts)

	doDelete := func() int {
		req, _ := http.NewRequest(http.MethodPost, ts.URL+"/account/delete", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		resp, _ := http.DefaultClient.Do(req)
		if resp != nil {
			resp.Body.Close()
			return resp.StatusCode
		}
		return 0
	}

	if code := doDelete(); code != http.StatusAccepted {
		t.Fatalf("first request: expected 202, got %d", code)
	}
	if code := doDelete(); code != http.StatusConflict {
		t.Errorf("second request: expected 409, got %d", code)
	}
}

// TestDataExportFormat verifies GET /account/export returns required fields.
func TestDataExportFormat(t *testing.T) {
	setupTestEnv()
	ts, _, cleanup := setupPrivacyTestServer(t)
	defer cleanup()

	_, token := makeTestSubscriberToken(t, ts)

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/account/export", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /account/export: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var export map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&export); err != nil {
		t.Fatalf("decode export: %v", err)
	}

	for _, field := range []string{"export_generated_at", "subscriber_id"} {
		if _, ok := export[field]; !ok {
			t.Errorf("missing required field %q in export", field)
		}
	}

	if genAt, ok := export["export_generated_at"].(string); ok {
		if _, err := time.Parse(time.RFC3339, genAt); err != nil {
			t.Errorf("export_generated_at is not valid RFC3339: %q", genAt)
		}
	}
}

// TestDataExportUnauth verifies GET /account/export requires authentication.
func TestDataExportUnauth(t *testing.T) {
	setupTestEnv()
	ts, _, cleanup := setupPrivacyTestServer(t)
	defer cleanup()

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/account/export", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /account/export (no auth): %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", resp.StatusCode)
	}
}

// TestProcessDeletionsAdminOnly verifies admin endpoint requires superowner.
func TestProcessDeletionsAdminOnly(t *testing.T) {
	setupTestEnv()
	ts, _, cleanup := setupPrivacyTestServer(t)
	defer cleanup()

	_, token := makeTestSubscriberToken(t, ts)

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/admin/privacy/process-deletions",
		bytes.NewBufferString("{}"))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /admin/privacy/process-deletions: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403 for non-superowner, got %d", resp.StatusCode)
	}
}
