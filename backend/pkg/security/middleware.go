// middleware.go — OWASP-aligned security middleware for all Roost HTTP services.
// P16-T02: Security Hardening
//
// All handlers should be wrapped with at least SecurityHeaders and RequestID.
// Auth endpoints should additionally use RateLimit(10) for brute-force protection.
// State-changing endpoints that render HTML (admin panel callbacks) use CSRFProtect.
package security

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strings"
	"sync"
	"time"
)

// ──────────────────────────────────────────────────────────────────────────────
// Security Headers
// ──────────────────────────────────────────────────────────────────────────────

// SecurityHeaders adds OWASP-recommended HTTP security headers to all responses.
// Should be the outermost middleware in every service's middleware chain.
//
// Headers set:
//   - Content-Security-Policy: strict-dynamic + nonce for scripts
//   - X-Frame-Options: DENY (clickjacking)
//   - X-Content-Type-Options: nosniff (MIME sniffing)
//   - Referrer-Policy: strict-origin-when-cross-origin
//   - Permissions-Policy: restricts camera, microphone, geolocation
//   - X-XSS-Protection: 0 (disabled — modern CSP supersedes this)
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Frame-Options", "DENY")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		h.Set("X-XSS-Protection", "0")
		// CSP: allow same-origin resources only.
		// Roost is a JSON API — no inline scripts needed in API responses.
		// The SvelteKit admin panel sets its own CSP via meta tag.
		h.Set("Content-Security-Policy",
			"default-src 'none'; frame-ancestors 'none'; base-uri 'none'")
		next.ServeHTTP(w, r)
	})
}

// ──────────────────────────────────────────────────────────────────────────────
// Rate Limiting
// ──────────────────────────────────────────────────────────────────────────────

// ipRateLimiter is a simple sliding-window IP-based rate limiter.
// It is intentionally simple — for production, use a Redis-backed limiter.
type ipRateLimiter struct {
	mu      sync.Mutex
	windows map[string][]time.Time
	rps     int           // requests per minute
	window  time.Duration // sliding window width
}

// newIPRateLimiter creates a rate limiter: rpm requests per minute.
func newIPRateLimiter(rpm int) *ipRateLimiter {
	return &ipRateLimiter{
		windows: make(map[string][]time.Time),
		rps:     rpm,
		window:  time.Minute,
	}
}

// allow returns true if the IP is under its rate limit.
func (l *ipRateLimiter) allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	cutoff := now.Add(-l.window)

	// Prune stale entries.
	times := l.windows[ip]
	pruned := times[:0]
	for _, t := range times {
		if t.After(cutoff) {
			pruned = append(pruned, t)
		}
	}
	if len(pruned) >= l.rps {
		l.windows[ip] = pruned
		return false
	}
	l.windows[ip] = append(pruned, now)
	return true
}

// RateLimit returns middleware that allows at most rpm requests per minute per IP.
// On rate limit breach, returns HTTP 429 with a Retry-After: 60 header.
//
// Recommended values:
//   - Default API endpoints: RateLimit(100)
//   - Auth/token endpoints: RateLimit(10)
//   - Stream URL endpoints: RateLimit(60)
func RateLimit(rpm int) func(http.Handler) http.Handler {
	limiter := newIPRateLimiter(rpm)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip := realIP(r)
			if !limiter.allow(ip) {
				w.Header().Set("Retry-After", "60")
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusTooManyRequests)
				w.Write([]byte(`{"error":"rate_limit_exceeded","message":"Too many requests. Please wait before retrying."}`))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// realIP extracts the real client IP, checking Cloudflare's CF-Connecting-IP
// header first, then X-Forwarded-For, then RemoteAddr.
func realIP(r *http.Request) string {
	if ip := r.Header.Get("CF-Connecting-IP"); ip != "" {
		return ip
	}
	if ip := r.Header.Get("X-Forwarded-For"); ip != "" {
		// X-Forwarded-For may be comma-separated: take the first (leftmost) IP.
		parts := strings.SplitN(ip, ",", 2)
		return strings.TrimSpace(parts[0])
	}
	// RemoteAddr is "ip:port" — strip the port.
	addr := r.RemoteAddr
	if i := strings.LastIndex(addr, ":"); i > 0 {
		return addr[:i]
	}
	return addr
}

// ──────────────────────────────────────────────────────────────────────────────
// CSRF Protection
// ──────────────────────────────────────────────────────────────────────────────

// csrfContextKey is the context key for the CSRF token.
type csrfContextKey struct{}

// CSRFProtect implements the Double Submit Cookie pattern.
// On GET requests: generates a CSRF token, sets it as a cookie, and injects it
// into the request context (for template rendering).
// On state-changing methods (POST/PUT/PATCH/DELETE): validates that the
// X-CSRF-Token header matches the roost_csrf cookie.
//
// Use this for the admin panel and subscriber portal SSR pages only — not on
// the pure JSON API endpoints (those are protected by CORS + Bearer tokens).
func CSRFProtect(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			// Safe methods: issue a fresh token if one isn't already set.
			token := csrfFromCookie(r)
			if token == "" {
				token = generateCSRFToken()
				http.SetCookie(w, &http.Cookie{
					Name:     "roost_csrf",
					Value:    token,
					Path:     "/",
					HttpOnly: false, // Must be readable by JS to embed in headers.
					Secure:   true,
					SameSite: http.SameSiteStrictMode,
				})
			}
			ctx := context.WithValue(r.Context(), csrfContextKey{}, token)
			next.ServeHTTP(w, r.WithContext(ctx))

		default:
			// State-changing methods: validate token.
			cookie := csrfFromCookie(r)
			header := r.Header.Get("X-CSRF-Token")
			if cookie == "" || header == "" || cookie != header {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusForbidden)
				w.Write([]byte(`{"error":"csrf_invalid","message":"CSRF token missing or invalid"}`))
				return
			}
			next.ServeHTTP(w, r)
		}
	})
}

// CSRFTokenFromContext extracts the CSRF token for template embedding.
func CSRFTokenFromContext(ctx context.Context) string {
	v, _ := ctx.Value(csrfContextKey{}).(string)
	return v
}

func csrfFromCookie(r *http.Request) string {
	c, err := r.Cookie("roost_csrf")
	if err != nil {
		return ""
	}
	return c.Value
}

func generateCSRFToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// ──────────────────────────────────────────────────────────────────────────────
// Input Sanitization
// ──────────────────────────────────────────────────────────────────────────────

// SanitizeString strips obvious XSS vectors from a string input.
// Removes <script> tags, javascript: URIs, and common event handler attributes.
// The API is JSON-only (not HTML), so this is a defence-in-depth measure —
// the primary protection is output encoding (never rendering user input as HTML).
func SanitizeString(s string) string {
	lower := strings.ToLower(s)
	// Reject strings containing HTML script tags.
	if strings.Contains(lower, "<script") || strings.Contains(lower, "</script") {
		return ""
	}
	// Reject javascript: URIs.
	if strings.Contains(lower, "javascript:") {
		return ""
	}
	// Strip on* event handler attributes (onclick=, onload=, etc.).
	result := s
	for _, prefix := range []string{"onload=", "onerror=", "onclick=", "onmouseover=", "onfocus=", "onblur="} {
		for {
			idx := strings.Index(strings.ToLower(result), prefix)
			if idx < 0 {
				break
			}
			result = result[:idx] + result[idx+len(prefix):]
		}
	}
	return result
}

// ──────────────────────────────────────────────────────────────────────────────
// UUID Validation
// ──────────────────────────────────────────────────────────────────────────────

// ValidateUUID returns true if id is a valid RFC-4122 UUID (any version).
// Use this to validate path parameters before passing to SQL queries,
// preventing injection via malformed path segments.
func ValidateUUID(id string) bool {
	if len(id) != 36 {
		return false
	}
	// UUID format: 8-4-4-4-12 hex chars separated by hyphens.
	for i, c := range id {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
		} else {
			if !isHexRune(c) {
				return false
			}
		}
	}
	return true
}

func isHexRune(c rune) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

// ──────────────────────────────────────────────────────────────────────────────
// Request ID
// ──────────────────────────────────────────────────────────────────────────────

// requestIDKey is the context key for the request ID.
type requestIDKey struct{}

// RequestID generates a unique ID for each request and attaches it to both
// the request context and the X-Request-ID response header. This enables
// correlation of log lines across services for a single user request.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if id == "" {
			b := make([]byte, 8)
			_, _ = rand.Read(b)
			id = hex.EncodeToString(b)
		}
		w.Header().Set("X-Request-ID", id)
		ctx := context.WithValue(r.Context(), requestIDKey{}, id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RequestIDFromContext extracts the request ID from context for log enrichment.
func RequestIDFromContext(ctx context.Context) string {
	v, _ := ctx.Value(requestIDKey{}).(string)
	return v
}
