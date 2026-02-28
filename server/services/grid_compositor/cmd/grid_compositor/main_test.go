// main_test.go — Grid Compositor service unit tests.
package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yourflock/roost/services/grid_compositor/internal/compositor"
)

func TestHealthEndpoint(t *testing.T) {
	cfg := loadConfig()
	mgr := compositor.New(cfg.SegmentDir, cfg.OutputBase)
	h := &handler{cfg: cfg, mgr: mgr}

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	h.handleHealth(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "roost-grid-compositor") {
		t.Errorf("expected service name in response, got: %s", body)
	}
}

func TestPathSegment(t *testing.T) {
	tests := []struct {
		path string
		n    int
		want string
	}{
		{"/compositor/sessions/abc-123", 2, "abc-123"},
		{"/compositor/sessions/abc-123/stream.m3u8", 3, "stream.m3u8"},
		{"/compositor/sessions", 2, ""},
	}
	for _, tt := range tests {
		got := pathSegment(tt.path, tt.n)
		if got != tt.want {
			t.Errorf("pathSegment(%q, %d) = %q, want %q", tt.path, tt.n, got, tt.want)
		}
	}
}

func TestCreateSessionMissingChannels(t *testing.T) {
	cfg := loadConfig()
	mgr := compositor.New("/tmp/test-segments", "/tmp/test-compositor-output")
	h := &handler{cfg: cfg, mgr: mgr}

	body := strings.NewReader(`{"layout":"2x2","channels":["ch1"]}`)
	req := httptest.NewRequest(http.MethodPost, "/compositor/sessions", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.handleCreate(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d (body: %s)", rec.Code, rec.Body.String())
	}
}

func TestGetSessionNotFound(t *testing.T) {
	cfg := loadConfig()
	mgr := compositor.New(cfg.SegmentDir, cfg.OutputBase)
	h := &handler{cfg: cfg, mgr: mgr}

	req := httptest.NewRequest(http.MethodGet, "/compositor/sessions/nonexistent", nil)
	rec := httptest.NewRecorder()
	h.handleGet(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestListSessionsEmpty(t *testing.T) {
	cfg := loadConfig()
	mgr := compositor.New(cfg.SegmentDir, cfg.OutputBase)
	h := &handler{cfg: cfg, mgr: mgr}

	req := httptest.NewRequest(http.MethodGet, "/compositor/sessions", nil)
	rec := httptest.NewRecorder()
	h.handleList(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"count":0`) {
		t.Errorf("expected empty session list, got: %s", rec.Body.String())
	}
}
