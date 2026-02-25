// redact.go — Sensitive data masking for safe logging.
// P16-T01: Structured Logging & Audit Trail
//
// These helpers ensure tokens, passwords, and PII are never written to logs
// in cleartext. Call before passing values to any log statement.
package logging

import "strings"

// RedactToken masks an API or session token for logging.
// Shows the first 8 characters followed by "..." to allow correlation
// without exposing the full credential.
//
// Examples:
//
//	"sk_live_abc123xyz" → "sk_live_..."
//	"" → "[empty]"
func RedactToken(t string) string {
	if len(t) == 0 {
		return "[empty]"
	}
	if len(t) <= 8 {
		return t[:1] + "..."
	}
	return t[:8] + "..."
}

// RedactEmail masks an email address for logging.
// Preserves the domain for debugging DNS/delivery issues while hiding the
// local part (username) to avoid PII exposure.
//
// Examples:
//
//	"alice@example.com" → "a...@example.com"
//	"bob@test.org"      → "b...@test.org"
//	"noatsign"          → "n..."
func RedactEmail(e string) string {
	if len(e) == 0 {
		return "[empty]"
	}
	parts := strings.SplitN(e, "@", 2)
	if len(parts) != 2 {
		if len(parts[0]) > 1 {
			return parts[0][:1] + "..."
		}
		return "..."
	}
	return parts[0][:1] + "...@" + parts[1]
}
