// public_mode_test.go — Integration tests for ROOST_MODE=public/private behavior.
// P20.3.002: Integration test
//
// Tests:
//  1. All /billing/* and /addon/* endpoints return 403 when mode=private
//  2. /healthz and /system/info are accessible in both modes
//  3. CDN URL signing round-trip test (no network required)
//
// These tests use net/http/httptest only — no live DB or external services required.
package tests

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/unyeco/roost/internal/cdn"
	"github.com/unyeco/roost/internal/config"
	"github.com/unyeco/roost/internal/handlers"
	"github.com/unyeco/roost/internal/middleware"
)

// buildTestMux wires up a minimal mux matching the gateway structure:
//   - /healthz — always open
//   - /system/info — always open, reports mode
//   - /billing/* — RequirePublicMode gate
//   - /addon/* — RequirePublicMode gate
func buildTestMux(cfg *config.Config) http.Handler {
	mux := http.NewServeMux()

	// Always-open endpoints.
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"status":"ok","mode":%q}`, cfg.Mode)
	})
	mux.Handle("/system/info", handlers.HandleSystemInfo(cfg))

	// Protected billing stub.
	billingHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"service":"billing"}`))
	})
	mux.Handle("/billing/",
		middleware.RequirePublicMode(cfg.IsPublicMode, billingHandler))

	// Protected addon stub.
	addonHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"service":"addon"}`))
	})
	mux.Handle("/addon/",
		middleware.RequirePublicMode(cfg.IsPublicMode, addonHandler))

	return mux
}

// --- Test: billing and addon routes require public mode ---

func TestPublicModeGate_BillingEndpointsReturn403InPrivateMode(t *testing.T) {
	cfg := &config.Config{Mode: config.ModePrivate, JWTSecret: strings.Repeat("a", 32)}
	srv := httptest.NewServer(buildTestMux(cfg))
	defer srv.Close()

	billingPaths := []string{
		"/billing/checkout",
		"/billing/webhook",
		"/billing/subscription",
		"/billing/cancel",
		"/billing/plans",
	}

	for _, path := range billingPaths {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		resp.Body.Close()

		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("GET %s in private mode: expected 403, got %d", path, resp.StatusCode)
		}
	}
}

func TestPublicModeGate_AddonEndpointsReturn403InPrivateMode(t *testing.T) {
	cfg := &config.Config{Mode: config.ModePrivate, JWTSecret: strings.Repeat("a", 32)}
	srv := httptest.NewServer(buildTestMux(cfg))
	defer srv.Close()

	addonPaths := []string{
		"/addon/manifest",
		"/addon/auth",
		"/addon/live",
	}

	for _, path := range addonPaths {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		resp.Body.Close()

		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("GET %s in private mode: expected 403, got %d", path, resp.StatusCode)
		}
	}
}

func TestPublicModeGate_ProtectedEndpointsAccessibleInPublicMode(t *testing.T) {
	cfg := &config.Config{Mode: config.ModePublic, JWTSecret: strings.Repeat("a", 32)}
	srv := httptest.NewServer(buildTestMux(cfg))
	defer srv.Close()

	paths := []string{"/billing/checkout", "/addon/manifest"}
	for _, path := range paths {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s in public mode: expected 200, got %d", path, resp.StatusCode)
		}
	}
}

// --- Test: healthz accessible in both modes ---

func TestHealthzAccessibleInBothModes(t *testing.T) {
	for _, mode := range []config.Mode{config.ModePrivate, config.ModePublic} {
		t.Run(string(mode), func(t *testing.T) {
			cfg := &config.Config{Mode: mode, JWTSecret: strings.Repeat("a", 32)}
			srv := httptest.NewServer(buildTestMux(cfg))
			defer srv.Close()

			resp, err := http.Get(srv.URL + "/healthz")
			if err != nil {
				t.Fatalf("GET /healthz: %v", err)
			}
			resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				t.Errorf("mode=%s: GET /healthz expected 200, got %d", mode, resp.StatusCode)
			}
		})
	}
}

// --- Test: /system/info reports correct mode and features ---

func TestSystemInfoAccessibleInBothModes(t *testing.T) {
	cases := []struct {
		mode            config.Mode
		wantBilling     bool
		wantSubscribers bool
		wantCDN         bool
	}{
		{config.ModePrivate, false, false, false},
		{config.ModePublic, true, true, true},
	}

	for _, tc := range cases {
		t.Run(string(tc.mode), func(t *testing.T) {
			cfg := &config.Config{Mode: tc.mode, JWTSecret: strings.Repeat("a", 32)}
			srv := httptest.NewServer(buildTestMux(cfg))
			defer srv.Close()

			resp, err := http.Get(srv.URL + "/system/info")
			if err != nil {
				t.Fatalf("GET /system/info: %v", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				t.Errorf("expected 200, got %d", resp.StatusCode)
			}

			var info handlers.SystemInfo
			if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
				t.Fatalf("decode response: %v", err)
			}

			if info.Mode != string(tc.mode) {
				t.Errorf("expected mode=%s, got %q", tc.mode, info.Mode)
			}
			if info.Features["billing"] != tc.wantBilling {
				t.Errorf("billing feature: want %v, got %v", tc.wantBilling, info.Features["billing"])
			}
			if info.Features["subscriber_management"] != tc.wantSubscribers {
				t.Errorf("subscriber_management feature: want %v, got %v", tc.wantSubscribers, info.Features["subscriber_management"])
			}
			if info.Features["cdn_relay"] != tc.wantCDN {
				t.Errorf("cdn_relay feature: want %v, got %v", tc.wantCDN, info.Features["cdn_relay"])
			}
		})
	}
}

// --- Test: CDN URL signing round-trip (no network required) ---

const integTestSecret = "integration-test-hmac-secret-32-bytes-long"

func TestCDNSigningRoundTrip(t *testing.T) {
	path := "/stream/channel-uk-bbc1/seg042.ts"
	expiresAt := time.Now().Add(15 * time.Minute).Unix()

	signed, err := cdn.SignURL("https://stream.roost.unity.dev", integTestSecret, path, expiresAt)
	if err != nil {
		t.Fatalf("SignURL: %v", err)
	}

	// Parse the signed URL and extract params.
	u, err := url.Parse(signed)
	if err != nil {
		t.Fatalf("parse signed URL: %v", err)
	}

	var parsedExpires int64
	if _, err := fmt.Sscanf(u.Query().Get("expires"), "%d", &parsedExpires); err != nil {
		t.Fatalf("parse expires: %v", err)
	}
	sig := u.Query().Get("sig")

	// Validation must pass.
	if !cdn.ValidateSignature(integTestSecret, path, parsedExpires, sig) {
		t.Error("ValidateSignature failed on a freshly signed URL")
	}

	// Tampered path must fail.
	if cdn.ValidateSignature(integTestSecret, "/stream/channel-uk-bbc2/seg042.ts", parsedExpires, sig) {
		t.Error("ValidateSignature should reject tampered path")
	}

	// Tampered expiry must fail.
	if cdn.ValidateSignature(integTestSecret, path, parsedExpires+1, sig) {
		t.Error("ValidateSignature should reject tampered expiry")
	}

	// Wrong secret must fail.
	if cdn.ValidateSignature("wrong-secret-of-sufficient-length-here", path, parsedExpires, sig) {
		t.Error("ValidateSignature should reject wrong secret")
	}
}

func TestCDNSigningExpiredURL(t *testing.T) {
	path := "/stream/channel-us-cnn/seg001.ts"
	pastExpiry := time.Now().Add(-5 * time.Minute).Unix()

	signed, err := cdn.SignURL("https://stream.roost.unity.dev", integTestSecret, path, pastExpiry)
	if err != nil {
		t.Fatalf("SignURL: %v", err)
	}

	u, _ := url.Parse(signed)
	sig := u.Query().Get("sig")

	if cdn.ValidateSignature(integTestSecret, path, pastExpiry, sig) {
		t.Error("ValidateSignature should reject already-expired URL")
	}
}
