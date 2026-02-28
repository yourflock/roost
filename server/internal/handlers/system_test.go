// system_test.go — Unit tests for HandleSystemInfo.
// P20.3.001: /system/info tests in both modes
package handlers_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/unyeco/roost/internal/config"
	"github.com/unyeco/roost/internal/handlers"
)

func makeConfig(mode config.Mode) *config.Config {
	return &config.Config{
		Mode:      mode,
		JWTSecret: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", // >= 32 chars
	}
}

func TestHandleSystemInfo_PrivateMode(t *testing.T) {
	cfg := makeConfig(config.ModePrivate)
	h := handlers.HandleSystemInfo(cfg)

	req := httptest.NewRequest(http.MethodGet, "/system/info", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	var info handlers.SystemInfo
	if err := json.NewDecoder(rr.Body).Decode(&info); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if info.Mode != "private" {
		t.Errorf("expected mode=private, got %q", info.Mode)
	}
	if info.Version == "" {
		t.Error("expected non-empty version")
	}
	if info.Features["subscriber_management"] {
		t.Error("subscriber_management should be false in private mode")
	}
	if info.Features["billing"] {
		t.Error("billing should be false in private mode")
	}
	if info.Features["cdn_relay"] {
		t.Error("cdn_relay should be false in private mode")
	}
}

func TestHandleSystemInfo_PublicMode(t *testing.T) {
	cfg := makeConfig(config.ModePublic)
	h := handlers.HandleSystemInfo(cfg)

	req := httptest.NewRequest(http.MethodGet, "/system/info", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	var info handlers.SystemInfo
	if err := json.NewDecoder(rr.Body).Decode(&info); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if info.Mode != "public" {
		t.Errorf("expected mode=public, got %q", info.Mode)
	}
	if !info.Features["subscriber_management"] {
		t.Error("subscriber_management should be true in public mode")
	}
	if !info.Features["billing"] {
		t.Error("billing should be true in public mode")
	}
	if !info.Features["cdn_relay"] {
		t.Error("cdn_relay should be true in public mode")
	}
}

func TestHandleSystemInfo_MethodNotAllowed(t *testing.T) {
	cfg := makeConfig(config.ModePrivate)
	h := handlers.HandleSystemInfo(cfg)

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		req := httptest.NewRequest(method, "/system/info", nil)
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)

		if rr.Code != http.StatusMethodNotAllowed {
			t.Errorf("method %s: expected 405, got %d", method, rr.Code)
		}
	}
}

func TestHandleSystemInfo_ContentType(t *testing.T) {
	cfg := makeConfig(config.ModePublic)
	h := handlers.HandleSystemInfo(cfg)

	req := httptest.NewRequest(http.MethodGet, "/system/info", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	ct := rr.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("expected Content-Type application/json, got %q", ct)
	}
}
