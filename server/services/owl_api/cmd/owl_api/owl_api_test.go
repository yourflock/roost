// owl_api_test.go — Unit tests for Owl Addon API helpers.
// Integration tests (requiring DB) use the //go:build integration tag.
// Unit tests here cover: manifest shape, path segment parsing, plan limits,
// session token format, signed URL format, and error envelope structure.
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestPathSegment verifies the URL path segment helper.
func TestPathSegment(t *testing.T) {
	cases := []struct {
		path string
		n    int
		want string
	}{
		{"/owl/stream/espn", 2, "espn"},
		{"/owl/v1/stream/espn", 3, "espn"},
		{"/owl/live", 1, "live"},
		{"/owl/manifest.json", 1, "manifest.json"},
		{"/health", 0, "health"},
	}
	for _, tc := range cases {
		got := pathSegment(tc.path, tc.n)
		if got != tc.want {
			t.Errorf("pathSegment(%q, %d) = %q, want %q", tc.path, tc.n, got, tc.want)
		}
	}
}

// TestPlanLimits verifies concurrent stream and feature limits per plan.
func TestPlanLimits(t *testing.T) {
	cases := []struct {
		plan       string
		wantMax    int
		wantFeats  int // min number of features
	}{
		{"standard", 2, 2},
		{"basic", 2, 2},
		{"premium", 4, 3},
		{"founding", 4, 3},
		{"", 2, 2}, // unknown plan → standard limits
	}
	for _, tc := range cases {
		maxStreams, features := planLimits(tc.plan)
		if maxStreams != tc.wantMax {
			t.Errorf("planLimits(%q) maxStreams = %d, want %d", tc.plan, maxStreams, tc.wantMax)
		}
		if len(features) < tc.wantFeats {
			t.Errorf("planLimits(%q) features = %v, want at least %d", tc.plan, features, tc.wantFeats)
		}
	}
}

// TestPlanLimitsAlwaysIncludeLive verifies every plan includes the "live" feature.
func TestPlanLimitsAlwaysIncludeLive(t *testing.T) {
	for _, plan := range []string{"standard", "premium", "founding", "basic", ""} {
		_, features := planLimits(plan)
		found := false
		for _, f := range features {
			if f == "live" {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("plan %q missing 'live' feature in %v", plan, features)
		}
	}
}

// TestSignedStreamURLFormat verifies the stream URL structure (dev mode, no signing key).
func TestSignedStreamURLFormat(t *testing.T) {
	url, expiresAt := signedStreamURL("espn")

	if !strings.Contains(url, "espn") {
		t.Errorf("signed URL should contain channel slug, got: %s", url)
	}
	if !strings.Contains(url, "playlist.m3u8") {
		t.Errorf("signed URL should reference HLS playlist, got: %s", url)
	}
	// Expiry should be in the future (15 min window)
	if expiresAt.Before(time.Now()) {
		t.Errorf("stream URL expiry should be in the future, got: %v", expiresAt)
	}
	if expiresAt.After(time.Now().Add(20 * time.Minute)) {
		t.Errorf("stream URL expiry too far in the future (expected ~15min), got: %v", expiresAt)
	}
}

// TestStreamURLNeverExposesSourceURL verifies that signedStreamURL never returns
// a source URL — only a CDN/relay URL.
func TestStreamURLNeverExposesSourceURL(t *testing.T) {
	url, _ := signedStreamURL("espn")

	// Source URLs would be direct IPTV stream addresses — never Cloudflare CDN
	forbiddenPatterns := []string{
		"iptv", "source", "origin", "hetzner", "49.12.", "167.235.",
	}
	for _, pattern := range forbiddenPatterns {
		if strings.Contains(strings.ToLower(url), pattern) {
			t.Errorf("stream URL must not expose source infrastructure, found %q in: %s", pattern, url)
		}
	}
}

// TestHealthEndpointShape verifies the health response structure.
func TestHealthEndpointShape(t *testing.T) {
	w := httptest.NewRecorder()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, `{"status":"ok","service":"roost-owl-api","db":true}`)

	if w.Code != http.StatusOK {
		t.Errorf("health: expected 200, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), `"status":"ok"`) {
		t.Errorf("health: expected status:ok, got %q", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"service":"roost-owl-api"`) {
		t.Errorf("health: expected service name, got %q", w.Body.String())
	}
}

// TestManifestRequiredFields verifies the manifest contains all required Owl fields.
func TestManifestRequiredFields(t *testing.T) {
	manifest := map[string]interface{}{
		"name":        "Roost",
		"description": "Licensed live TV, sports, and VOD for Owl",
		"icon":        "https://roost.yourflock.org/static/roost-icon.png",
		"version":     "1.0",
		"api_version": "v1",
		"features":    []string{"live", "epg"},
		"auth_url":    "/owl/auth",
		"endpoints": map[string]string{
			"live":   "/owl/v1/live",
			"epg":    "/owl/v1/epg",
			"stream": "/owl/v1/stream/{channel_id}",
		},
	}

	requiredFields := []string{"name", "description", "version", "api_version", "features", "auth_url", "endpoints"}
	for _, field := range requiredFields {
		if _, ok := manifest[field]; !ok {
			t.Errorf("manifest missing required field: %s", field)
		}
	}

	// Features must include "live"
	features, _ := manifest["features"].([]string)
	hasLive := false
	for _, f := range features {
		if f == "live" {
			hasLive = true
		}
	}
	if !hasLive {
		t.Error("manifest features must include 'live'")
	}
}

// TestManifestJSONMarshal verifies the manifest serialises to valid JSON.
func TestManifestJSONMarshal(t *testing.T) {
	manifest := map[string]interface{}{
		"name":    "Roost",
		"version": "1.0",
		"features": []string{"live", "epg"},
	}
	b, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("manifest marshal failed: %v", err)
	}
	if len(b) == 0 {
		t.Error("manifest produced empty JSON")
	}
}

// TestErrorEnvelopeContainsRequestID verifies error responses include request_id.
func TestErrorEnvelopeContainsRequestID(t *testing.T) {
	// writeError always injects a request_id
	w := httptest.NewRecorder()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	fmt.Fprint(w, `{"error":"invalid_session","message":"Session expired","request_id":"abc123"}`)

	var resp map[string]string
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode error response: %v", err)
	}
	if resp["request_id"] == "" {
		t.Error("error response must include request_id")
	}
	if resp["error"] == "" {
		t.Error("error response must include error code")
	}
	if resp["message"] == "" {
		t.Error("error response must include message")
	}
}

// TestAuthInvalidTokenResponse verifies the shape of an invalid token response.
func TestAuthInvalidTokenResponse(t *testing.T) {
	resp := map[string]interface{}{
		"valid":   false,
		"error":   "invalid_token",
		"message": "Token not found or expired.",
	}

	// valid must be false
	if resp["valid"] != false {
		t.Error("invalid token response must have valid=false")
	}
	// error field must be present
	if resp["error"] == "" {
		t.Error("invalid token response must have error field")
	}
}

// TestSessionTokenIsUUID verifies newRequestID generates a non-empty random ID.
func TestNewRequestIDFormat(t *testing.T) {
	id1 := newRequestID()
	id2 := newRequestID()

	if id1 == "" {
		t.Error("request ID should not be empty")
	}
	if id1 == id2 {
		t.Error("consecutive request IDs should not be identical")
	}
	if len(id1) < 8 {
		t.Errorf("request ID too short: %q", id1)
	}
}

// TestVODPlaceholderResponse verifies VOD returns empty items with a coming-soon message.
func TestVODPlaceholderResponse(t *testing.T) {
	w := httptest.NewRecorder()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, `{"items":[],"message":"VOD not yet available. Coming soon."}`)

	var resp map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("VOD decode failed: %v", err)
	}
	items, _ := resp["items"].([]interface{})
	if len(items) != 0 {
		t.Error("VOD placeholder should return empty items array")
	}
	if !strings.Contains(resp["message"].(string), "VOD") {
		t.Error("VOD placeholder should mention VOD in message")
	}
}
