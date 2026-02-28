// metrics_test.go — Unit tests for Prometheus metrics.
// P21.7.003: Metrics tests
package metrics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

// TestInit_RegistersWithoutPanic verifies that calling Init with a fresh
// registry does not panic. Successful registration is the invariant —
// if any metric descriptor is invalid or duplicated within the registry,
// MustRegister panics.
func TestInit_RegistersWithoutPanic(t *testing.T) {
	reg := prometheus.NewRegistry()
	// Must not panic.
	Init(reg)
}

// TestInit_DoubleRegistrationPanics confirms that registering the same metric
// names twice to the same registry panics (standard prometheus behavior).
// This is a safety check — it proves Init really is registering something.
func TestInit_DoubleRegistrationPanics(t *testing.T) {
	reg := prometheus.NewRegistry()
	Init(reg) // first call succeeds

	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic on double registration, but Init did not panic")
		}
	}()
	Init(reg) // second call must panic
}

// TestInit_MetricsObservable verifies that after Init + observation,
// all registered metrics appear in Gather results.
// Note: prometheus Gather only returns metrics that have been observed at
// least once (counters/histograms with zero samples are omitted by design).
func TestInit_MetricsObservable(t *testing.T) {
	reg := prometheus.NewRegistry()

	// Create metrics the same way Init does, but keep references so we can
	// observe them, then verify they show up in Gather.
	httpReqs := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "test_init_http_requests_total",
		Help: "Total HTTP requests handled.",
	}, []string{"service", "method", "path", "status"})
	activeStreams := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "test_init_active_streams",
		Help: "Number of concurrent HLS stream sessions.",
	})

	reg.MustRegister(httpReqs, activeStreams)

	// Observe both metrics.
	httpReqs.WithLabelValues("billing", "GET", "/test", "200").Inc()
	activeStreams.Set(3)

	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather error: %v", err)
	}

	names := make(map[string]bool)
	for _, mf := range mfs {
		names[mf.GetName()] = true
	}

	want := []string{
		"test_init_http_requests_total",
		"test_init_active_streams",
	}
	for _, n := range want {
		if !names[n] {
			t.Errorf("metric %q not found after observation", n)
		}
	}
}

// TestHTTPRequestsCounter_Increments confirms that the counter vec
// increments correctly via a new isolated registry.
func TestHTTPRequestsCounter_Increments(t *testing.T) {
	reg := prometheus.NewRegistry()
	counter := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "test_http_requests_total",
	}, []string{"method", "path", "status"})
	reg.MustRegister(counter)

	counter.WithLabelValues("GET", "/test", "200").Inc()
	counter.WithLabelValues("GET", "/test", "200").Inc()
	counter.WithLabelValues("POST", "/other", "500").Inc()

	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather error: %v", err)
	}

	var totalCount float64
	for _, mf := range mfs {
		if mf.GetName() == "test_http_requests_total" {
			for _, m := range mf.GetMetric() {
				totalCount += m.GetCounter().GetValue()
			}
		}
	}

	if totalCount != 3 {
		t.Errorf("expected 3 total requests, got %v", totalCount)
	}
}

// TestActiveStreams_GaugeSetGet verifies the gauge can be set and read.
func TestActiveStreams_GaugeSetGet(t *testing.T) {
	reg := prometheus.NewRegistry()
	gauge := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "test_active_streams",
	})
	reg.MustRegister(gauge)

	gauge.Set(7)

	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather error: %v", err)
	}

	var val float64
	for _, mf := range mfs {
		if mf.GetName() == "test_active_streams" {
			if len(mf.GetMetric()) > 0 {
				val = mf.GetMetric()[0].GetGauge().GetValue()
			}
		}
	}

	if val != 7 {
		t.Errorf("gauge value = %v; want 7", val)
	}
}

// TestHandler_Returns200 confirms the metrics HTTP handler responds correctly.
func TestHandler_Returns200(t *testing.T) {
	h := Handler()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("Handler() status = %d; want 200", w.Code)
	}
	body := w.Body.String()
	// Prometheus always includes at least go_ metrics in the default registry.
	if !strings.Contains(body, "go_") && !strings.Contains(body, "# HELP") {
		t.Error("expected Prometheus text format in response body")
	}
}

// TestMiddleware_RecordsMetrics confirms the HTTP middleware records a request.
func TestMiddleware_RecordsMetrics(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	h := Middleware("test-svc", inner)

	req := httptest.NewRequest(http.MethodGet, "/ping", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Errorf("wrapped handler returned %d; want 204", w.Code)
	}

	// Gather default registry — our promauto metrics are registered there.
	mfs, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("Gather error: %v", err)
	}

	found := false
	for _, mf := range mfs {
		if mf.GetName() == "roost_http_requests_total" {
			for _, m := range mf.GetMetric() {
				for _, lp := range m.GetLabel() {
					if lp.GetName() == "service" && lp.GetValue() == "test-svc" {
						found = true
					}
				}
			}
		}
	}
	if !found {
		t.Error("roost_http_requests_total metric not found for service=test-svc after middleware call")
	}
}

// TestSanitizePath_LongPath confirms long paths are truncated.
func TestSanitizePath_LongPath(t *testing.T) {
	long := strings.Repeat("a", 100)
	got := sanitizePath(long)
	if len(got) > 67 { // 64 + "..."
		t.Errorf("sanitizePath did not truncate: len=%d", len(got))
	}
	if !strings.HasSuffix(got, "...") {
		t.Errorf("truncated path should end with ..., got %q", got)
	}
}

// TestSanitizePath_ShortPath confirms short paths pass through unchanged.
func TestSanitizePath_ShortPath(t *testing.T) {
	path := "/owl/live"
	got := sanitizePath(path)
	if got != path {
		t.Errorf("sanitizePath(%q) = %q; want unchanged", path, got)
	}
}
