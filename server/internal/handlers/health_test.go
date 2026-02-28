// health_test.go — Unit tests for health check handlers.
// P21.3.001: Health check handler tests
package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// ── Liveness ──────────────────────────────────────────────────────────────────

func TestLiveness_AlwaysOK(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()
	Liveness(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Liveness status = %d; want 200", w.Code)
	}

	var resp healthResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Status != "ok" {
		t.Errorf("Liveness status = %q; want ok", resp.Status)
	}
}

func TestLiveness_ContentType(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()
	Liveness(w, req)

	ct := w.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("Content-Type = %q; want application/json", ct)
	}
}

// ── Readiness — no deps ───────────────────────────────────────────────────────

func TestReadiness_NoDeps_AlwaysOK(t *testing.T) {
	h := Readiness(nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/ready", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Readiness(no deps) status = %d; want 200", w.Code)
	}
}

// ── mockPinger — test helper ──────────────────────────────────────────────────

type mockPinger struct {
	err error
}

func (m *mockPinger) PingContext(_ context.Context) error {
	return m.err
}

// ── Readiness — redis only ────────────────────────────────────────────────────

func TestReadiness_HealthyRedis(t *testing.T) {
	h := Readiness(nil, &mockPinger{err: nil})
	req := httptest.NewRequest(http.MethodGet, "/ready", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d; want 200", w.Code)
	}
	var resp healthResponse
	json.NewDecoder(w.Body).Decode(&resp) //nolint:errcheck
	if resp.Checks["redis"] != "ok" {
		t.Errorf("checks.redis = %q; want ok", resp.Checks["redis"])
	}
}

func TestReadiness_DegradedRedis(t *testing.T) {
	h := Readiness(nil, &mockPinger{err: errors.New("connection refused")})
	req := httptest.NewRequest(http.MethodGet, "/ready", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d; want 503", w.Code)
	}
	var resp healthResponse
	json.NewDecoder(w.Body).Decode(&resp) //nolint:errcheck
	if resp.Status != "degraded" {
		t.Errorf("status = %q; want degraded", resp.Status)
	}
	if resp.Checks["redis"] == "ok" {
		t.Error("degraded redis should not show ok")
	}
}

// ── Readiness — status field ──────────────────────────────────────────────────

func TestReadiness_StatusDegradedOnAnyFailure(t *testing.T) {
	h := Readiness(nil, &mockPinger{err: errors.New("timeout")})
	req := httptest.NewRequest(http.MethodGet, "/ready", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	var resp healthResponse
	json.NewDecoder(w.Body).Decode(&resp) //nolint:errcheck
	if resp.Status != "degraded" {
		t.Errorf("want degraded, got %q", resp.Status)
	}
}

func TestReadiness_JSONResponse(t *testing.T) {
	h := Readiness(nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/ready", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q; want application/json", ct)
	}
}
