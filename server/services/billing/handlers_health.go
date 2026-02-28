// handlers_health.go — Extended health and readiness endpoints.
// P16-T06: Incident Response & Disaster Recovery
//
// GET /health/detailed — comprehensive health check for monitoring dashboards
//   Returns status of DB connection, dependent services (relay, epg, catalog),
//   and current service version.
//
// GET /health/ready — readiness probe for load balancer health checks
//   Returns 200 only when all critical dependencies are healthy.
//   Returns 503 if any critical dependency is unhealthy.
//   Use this as the load balancer health check target.
package billing

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

// serviceHealthStatus represents the health of a single dependent service.
type serviceHealthStatus struct {
	Name      string  `json:"name"`
	Status    string  `json:"status"` // "ok" | "degraded" | "down" | "unknown"
	LatencyMs float64 `json:"latency_ms,omitempty"`
	Error     string  `json:"error,omitempty"`
}

// detailedHealthResponse is the full response body for GET /health/detailed.
type detailedHealthResponse struct {
	Status   string `json:"status"` // "ok" | "degraded" | "down"
	Service  string `json:"service"`
	Version  string `json:"version"`
	Uptime   string `json:"uptime,omitempty"`
	Database struct {
		Connected bool    `json:"connected"`
		LatencyMs float64 `json:"latency_ms"`
		Error     string  `json:"error,omitempty"`
	} `json:"database"`
	Services  []serviceHealthStatus `json:"services"`
	Timestamp string                `json:"timestamp"`
}

var startTime = time.Now()

// handleDetailedHealth handles GET /health/detailed.
// No authentication required — used by monitoring systems and ops dashboards.
func (s *Server) handleDetailedHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET only")
		return
	}

	resp := detailedHealthResponse{
		Status:    "ok",
		Service:   "roost-billing",
		Version:   getEnv("ROOST_VERSION", "dev"),
		Uptime:    time.Since(startTime).Round(time.Second).String(),
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}

	// Check database.
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	start := time.Now()
	if err := s.db.PingContext(ctx); err != nil {
		resp.Database.Connected = false
		resp.Database.Error = err.Error()
		resp.Status = "down"
	} else {
		resp.Database.Connected = true
		resp.Database.LatencyMs = float64(time.Since(start).Milliseconds())
	}

	// Check dependent services via their health endpoints.
	serviceURLs := map[string]string{
		"relay":   getEnv("RELAY_URL", "http://localhost:8090") + "/health",
		"owl_api": getEnv("OWL_API_URL", "http://localhost:8091") + "/health",
		"epg":     getEnv("EPG_URL", "http://localhost:8092") + "/health",
		"catalog": getEnv("CATALOG_URL", "http://localhost:8093") + "/health",
	}

	client := &http.Client{Timeout: 2 * time.Second}
	for name, url := range serviceURLs {
		svc := checkService(client, name, url)
		resp.Services = append(resp.Services, svc)
		if svc.Status == "down" && resp.Status == "ok" {
			resp.Status = "degraded"
		}
	}

	statusCode := http.StatusOK
	if resp.Status == "down" {
		statusCode = http.StatusServiceUnavailable
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(resp)
}

// handleReadinessProbe handles GET /health/ready.
// Returns 200 when the billing service can serve traffic (DB connected).
// Returns 503 when the DB is unreachable (billing cannot function without DB).
// Used as the load balancer or k8s readiness probe target.
func (s *Server) handleReadinessProbe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET only")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.db.PingContext(ctx); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(map[string]string{
			"status": "not_ready",
			"reason": "database unavailable",
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "ready"})
}

// checkService performs a GET against the given health URL and returns the status.
func checkService(client *http.Client, name, url string) serviceHealthStatus {
	start := time.Now()
	resp, err := client.Get(url)
	latency := float64(time.Since(start).Milliseconds())

	if err != nil {
		return serviceHealthStatus{
			Name:   name,
			Status: "down",
			Error:  err.Error(),
		}
	}
	defer resp.Body.Close()

	status := "ok"
	if resp.StatusCode >= 500 {
		status = "down"
	} else if resp.StatusCode >= 400 {
		status = "degraded"
	}

	return serviceHealthStatus{
		Name:      name,
		Status:    status,
		LatencyMs: latency,
	}
}

