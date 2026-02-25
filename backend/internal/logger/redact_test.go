// redact_test.go — Unit tests for sensitive field redaction helpers.
// P21.7.002: Redaction tests
package logger

import (
	"strings"
	"testing"
)

// ── RedactToken ───────────────────────────────────────────────────────────────

func TestRedactToken_NormalToken(t *testing.T) {
	token := "sk_live_abcdefgh1234"
	got := RedactToken(token)
	// First 8 chars preserved.
	if !strings.HasPrefix(got, "sk_live_") {
		t.Errorf("RedactToken(%q) = %q; want prefix %q", token, got, "sk_live_")
	}
	// Rest replaced with ****.
	if !strings.HasSuffix(got, "****") {
		t.Errorf("RedactToken(%q) = %q; want suffix ****", token, got)
	}
	// Full token not present in output.
	if strings.Contains(got, "1234") {
		t.Errorf("RedactToken(%q) = %q; tail chars should be redacted", token, got)
	}
}

func TestRedactToken_ShortToken(t *testing.T) {
	// Tokens <= 8 chars show everything + appended star.
	token := "abc"
	got := RedactToken(token)
	if !strings.HasPrefix(got, "abc") {
		t.Errorf("RedactToken(%q) = %q; expected original prefix", token, got)
	}
	if !strings.HasSuffix(got, "*") {
		t.Errorf("RedactToken(%q) = %q; expected trailing *", token, got)
	}
}

func TestRedactToken_ExactlyEightChars(t *testing.T) {
	token := "12345678"
	got := RedactToken(token)
	// Exactly 8 chars → treated as short, appends *.
	if !strings.HasSuffix(got, "*") {
		t.Errorf("RedactToken(%q) = %q; expected trailing *", token, got)
	}
}

func TestRedactToken_Empty(t *testing.T) {
	got := RedactToken("")
	if got != "[empty]" {
		t.Errorf("RedactToken(%q) = %q; want [empty]", "", got)
	}
}

func TestRedactToken_LongToken_OnlyFirst8Kept(t *testing.T) {
	token := "abcdefghijklmnopqrstuvwxyz"
	got := RedactToken(token)
	if got != "abcdefgh****" {
		t.Errorf("RedactToken(%q) = %q; want %q", token, got, "abcdefgh****")
	}
}

// ── RedactEmail ───────────────────────────────────────────────────────────────

func TestRedactEmail_Standard(t *testing.T) {
	tests := []struct {
		input   string
		wantPfx string // prefix of masked local part
		domain  string
	}{
		{"alice@example.com", "a***", "example.com"},
		{"bob@test.org", "b***", "test.org"},
		{"z@z.com", "z***", "z.com"},
	}
	for _, tt := range tests {
		got := RedactEmail(tt.input)
		want := tt.wantPfx + "@" + tt.domain
		if got != want {
			t.Errorf("RedactEmail(%q) = %q; want %q", tt.input, got, want)
		}
	}
}

func TestRedactEmail_DomainPreserved(t *testing.T) {
	got := RedactEmail("user@yourflock.com")
	if !strings.Contains(got, "yourflock.com") {
		t.Errorf("RedactEmail should preserve domain, got %q", got)
	}
}

func TestRedactEmail_NoAtSign(t *testing.T) {
	got := RedactEmail("noatsign")
	if strings.Contains(got, "oatsign") {
		t.Errorf("RedactEmail(%q) = %q; original chars after first should be redacted", "noatsign", got)
	}
	if !strings.HasPrefix(got, "n") {
		t.Errorf("RedactEmail(%q) = %q; should preserve first char", "noatsign", got)
	}
}

func TestRedactEmail_Empty(t *testing.T) {
	got := RedactEmail("")
	if got != "[empty]" {
		t.Errorf("RedactEmail(%q) = %q; want [empty]", "", got)
	}
}

// ── RedactIP ──────────────────────────────────────────────────────────────────

func TestRedactIP_IPv4_LastOctetZeroed(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"192.168.1.42", "192.168.1.0"},
		{"10.0.0.1", "10.0.0.0"},
		{"172.16.254.255", "172.16.254.0"},
	}
	for _, tt := range tests {
		got := RedactIP(tt.input)
		if got != tt.want {
			t.Errorf("RedactIP(%q) = %q; want %q", tt.input, got, tt.want)
		}
	}
}

func TestRedactIP_IPv4WithPort(t *testing.T) {
	// r.RemoteAddr format: "host:port"
	got := RedactIP("192.168.1.42:54321")
	if got != "192.168.1.0" {
		t.Errorf("RedactIP(with port) = %q; want 192.168.1.0", got)
	}
}

func TestRedactIP_IPv6_Last64BitsZeroed(t *testing.T) {
	// 2001:db8::1 → last 64 bits zeroed → 2001:db8::
	got := RedactIP("2001:db8::1")
	if strings.Contains(got, "::1") {
		t.Errorf("RedactIP(%q) = %q; last 64 bits should be zeroed", "2001:db8::1", got)
	}
	if !strings.HasPrefix(got, "2001:db8") {
		t.Errorf("RedactIP(%q) = %q; first 64 bits should be preserved", "2001:db8::1", got)
	}
}

func TestRedactIP_Invalid(t *testing.T) {
	got := RedactIP("not-an-ip")
	if got != "[invalid-ip]" {
		t.Errorf("RedactIP(%q) = %q; want [invalid-ip]", "not-an-ip", got)
	}
}

func TestRedactIP_Loopback(t *testing.T) {
	got := RedactIP("127.0.0.1")
	if got != "127.0.0.0" {
		t.Errorf("RedactIP(loopback) = %q; want 127.0.0.0", got)
	}
}
