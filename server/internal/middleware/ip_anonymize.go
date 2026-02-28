// ip_anonymize.go — IP anonymization middleware (P22.3.001).
//
// For GDPR compliance, raw IP addresses must not be stored in logs or analytics
// for non-admin routes. This middleware:
//
//  1. Strips the last octet of IPv4 addresses (10.20.30.40 → 10.20.30.0)
//  2. Zeros the last 80 bits of IPv6 addresses (interface identifier portion)
//  3. Stores the anonymized IP in the request context for downstream handlers
//  4. SHA-256 hashes the full IP and stores it in context for audit_log (ip_hash)
//
// Admin routes (prefix /admin, /internal) bypass anonymization and use the
// raw IP for security audit trails.
package middleware

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/http"
	"strings"
)

// anonIPKey is the context key for the anonymized IP string.
type anonIPKey struct{}

// ipHashKey is the context key for the SHA-256 hash of the full IP.
type ipHashKey struct{}

// AnonIPFromContext returns the anonymized IP stored by IPAnonymize middleware.
// Returns "" if middleware was not applied.
func AnonIPFromContext(ctx context.Context) string {
	v, _ := ctx.Value(anonIPKey{}).(string)
	return v
}

// IPHashFromContext returns the SHA-256 hash of the full IP from context.
// Returns "" if middleware was not applied.
func IPHashFromContext(ctx context.Context) string {
	v, _ := ctx.Value(ipHashKey{}).(string)
	return v
}

// IPAnonymize is HTTP middleware that anonymizes client IP addresses for non-admin routes.
//
// - Non-admin routes: last octet stripped (IPv4) or interface ID zeroed (IPv6)
// - Admin routes (/admin/*, /internal/*): raw IP preserved for security auditing
//
// Both the anonymized IP string and the full-IP SHA-256 hash are injected into context.
func IPAnonymize(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rawIP := realIP(r)
		isAdmin := strings.HasPrefix(r.URL.Path, "/admin") ||
			strings.HasPrefix(r.URL.Path, "/internal")

		var anonIP, ipHash string
		if isAdmin {
			// Admin routes: keep full IP for security audit trail.
			anonIP = rawIP
		} else {
			anonIP = anonymizeIP(rawIP)
		}
		ipHash = hashIP(rawIP)

		ctx := context.WithValue(r.Context(), anonIPKey{}, anonIP)
		ctx = context.WithValue(ctx, ipHashKey{}, ipHash)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// anonymizeIP removes identifying information from an IP address.
// IPv4: zero the last octet. IPv6: zero the last 80 bits (interface identifier).
// Returns the original string if parsing fails.
func anonymizeIP(raw string) string {
	ip := net.ParseIP(raw)
	if ip == nil {
		return raw
	}

	if ip4 := ip.To4(); ip4 != nil {
		// IPv4: zero last octet.
		ip4[3] = 0
		return ip4.String()
	}

	// IPv6: zero last 80 bits (bytes 6–15 of the 16-byte form).
	ip6 := ip.To16()
	if ip6 == nil {
		return raw
	}
	for i := 6; i < 16; i++ {
		ip6[i] = 0
	}
	return ip6.String()
}

// hashIP returns the hex-encoded SHA-256 hash of the raw IP string.
// Used for audit_log.ip_hash (P22.3.001 migration 042).
func hashIP(raw string) string {
	if raw == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// realIP extracts the client IP from the request, respecting Cloudflare's
// CF-Connecting-IP header (most trustworthy), then X-Forwarded-For, then RemoteAddr.
func realIP(r *http.Request) string {
	if cf := r.Header.Get("CF-Connecting-IP"); cf != "" {
		return strings.TrimSpace(cf)
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		return strings.TrimSpace(parts[0])
	}
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		return strings.TrimSpace(xri)
	}
	addr := r.RemoteAddr
	if host, _, err := net.SplitHostPort(addr); err == nil {
		return host
	}
	return addr
}
