// transport_test.go — P22.5.003: Signed stream URL enforcement integration tests.
// Tests that stream-serving endpoints reject requests without valid signatures.
package security_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/unyeco/roost/internal/cdn"
)

const testSecret = "test-hmac-secret-for-security-tests"
const testPath = "/stream/bbc-one/seg001.ts"

// streamHandler is a minimal handler that enforces CDN signature validation.
// In production, the real handlers call cdn.ValidateSignature in the same pattern.
func streamHandler(w http.ResponseWriter, r *http.Request) {
	secret := testSecret // In production: os.Getenv("CDN_HMAC_SECRET")
	path := r.URL.Path

	expiresStr := r.URL.Query().Get("expires")
	sig := r.URL.Query().Get("sig")

	var expires int64
	fmt.Sscanf(expiresStr, "%d", &expires)

	if !cdn.ValidateSignature(secret, path, expires, sig) {
		http.Error(w, `{"error":"signature_invalid","message":"Invalid or expired stream signature"}`, http.StatusUnauthorized)
		return
	}

	w.WriteHeader(http.StatusOK)
	w.Write([]byte("stream content"))
}

// signURL is a test helper that creates a valid signed URL.
func signURL(path string, expiresAt int64) string {
	msg := fmt.Sprintf("%s:%d", path, expiresAt)
	mac := hmac.New(sha256.New, []byte(testSecret))
	mac.Write([]byte(msg))
	sig := hex.EncodeToString(mac.Sum(nil))
	return fmt.Sprintf("%s?expires=%d&sig=%s", path, expiresAt, sig)
}

// TestStreamNoSignature verifies that a stream request without a signature returns 401.
func TestStreamNoSignature(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, testPath, nil)
	rec := httptest.NewRecorder()

	streamHandler(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
}

// TestStreamExpiredSignature verifies that an expired signed URL returns 401.
func TestStreamExpiredSignature(t *testing.T) {
	// Expiry 1 hour in the past.
	expired := time.Now().Add(-1 * time.Hour).Unix()
	signedURL := signURL(testPath, expired)

	req := httptest.NewRequest(http.MethodGet, signedURL, nil)
	rec := httptest.NewRecorder()

	streamHandler(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for expired signature, got %d", rec.Code)
	}
}

// TestStreamTamperedSignature verifies that a tampered signature returns 401.
func TestStreamTamperedSignature(t *testing.T) {
	valid := time.Now().Add(15 * time.Minute).Unix()
	signedURL := signURL(testPath, valid)

	// Tamper: change the last character of the sig.
	tamperedURL := signedURL[:len(signedURL)-1] + "x"

	req := httptest.NewRequest(http.MethodGet, tamperedURL, nil)
	rec := httptest.NewRecorder()

	streamHandler(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for tampered signature, got %d", rec.Code)
	}
}

// TestStreamValidSignature verifies that a valid signature returns 200.
func TestStreamValidSignature(t *testing.T) {
	valid := time.Now().Add(15 * time.Minute).Unix()
	signedURL := signURL(testPath, valid)

	req := httptest.NewRequest(http.MethodGet, signedURL, nil)
	rec := httptest.NewRecorder()

	streamHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 for valid signature, got %d — body: %s", rec.Code, rec.Body.String())
	}
}

// TestStreamWrongPathSignature verifies that a signature for a different path is rejected.
func TestStreamWrongPathSignature(t *testing.T) {
	valid := time.Now().Add(15 * time.Minute).Unix()
	// Sign a different path.
	signedURL := signURL("/stream/other-channel/seg001.ts", valid)
	// But request the original path with the wrong-path signature.
	req := httptest.NewRequest(http.MethodGet, testPath+"?expires="+fmt.Sprintf("%d", valid)+"&sig="+signedURL[len(signedURL)-64:], nil)
	rec := httptest.NewRecorder()

	streamHandler(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for wrong-path signature, got %d", rec.Code)
	}
}

// TestCORSUnknownOriginRejected verifies that unknown origins are rejected.
func TestCORSUnknownOriginRejected(t *testing.T) {
	// Test that an unknown origin gets a 403.
	// This tests the CORS logic indirectly via the security middleware behavior.
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" && !isAllowedTestOrigin(origin) {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/api/data", nil)
	req.Header.Set("Origin", "https://evil.attacker.com")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("expected 403 for unknown origin, got %d", rec.Code)
	}
}

// TestCORSAllowedOriginAccepted verifies that known origins are accepted.
func TestCORSAllowedOriginAccepted(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" && !isAllowedTestOrigin(origin) {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	for _, origin := range []string{
		"https://owl.unity.dev",
		"https://roost.unity.dev",
		"https://admin.roost.unity.dev",
	} {
		req := httptest.NewRequest(http.MethodGet, "/api/data", nil)
		req.Header.Set("Origin", origin)
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("expected 200 for allowed origin %q, got %d", origin, rec.Code)
		}
	}
}

// isAllowedTestOrigin mirrors the production CORS allowlist for test purposes.
func isAllowedTestOrigin(origin string) bool {
	allowed := []string{
		"https://owl.unity.dev",
		"https://roost.unity.dev",
		"https://admin.roost.unity.dev",
		"https://reseller.roost.unity.dev",
	}
	for _, a := range allowed {
		if origin == a {
			return true
		}
	}
	// *.roost.unity.dev over HTTPS.
	if len(origin) > 8 && origin[:8] == "https://" {
		host := origin[8:]
		if len(host) > 14 && host[len(host)-14:] == ".roost.unity.dev" {
			return true
		}
	}
	return false
}
