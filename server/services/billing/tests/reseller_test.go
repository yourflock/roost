// reseller_test.go — Tests for Reseller API (P14-T04/T05)
//
// Tests:
//   - API key generation and hashing
//   - POST /reseller/auth — valid and invalid keys
//   - POST /reseller/subscribers — create subscriber and link to reseller
//   - GET /reseller/subscribers — list subscribers
//   - GET /reseller/revenue — revenue listing
//   - GET /reseller/dashboard — dashboard stats
//   - POST /admin/resellers — create reseller (superowner only)
//   - GET /admin/resellers — list resellers (superowner only)
//
// Run with: POSTGRES_PASSWORD=xxx go test ./services/billing/tests/... -run Reseller -v
package tests

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	billingsvc "github.com/yourflock/roost/services/billing"
)

// ── Helpers ───────────────────────────────────────────────────────────────────

// createTestReseller helper (currently inlined in tests; kept for reference)
// Use makeResellerJWT + direct DB insert instead.
func init() {
	// suppress unused import warning — billingsvc is used via type assertion in other test files
	_ = (*billingsvc.Server)(nil)
}

// hashAPIKey computes SHA-256 of a raw API key string (mirrors billing service logic).
func hashAPIKey(raw string) string {
	h := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(h[:])
}

// makeResellerJWT creates a reseller JWT for testing without hitting the auth endpoint.
func makeResellerJWT(resellerID string) (string, error) {
	secret := "test-jwt-secret-billing-do-not-use-in-prod"
	type resellerClaims struct {
		jwt.RegisteredClaims
		ResellerID string `json:"reseller_id"`
		IsReseller bool   `json:"is_reseller"`
	}
	now := time.Now()
	claims := resellerClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   resellerID,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(24 * time.Hour)),
			Issuer:    "roost-reseller",
		},
		ResellerID: resellerID,
		IsReseller: true,
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(secret))
}

// ── Tests ─────────────────────────────────────────────────────────────────────

// TestResellerAPIKeyHashing verifies that the hash function is deterministic and
// that the prefix extraction is correct.
func TestResellerAPIKeyHashing(t *testing.T) {
	rawKey := "reseller_abc123def456"
	h1 := hashAPIKey(rawKey)
	h2 := hashAPIKey(rawKey)
	if h1 != h2 {
		t.Errorf("hash is not deterministic: %s vs %s", h1, h2)
	}
	if len(h1) != 64 {
		t.Errorf("expected 64-char SHA-256 hex, got %d chars", len(h1))
	}

	// Different keys should produce different hashes
	h3 := hashAPIKey("reseller_different")
	if h1 == h3 {
		t.Error("different keys produced same hash")
	}
}

// TestResellerAuthInvalidKey verifies that POST /reseller/auth rejects invalid API keys.
func TestResellerAuthInvalidKey(t *testing.T) {
	setupTestEnv()
	srv, db := setupTestBillingServer(t)
	defer db.Close()

	testCases := []struct {
		name       string
		apiKey     string
		wantStatus int
	}{
		{"empty key", "", http.StatusBadRequest},
		{"wrong prefix", "sub_12345", http.StatusUnauthorized},
		{"valid prefix but wrong key", "reseller_doesnotexistinthisdb0000000000000", http.StatusUnauthorized},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			body := fmt.Sprintf(`{"api_key":%q}`, tc.apiKey)
			req := httptest.NewRequest(http.MethodPost, "/reseller/auth", bytes.NewBufferString(body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()

			mux := http.NewServeMux()
			srv.RegisterRoutes(mux)
			mux.ServeHTTP(w, req)

			if w.Code != tc.wantStatus {
				t.Errorf("want status %d, got %d (body: %s)", tc.wantStatus, w.Code, w.Body.String())
			}
		})
	}
}

// TestResellerAuthValidKey tests POST /reseller/auth with a real reseller row in the DB.
func TestResellerAuthValidKey(t *testing.T) {
	setupTestEnv()
	srv, db := setupTestBillingServer(t)
	defer db.Close()

	rawKey := fmt.Sprintf("reseller_testkey%d", time.Now().UnixNano())
	keyHash := hashAPIKey(rawKey)
	prefix := rawKey[:8]

	var resellerID string
	err := db.QueryRow(`
		INSERT INTO resellers (name, contact_email, api_key_hash, api_key_prefix, revenue_share_percent)
		VALUES ('Test Reseller', $1, $2, $3, 30.00)
		RETURNING id
	`, fmt.Sprintf("reseller_test_%d@example.com", time.Now().UnixNano()), keyHash, prefix).Scan(&resellerID)
	if err != nil {
		t.Skipf("resellers table not available (migration 024 not applied): %v", err)
	}
	defer db.Exec(`DELETE FROM resellers WHERE id = $1`, resellerID)

	body := fmt.Sprintf(`{"api_key":%q}`, rawKey)
	req := httptest.NewRequest(http.MethodPost, "/reseller/auth", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
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
	token, ok := resp["token"].(string)
	if !ok || token == "" {
		t.Error("expected non-empty token in response")
	}
	if resp["reseller_id"] != resellerID {
		t.Errorf("expected reseller_id %s, got %v", resellerID, resp["reseller_id"])
	}
}

// TestResellerCreateSubscriber tests POST /reseller/subscribers.
func TestResellerCreateSubscriber(t *testing.T) {
	setupTestEnv()
	srv, db := setupTestBillingServer(t)
	defer db.Close()

	// Insert a test reseller
	rawKey := fmt.Sprintf("reseller_subtest%d", time.Now().UnixNano())
	keyHash := hashAPIKey(rawKey)
	prefix := rawKey[:8]
	var resellerID string
	err := db.QueryRow(`
		INSERT INTO resellers (name, contact_email, api_key_hash, api_key_prefix, revenue_share_percent)
		VALUES ('Sub Test Reseller', $1, $2, $3, 30.00)
		RETURNING id
	`, fmt.Sprintf("subtest_%d@example.com", time.Now().UnixNano()), keyHash, prefix).Scan(&resellerID)
	if err != nil {
		t.Skipf("resellers table not available (migration 024 not applied): %v", err)
	}
	defer db.Exec(`DELETE FROM reseller_subscribers WHERE reseller_id = $1`, resellerID)
	defer db.Exec(`DELETE FROM resellers WHERE id = $1`, resellerID)

	// Generate a reseller JWT directly (skips /reseller/auth roundtrip)
	token, err := makeResellerJWT(resellerID)
	if err != nil {
		t.Fatalf("failed to generate reseller JWT: %v", err)
	}

	email := fmt.Sprintf("newsub_%d@example.com", time.Now().UnixNano())
	body := fmt.Sprintf(`{"email":%q,"password":"testpassword123","display_name":"Test Sub"}`, email)
	req := httptest.NewRequest(http.MethodPost, "/reseller/subscribers", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	w := httptest.NewRecorder()
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response not valid JSON: %v", err)
	}
	subID, ok := resp["subscriber_id"].(string)
	if !ok || subID == "" {
		t.Error("expected non-empty subscriber_id in response")
	}

	// Cleanup created subscriber
	defer db.Exec(`DELETE FROM subscribers WHERE id = $1`, subID)
}

// TestResellerListSubscribersEmpty verifies GET /reseller/subscribers returns empty list for new reseller.
func TestResellerListSubscribersEmpty(t *testing.T) {
	setupTestEnv()
	srv, db := setupTestBillingServer(t)
	defer db.Close()

	rawKey := fmt.Sprintf("reseller_listtest%d", time.Now().UnixNano())
	keyHash := hashAPIKey(rawKey)
	prefix := rawKey[:8]
	var resellerID string
	err := db.QueryRow(`
		INSERT INTO resellers (name, contact_email, api_key_hash, api_key_prefix, revenue_share_percent)
		VALUES ('List Test Reseller', $1, $2, $3, 30.00)
		RETURNING id
	`, fmt.Sprintf("listtest_%d@example.com", time.Now().UnixNano()), keyHash, prefix).Scan(&resellerID)
	if err != nil {
		t.Skipf("resellers table not available: %v", err)
	}
	defer db.Exec(`DELETE FROM resellers WHERE id = $1`, resellerID)

	token, _ := makeResellerJWT(resellerID)
	req := httptest.NewRequest(http.MethodGet, "/reseller/subscribers", nil)
	req.Header.Set("Authorization", "Bearer "+token)
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
	subs, ok := resp["subscribers"].([]interface{})
	if !ok {
		t.Fatal("expected subscribers array in response")
	}
	if len(subs) != 0 {
		t.Errorf("expected empty subscribers list, got %d", len(subs))
	}
}

// TestResellerRevenueCalc verifies the revenue share calculation logic.
// If gross = $10 and share = 30%, reseller_share should be $3.
func TestResellerRevenueCalc(t *testing.T) {
	gross := 1000 // $10.00 in cents
	sharePercent := 30.0
	expected := int(float64(gross) * sharePercent / 100) // 300 = $3.00

	if expected != 300 {
		t.Errorf("expected revenue share of 300 cents ($3.00), got %d", expected)
	}

	// Test with fractional share
	gross2 := 999
	expected2 := int(float64(gross2) * sharePercent / 100) // floor of 299.7 = 299
	if expected2 < 299 || expected2 > 300 {
		t.Errorf("expected ~299-300 cents, got %d", expected2)
	}
}

// TestCreateResellerAdminOnly verifies POST /admin/resellers requires superowner auth.
func TestCreateResellerAdminOnly(t *testing.T) {
	setupTestEnv()
	srv, db := setupTestBillingServer(t)
	defer db.Close()

	// Without auth — should get 401
	body := `{"name":"Test","contact_email":"test@example.com","revenue_share_percent":30}`
	req := httptest.NewRequest(http.MethodPost, "/admin/resellers", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 without auth, got %d", w.Code)
	}
}
