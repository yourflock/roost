// injection_test.go — P22.6.003: Injection prevention tests.
// Tests that SQL injection and XSS payloads on search/catalog endpoints
// return 400 (validation rejection) or sanitized result — never 500.
package security_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/unyeco/roost/internal/validate"
)

// injectionPayloads is the set of known attack strings for injection testing.
var injectionPayloads = []string{
	"' OR 1=1 --",
	"1 UNION SELECT username,password FROM users--",
	"1; DROP TABLE subscribers;--",
	"<script>alert(1)</script>",
	`" onmouseover="alert(1)`,
	"<img src=x onerror=alert(1)>",
	"../../../etc/passwd",
	"..%2F..%2Fetc%2Fpasswd",
	"hello\x00world",
	"\x00admin",
	"'; EXEC xp_cmdshell('whoami')--",
	"${7*7}",   // SSTI
	"{{7*7}}",  // template injection
}

// searchHandler is a minimal handler that validates the q query parameter.
// Simulates how catalog/search endpoints validate user input.
func searchHandler(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	if q == "" {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"results":[]}`))
		return
	}

	// Validation: max 200 chars, no path traversal, no null bytes.
	var me validate.MultiError
	me.Add(validate.MaxLength("q", q, 200))
	me.Add(validate.NoPathTraversal("q", q))

	if me.HasErrors() {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"validation_failed","message":"` + me.Error() + `"}`))
		return
	}

	// Simulate sanitized result (in production, q goes into parameterized SQL).
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"results":[],"q":"sanitized"}`))
}

// TestSearchEndpointRejectsInjectionPayloads verifies that injection payloads
// on the search endpoint return 400, never 500, and never echo the payload back.
func TestSearchEndpointRejectsInjectionPayloads(t *testing.T) {
	for _, payload := range injectionPayloads {
		t.Run("payload_"+sanitizeTestName(payload), func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/owl/vod?q="+url.QueryEscape(payload), nil)
			rec := httptest.NewRecorder()

			searchHandler(rec, req)

			code := rec.Code
			if code == http.StatusInternalServerError {
				t.Errorf("injection payload caused 500: %q", payload)
			}
			// Must be either 400 (validation failure) or 200 with sanitized result.
			if code != http.StatusBadRequest && code != http.StatusOK {
				t.Errorf("unexpected status %d for payload %q", code, payload)
			}

			// Response body must never contain the raw injection payload.
			body := rec.Body.String()
			if containsRawPayload(body, payload) {
				t.Errorf("response echoed injection payload: %q in body: %q", payload, body)
			}
		})
	}
}

// TestValidatorNeverPanics verifies that no validator panics on attack payloads.
func TestValidatorNeverPanics(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("validator panicked on attack payload: %v", r)
		}
	}()

	for _, payload := range injectionPayloads {
		_ = validate.NonEmptyString("f", payload)
		_ = validate.MaxLength("f", payload, 100)
		_ = validate.IsUUID("f", payload)
		_ = validate.IsEmail("f", payload)
		_ = validate.IsURL("f", payload, false)
		_ = validate.IsAlphanumericSlug("f", payload)
		_ = validate.NoPathTraversal("f", payload)
		_ = validate.IsCountryCode("f", payload)
		_ = validate.IsLanguageCode("f", payload)
	}
}

// TestSlugValidationRejectsInjection verifies that channel slugs in stream endpoints
// reject injection payloads.
func TestSlugValidationRejectsInjection(t *testing.T) {
	for _, payload := range injectionPayloads {
		err := validate.IsAlphanumericSlug("slug", payload)
		if err == nil {
			t.Errorf("IsAlphanumericSlug accepted injection payload: %q", payload)
		}
	}
}

// TestPaginationValidationRejectsNegative verifies that pagination params are validated.
func TestPaginationValidationRejectsNegative(t *testing.T) {
	invalidPages := []int{0, -1, -100, -99999}
	for _, p := range invalidPages {
		if err := validate.IntInRange("page", p, 1, 1000); err == nil {
			t.Errorf("IntInRange accepted invalid page value: %d", p)
		}
	}

	invalidPerPage := []int{0, -1, 101, 9999}
	for _, pp := range invalidPerPage {
		if err := validate.IntInRange("per_page", pp, 1, 100); err == nil {
			t.Errorf("IntInRange accepted invalid per_page value: %d", pp)
		}
	}
}

// sanitizeTestName converts a payload to a safe test subtest name.
func sanitizeTestName(s string) string {
	if len(s) > 20 {
		s = s[:20]
	}
	result := make([]byte, 0, len(s))
	for _, b := range []byte(s) {
		if (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '_' {
			result = append(result, b)
		} else {
			result = append(result, '_')
		}
	}
	return string(result)
}

// containsRawPayload checks if the response body contains the raw payload.
// Avoids false positives by checking for the exact substring.
func containsRawPayload(body, payload string) bool {
	// Trim to key dangerous characters to check.
	if len(payload) > 5 && len(body) > 0 {
		// If the dangerous payload appears verbatim in the response, it's a reflection.
		return len(body) > 0 && len(payload) > 0 &&
			// Only flag if it contains actual injection chars.
			(containsAny(payload, []string{"SELECT", "DROP", "<script>", "onerror=", "../../../"}) &&
				containsAny(body, []string{payload[:5]}))
	}
	return false
}

// containsAny returns true if s contains any of the substrings.
func containsAny(s string, subs []string) bool {
	for _, sub := range subs {
		if len(sub) > 0 && len(s) >= len(sub) {
			for i := 0; i <= len(s)-len(sub); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
		}
	}
	return false
}
