// Package cdnrouter provides multi-CDN routing for HLS segment serving.
// Primary CDN is Cloudflare (global), secondary is Hetzner Object Storage (EU-preferred).
// Region-aware: EU subscribers route to Hetzner, others default to Cloudflare.
package cdnrouter

import (
	"fmt"
	"net/http"
	"sync"
	"time"
)

// CDNRouter routes HLS segment requests to the appropriate CDN based on
// subscriber region and CDN health state.
type CDNRouter struct {
	mu        sync.RWMutex
	primary   *CDNEndpoint
	secondary *CDNEndpoint
}

// CDNEndpoint represents a single CDN provider configuration.
type CDNEndpoint struct {
	Name     string
	BaseURL  string
	IsHealth bool
	LastPing time.Time
}

// HealthStatus holds the result of a CDN health probe.
type HealthStatus struct {
	Provider  string        `json:"provider"`
	BaseURL   string        `json:"base_url"`
	Status    string        `json:"status"` // "healthy" | "degraded" | "unreachable"
	LatencyMs int64         `json:"latency_ms"`
	ProbedAt  time.Time     `json:"probed_at"`
}

const (
	// CDNCloudflareURL is the primary global CDN (Cloudflare).
	CDNCloudflareURL = "https://cdn.yourflock.org"

	// CDNHetznerURL is the secondary EU-preferred CDN (Hetzner Object Storage + CDN).
	CDNHetznerURL = "https://cdn-eu.yourflock.org"

	// healthProbeTimeout is the maximum time to wait for a CDN health probe.
	healthProbeTimeout = 5 * time.Second

	// healthProbeLatencyThreshold is the latency above which a CDN is considered degraded.
	healthProbeLatencyThreshold = 2000 // milliseconds
)

// New creates a CDNRouter with Cloudflare as primary and Hetzner as secondary.
func New() *CDNRouter {
	return &CDNRouter{
		primary: &CDNEndpoint{
			Name:     "cloudflare",
			BaseURL:  CDNCloudflareURL,
			IsHealth: true,
			LastPing: time.Time{},
		},
		secondary: &CDNEndpoint{
			Name:     "hetzner",
			BaseURL:  CDNHetznerURL,
			IsHealth: true,
			LastPing: time.Time{},
		},
	}
}

// RouteRequest returns the CDN base URL for serving HLS content to a given subscriber region.
//
// Routing logic:
//   - EU subscribers: prefer Hetzner CDN (lower latency, same DC as origin)
//   - All other regions: prefer Cloudflare (global PoP coverage)
//   - If the preferred CDN is unhealthy, fall back to the other one
//   - If both are unhealthy, return the primary base URL (failsafe — better than no URL)
func (r *CDNRouter) RouteRequest(subscriberRegion string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	switch subscriberRegion {
	case "eu":
		// EU traffic: prefer Hetzner (same DC as flock-prod and roost-prod in fsn1)
		if r.secondary.IsHealth {
			return r.secondary.BaseURL
		}
		// Hetzner down → fall back to Cloudflare
		if r.primary.IsHealth {
			return r.primary.BaseURL
		}
		return r.secondary.BaseURL // last resort
	default:
		// Non-EU: prefer Cloudflare global PoP
		if r.primary.IsHealth {
			return r.primary.BaseURL
		}
		// Cloudflare down → fall back to Hetzner
		if r.secondary.IsHealth {
			return r.secondary.BaseURL
		}
		return r.primary.BaseURL // last resort
	}
}

// BuildSegmentURL constructs the full CDN URL for an HLS segment.
//
// Format: {cdn_base_url}/segments/{slug}/{segment}
// Example: https://cdn.yourflock.org/segments/bbc-one/seg001.ts
func (r *CDNRouter) BuildSegmentURL(subscriberRegion, slug, segment string) string {
	base := r.RouteRequest(subscriberRegion)
	return fmt.Sprintf("%s/segments/%s/%s", base, slug, segment)
}

// HealthCheck probes both CDN endpoints and updates their health state.
// Returns the health status for both CDNs. This should be called periodically
// (e.g., every 30 seconds by a background goroutine).
func (r *CDNRouter) HealthCheck() (cloudflare, hetzner HealthStatus) {
	cfStatus := probeCDN("cloudflare", CDNCloudflareURL)
	hzStatus := probeCDN("hetzner", CDNHetznerURL)

	r.mu.Lock()
	defer r.mu.Unlock()

	r.primary.IsHealth = cfStatus.Status == "healthy" || cfStatus.Status == "degraded"
	r.primary.LastPing = cfStatus.ProbedAt

	r.secondary.IsHealth = hzStatus.Status == "healthy" || hzStatus.Status == "degraded"
	r.secondary.LastPing = hzStatus.ProbedAt

	return cfStatus, hzStatus
}

// Failover manually switches the primary and secondary CDN endpoints.
// After a failover, EU traffic routing is unaffected (still prefers Hetzner),
// but non-EU traffic will use the new primary.
func (r *CDNRouter) Failover() (newPrimaryName string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.primary, r.secondary = r.secondary, r.primary
	return r.primary.Name
}

// Status returns the current health state of both CDN endpoints.
func (r *CDNRouter) Status() (primaryName, primaryURL string, primaryHealthy bool, secondaryName, secondaryURL string, secondaryHealthy bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.primary.Name, r.primary.BaseURL, r.primary.IsHealth,
		r.secondary.Name, r.secondary.BaseURL, r.secondary.IsHealth
}

// probeCDN performs a single HTTP health probe against the CDN endpoint.
func probeCDN(name, baseURL string) HealthStatus {
	status := HealthStatus{
		Provider: name,
		BaseURL:  baseURL,
		ProbedAt: time.Now().UTC(),
	}

	client := &http.Client{Timeout: healthProbeTimeout}
	start := time.Now()
	resp, err := client.Get(baseURL + "/health")
	elapsed := time.Since(start).Milliseconds()
	status.LatencyMs = elapsed

	if err != nil {
		status.Status = "unreachable"
		return status
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode < 300 && elapsed <= int64(healthProbeLatencyThreshold):
		status.Status = "healthy"
	case resp.StatusCode < 300:
		status.Status = "degraded"
	case resp.StatusCode < 500:
		status.Status = "degraded"
	default:
		status.Status = "unreachable"
	}

	return status
}
