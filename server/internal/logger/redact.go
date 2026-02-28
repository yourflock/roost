// redact.go — Sensitive data masking for safe structured logging.
// P21.1.003: Sensitive Field Redaction
//
// All helpers in this file mask PII and credentials before they reach log
// output. Use them whenever logging tokens, emails, or IP addresses.
//
// Examples:
//
//	logger.Info("auth",
//	    "token",  logger.RedactToken(apiToken),
//	    "email",  logger.RedactEmail(user.Email),
//	    "client", logger.RedactIP(r.RemoteAddr),
//	)
package logger

import (
	"net"
	"strings"
)

// RedactToken masks an API key, session token, or bearer token for logging.
// It keeps the first 8 characters so the value can be correlated across log
// lines and request IDs, then appends "****" to make redaction obvious.
//
// Examples:
//
//	"sk_live_abcdefgh1234"  →  "sk_live_****"
//	"tok_abc"               →  "tok_abc*"    (short: show all, append *)
//	""                      →  "[empty]"
func RedactToken(token string) string {
	if len(token) == 0 {
		return "[empty]"
	}
	if len(token) <= 8 {
		return token + "*"
	}
	return token[:8] + "****"
}

// RedactEmail masks the local part of an email address.
// The domain is preserved so DNS / delivery debugging remains possible.
// Only the first character of the local part is shown followed by "***".
//
// Examples:
//
//	"alice@example.com"  →  "a***@example.com"
//	"bob@test.org"       →  "b***@test.org"
//	"noatsign"           →  "n***"
//	""                   →  "[empty]"
func RedactEmail(email string) string {
	if len(email) == 0 {
		return "[empty]"
	}
	parts := strings.SplitN(email, "@", 2)
	local := parts[0]
	masked := "***"
	if len(local) > 0 {
		masked = string(local[0]) + "***"
	}
	if len(parts) == 2 {
		return masked + "@" + parts[1]
	}
	return masked
}

// RedactIP masks the host-specific portion of an IP address.
//
// For IPv4: last octet is replaced with "0".
//   "192.168.1.42"  →  "192.168.1.0"
//
// For IPv6: last 64 bits (4 groups) are replaced with zeros.
//   "2001:db8:85a3::8a2e:370:7334"  →  "2001:db8:85a3:0:0:0:0:0"
//
// If the input contains a port (e.g. from r.RemoteAddr), the port is stripped.
// Unparseable values are returned as "[invalid-ip]".
func RedactIP(ipStr string) string {
	// Strip port if present (host:port format from net.Addr).
	host, _, err := net.SplitHostPort(ipStr)
	if err != nil {
		// Not host:port — try as raw IP.
		host = ipStr
	}

	ip := net.ParseIP(strings.TrimSpace(host))
	if ip == nil {
		return "[invalid-ip]"
	}

	if ip4 := ip.To4(); ip4 != nil {
		// IPv4: zero the last octet.
		return net.IP{ip4[0], ip4[1], ip4[2], 0}.String()
	}

	// IPv6: zero the last 64 bits (bytes 8-15).
	ip16 := ip.To16()
	masked := make(net.IP, 16)
	copy(masked, ip16)
	for i := 8; i < 16; i++ {
		masked[i] = 0
	}
	return masked.String()
}
