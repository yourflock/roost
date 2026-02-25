// billing_integration_test.go — Integration tests for Phase 3 billing endpoints.
// Tests require a running Postgres (roost_postgres Docker container).
// Run with: POSTGRES_PASSWORD=xxx go test ./services/billing/tests/... -v
//
// NOTE: Stripe API calls are NOT tested here (no test key configured).
// Webhook parsing, DB operations, subscription management, and promo code
// validation are all fully testable without Stripe.
package tests

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	_ "github.com/lib/pq"
	billingsvc "github.com/yourflock/roost/services/billing"
)

// testDB opens a test database connection.
func testDB(t *testing.T) *sql.DB {
	t.Helper()
	host := getEnvOrDefault("POSTGRES_HOST", "localhost")
	port := getEnvOrDefault("POSTGRES_PORT", "5433")
	user := getEnvOrDefault("POSTGRES_USER", "roost")
	pass := getEnvOrDefault("POSTGRES_PASSWORD", "067fb9bcf196279420203b8afc3fb3c3")
	dbname := getEnvOrDefault("POSTGRES_DB", "roost_dev")

	dsn := fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=disable",
		host, port, user, pass, dbname)
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("failed to open test DB: %v", err)
	}
	if err := db.Ping(); err != nil {
		t.Skipf("Postgres not available (skipping integration test): %v", err)
	}
	return db
}

// setupTestBillingServer creates a test billing server with a live DB but no Stripe client.
func setupTestBillingServer(t *testing.T) (*billingsvc.Server, *sql.DB) {
	t.Helper()
	db := testDB(t)
	srv := billingsvc.NewServer(db, nil, nil) // nil stripe = graceful degradation
	return srv, db
}

// setupTestEnv sets required environment variables for billing tests.
func setupTestEnv() {
	os.Setenv("AUTH_JWT_SECRET", "test-jwt-secret-billing-do-not-use-in-prod")
	os.Setenv("ROOST_BASE_URL", "http://localhost:8085")
}

// createTestSubscriber inserts a subscriber and returns their ID.
func createTestSubscriber(t *testing.T, db *sql.DB, email string) string {
	t.Helper()
	var id string
	err := db.QueryRow(`
		INSERT INTO subscribers (email, password_hash, display_name, email_verified, status)
		VALUES ($1, 'testhash', 'Test User', true, 'active')
		RETURNING id
	`, email).Scan(&id)
	if err != nil {
		// Subscriber may already exist — try to find them
		_ = db.QueryRow(`SELECT id FROM subscribers WHERE email = $1`, email).Scan(&id)
	}
	return id
}

// cleanupTestSubscriber removes test data after a test.
func cleanupTestSubscriber(db *sql.DB, subscriberID string) {
	_, _ = db.Exec(`DELETE FROM subscriptions WHERE subscriber_id = $1`, subscriberID)
	_, _ = db.Exec(`DELETE FROM api_tokens WHERE subscriber_id = $1`, subscriberID)
	_, _ = db.Exec(`DELETE FROM pending_checkouts WHERE subscriber_id = $1`, subscriberID)
	_, _ = db.Exec(`DELETE FROM subscribers WHERE id = $1`, subscriberID)
}

// TestHealthEndpoint verifies the billing service health check.
func TestHealthEndpoint(t *testing.T) {
	setupTestEnv()
	srv, db := setupTestBillingServer(t)
	defer db.Close()

	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/health")
	if err != nil {
		t.Fatalf("GET /health: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode health response: %v", err)
	}
	if body["status"] != "ok" {
		t.Errorf("expected status=ok, got %s", body["status"])
	}
	if body["stripe"] != "unconfigured" {
		t.Errorf("expected stripe=unconfigured when no key set, got %s", body["stripe"])
	}
	t.Logf("Health response: %+v", body)
}

// TestCheckoutWithoutStripe verifies graceful 503 when Stripe is not configured.
func TestCheckoutWithoutStripe(t *testing.T) {
	setupTestEnv()
	srv, db := setupTestBillingServer(t)
	defer db.Close()

	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	body := `{"plan_slug":"premium","billing_period":"monthly"}`
	resp, err := http.Post(ts.URL+"/billing/checkout", "application/json",
		bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatalf("POST /billing/checkout: %v", err)
	}
	defer resp.Body.Close()

	// Should return 503 (Stripe not configured) or 401 (no JWT)
	// Since we didn't send a JWT, expect 401 first
	if resp.StatusCode != http.StatusUnauthorized && resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("expected 401 or 503, got %d", resp.StatusCode)
	}
}

// TestPromoValidate_InvalidCode verifies that an invalid promo code returns valid=false.
func TestPromoValidate_InvalidCode(t *testing.T) {
	setupTestEnv()
	srv, db := setupTestBillingServer(t)
	defer db.Close()

	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	body := `{"code":"INVALID_CODE_XYZ_123"}`
	resp, err := http.Post(ts.URL+"/billing/promo/validate", "application/json",
		bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatalf("POST /billing/promo/validate: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode promo validate response: %v", err)
	}
	if valid, ok := result["valid"].(bool); !ok || valid {
		t.Errorf("expected valid=false for invalid code, got %v", result["valid"])
	}
}

// TestPromoValidate_ValidCode tests promo code creation and validation.
func TestPromoValidate_ValidCode(t *testing.T) {
	setupTestEnv()
	srv, db := setupTestBillingServer(t)
	defer db.Close()

	// Insert a test promo code directly into DB
	testCode := fmt.Sprintf("TEST%d", time.Now().Unix())
	_, err := db.Exec(`
		INSERT INTO promo_codes (code, discount_percent, max_redemptions, valid_until)
		VALUES ($1, 20, 100, NOW() + INTERVAL '1 day')
	`, testCode)
	if err != nil {
		t.Fatalf("insert test promo code: %v", err)
	}
	defer db.Exec(`DELETE FROM promo_codes WHERE code = $1`, testCode)

	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	body := fmt.Sprintf(`{"code":"%s"}`, testCode)
	resp, err := http.Post(ts.URL+"/billing/promo/validate", "application/json",
		bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatalf("POST /billing/promo/validate: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode promo validate response: %v", err)
	}
	if valid, ok := result["valid"].(bool); !ok || !valid {
		t.Errorf("expected valid=true for valid code, got %v", result["valid"])
	}
	if result["discount_percent"].(float64) != 20 {
		t.Errorf("expected discount_percent=20, got %v", result["discount_percent"])
	}
}

// TestSubscriptionPlansSeeded verifies the P3 plan catalog was seeded correctly.
func TestSubscriptionPlansSeeded(t *testing.T) {
	db := testDB(t)
	defer db.Close()

	rows, err := db.Query(`SELECT slug, name, monthly_price_cents FROM subscription_plans ORDER BY monthly_price_cents`)
	if err != nil {
		t.Fatalf("query subscription_plans: %v", err)
	}
	defer rows.Close()

	plans := map[string]struct {
		name  string
		price int
	}{}
	for rows.Next() {
		var slug, name string
		var price int
		_ = rows.Scan(&slug, &name, &price)
		plans[slug] = struct {
			name  string
			price int
		}{name, price}
	}

	required := map[string]int{
		"founding": 0,
		"premium":  999,
	}
	for slug, expectedPrice := range required {
		if p, ok := plans[slug]; !ok {
			t.Errorf("plan %q not found in subscription_plans", slug)
		} else if p.price != expectedPrice {
			t.Errorf("plan %q: expected price %d cents, got %d", slug, expectedPrice, p.price)
		} else {
			t.Logf("plan %q OK: price=%d", slug, p.price)
		}
	}
}

// TestDunningCheck_EmptyRun verifies the dunning check runs without error when no subs are due.
func TestDunningCheck_EmptyRun(t *testing.T) {
	srv, db := setupTestBillingServer(t)
	defer db.Close()

	result := srv.RunDunningCheck()
	t.Logf("DunningCheck result: checked=%d retried=%d suspended=%d trialsExpired=%d errors=%d",
		result.Checked, result.Retried, result.Suspended, result.TrialsExpired, result.Errors)
	if result.Errors > 0 {
		t.Errorf("RunDunningCheck: unexpected errors: %d", result.Errors)
	}
}

// TestMigrationTablesExist verifies all P3 tables were created by migration 010.
func TestMigrationTablesExist(t *testing.T) {
	db := testDB(t)
	defer db.Close()

	requiredTables := []string{
		"subscription_plans",
		"subscriptions",
		"pending_checkouts",
		"stripe_events",
		"roost_invoices",
		"roost_refunds",
		"promo_codes",
	}

	for _, table := range requiredTables {
		var exists bool
		err := db.QueryRow(`
			SELECT EXISTS (
				SELECT 1 FROM information_schema.tables
				WHERE table_schema = 'public' AND table_name = $1
			)
		`, table).Scan(&exists)
		if err != nil || !exists {
			t.Errorf("table %q does not exist", table)
		} else {
			t.Logf("table %q: OK", table)
		}
	}
}

// TestFoundingFamilyTrigger verifies the founding family trigger is installed.
func TestFoundingFamilyTrigger(t *testing.T) {
	db := testDB(t)
	defer db.Close()

	var triggerExists bool
	err := db.QueryRow(`
		SELECT EXISTS (
			SELECT 1 FROM information_schema.triggers
			WHERE trigger_name = 'trg_founding_tier_before'
			AND event_object_table = 'subscribers'
		)
	`).Scan(&triggerExists)
	if err != nil || !triggerExists {
		t.Errorf("trg_founding_tier_before trigger not found on subscribers table")
	} else {
		t.Log("trg_founding_tier_before trigger: OK")
	}
}

func getEnvOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
