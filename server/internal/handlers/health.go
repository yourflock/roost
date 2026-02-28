// Package handlers provides shared HTTP handler functions for all Roost services.
// P21.3.001: Health Check Handlers
//
// Two endpoints are defined:
//
//	GET /healthz  — liveness probe. Always 200 if the process is running.
//	              Used by Hetzner/Cloudflare health checks and load balancers.
//
//	GET /ready    — readiness probe. Checks DB and Redis connectivity.
//	              Returns 200 {"status":"ok"} when all deps are healthy.
//	              Returns 503 {"status":"degraded"} when any dep is unavailable.
//
// Mount these early so they are reachable before auth middleware.
// They should never require authentication.
//
// Usage in a service main:
//
//	import "github.com/yourflock/roost/internal/handlers"
//
//	db, _ := sql.Open(...)
//	mux.HandleFunc("GET /healthz", handlers.Liveness)
//	mux.HandleFunc("GET /ready",   handlers.Readiness(db, nil)) // nil = no Redis
package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"time"
)

// healthResponse is the JSON body for both probes.
type healthResponse struct {
	Status string            `json:"status"`           // "ok" | "degraded"
	Checks map[string]string `json:"checks,omitempty"` // only for /ready
}

// Liveness is a GET /healthz handler.
// It always returns 200 {"status":"ok"} as long as the process is running.
// No dependency checks — this is purely a process-alive probe.
func Liveness(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, healthResponse{Status: "ok"})
}

// Pinger is the interface used by Readiness to check a dependency.
// *sql.DB and any Redis client that exposes Ping satisfy this interface.
type Pinger interface {
	PingContext(ctx context.Context) error
}

// Readiness returns a GET /ready handler that checks all provided Pingers.
// Pass nil for deps you don't have (e.g. a service without Redis).
//
//	handlers.Readiness(db, redisClient)   // both
//	handlers.Readiness(db, nil)           // DB only
//	handlers.Readiness(nil, nil)          // always 200 (no deps)
func Readiness(db *sql.DB, redis Pinger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()

		checks := make(map[string]string)
		degraded := false

		if db != nil {
			if err := db.PingContext(ctx); err != nil {
				checks["db"] = "error: " + err.Error()
				degraded = true
			} else {
				checks["db"] = "ok"
			}
		}

		if redis != nil {
			if err := redis.PingContext(ctx); err != nil {
				checks["redis"] = "error: " + err.Error()
				degraded = true
			} else {
				checks["redis"] = "ok"
			}
		}

		status := "ok"
		code := http.StatusOK
		if degraded {
			status = "degraded"
			code = http.StatusServiceUnavailable
		}

		writeJSON(w, code, healthResponse{
			Status: status,
			Checks: checks,
		})
	}
}

// writeJSON encodes v as JSON and writes it with the given status code.
func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v) //nolint:errcheck
}
