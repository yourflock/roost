// main.go — Roost unified startup entrypoint.
// P20.1.003: Startup validation for public mode
//
// This binary:
//   1. Loads config from environment (via internal/config)
//   2. In public mode, validates external dependencies before accepting traffic:
//      - Stripe API key (lightweight /v1/balance ping)
//      - CDN relay health endpoint
//      - HMAC secret length (>= 32 bytes)
//   3. Registers /system/info on the local API mux
//   4. Exits non-zero on any validation failure so container orchestration can
//      restart or alert immediately rather than silently serving broken requests.
//
// For the full multi-service deployment, each service (auth, billing, owl_api,
// relay) runs its own binary. This entrypoint is for the orchestrator / health
// gateway that fronts all services and exposes /system/info + /healthz at the
// cluster level.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/yourflock/roost/internal/config"
	"github.com/yourflock/roost/internal/handlers"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config error: %v", err)
	}

	log.Printf("Roost starting in %s mode", cfg.Mode)

	if cfg.IsPublicMode() {
		if err := validatePublicMode(cfg); err != nil {
			log.Fatalf("public-mode startup validation failed: %v", err)
		}
		log.Printf("public-mode validation passed")
	}

	mux := http.NewServeMux()

	// Health check — accessible in both modes.
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"status":"ok","mode":%q}`, cfg.Mode)
	})

	// System info — reports mode and feature flags.
	mux.HandleFunc("/system/info", handlers.HandleSystemInfo(cfg))

	addr := ":" + cfg.Port
	log.Printf("Roost gateway listening on %s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("server error: %v", err)
	}
}

// validatePublicMode performs lightweight liveness checks for all external
// dependencies required in public mode. Uses net/http only — no heavy SDKs.
func validatePublicMode(cfg *config.Config) error {
	// 1. HMAC secret length (already checked by config.Load, belt-and-suspenders).
	if len(cfg.CDNHMACSecret) < 32 {
		return fmt.Errorf("CDN_HMAC_SECRET must be at least 32 bytes (got %d)", len(cfg.CDNHMACSecret))
	}

	// 2. Stripe API key — ping /v1/balance with the secret key.
	if err := pingStripe(cfg.StripeSecretKey); err != nil {
		return fmt.Errorf("Stripe validation failed: %w", err)
	}

	// 3. CDN relay health — GET {CDN_RELAY_URL}/health.
	if cfg.CDNRelayURL != "" {
		if err := pingHealth(cfg.CDNRelayURL + "/health"); err != nil {
			return fmt.Errorf("CDN relay health check failed (%s): %w", cfg.CDNRelayURL, err)
		}
	} else {
		log.Printf("warning: CDN_RELAY_URL not set; CDN relay validation skipped")
	}

	return nil
}

// pingStripe sends a lightweight GET /v1/balance to verify the Stripe secret
// key is valid. Uses Basic auth (sk_... as username, empty password).
// Stripe returns 200 on success, 401 if the key is invalid.
func pingStripe(secretKey string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.stripe.com/v1/balance", nil)
	if err != nil {
		return err
	}
	req.SetBasicAuth(secretKey, "")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("could not reach Stripe API: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("STRIPE_SECRET_KEY is invalid (HTTP 401 from Stripe)")
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected Stripe response: HTTP %d", resp.StatusCode)
	}
	return nil
}

// pingHealth sends a GET request to the given URL and expects a 200 response
// with a JSON body containing "status":"ok". Accepts any non-5xx as healthy
// to be lenient with different health endpoint implementations.
func pingHealth(url string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("could not reach %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 500 {
		return fmt.Errorf("health endpoint %s returned HTTP %d", url, resp.StatusCode)
	}

	// Best-effort JSON decode to confirm we got a health response.
	var body struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		// Non-JSON health endpoints are fine — we only check status code.
		return nil
	}
	if body.Status != "" && body.Status != "ok" && body.Status != "healthy" {
		log.Printf("warning: health endpoint %s returned status=%q", url, body.Status)
	}
	return nil
}

// getEnv returns an environment variable value with a fallback default.
func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
