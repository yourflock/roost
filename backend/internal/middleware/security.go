// security.go — Transport security and CORS middleware (P22.5).
//
// CORS: Only approved Roost portal origins are allowed.
// In development (ROOST_ENV=development), localhost:* is additionally allowed.
//
// Security headers: HSTS, X-Frame-Options, X-Content-Type-Options,
// Referrer-Policy, Content-Security-Policy, Permissions-Policy.
//
// P22.5.002: CORS origin allowlist enforcement.
// P22.5.001: Security header hardening.
package middleware

import (
	"net/http"
	"os"
	"strings"
)

// SecurityHeaders adds standard HTTP security headers to every response.
// Should be applied to all routes except /metrics and health checks.
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()

		// HSTS — 2 years, includeSubDomains, preload-safe.
		// Set in production only (not localhost).
		if !isLocalhost(r.Host) {
			h.Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains; preload")
		}

		// Prevent Roost from being embedded in iframes.
		h.Set("X-Frame-Options", "DENY")

		// Block MIME-type sniffing.
		h.Set("X-Content-Type-Options", "nosniff")

		// Send minimal referrer info across origins.
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")

		// Disable browser features not needed by the API.
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=()")

		// CSP for API endpoints: media served from CDN relay, no inline content.
		h.Set("Content-Security-Policy",
			"default-src 'self'; media-src 'self' https://stream.yourflock.org; connect-src 'self'; frame-ancestors 'none'")

		next.ServeHTTP(w, r)
	})
}

// CORS applies Cross-Origin Resource Sharing headers (P22.5.002).
//
// Allowlist:
//   - https://owl.yourflock.org
//   - https://*.yourflock.org
//   - https://roost.yourflock.org (portal)
//   - https://admin.roost.yourflock.org
//   - https://reseller.roost.yourflock.org
//   - http://localhost:* (dev only, gated by ROOST_ENV=development)
//   - ROOST_ALLOWED_ORIGINS env var (comma-separated additional origins)
//
// Unknown origins: no CORS headers set → browser blocks the request.
// Internal service routes (/internal/*): CORS bypass (not browser-accessible).
func CORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin == "" {
			// Not a cross-origin request (direct curl, server-to-server, /internal).
			next.ServeHTTP(w, r)
			return
		}

		if isCORSAllowed(origin) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			w.Header().Add("Vary", "Origin")
		} else {
			// Unknown origin — return 403 explicitly (per P22.5.002).
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte(`{"error":"cors_origin_blocked","message":"Origin not allowed"}`))
			return
		}

		if r.Method == http.MethodOptions {
			// Preflight.
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Request-ID")
			w.Header().Set("Access-Control-Max-Age", "86400")
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// isCORSAllowed returns true if the origin is in the allow list.
func isCORSAllowed(origin string) bool {
	// Development: allow localhost with any port.
	if os.Getenv("ROOST_ENV") == "development" {
		if strings.HasPrefix(origin, "http://localhost:") ||
			origin == "http://localhost" {
			return true
		}
	}

	// Static allowlist.
	static := []string{
		"https://owl.yourflock.org",
		"https://roost.yourflock.org",
		"https://admin.roost.yourflock.org",
		"https://reseller.roost.yourflock.org",
	}
	for _, allowed := range static {
		if origin == allowed {
			return true
		}
	}

	// Wildcard: any *.yourflock.org subdomain over HTTPS.
	if strings.HasPrefix(origin, "https://") &&
		strings.HasSuffix(origin, ".yourflock.org") {
		return true
	}

	// Dynamic list from env.
	if extra := os.Getenv("ROOST_ALLOWED_ORIGINS"); extra != "" {
		for _, o := range strings.Split(extra, ",") {
			if strings.TrimSpace(o) == origin {
				return true
			}
		}
	}

	return false
}

// isLocalhost returns true for localhost/127.0.0.1 host headers.
func isLocalhost(host string) bool {
	return strings.HasPrefix(host, "localhost") ||
		strings.HasPrefix(host, "127.0.0.1") ||
		strings.HasPrefix(host, "[::1]")
}
