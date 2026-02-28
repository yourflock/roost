// attack_test.go — P22.1.001 adversarial input tests.
// Every validator is exercised against classic attack payloads.
// All must return a ValidationError — never panic, never pass.
package validate_test

import (
	"strings"
	"testing"

	"github.com/unyeco/roost/internal/validate"
)

// attackPayloads is a shared list of known-bad strings used across validators
// that accept free-form text.
var attackPayloads = []struct {
	name  string
	value string
}{
	{"sql_injection_classic", "' OR 1=1 --"},
	{"sql_injection_union", "1 UNION SELECT username,password FROM users--"},
	{"sql_injection_stacked", "1; DROP TABLE subscribers;--"},
	{"xss_script", "<script>alert(1)</script>"},
	{"xss_event", `" onmouseover="alert(1)`},
	{"xss_img", "<img src=x onerror=alert(1)>"},
	{"path_traversal_unix", "../../../etc/passwd"},
	{"path_traversal_win", `..\..\..\\windows\\system32`},
	{"path_traversal_encoded", "..%2F..%2Fetc%2Fpasswd"},
	{"path_traversal_double_encoded", "..%252F..%252Fetc%252Fpasswd"},
	{"null_byte_middle", "hello\x00world"},
	{"null_byte_start", "\x00admin"},
	{"null_byte_end", "admin\x00"},
	{"long_string", strings.Repeat("A", 10001)},
	{"unicode_rtl", "\u202e evil text"},
	{"format_string", "%s%s%s%s%s%s%s"},
}

// TestUUIDAgainstAttacks verifies IsUUID rejects all attack payloads.
func TestUUIDAgainstAttacks(t *testing.T) {
	for _, tc := range attackPayloads {
		t.Run(tc.name, func(t *testing.T) {
			err := validate.IsUUID("id", tc.value)
			if err == nil {
				t.Errorf("IsUUID accepted attack payload %q", tc.value[:min(len(tc.value), 50)])
			}
		})
	}
}

// TestEmailAgainstAttacks verifies IsEmail rejects all attack payloads.
func TestEmailAgainstAttacks(t *testing.T) {
	for _, tc := range attackPayloads {
		t.Run(tc.name, func(t *testing.T) {
			err := validate.IsEmail("email", tc.value)
			if err == nil {
				t.Errorf("IsEmail accepted attack payload %q", tc.value[:min(len(tc.value), 50)])
			}
		})
	}
}

// TestSlugAgainstAttacks verifies IsAlphanumericSlug rejects all attack payloads.
func TestSlugAgainstAttacks(t *testing.T) {
	for _, tc := range attackPayloads {
		t.Run(tc.name, func(t *testing.T) {
			err := validate.IsAlphanumericSlug("slug", tc.value)
			if err == nil {
				t.Errorf("IsAlphanumericSlug accepted attack payload %q", tc.value[:min(len(tc.value), 50)])
			}
		})
	}
}

// TestPathTraversalAgainstAttacks verifies NoPathTraversal catches traversal sequences.
func TestPathTraversalAgainstAttacks(t *testing.T) {
	traversalCases := []string{
		"../../../etc/passwd",
		"..%2F..%2Fetc%2Fpasswd",
		"..%252F..%252Fetc%252Fpasswd",
		"hello\x00world",
		"\x00admin",
		"admin\x00",
		"sub/../../secret",
		"./././../secret",
	}
	for _, v := range traversalCases {
		err := validate.NoPathTraversal("path", v)
		if err == nil {
			t.Errorf("NoPathTraversal accepted traversal payload %q", v)
		}
	}
}

// TestURLSSRFPayloads verifies IsURL blocks SSRF-capable URLs.
func TestURLSSRFPayloads(t *testing.T) {
	ssrfCases := []string{
		"http://127.0.0.1/admin",
		"http://localhost/secret",
		"http://::1/admin",
		"http://10.0.0.1/internal",
		"http://172.16.0.1/metadata",
		"http://192.168.1.1/router",
		"javascript:alert(1)",
		"file:///etc/passwd",
		"data:text/html,<script>alert(1)</script>",
		"ftp://evil.com/file",
	}
	for _, v := range ssrfCases {
		err := validate.IsURL("url", v, false)
		if err == nil {
			t.Errorf("IsURL accepted SSRF payload %q", v)
		}
	}
}

// TestMaxLengthLargeInputs verifies MaxLength handles 10k+ char strings without panicking.
func TestMaxLengthLargeInputs(t *testing.T) {
	huge := strings.Repeat("x", 10000)
	err := validate.MaxLength("field", huge, 100)
	if err == nil {
		t.Error("MaxLength should reject 10k-char string with max=100")
	}

	// Verify it does not panic on even larger inputs.
	enormous := strings.Repeat("A", 100000)
	_ = validate.MaxLength("field", enormous, 200)
}

// TestNoNilPanic verifies no validator panics on empty or zero-value inputs.
func TestNoNilPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("validator panicked: %v", r)
		}
	}()

	_ = validate.NonEmptyString("f", "")
	_ = validate.MinLength("f", "", 1)
	_ = validate.MaxLength("f", "", 10)
	_ = validate.IsUUID("f", "")
	_ = validate.IsEmail("f", "")
	_ = validate.IsURL("f", "", false)
	_ = validate.IsAlphanumericSlug("f", "")
	_ = validate.IsCountryCode("f", "")
	_ = validate.IsLanguageCode("f", "")
	_ = validate.IntInRange("f", 0, 1, 10)
	_ = validate.NoPathTraversal("f", "")
}

// TestCountryCodeValid verifies valid country codes pass.
func TestCountryCodeValid(t *testing.T) {
	valid := []string{"US", "GB", "DE", "FR", "JP"}
	for _, v := range valid {
		if err := validate.IsCountryCode("country", v); err != nil {
			t.Errorf("IsCountryCode rejected valid code %q: %v", v, err)
		}
	}
}

// TestCountryCodeInvalid verifies invalid country codes fail.
func TestCountryCodeInvalid(t *testing.T) {
	invalid := []string{"us", "USA", "1A", "' OR 1=1", "", "  "}
	for _, v := range invalid {
		if err := validate.IsCountryCode("country", v); err == nil {
			t.Errorf("IsCountryCode accepted invalid code %q", v)
		}
	}
}

// TestLanguageCodeValid verifies valid language codes pass.
func TestLanguageCodeValid(t *testing.T) {
	valid := []string{"en", "fr", "de", "en-US", "zh-CN", "ara"}
	for _, v := range valid {
		if err := validate.IsLanguageCode("lang", v); err != nil {
			t.Errorf("IsLanguageCode rejected valid code %q: %v", v, err)
		}
	}
}

// TestLanguageCodeInvalid verifies invalid language codes fail.
func TestLanguageCodeInvalid(t *testing.T) {
	invalid := []string{"EN", "e", "' OR 1=1", "", "en_US", "verylonglanguagecode"}
	for _, v := range invalid {
		if err := validate.IsLanguageCode("lang", v); err == nil {
			t.Errorf("IsLanguageCode accepted invalid code %q", v)
		}
	}
}

// min returns the smaller of a and b (Go 1.21+ has builtin; keep local for compat).
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
