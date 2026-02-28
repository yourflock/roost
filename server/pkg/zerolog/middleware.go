// middleware.go — HTTP middleware that logs stream requests using only
// permitted (non-PII) fields from the SafeLogger allowlist.
//
// Usage (standard net/http):
//
//	sl := zerolog.New("relay")
//	mux.Handle("/hls/", zerolog.StreamLoggingMiddleware(sl, "/hls/")(yourHandler))
//
// Usage (with chi):
//
//	r.Use(zerolog.StreamLoggingMiddleware(sl, ""))
//
// The middleware does NOT log the full request path — only the path prefix
// (first two segments). This prevents channel IDs or stream tokens that
// appear in the path from being written to logs.
package zerolog

import (
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
)

// StreamLoggingMiddleware returns an http.Handler middleware that logs each
// request using only safe (non-PII) fields.
//
// pathPrefixOverride — if non-empty, use this string as the "path_prefix"
// log field instead of deriving it from the request path. Use when the
// handler is mounted at a known, safe prefix (e.g. "/hls/").
func StreamLoggingMiddleware(sl *SafeLogger, pathPrefixOverride string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			reqID := uuid.New().String()

			// Wrap ResponseWriter to capture status code.
			wrapped := &responseRecorder{ResponseWriter: w, status: http.StatusOK}

			next.ServeHTTP(wrapped, r)

			duration := time.Since(start)
			prefix := pathPrefixOverride
			if prefix == "" {
				prefix = safePathPrefix(r.URL.Path)
			}

			sl.Log(Fields{
				"request_id":  reqID,
				"status":      wrapped.status,
				"method":      r.Method,
				"path_prefix": prefix,
				"duration_ms": duration.Milliseconds(),
				// CF-Ray header is safe (see fields.go).
				"cf_ray": r.Header.Get("CF-Ray"),
			})
		})
	}
}

// responseRecorder wraps http.ResponseWriter to capture the status code.
// WriteHeader is called once; subsequent calls are no-ops.
type responseRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (r *responseRecorder) WriteHeader(status int) {
	if r.wroteHeader {
		return
	}
	r.status = status
	r.wroteHeader = true
	r.ResponseWriter.WriteHeader(status)
}

func (r *responseRecorder) Write(b []byte) (int, error) {
	if !r.wroteHeader {
		r.WriteHeader(http.StatusOK)
	}
	return r.ResponseWriter.Write(b)
}

// safePathPrefix extracts the first two non-empty path segments.
// "/hls/ch001/seg0042.ts" → "/hls/ch001"
// "/relay/live/stream.m3u8" → "/relay/live"
// "/health" → "/health"
func safePathPrefix(path string) string {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	var safe []string
	for _, p := range parts {
		if p != "" {
			safe = append(safe, p)
		}
		if len(safe) == 2 {
			break
		}
	}
	if len(safe) == 0 {
		return "/"
	}
	return "/" + strings.Join(safe, "/")
}
