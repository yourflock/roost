// growth_test.go — Integration tests for P17 subscriber growth features.
//
// Tests: trial creation, trial abuse, promo code validation, referral code generation,
//        referral idempotency, self-referral rejection, referral claim,
//        analytics access control, analytics returns data, churn pause.
//
// Run with: POSTGRES_PASSWORD=xxx go test ./services/billing/tests/... -v -run Test
package tests

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// postGrowthJSON performs a POST request with a JSON body and returns the response.
func postGrowthJSON(mux http.Handler, path, authHeader string, body any) *httptest.ResponseRecorder {
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	return rr
}

// getGrowthAuth performs a GET request with an auth header.
func getGrowthAuth(mux http.Handler, path, authHeader string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	return rr
}

// growthEmailHash returns SHA-256 of lower(email) as hex string.
func growthEmailHash(email string) string {
	h := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(email))))
	return fmt.Sprintf("%x", h)
}

// --- P17-T01: Free Trial ---

// TestGrowthTrialStart verifies a new subscriber can start a 7-day free trial.
func TestGrowthTrialStart(t *testing.T) {
	setupTestEnv()
	srv, db := setupTestBillingServer(t)
	defer db.Close()

	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	email := fmt.Sprintf("trialstart+%d@test.com", time.Now().UnixNano())
	subID := createTestSubscriber(t, db, email)
	defer cleanupTestSubscriber(db, subID)
	defer db.Exec(`DELETE FROM trial_abuse_tracking WHERE created_at > now() - INTERVAL '5 minutes'`)

	authHeader := "Bearer " + createTestJWT(t, subID)
	rr := postGrowthJSON(mux, "/billing/trial", authHeader, nil)

	if rr.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp map[string]any
	json.NewDecoder(rr.Body).Decode(&resp)

	if tok, ok := resp["api_token"].(string); !ok || tok == "" {
		t.Error("expected non-empty api_token in response")
	}
	if resp["subscription_id"] == nil {
		t.Error("expected subscription_id in response")
	}
	if resp["trial_end"] == nil {
		t.Error("expected trial_end in response")
	}
}

// TestGrowthTrialDuplicateRejected verifies a second trial attempt is blocked.
func TestGrowthTrialDuplicateRejected(t *testing.T) {
	setupTestEnv()
	srv, db := setupTestBillingServer(t)
	defer db.Close()

	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	email := fmt.Sprintf("trialdup+%d@test.com", time.Now().UnixNano())
	subID := createTestSubscriber(t, db, email)
	defer cleanupTestSubscriber(db, subID)
	defer db.Exec(`DELETE FROM trial_abuse_tracking WHERE created_at > now() - INTERVAL '5 minutes'`)

	authHeader := "Bearer " + createTestJWT(t, subID)

	rr1 := postGrowthJSON(mux, "/billing/trial", authHeader, nil)
	if rr1.Code != http.StatusCreated {
		t.Skipf("first trial returned %d (DB may not have trial columns yet — skip)", rr1.Code)
	}

	rr2 := postGrowthJSON(mux, "/billing/trial", authHeader, nil)
	if rr2.Code == http.StatusCreated {
		t.Error("second trial should not return 201")
	}
}

// TestGrowthTrialAbuseBlockedByEmailHash verifies email-hash based abuse prevention.
func TestGrowthTrialAbuseBlockedByEmailHash(t *testing.T) {
	setupTestEnv()
	srv, db := setupTestBillingServer(t)
	defer db.Close()

	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	email := fmt.Sprintf("abusehash+%d@test.com", time.Now().UnixNano())
	subID := createTestSubscriber(t, db, email)
	defer cleanupTestSubscriber(db, subID)

	// Pre-seed the abuse tracking with this email's hash.
	hash := growthEmailHash(email)
	db.Exec(`INSERT INTO trial_abuse_tracking (email_hash) VALUES ($1)`, hash)
	defer db.Exec(`DELETE FROM trial_abuse_tracking WHERE email_hash = $1`, hash)

	authHeader := "Bearer " + createTestJWT(t, subID)
	rr := postGrowthJSON(mux, "/billing/trial", authHeader, nil)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected 403 (abuse blocked), got %d: %s", rr.Code, rr.Body.String())
	}
}

// TestGrowthTrialStatusEndpoint verifies GET /billing/trial after starting a trial.
func TestGrowthTrialStatusEndpoint(t *testing.T) {
	setupTestEnv()
	srv, db := setupTestBillingServer(t)
	defer db.Close()

	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	email := fmt.Sprintf("trialstatus+%d@test.com", time.Now().UnixNano())
	subID := createTestSubscriber(t, db, email)
	defer cleanupTestSubscriber(db, subID)
	defer db.Exec(`DELETE FROM trial_abuse_tracking WHERE created_at > now() - INTERVAL '5 minutes'`)

	authHeader := "Bearer " + createTestJWT(t, subID)
	rr1 := postGrowthJSON(mux, "/billing/trial", authHeader, nil)
	if rr1.Code != http.StatusCreated {
		t.Skipf("trial creation returned %d — skipping status check", rr1.Code)
	}

	rr2 := getGrowthAuth(mux, "/billing/trial", authHeader)
	if rr2.Code != http.StatusOK {
		t.Fatalf("expected 200 for trial status, got %d: %s", rr2.Code, rr2.Body.String())
	}

	var resp map[string]any
	json.NewDecoder(rr2.Body).Decode(&resp)
	if resp["trial_end"] == nil {
		t.Error("expected trial_end in status response")
	}
	if resp["days_remaining"] == nil {
		t.Error("expected days_remaining in status response")
	}
}

// --- P17-T02: Promotional Codes ---

// TestGrowthPromoCodeValidInDB verifies validate endpoint returns valid=true for an existing code.
func TestGrowthPromoCodeValidInDB(t *testing.T) {
	setupTestEnv()
	srv, db := setupTestBillingServer(t)
	defer db.Close()

	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	code := fmt.Sprintf("GROW%d", time.Now().UnixNano()%100000)
	_, err := db.Exec(`
		INSERT INTO promo_codes (code, discount_percent, max_redemptions, valid_from, is_active)
		VALUES ($1, 15, 100, now(), true)
	`, code)
	if err != nil {
		t.Skipf("promo_codes table not available: %v", err)
	}
	defer db.Exec(`DELETE FROM promo_codes WHERE code = $1`, code)

	rr := postGrowthJSON(mux, "/billing/promo/validate", "", map[string]any{"code": code})
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp map[string]any
	json.NewDecoder(rr.Body).Decode(&resp)
	valid, _ := resp["valid"].(bool)
	if !valid {
		t.Errorf("expected valid=true for seeded code, got: %v", resp)
	}
}

// TestGrowthPromoCodeInvalidReturnsValidFalse verifies invalid codes get valid=false, not an error.
func TestGrowthPromoCodeInvalidReturnsValidFalse(t *testing.T) {
	setupTestEnv()
	srv, db := setupTestBillingServer(t)
	defer db.Close()

	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	rr := postGrowthJSON(mux, "/billing/promo/validate", "", map[string]any{
		"code": "THISDEFINITELYNEVEREEXISTS99999",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 even for invalid code, got %d", rr.Code)
	}

	var resp map[string]any
	json.NewDecoder(rr.Body).Decode(&resp)
	valid, _ := resp["valid"].(bool)
	if valid {
		t.Error("expected valid=false for nonexistent code")
	}
}

// --- P17-T03: Referral Program ---

// TestGrowthReferralCodeCreated verifies GET /billing/referral generates a code.
func TestGrowthReferralCodeCreated(t *testing.T) {
	setupTestEnv()
	srv, db := setupTestBillingServer(t)
	defer db.Close()

	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	email := fmt.Sprintf("refcreate+%d@test.com", time.Now().UnixNano())
	subID := createTestSubscriber(t, db, email)
	defer cleanupTestSubscriber(db, subID)
	defer db.Exec(`DELETE FROM referral_codes WHERE subscriber_id = $1`, subID)

	authHeader := "Bearer " + createTestJWT(t, subID)
	rr := getGrowthAuth(mux, "/billing/referral", authHeader)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp map[string]any
	json.NewDecoder(rr.Body).Decode(&resp)
	code, ok := resp["code"].(string)
	if !ok || !strings.HasPrefix(code, "ROOST-") {
		t.Errorf("expected ROOST-XXXXX code format, got: %v", resp["code"])
	}
	if resp["link"] == nil {
		t.Error("expected link in referral response")
	}
}

// TestGrowthReferralCodeIdempotent verifies the same code is returned on repeat calls.
func TestGrowthReferralCodeIdempotent(t *testing.T) {
	setupTestEnv()
	srv, db := setupTestBillingServer(t)
	defer db.Close()

	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	email := fmt.Sprintf("refidempotent+%d@test.com", time.Now().UnixNano())
	subID := createTestSubscriber(t, db, email)
	defer cleanupTestSubscriber(db, subID)
	defer db.Exec(`DELETE FROM referral_codes WHERE subscriber_id = $1`, subID)

	authHeader := "Bearer " + createTestJWT(t, subID)
	rr1 := getGrowthAuth(mux, "/billing/referral", authHeader)
	rr2 := getGrowthAuth(mux, "/billing/referral", authHeader)

	var r1, r2 map[string]any
	json.NewDecoder(rr1.Body).Decode(&r1)
	json.NewDecoder(rr2.Body).Decode(&r2)
	if r1["code"] != r2["code"] {
		t.Errorf("expected same code on repeat calls, got %v vs %v", r1["code"], r2["code"])
	}
}

// TestGrowthReferralSelfRejected verifies self-referral returns 400.
func TestGrowthReferralSelfRejected(t *testing.T) {
	setupTestEnv()
	srv, db := setupTestBillingServer(t)
	defer db.Close()

	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	email := fmt.Sprintf("refself+%d@test.com", time.Now().UnixNano())
	subID := createTestSubscriber(t, db, email)
	defer cleanupTestSubscriber(db, subID)
	defer db.Exec(`DELETE FROM referral_codes WHERE subscriber_id = $1`, subID)

	authHeader := "Bearer " + createTestJWT(t, subID)

	// Get the code first.
	rr := getGrowthAuth(mux, "/billing/referral", authHeader)
	var codeResp map[string]any
	json.NewDecoder(rr.Body).Decode(&codeResp)
	code, _ := codeResp["code"].(string)
	if code == "" {
		t.Skip("could not get referral code — skipping self-referral test")
	}

	// Claim own code.
	rrClaim := postGrowthJSON(mux, "/billing/referral/claim", authHeader, map[string]any{"code": code})
	if rrClaim.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for self-referral, got %d: %s", rrClaim.Code, rrClaim.Body.String())
	}
}

// TestGrowthReferralListEmpty verifies empty referral list returns [] not null.
func TestGrowthReferralListEmpty(t *testing.T) {
	setupTestEnv()
	srv, db := setupTestBillingServer(t)
	defer db.Close()

	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	email := fmt.Sprintf("reflistempty+%d@test.com", time.Now().UnixNano())
	subID := createTestSubscriber(t, db, email)
	defer cleanupTestSubscriber(db, subID)

	authHeader := "Bearer " + createTestJWT(t, subID)
	rr := getGrowthAuth(mux, "/billing/referral/list", authHeader)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 for empty referral list, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp map[string]any
	json.NewDecoder(rr.Body).Decode(&resp)
	referrals, ok := resp["referrals"].([]any)
	if !ok {
		t.Error("expected referrals to be an array (not null)")
	} else if len(referrals) != 0 {
		t.Errorf("expected empty referrals, got %d items", len(referrals))
	}
}

// --- P17-T05: Analytics ---

// TestGrowthAnalyticsForbiddenForNonAdmin verifies non-admin gets 403.
func TestGrowthAnalyticsForbiddenForNonAdmin(t *testing.T) {
	setupTestEnv()
	srv, db := setupTestBillingServer(t)
	defer db.Close()

	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	email := fmt.Sprintf("nonadmin+%d@test.com", time.Now().UnixNano())
	subID := createTestSubscriber(t, db, email)
	defer cleanupTestSubscriber(db, subID)

	authHeader := "Bearer " + createTestJWT(t, subID)
	rr := getGrowthAuth(mux, "/admin/analytics/subscribers", authHeader)
	if rr.Code != http.StatusForbidden {
		t.Errorf("expected 403 for non-admin, got %d", rr.Code)
	}
}

// TestGrowthAnalyticsOKForAdmin verifies superowner can access subscriber analytics.
func TestGrowthAnalyticsOKForAdmin(t *testing.T) {
	setupTestEnv()
	srv, db := setupTestBillingServer(t)
	defer db.Close()

	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	email := fmt.Sprintf("adminanalytics+%d@test.com", time.Now().UnixNano())
	subID := createTestSubscriber(t, db, email)
	db.Exec(`UPDATE subscribers SET is_superowner = TRUE WHERE id = $1`, subID)
	defer cleanupTestSubscriber(db, subID)

	authHeader := "Bearer " + createTestJWT(t, subID)
	rr := getGrowthAuth(mux, "/admin/analytics/subscribers", authHeader)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 for admin analytics, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp map[string]any
	json.NewDecoder(rr.Body).Decode(&resp)
	if _, ok := resp["days"]; !ok {
		t.Error("expected 'days' key in analytics response")
	}
	if _, ok := resp["latest_mrr_cents"]; !ok {
		t.Error("expected 'latest_mrr_cents' key in analytics response")
	}
}

// --- P17-T06: Churn Prevention ---

// TestGrowthPauseNoSubReturns404 verifies pause with no active subscription returns 404.
func TestGrowthPauseNoSubReturns404(t *testing.T) {
	setupTestEnv()
	srv, db := setupTestBillingServer(t)
	defer db.Close()

	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	email := fmt.Sprintf("pausenosub+%d@test.com", time.Now().UnixNano())
	subID := createTestSubscriber(t, db, email)
	defer cleanupTestSubscriber(db, subID)

	authHeader := "Bearer " + createTestJWT(t, subID)
	rr := getGrowthAuth(mux, "/billing/pause-subscription", authHeader)
	if rr.Code != http.StatusNotFound {
		t.Errorf("expected 404 when no active subscription, got %d: %s", rr.Code, rr.Body.String())
	}
}

// TestGrowthPauseActiveSub verifies an active subscription can be paused with a 30-day resume.
func TestGrowthPauseActiveSub(t *testing.T) {
	setupTestEnv()
	srv, db := setupTestBillingServer(t)
	defer db.Close()

	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	email := fmt.Sprintf("pauseactive+%d@test.com", time.Now().UnixNano())
	subID := createTestSubscriber(t, db, email)
	defer cleanupTestSubscriber(db, subID)

	// Insert an active subscription for this subscriber.
	_, err := db.Exec(`
		INSERT INTO subscriptions (subscriber_id, status, billing_period, cancel_at_period_end)
		VALUES ($1, 'active', 'monthly', FALSE)
	`, subID)
	if err != nil {
		t.Skipf("could not insert subscription: %v", err)
	}

	authHeader := "Bearer " + createTestJWT(t, subID)
	rr := getGrowthAuth(mux, "/billing/pause-subscription", authHeader)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 for pause, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp map[string]any
	json.NewDecoder(rr.Body).Decode(&resp)
	if resp["status"] != "paused" {
		t.Errorf("expected status=paused, got: %v", resp["status"])
	}
	if resp["resume_at"] == nil {
		t.Error("expected resume_at in pause response")
	}

	// Verify DB was updated.
	var dbStatus string
	db.QueryRow(`SELECT status FROM subscriptions WHERE subscriber_id = $1`, subID).Scan(&dbStatus)
	if dbStatus != "paused" {
		t.Errorf("expected subscription status='paused' in DB, got '%s'", dbStatus)
	}
}
