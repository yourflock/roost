// auth_integration_test.go — integration tests for Phase 2 auth endpoints.
// These tests require a running Postgres (roost_postgres Docker container).
// Run with: POSTGRES_PASSWORD=xxx go test ./services/auth/tests/... -v
package tests

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/lib/pq"
	"github.com/yourflock/roost/internal/ratelimit"
	authsvc "github.com/yourflock/roost/services/auth"
)

// testDB opens a test database connection using env vars.
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

// setupTestEnv sets required environment variables for auth tests.
func setupTestEnv() {
	os.Setenv("AUTH_JWT_SECRET", "test-jwt-secret-do-not-use-in-production")
	os.Setenv("AUTH_TOTP_KEY", "a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d2e3f4a5b6c7d8e9f0a1b2")
	os.Setenv("ROOST_BASE_URL", "http://localhost:3001")
}

// uniqueEmail generates a unique test email to avoid conflicts between runs.
func uniqueEmail() string {
	return fmt.Sprintf("test_%d@integration-test.example.com", time.Now().UnixNano())
}

// TestRegistration verifies the full registration flow.
func TestRegistration(t *testing.T) {
	setupTestEnv()
	db := testDB(t)
	defer db.Close()

	limiter := ratelimit.New(nil) // no Redis in test
	handler := authsvc.HandleRegister(db, limiter)

	t.Run("valid registration returns 201", func(t *testing.T) {
		email := uniqueEmail()
		body := fmt.Sprintf(`{"email":%q,"password":"testpass123","display_name":"Integration Test User"}`, email)

		req := httptest.NewRequest(http.MethodPost, "/auth/register", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()

		handler.ServeHTTP(w, req)

		if w.Code != http.StatusCreated {
			t.Errorf("expected 201, got %d: %s", w.Code, w.Body.String())
		}

		var resp map[string]interface{}
		json.NewDecoder(w.Body).Decode(&resp)

		if resp["email_verified"] != false {
			t.Error("new subscriber should not be email_verified")
		}
		if resp["subscriber_id"] == "" || resp["subscriber_id"] == nil {
			t.Error("subscriber_id should be non-empty")
		}

		// Verify subscriber row was created
		var count int
		db.QueryRow("SELECT COUNT(*) FROM subscribers WHERE email = $1", email).Scan(&count)
		if count != 1 {
			t.Error("subscriber row not found in DB after registration")
		}

		// Verify verification token was created
		var tokenCount int
		db.QueryRow("SELECT COUNT(*) FROM email_verification_tokens WHERE subscriber_id = (SELECT id FROM subscribers WHERE email = $1)", email).Scan(&tokenCount)
		if tokenCount != 1 {
			t.Error("email verification token not created")
		}

		// Verify password is hashed (not plaintext)
		var passwordHash string
		db.QueryRow("SELECT password_hash FROM subscribers WHERE email = $1", email).Scan(&passwordHash)
		if passwordHash == "testpass123" {
			t.Error("password stored as plaintext — must be bcrypt hash")
		}
		if !strings.HasPrefix(passwordHash, "$2a$") {
			t.Errorf("password hash should be bcrypt format, got: %q", passwordHash[:10])
		}

		// Cleanup
		defer db.Exec("DELETE FROM subscribers WHERE email = $1", email)
	})

	t.Run("weak password rejected with 400", func(t *testing.T) {
		body := `{"email":"test@example.com","password":"short","display_name":"Test"}`
		req := httptest.NewRequest(http.MethodPost, "/auth/register", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()

		handler.ServeHTTP(w, req)

		if w.Code != http.StatusBadRequest {
			t.Errorf("expected 400 for weak password, got %d", w.Code)
		}

		var resp map[string]string
		json.NewDecoder(w.Body).Decode(&resp)
		if resp["error"] != "weak_password" {
			t.Errorf("expected error=weak_password, got: %s", resp["error"])
		}
	})

	t.Run("invalid email format rejected with 400", func(t *testing.T) {
		body := `{"email":"notanemail","password":"testpass123","display_name":"Test"}`
		req := httptest.NewRequest(http.MethodPost, "/auth/register", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()

		handler.ServeHTTP(w, req)

		if w.Code != http.StatusBadRequest {
			t.Errorf("expected 400 for invalid email, got %d", w.Code)
		}
	})

	t.Run("duplicate email returns 409 with generic message", func(t *testing.T) {
		email := uniqueEmail()
		body := fmt.Sprintf(`{"email":%q,"password":"testpass123","display_name":"Test"}`, email)

		// First registration
		req1 := httptest.NewRequest(http.MethodPost, "/auth/register", strings.NewReader(body))
		req1.Header.Set("Content-Type", "application/json")
		w1 := httptest.NewRecorder()
		handler.ServeHTTP(w1, req1)

		if w1.Code != http.StatusCreated {
			t.Fatalf("first registration failed: %d %s", w1.Code, w1.Body.String())
		}

		// Duplicate registration
		req2 := httptest.NewRequest(http.MethodPost, "/auth/register", strings.NewReader(body))
		req2.Header.Set("Content-Type", "application/json")
		w2 := httptest.NewRecorder()
		handler.ServeHTTP(w2, req2)

		if w2.Code != http.StatusConflict {
			t.Errorf("expected 409 for duplicate email, got %d", w2.Code)
		}

		// Error must NOT say "email already exists" (privacy)
		var resp map[string]string
		json.NewDecoder(w2.Body).Decode(&resp)
		if strings.Contains(resp["message"], "email") && strings.Contains(resp["message"], "exists") {
			t.Error("duplicate email error reveals email existence — privacy violation")
		}

		defer db.Exec("DELETE FROM subscribers WHERE email = $1", email)
	})

	t.Run("invalid JSON returns 400", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/auth/register", strings.NewReader("not json"))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()

		handler.ServeHTTP(w, req)

		if w.Code != http.StatusBadRequest {
			t.Errorf("expected 400 for invalid JSON, got %d", w.Code)
		}
	})
}

// TestLoginErrors verifies login returns generic errors without leaking field names.
func TestLoginErrors(t *testing.T) {
	setupTestEnv()
	db := testDB(t)
	defer db.Close()

	limiter := ratelimit.New(nil)
	handler := authsvc.HandleLogin(db, limiter)

	t.Run("wrong password returns 401 with generic message", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/auth/login",
			strings.NewReader(`{"email":"test@example.com","password":"wrongpassword"}`))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()

		handler.ServeHTTP(w, req)

		if w.Code != http.StatusUnauthorized {
			t.Errorf("expected 401, got %d", w.Code)
		}

		var resp map[string]string
		json.NewDecoder(w.Body).Decode(&resp)
		if resp["error"] != "invalid_credentials" {
			t.Errorf("expected error=invalid_credentials, got %q", resp["error"])
		}
	})

	t.Run("unknown email returns same 401 as wrong password", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/auth/login",
			strings.NewReader(`{"email":"nobody_exists_xyz123@example.com","password":"testpass123"}`))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()

		handler.ServeHTTP(w, req)

		// Must be same status and error code as wrong password (timing attack prevention)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("expected 401 for unknown email, got %d", w.Code)
		}

		var resp map[string]string
		json.NewDecoder(w.Body).Decode(&resp)
		if resp["error"] != "invalid_credentials" {
			t.Errorf("expected generic error, got %q — leaks email existence", resp["error"])
		}
	})
}

// TestForgotPasswordPrivacy verifies that forgot-password always returns 200.
func TestForgotPasswordPrivacy(t *testing.T) {
	setupTestEnv()
	db := testDB(t)
	defer db.Close()

	limiter := ratelimit.New(nil)
	handler := authsvc.HandleForgotPassword(db, limiter)

	t.Run("unknown email returns 200 without error", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/auth/forgot-password",
			strings.NewReader(`{"email":"nobody_xyz@example.com"}`))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()

		handler.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("expected 200 for privacy, got %d", w.Code)
		}
	})
}

// TestResendVerificationPrivacy verifies resend always returns 200.
func TestResendVerificationPrivacy(t *testing.T) {
	setupTestEnv()
	db := testDB(t)
	defer db.Close()

	limiter := ratelimit.New(nil)
	handler := authsvc.HandleResendVerification(db, limiter)

	t.Run("unknown email returns 200", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/auth/resend-verification",
			strings.NewReader(`{"email":"nobody@example.com"}`))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()

		handler.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("expected 200 for privacy, got %d", w.Code)
		}
	})
}

func getEnvOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
