// security_test.go — Unit tests for the security package.
// P16-T02: Security Hardening
package security_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/unyeco/roost/pkg/security"
)

// ── ValidateUUID ─────────────────────────────────────────────────────────────

func TestValidateUUID_Valid(t *testing.T) {
	validUUIDs := []string{
		"550e8400-e29b-41d4-a716-446655440000",
		"6ba7b810-9dad-11d1-80b4-00c04fd430c8",
		"00000000-0000-0000-0000-000000000000",
		"ffffffff-ffff-ffff-ffff-ffffffffffff",
		"FFFFFFFF-FFFF-FFFF-FFFF-FFFFFFFFFFFF", // uppercase should also work
	}
	for _, id := range validUUIDs {
		if !security.ValidateUUID(id) {
			t.Errorf("expected valid UUID: %q", id)
		}
	}
}

func TestValidateUUID_Invalid(t *testing.T) {
	invalidUUIDs := []string{
		"",
		"not-a-uuid",
		"550e8400-e29b-41d4-a716-44665544000",   // 35 chars
		"550e8400-e29b-41d4-a716-4466554400000",  // 37 chars
		"550e8400e29b41d4a716446655440000",        // missing hyphens
		"550e8400-e29b-41d4-a716-44665544000G",   // invalid hex char
		"'; DROP TABLE subscribers; --",           // SQL injection
		"<script>alert(1)</script>ffffffffffffffff", // XSS
	}
	for _, id := range invalidUUIDs {
		if security.ValidateUUID(id) {
			t.Errorf("expected invalid UUID: %q", id)
		}
	}
}

// ── SanitizeString ────────────────────────────────────────────────────────────

func TestSanitizeString_ScriptTag(t *testing.T) {
	input := `<script>alert('xss')</script>`
	got := security.SanitizeString(input)
	if got != "" {
		t.Errorf("expected empty string for script tag input, got %q", got)
	}
}

func TestSanitizeString_JavascriptURI(t *testing.T) {
	input := "javascript:alert(1)"
	got := security.SanitizeString(input)
	if got != "" {
		t.Errorf("expected empty string for javascript: URI, got %q", got)
	}
}

func TestSanitizeString_EventHandler(t *testing.T) {
	input := `Hello onclick=alert(1) World`
	got := security.SanitizeString(input)
	if strings.Contains(strings.ToLower(got), "onclick") {
		t.Errorf("expected onclick to be stripped, got %q", got)
	}
}

func TestSanitizeString_CleanInput(t *testing.T) {
	input := "Hello, World! This is a normal string."
	got := security.SanitizeString(input)
	if got != input {
		t.Errorf("expected unchanged clean input, got %q", got)
	}
}

func TestSanitizeString_Empty(t *testing.T) {
	got := security.SanitizeString("")
	if got != "" {
		t.Errorf("expected empty string, got %q", got)
	}
}

// ── SecurityHeaders Middleware ────────────────────────────────────────────────

func TestSecurityHeaders(t *testing.T) {
	handler := security.SecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	checks := map[string]string{
		"X-Frame-Options":        "DENY",
		"X-Content-Type-Options": "nosniff",
		"Referrer-Policy":        "strict-origin-when-cross-origin",
	}
	for header, expected := range checks {
		got := rr.Header().Get(header)
		if got != expected {
			t.Errorf("header %q: expected %q, got %q", header, expected, got)
		}
	}
}

// ── RequestID Middleware ──────────────────────────────────────────────────────

func TestRequestID_GeneratesID(t *testing.T) {
	handler := security.RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := security.RequestIDFromContext(r.Context())
		if id == "" {
			t.Error("expected request ID in context, got empty string")
		}
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Header().Get("X-Request-ID") == "" {
		t.Error("expected X-Request-ID header in response")
	}
}

func TestRequestID_PropagatesExistingID(t *testing.T) {
	const existingID = "my-correlation-id-123"
	handler := security.RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := security.RequestIDFromContext(r.Context())
		if id != existingID {
			t.Errorf("expected request ID %q, got %q", existingID, id)
		}
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("X-Request-ID", existingID)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
}

// ── RateLimit Middleware ──────────────────────────────────────────────────────

func TestRateLimit_AllowsUnderLimit(t *testing.T) {
	handler := security.RateLimit(100)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	for i := 0; i < 5; i++ {
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		req.RemoteAddr = "1.2.3.4:50000"
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Errorf("request %d: expected 200, got %d", i+1, rr.Code)
		}
	}
}

func TestRateLimit_BlocksOverLimit(t *testing.T) {
	// Set limit to 2 — 3rd request should be blocked.
	handler := security.RateLimit(2)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	for i := 0; i < 3; i++ {
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		req.RemoteAddr = "9.8.7.6:50000"
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if i < 2 {
			if rr.Code != http.StatusOK {
				t.Errorf("request %d should be allowed, got %d", i+1, rr.Code)
			}
		} else {
			if rr.Code != http.StatusTooManyRequests {
				t.Errorf("request %d should be rate limited (429), got %d", i+1, rr.Code)
			}
		}
	}
}
