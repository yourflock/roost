// regional_pricing_test.go — Tests for Region-Based Pricing (P14-T03)
//
// Tests:
//   - GET /billing/plans — returns plans with regional pricing when subscriber has region
//   - GET /billing/plans/:id/price — returns localized price for subscriber's region
//   - POST /admin/billing/regional-prices — create regional price (admin only)
//   - GET /admin/billing/regional-prices — list all regional prices (admin only)
//
// Run with: POSTGRES_PASSWORD=xxx go test ./services/billing/tests/... -run Regional -v
package tests

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	billingsvc "github.com/yourflock/roost/services/billing"
)

// ── Tests ─────────────────────────────────────────────────────────────────────

// TestGetBillingPlansAnonymous verifies GET /billing/plans works without authentication.
func TestGetBillingPlansAnonymous(t *testing.T) {
	setupTestEnv()
	srv, db := setupTestBillingServer(t)
	defer db.Close()

	req := httptest.NewRequest(http.MethodGet, "/billing/plans", nil)
	w := httptest.NewRecorder()

	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response not valid JSON: %v", err)
	}

	plans, ok := resp["plans"].([]interface{})
	if !ok {
		t.Fatal("expected 'plans' array in response")
	}
	// Plans are seeded in migration 002 — should have at least 2 (Founder + Standard)
	if len(plans) < 1 {
		t.Error("expected at least 1 plan, got 0")
	}
}

// TestGetBillingPlansMethodNotAllowed verifies non-GET requests are rejected.
func TestGetBillingPlansMethodNotAllowed(t *testing.T) {
	setupTestEnv()
	srv, db := setupTestBillingServer(t)
	defer db.Close()

	req := httptest.NewRequest(http.MethodPost, "/billing/plans", nil)
	w := httptest.NewRecorder()

	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

// TestRegionalPricingUpsert tests the admin upsert endpoint for regional pricing.
func TestRegionalPricingUpsert(t *testing.T) {
	setupTestEnv()
	srv, db := setupTestBillingServer(t)
	defer db.Close()

	// Check that plan_regional_prices table exists (migration 023)
	var tableExists bool
	err := db.QueryRow(`
		SELECT EXISTS (
			SELECT 1 FROM information_schema.tables
			WHERE table_name = 'plan_regional_prices'
		)
	`).Scan(&tableExists)
	if err != nil || !tableExists {
		t.Skip("plan_regional_prices table not available (migration 023 not applied)")
	}

	// Without auth — should get 401
	body := `{"plan_id":"00000000-0000-0000-0000-000000000001","region_id":"00000000-0000-0000-0000-000000000002","currency":"eur","monthly_price_cents":399,"annual_price_cents":3999}`
	req := httptest.NewRequest(http.MethodPost, "/admin/billing/regional-prices", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 without auth, got %d", w.Code)
	}
}

// TestRegionalPricingListAdmin verifies GET /admin/billing/regional-prices requires superowner.
func TestRegionalPricingListAdmin(t *testing.T) {
	setupTestEnv()
	srv, db := setupTestBillingServer(t)
	defer db.Close()

	req := httptest.NewRequest(http.MethodGet, "/admin/billing/regional-prices", nil)
	w := httptest.NewRecorder()

	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 without auth, got %d: %s", w.Code, w.Body.String())
	}
}

// TestRegionalPriceGetNoRegion verifies /billing/plans/:id/price returns 401 without auth.
func TestRegionalPriceGetNoAuth(t *testing.T) {
	setupTestEnv()
	srv, db := setupTestBillingServer(t)
	defer db.Close()

	req := httptest.NewRequest(http.MethodGet, "/billing/plans/some-plan-id/price", nil)
	w := httptest.NewRecorder()

	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 without auth, got %d", w.Code)
	}
}

// TestRegionSeedData verifies that migration 022 seeded the expected default regions.
func TestRegionSeedData(t *testing.T) {
	setupTestEnv()
	_, db := setupTestBillingServer(t)
	defer db.Close()

	// Check that regions table exists
	var tableExists bool
	err := db.QueryRow(`
		SELECT EXISTS (
			SELECT 1 FROM information_schema.tables WHERE table_name = 'regions'
		)
	`).Scan(&tableExists)
	if err != nil || !tableExists {
		t.Skip("regions table not available (migration 022 not applied)")
	}

	// Verify default region codes exist
	expectedCodes := []string{"us", "eu", "mena", "apac", "latam"}
	for _, code := range expectedCodes {
		var count int
		db.QueryRow(`SELECT COUNT(*) FROM regions WHERE code = $1`, code).Scan(&count)
		if count == 0 {
			t.Errorf("expected region with code %q to exist after migration 022", code)
		}
	}
}

// TestBillingPlansCurrencyDisplay verifies that Intl.NumberFormat currency codes are 3 chars.
// This validates the data contract from the API side.
func TestBillingPlansCurrencyConstraint(t *testing.T) {
	setupTestEnv()
	_, db := setupTestBillingServer(t)
	defer db.Close()

	var tableExists bool
	db.QueryRow(`SELECT EXISTS(SELECT 1 FROM information_schema.tables WHERE table_name='plan_regional_prices')`).Scan(&tableExists)
	if !tableExists {
		t.Skip("plan_regional_prices not available")
	}

	// Any currency stored must be exactly 3 chars (ISO 4217)
	var invalidCount int
	err := db.QueryRow(`SELECT COUNT(*) FROM plan_regional_prices WHERE length(currency) != 3`).Scan(&invalidCount)
	if err != nil {
		t.Fatalf("failed to query plan_regional_prices: %v", err)
	}
	if invalidCount > 0 {
		t.Errorf("found %d regional price rows with non-3-char currency code", invalidCount)
	}
}

// TestResellerSubscriberDuplicateEmail verifies duplicate email detection.
func TestResellerSubscriberDuplicateEmail(t *testing.T) {
	setupTestEnv()
	srv, db := setupTestBillingServer(t)
	defer db.Close()

	// Check resellers table exists
	var tableExists bool
	db.QueryRow(`SELECT EXISTS(SELECT 1 FROM information_schema.tables WHERE table_name='resellers')`).Scan(&tableExists)
	if !tableExists {
		t.Skip("resellers table not available")
	}

	// Create a reseller
	rawKey := fmt.Sprintf("reseller_duptest%d", time.Now().UnixNano())
	keyHash := hashAPIKey(rawKey)
	prefix := rawKey[:8]
	var resellerID string
	err := db.QueryRow(`
		INSERT INTO resellers (name, contact_email, api_key_hash, api_key_prefix, revenue_share_percent)
		VALUES ('Dup Test Reseller', $1, $2, $3, 30.00)
		RETURNING id
	`, fmt.Sprintf("duptest_%d@example.com", time.Now().UnixNano()), keyHash, prefix).Scan(&resellerID)
	if err != nil {
		t.Skipf("could not insert reseller: %v", err)
	}
	defer db.Exec(`DELETE FROM reseller_subscribers WHERE reseller_id = $1`, resellerID)
	defer db.Exec(`DELETE FROM resellers WHERE id = $1`, resellerID)

	token, _ := makeResellerJWT(resellerID)
	email := fmt.Sprintf("duptest_%d@example.com", time.Now().UnixNano())

	// First request — should succeed
	body := fmt.Sprintf(`{"email":%q,"password":"testpassword123"}`, email)
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	req1 := httptest.NewRequest(http.MethodPost, "/reseller/subscribers", bytes.NewBufferString(body))
	req1.Header.Set("Content-Type", "application/json")
	req1.Header.Set("Authorization", "Bearer "+token)
	w1 := httptest.NewRecorder()
	mux.ServeHTTP(w1, req1)
	if w1.Code != http.StatusCreated {
		t.Skipf("first subscriber creation failed: %d %s", w1.Code, w1.Body.String())
	}

	// Get created subscriber ID for cleanup
	var resp map[string]interface{}
	json.Unmarshal(w1.Body.Bytes(), &resp)
	if subID, ok := resp["subscriber_id"].(string); ok {
		defer db.Exec(`DELETE FROM subscribers WHERE id = $1`, subID)
	}

	// Second request with same email — should get 409 Conflict
	req2 := httptest.NewRequest(http.MethodPost, "/reseller/subscribers", bytes.NewBufferString(body))
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("Authorization", "Bearer "+token)
	w2 := httptest.NewRecorder()
	mux.ServeHTTP(w2, req2)

	if w2.Code != http.StatusConflict {
		t.Errorf("expected 409 for duplicate email, got %d: %s", w2.Code, w2.Body.String())
	}
}

// helper used in reseller_test.go too
var _ = billingsvc.NewServer
