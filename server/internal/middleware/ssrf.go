// ssrf.go — SSRF Guard middleware (P22.1.003).
//
// Blocks outbound HTTP requests to RFC 1918 private ranges, link-local
// addresses, loopback, and IPv6 ULA ranges when the target URL is derived
// from a user-supplied value.
//
// Usage:
//
//	mux.Handle("/proxy", SSRFGuard(proxyHandler))
//
// The middleware resolves the target host from the "X-Proxy-Target" header
// (or the request URL for direct proxy handlers). For services that make
// outbound calls based on user-supplied URLs, call IsPrivateHost(host) before
// dialing.
package middleware

import (
	"encoding/json"
	"net"
	"net/http"
	"strings"
)

// privateRanges lists the CIDR blocks that must never be reachable via
// user-supplied URLs.
var privateRanges []*net.IPNet

func init() {
	cidrs := []string{
		// IPv4 RFC 1918 private ranges.
		"10.0.0.0/8",
		"172.16.0.0/12",
		"192.168.0.0/16",
		// IPv4 loopback.
		"127.0.0.0/8",
		// IPv4 link-local (includes AWS/GCP/Azure metadata endpoint 169.254.169.254).
		"169.254.0.0/16",
		// IPv6 loopback.
		"::1/128",
		// IPv6 unique local (ULA) — fc00::/7 covers fc00:: to fdff::
		"fc00::/7",
		// IPv6 link-local.
		"fe80::/10",
	}
	for _, cidr := range cidrs {
		_, block, err := net.ParseCIDR(cidr)
		if err == nil {
			privateRanges = append(privateRanges, block)
		}
	}
}

// IsPrivateHost returns true if the given host (hostname or IP) resolves to
// a private/loopback/link-local address.
//
// For hostnames, this performs a DNS lookup. If DNS lookup fails, returns true
// (fail-safe: block when we cannot verify).
func IsPrivateHost(host string) bool {
	// Strip port if present.
	h, _, err := net.SplitHostPort(host)
	if err != nil {
		// No port — host is the whole string.
		h = host
	}

	// Direct IP address check.
	if ip := net.ParseIP(h); ip != nil {
		return isPrivateIP(ip)
	}

	// Hostname — resolve to IPs.
	ips, err := net.LookupHost(h)
	if err != nil {
		// Cannot resolve → fail-safe block.
		return true
	}
	for _, addr := range ips {
		if ip := net.ParseIP(addr); ip != nil && isPrivateIP(ip) {
			return true
		}
	}
	return false
}

// isPrivateIP checks whether an IP is in any of the blocked ranges.
func isPrivateIP(ip net.IP) bool {
	// Check for unspecified / any addresses.
	if ip.IsUnspecified() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return true
	}
	for _, block := range privateRanges {
		if block.Contains(ip) {
			return true
		}
	}
	return false
}

// SSRFGuard is an HTTP middleware that rejects requests where the
// "X-Proxy-Target" header or request URL contains a private/internal address.
//
// Apply this to any handler that makes outbound HTTP calls based on
// user-supplied values. For inline checks in handlers, use IsPrivateHost.
func SSRFGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Check explicit proxy target header first.
		target := r.Header.Get("X-Proxy-Target")
		if target == "" {
			// Fall back to the request host itself (for transparent proxies).
			target = r.URL.Host
		}

		if target != "" {
			host := extractHost(target)
			if IsPrivateHost(host) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]string{
					"error":   "invalid_target",
					"message": "invalid target address",
				})
				return
			}
		}

		next.ServeHTTP(w, r)
	})
}

// extractHost pulls the hostname from a raw URL or host:port string.
func extractHost(raw string) string {
	// If it looks like a full URL, extract the host portion.
	if strings.Contains(raw, "://") {
		// Quick extraction without full URL parsing to avoid import cycle.
		rest := raw[strings.Index(raw, "://")+3:]
		// Strip path.
		if i := strings.IndexByte(rest, '/'); i >= 0 {
			rest = rest[:i]
		}
		// Strip query string.
		if i := strings.IndexByte(rest, '?'); i >= 0 {
			rest = rest[:i]
		}
		return rest
	}
	return raw
}
