//go:build integration
// +build integration

package tests

import (
	"context"
	"net/http"
	"testing"
	"time"
)

func TestHealthEndpointResponds(t *testing.T) {
	// Start daemon in test mode (no real DVB device needed).
	// This test verifies the health endpoint returns 200 when antbox is running.

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = ctx

	resp, err := http.Get("http://localhost:8087/health")
	if err != nil {
		t.Skipf("antbox not running: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}
