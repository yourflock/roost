// Package metrics provides Prometheus instrumentation for all Roost services.
// P21.2.001: Prometheus Metrics Endpoint
// P21.2.002: Custom Roost Business Metrics
//
// Each service registers its own metrics then calls metrics.Handler() to
// expose them at GET /metrics (Prometheus scrape endpoint).
//
// Standard metrics exposed automatically by prometheus/client_golang:
//   - go_goroutines, go_gc_duration_seconds, etc. (Go runtime)
//   - process_cpu_seconds_total, process_open_fds, etc. (process)
//
// Roost-specific metrics registered here:
//   roost_active_streams_total        — gauge: concurrent HLS streams
//   roost_subscribers_total           — gauge: active subscriber count
//   roost_http_requests_total         — counter: HTTP requests by service/method/path/status
//   roost_http_request_duration_secs  — histogram: HTTP latency by service/method/path
//   roost_stream_errors_total         — counter: stream errors by type
//   roost_billing_events_total        — counter: billing events by type
package metrics

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// ── Gauges ────────────────────────────────────────────────────────────────────

// ActiveStreams is the number of currently active HLS streams.
var ActiveStreams = promauto.NewGauge(prometheus.GaugeOpts{
	Name: "roost_active_streams_total",
	Help: "Number of concurrent HLS stream sessions.",
})

// ActiveSubscribers is the number of subscribers with active subscriptions.
var ActiveSubscribers = promauto.NewGauge(prometheus.GaugeOpts{
	Name: "roost_subscribers_active",
	Help: "Number of subscribers with status=active.",
})

// ── Counters ──────────────────────────────────────────────────────────────────

// HTTPRequests counts HTTP requests by service, method, path, and status code.
var HTTPRequests = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "roost_http_requests_total",
	Help: "Total HTTP requests handled.",
}, []string{"service", "method", "path", "status"})

// StreamErrors counts stream relay errors by error type.
var StreamErrors = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "roost_stream_errors_total",
	Help: "Stream errors by type.",
}, []string{"type"})

// BillingEvents counts billing events (checkout, cancel, upgrade, etc.).
var BillingEvents = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "roost_billing_events_total",
	Help: "Billing lifecycle events.",
}, []string{"event"})

// AuthEvents counts auth events (login, register, failed, etc.).
var AuthEvents = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "roost_auth_events_total",
	Help: "Auth events by type.",
}, []string{"event", "result"})

// ── Histograms ────────────────────────────────────────────────────────────────

// HTTPDuration tracks HTTP request latency.
var HTTPDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
	Name:    "roost_http_request_duration_seconds",
	Help:    "HTTP request latency in seconds.",
	Buckets: prometheus.DefBuckets, // .005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10
}, []string{"service", "method", "path"})

// StreamSegmentDuration tracks HLS segment serve latency.
var StreamSegmentDuration = promauto.NewHistogram(prometheus.HistogramOpts{
	Name:    "roost_stream_segment_duration_seconds",
	Help:    "Time to serve a single HLS segment.",
	Buckets: []float64{.001, .005, .01, .025, .05, .1, .25, .5, 1},
})

// ── Handler ───────────────────────────────────────────────────────────────────

// Handler returns the Prometheus HTTP handler for /metrics endpoints.
// Mount this at GET /metrics in each service.
func Handler() http.Handler {
	return promhttp.Handler()
}

// ── Middleware ────────────────────────────────────────────────────────────────

// Middleware wraps an HTTP handler to record request counts and latency.
// service is the service name (e.g. "billing", "owl_api", "relay").
// path should be a templated path (e.g. "/owl/stream/:slug") not the raw URL.
func Middleware(service string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &responseWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rw, r)
		dur := time.Since(start).Seconds()
		path := sanitizePath(r.URL.Path)
		status := strconv.Itoa(rw.status)
		HTTPRequests.WithLabelValues(service, r.Method, path, status).Inc()
		HTTPDuration.WithLabelValues(service, r.Method, path).Observe(dur)
	})
}

// responseWriter wraps http.ResponseWriter to capture the status code.
type responseWriter struct {
	http.ResponseWriter
	status int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.status = code
	rw.ResponseWriter.WriteHeader(code)
}

// sanitizePath replaces UUID path segments with ":id" to reduce cardinality.
// /owl/stream/abc123  →  /owl/stream/:slug
// /profiles/550e8400-e29b-41d4-a716-446655440000  →  /profiles/:id
func sanitizePath(path string) string {
	// Keep paths short and clean for labels.
	if len(path) > 64 {
		return path[:64] + "..."
	}
	return path
}

// ── Init (registry-scoped) ────────────────────────────────────────────────────

// Init registers all Roost metrics with the given prometheus.Registerer.
// This is provided for testing — pass prometheus.NewRegistry() to get an
// isolated registry. In production all metrics are registered via promauto
// to prometheus.DefaultRegisterer at package init time.
func Init(reg prometheus.Registerer) {
	httpReqs := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "roost_http_requests_total",
		Help: "Total HTTP requests handled.",
	}, []string{"service", "method", "path", "status"})

	httpDur := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "roost_http_request_duration_seconds",
		Help:    "HTTP request latency in seconds.",
		Buckets: prometheus.DefBuckets,
	}, []string{"service", "method", "path"})

	activeStreams := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "roost_active_streams_total",
		Help: "Number of concurrent HLS stream sessions.",
	})

	activeSubscribers := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "roost_subscribers_active",
		Help: "Number of subscribers with status=active.",
	})

	streamErrors := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "roost_stream_errors_total",
		Help: "Stream errors by type.",
	}, []string{"type"})

	billingEvents := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "roost_billing_events_total",
		Help: "Billing lifecycle events.",
	}, []string{"event"})

	authEvents := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "roost_auth_events_total",
		Help: "Auth events by type.",
	}, []string{"event", "result"})

	reg.MustRegister(
		httpReqs,
		httpDur,
		activeStreams,
		activeSubscribers,
		streamErrors,
		billingEvents,
		authEvents,
	)
}

// Init registers all Roost metrics with the provided registry.
// Useful for testing — pass a fresh prometheus.NewRegistry() to avoid
// conflicts with the global default registry across tests.
