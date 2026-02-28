//go:build submarine

// dialer.go — Allowlist-only outbound dialer for Submarine Mode.
//
// Build tag: submarine
// Usage: go build -tags submarine ./...
//
// When built with the "submarine" tag, ALL outbound HTTP connections are
// restricted to domains listed in ROOST_SUBMARINE_ALLOWLIST (comma-separated).
// Any connection attempt to an unlisted domain returns an error immediately.
//
// This is the privacy mode for operators who want zero telemetry and zero
// undocumented outbound connections. Useful for air-gapped or LAN-only
// deployments where external calls are policy violations.
//
// Env vars:
//   ROOST_SUBMARINE_ALLOWLIST — comma-separated list of allowed hostnames/domains
//     e.g.: "yourflock.org,roost.yourflock.org,cloudflare.com"
//
// Allowlist rules:
//   - Exact hostname match: "api.example.com" allows api.example.com only
//   - Subdomain match: "example.com" allows *.example.com and example.com itself
//   - Port is stripped before comparison
//
// To use the submarine client in a service:
//   client := roostnет.NewHTTPClient()
package roostnет

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
)

var submarineAllowlist []string

func init() {
	raw := os.Getenv("ROOST_SUBMARINE_ALLOWLIST")
	if raw == "" {
		return
	}
	parts := strings.Split(raw, ",")
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			submarineAllowlist = append(submarineAllowlist, p)
		}
	}
}

// isAllowed checks whether host is permitted by the submarine allowlist.
// host may include a port (e.g. "api.example.com:443") — the port is stripped.
func isAllowed(host string) bool {
	h, _, err := net.SplitHostPort(host)
	if err != nil {
		// No port — use as-is
		h = host
	}
	for _, allowed := range submarineAllowlist {
		// Exact match
		if h == allowed {
			return true
		}
		// Subdomain match: h ends with "."+allowed
		if strings.HasSuffix(h, "."+allowed) {
			return true
		}
	}
	return false
}

// SubmarineDialContext is a net.DialContext replacement that enforces the allowlist.
func SubmarineDialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	if !isAllowed(addr) {
		return nil, fmt.Errorf(
			"roost submarine mode: outbound connection to %q blocked (not in ROOST_SUBMARINE_ALLOWLIST)",
			addr,
		)
	}
	var d net.Dialer
	return d.DialContext(ctx, network, addr)
}

// NewHTTPClient returns an *http.Client with the submarine allowlist dialer.
// Use this client for all external HTTP calls in submarine-tagged builds.
func NewHTTPClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			DialContext: SubmarineDialContext,
		},
	}
}
