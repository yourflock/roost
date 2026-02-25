// Package middleware provides shared HTTP middleware for all Roost services.
// P20.1.001: Feature Flag Middleware
// P21.1.002: HTTP Request Logging Middleware
package middleware

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
)

// contextKey is the unexported type for context keys to prevent collisions.
type contextKey int

const loggerKey contextKey = 1

// RequirePublicMode protects a route so it only responds when ROOST_MODE=public.
// Returns 403 with a JSON body in private mode.
// isPublicMode is a function to allow per-request evaluation (e.g., env reload).
func RequirePublicMode(isPublicMode func() bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isPublicMode() {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			json.NewEncoder(w).Encode(map[string]string{
				"error": "this endpoint requires public mode",
				"mode":  "private",
				"docs":  "https://docs.yourflock.org/roost/public-mode",
			})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// RequestLogger is a middleware that logs each HTTP request as a single
// structured JSON line using log/slog.
// P21.1.002: HTTP Request Logging Middleware
func RequestLogger(logger *slog.Logger, service string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Skip health/metrics endpoints at INFO level.
			isHealthPath := r.URL.Path == "/health" || r.URL.Path == "/healthz" ||
				r.URL.Path == "/metrics" || r.URL.Path == "/health/ready"

			reqID := uuid.New().String()
			w.Header().Set("X-Request-ID", reqID)

			start := time.Now()
			rw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rw, r)
			dur := time.Since(start).Milliseconds()

			level := slog.LevelInfo
			if isHealthPath {
				level = slog.LevelDebug
			}
			if rw.status >= 500 {
				level = slog.LevelWarn
			}

			logger.Log(r.Context(), level, "request",
				"service", service,
				"method", r.Method,
				"path", r.URL.Path,
				"status", rw.status,
				"duration_ms", dur,
				"request_id", reqID,
			)
		})
	}
}

// statusWriter captures the HTTP response status code.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (sw *statusWriter) WriteHeader(code int) {
	sw.status = code
	sw.ResponseWriter.WriteHeader(code)
}

// Recover is a panic recovery middleware that returns 500 without crashing.
func Recover(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if err := recover(); err != nil {
					logger.Error("panic recovered",
						"path", r.URL.Path,
						"panic", strconv.Quote(fmtPanic(err)),
					)
					http.Error(w, "Internal Server Error", http.StatusInternalServerError)
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

func fmtPanic(v interface{}) string {
	switch e := v.(type) {
	case error:
		return e.Error()
	case string:
		return e
	default:
		return "unknown panic"
	}
}
