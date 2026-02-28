// main.go — Roost ↔ Owl Community Addon API contract test runner.
//
// Tests all endpoints defined in .github/docs/owl-addon-api.md.
// Verifies response shapes, HTTP status codes, required fields, and auth behavior.
//
// Usage:
//
//	# Against a managed instance
//	ROOST_BASE_URL=https://api.roost.unity.dev/tv ROOST_API_TOKEN=roost_xxx go run ./cmd/contract-test/
//
//	# Against self-hosted instance
//	ROOST_BASE_URL=https://my-roost.example.com ROOST_API_TOKEN=roost_xxx go run ./cmd/contract-test/
//
// Exit codes:
//
//	0 = all tests pass
//	1 = one or more tests failed
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// --- Config ---

type config struct {
	BaseURL  string
	APIToken string
	Timeout  time.Duration
}

func loadConfig() config {
	base := os.Getenv("ROOST_BASE_URL")
	if base == "" {
		base = "https://api.roost.unity.dev/tv"
	}
	token := os.Getenv("ROOST_API_TOKEN")
	if token == "" {
		fmt.Fprintln(os.Stderr, "WARNING: ROOST_API_TOKEN not set — auth tests will use an invalid token")
		token = "roost_test_invalid_token_for_contract_shape_check"
	}
	return config{
		BaseURL:  strings.TrimRight(base, "/"),
		APIToken: token,
		Timeout:  15 * time.Second,
	}
}

// --- Test runner ---

type testResult struct {
	Name   string
	Pass   bool
	Status int
	Notes  string
}

var results []testResult
var sessionToken string

func run(name string, fn func(cfg config, client *http.Client) (bool, int, string), cfg config, client *http.Client) {
	pass, status, notes := fn(cfg, client)
	results = append(results, testResult{name, pass, status, notes})
	icon := "PASS"
	if !pass {
		icon = "FAIL"
	}
	fmt.Printf("[%s] %s (HTTP %d) — %s\n", icon, name, status, notes)
}

// --- Helper: HTTP request ---

func doRequest(client *http.Client, method, url string, body any, headers map[string]string) (*http.Response, []byte, error) {
	var bodyReader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, nil, err
		}
		bodyReader = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, url, bodyReader)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Owl-Version", "1.0.0")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	return resp, respBody, err
}

func authHeader() map[string]string {
	return map[string]string{"Authorization": "Bearer " + sessionToken}
}

// --- Tests ---

// T1: GET /owl/manifest.json — public discovery, no auth
func testManifest(cfg config, client *http.Client) (bool, int, string) {
	resp, body, err := doRequest(client, "GET", cfg.BaseURL+"/v1/owl/manifest.json", nil, nil)
	if err != nil {
		return false, 0, "connection error: " + err.Error()
	}
	if resp.StatusCode != 200 {
		return false, resp.StatusCode, fmt.Sprintf("expected 200, got %d. Body: %s", resp.StatusCode, string(body))
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return false, resp.StatusCode, "invalid JSON: " + err.Error()
	}
	required := []string{"name", "version", "api_version", "features", "auth_url", "endpoints"}
	for _, field := range required {
		if _, ok := m[field]; !ok {
			return false, resp.StatusCode, "missing required field: " + field
		}
	}
	name, _ := m["name"].(string)
	if name == "" {
		return false, resp.StatusCode, "manifest.name is empty"
	}
	return true, resp.StatusCode, fmt.Sprintf("manifest '%s' v%v OK", name, m["version"])
}

// T2: POST /owl/v1/auth — valid token
func testAuthValidToken(cfg config, client *http.Client) (bool, int, string) {
	body := map[string]any{
		"token": cfg.APIToken,
		"client": map[string]any{
			"platform":  "web",
			"version":   "1.0.0",
			"device_id": "contract-test-device-001",
		},
	}
	resp, respBody, err := doRequest(client, "POST", cfg.BaseURL+"/v1/auth", body, nil)
	if err != nil {
		return false, 0, "connection error: " + err.Error()
	}
	var m map[string]any
	if err := json.Unmarshal(respBody, &m); err != nil {
		return false, resp.StatusCode, "invalid JSON: " + err.Error()
	}
	// Token may be invalid in test env — check shape regardless
	if resp.StatusCode == 200 {
		required := []string{"valid", "session_token", "expires_at", "subscriber"}
		for _, field := range required {
			if _, ok := m[field]; !ok {
				return false, resp.StatusCode, "missing field: " + field
			}
		}
		valid, _ := m["valid"].(bool)
		if !valid {
			return false, resp.StatusCode, "valid=false on 200 response"
		}
		sessionToken, _ = m["session_token"].(string)
		return true, resp.StatusCode, "session_token obtained, subscriber info present"
	}
	if resp.StatusCode == 401 {
		// Invalid token — check error shape
		if errCode, ok := m["error"].(string); ok && errCode == "invalid_token" {
			return true, resp.StatusCode, "401 + error shape correct (token is invalid in this env)"
		}
		return false, resp.StatusCode, "401 but missing error field or wrong error code"
	}
	return false, resp.StatusCode, fmt.Sprintf("unexpected status. Body: %s", string(respBody))
}

// T3: POST /owl/v1/auth — invalid token must return error shape
func testAuthInvalidToken(cfg config, client *http.Client) (bool, int, string) {
	body := map[string]any{
		"token": "roost_definitely_invalid_0000000000",
		"client": map[string]any{
			"platform":  "web",
			"version":   "1.0.0",
			"device_id": "contract-test-device-002",
		},
	}
	resp, respBody, err := doRequest(client, "POST", cfg.BaseURL+"/v1/auth", body, nil)
	if err != nil {
		return false, 0, "connection error: " + err.Error()
	}
	if resp.StatusCode != 401 {
		return false, resp.StatusCode, fmt.Sprintf("expected 401 for invalid token, got %d", resp.StatusCode)
	}
	var m map[string]any
	if err := json.Unmarshal(respBody, &m); err != nil {
		return false, resp.StatusCode, "invalid JSON: " + err.Error()
	}
	required := []string{"valid", "error", "message"}
	for _, field := range required {
		if _, ok := m[field]; !ok {
			return false, resp.StatusCode, "missing field: " + field
		}
	}
	valid, _ := m["valid"].(bool)
	if valid {
		return false, resp.StatusCode, "valid=true for invalid token"
	}
	return true, resp.StatusCode, "correct 401 + error shape for invalid token"
}

// T4: GET /owl/v1/live — channel list
func testLiveChannels(cfg config, client *http.Client) (bool, int, string) {
	if sessionToken == "" {
		return false, 0, "no session_token — skipping (auth failed)"
	}
	resp, respBody, err := doRequest(client, "GET", cfg.BaseURL+"/v1/live", nil, authHeader())
	if err != nil {
		return false, 0, "connection error: " + err.Error()
	}
	if resp.StatusCode == 401 {
		return false, resp.StatusCode, "401 Unauthorized — session token invalid or expired"
	}
	if resp.StatusCode != 200 {
		return false, resp.StatusCode, fmt.Sprintf("expected 200, got %d", resp.StatusCode)
	}
	var m map[string]any
	if err := json.Unmarshal(respBody, &m); err != nil {
		return false, resp.StatusCode, "invalid JSON: " + err.Error()
	}
	required := []string{"channels", "total", "updated_at"}
	for _, field := range required {
		if _, ok := m[field]; !ok {
			return false, resp.StatusCode, "missing field: " + field
		}
	}
	channels, _ := m["channels"].([]any)
	if len(channels) > 0 {
		ch, _ := channels[0].(map[string]any)
		chRequired := []string{"id", "name", "category", "stream_url"}
		for _, f := range chRequired {
			if _, ok := ch[f]; !ok {
				return false, resp.StatusCode, fmt.Sprintf("channel missing field: %s", f)
			}
		}
		// Verify source_url is NOT leaked
		if _, hasSource := ch["source_url"]; hasSource {
			return false, resp.StatusCode, "SECURITY: channel response leaks source_url"
		}
	}
	total, _ := m["total"].(float64)
	return true, resp.StatusCode, fmt.Sprintf("%d channels returned, source_url not exposed", int(total))
}

// T5: GET /owl/v1/live with category filter
func testLiveChannelsFilter(cfg config, client *http.Client) (bool, int, string) {
	if sessionToken == "" {
		return false, 0, "no session_token — skipping"
	}
	url := cfg.BaseURL + "/v1/live?category=sports"
	resp, respBody, err := doRequest(client, "GET", url, nil, authHeader())
	if err != nil {
		return false, 0, "connection error: " + err.Error()
	}
	if resp.StatusCode == 401 {
		return false, resp.StatusCode, "401 Unauthorized"
	}
	if resp.StatusCode != 200 {
		return false, resp.StatusCode, fmt.Sprintf("expected 200, got %d", resp.StatusCode)
	}
	var m map[string]any
	if err := json.Unmarshal(respBody, &m); err != nil {
		return false, resp.StatusCode, "invalid JSON: " + err.Error()
	}
	if _, ok := m["channels"]; !ok {
		return false, resp.StatusCode, "missing channels field"
	}
	return true, resp.StatusCode, "category=sports filter accepted"
}

// T6: GET /owl/v1/epg — EPG data
func testEPG(cfg config, client *http.Client) (bool, int, string) {
	if sessionToken == "" {
		return false, 0, "no session_token — skipping"
	}
	from := time.Now().UTC().Format(time.RFC3339)
	to := time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339)
	url := fmt.Sprintf("%s/v1/epg?from=%s&to=%s", cfg.BaseURL, from, to)
	resp, respBody, err := doRequest(client, "GET", url, nil, authHeader())
	if err != nil {
		return false, 0, "connection error: " + err.Error()
	}
	if resp.StatusCode == 401 {
		return false, resp.StatusCode, "401 Unauthorized"
	}
	if resp.StatusCode != 200 {
		return false, resp.StatusCode, fmt.Sprintf("expected 200, got %d", resp.StatusCode)
	}
	var m map[string]any
	if err := json.Unmarshal(respBody, &m); err != nil {
		return false, resp.StatusCode, "invalid JSON: " + err.Error()
	}
	required := []string{"epg", "generated_at"}
	for _, field := range required {
		if _, ok := m[field]; !ok {
			return false, resp.StatusCode, "missing field: " + field
		}
	}
	return true, resp.StatusCode, "EPG response shape OK"
}

// T7: EPG missing required params — should return 400
func testEPGMissingParams(cfg config, client *http.Client) (bool, int, string) {
	if sessionToken == "" {
		return false, 0, "no session_token — skipping"
	}
	resp, respBody, err := doRequest(client, "GET", cfg.BaseURL+"/v1/epg", nil, authHeader())
	if err != nil {
		return false, 0, "connection error: " + err.Error()
	}
	if resp.StatusCode != 400 {
		return false, resp.StatusCode, fmt.Sprintf("expected 400 for missing from/to, got %d. Body: %s", resp.StatusCode, string(respBody))
	}
	return true, resp.StatusCode, "correctly rejects EPG request without from/to"
}

// T8: POST /owl/v1/stream/{channel_id}
func testStreamURL(cfg config, client *http.Client) (bool, int, string) {
	if sessionToken == "" {
		return false, 0, "no session_token — skipping"
	}
	body := map[string]any{"quality": "auto"}
	resp, respBody, err := doRequest(client, "POST", cfg.BaseURL+"/v1/stream/espn", body, authHeader())
	if err != nil {
		return false, 0, "connection error: " + err.Error()
	}
	if resp.StatusCode == 401 {
		return false, resp.StatusCode, "401 Unauthorized"
	}
	if resp.StatusCode == 503 {
		var m map[string]any
		if err := json.Unmarshal(respBody, &m); err != nil {
			return false, resp.StatusCode, "503 with invalid JSON"
		}
		if _, ok := m["error"]; !ok {
			return false, resp.StatusCode, "503 without error field"
		}
		return true, resp.StatusCode, "503 channel unavailable — error shape OK"
	}
	if resp.StatusCode != 200 {
		return false, resp.StatusCode, fmt.Sprintf("expected 200 or 503, got %d", resp.StatusCode)
	}
	var m map[string]any
	if err := json.Unmarshal(respBody, &m); err != nil {
		return false, resp.StatusCode, "invalid JSON: " + err.Error()
	}
	required := []string{"stream_url", "expires_at", "quality", "format"}
	for _, field := range required {
		if _, ok := m[field]; !ok {
			return false, resp.StatusCode, "missing field: " + field
		}
	}
	streamURL, _ := m["stream_url"].(string)
	if !strings.HasSuffix(streamURL, ".m3u8") {
		return false, resp.StatusCode, "stream_url does not end in .m3u8"
	}
	expires, _ := m["expires_at"].(string)
	t, err := time.Parse(time.RFC3339, expires)
	if err != nil {
		return false, resp.StatusCode, "expires_at not valid RFC3339: " + expires
	}
	ttl := time.Until(t)
	if ttl < 13*time.Minute || ttl > 16*time.Minute {
		return false, resp.StatusCode, fmt.Sprintf("stream URL TTL unexpected: %v (want ~15min)", ttl.Round(time.Minute))
	}
	return true, resp.StatusCode, fmt.Sprintf("HLS stream URL obtained, TTL=%v", ttl.Round(time.Minute))
}

// T9: GET /owl/v1/vod — placeholder response
func testVODPlaceholder(cfg config, client *http.Client) (bool, int, string) {
	if sessionToken == "" {
		return false, 0, "no session_token — skipping"
	}
	resp, respBody, err := doRequest(client, "GET", cfg.BaseURL+"/v1/vod", nil, authHeader())
	if err != nil {
		return false, 0, "connection error: " + err.Error()
	}
	if resp.StatusCode != 200 {
		return false, resp.StatusCode, fmt.Sprintf("expected 200, got %d", resp.StatusCode)
	}
	var m map[string]any
	if err := json.Unmarshal(respBody, &m); err != nil {
		return false, resp.StatusCode, "invalid JSON: " + err.Error()
	}
	if _, ok := m["items"]; !ok {
		return false, resp.StatusCode, "missing items field"
	}
	return true, resp.StatusCode, "VOD placeholder response OK"
}

// T10: GET /health — unauthenticated health endpoint
func testHealth(cfg config, client *http.Client) (bool, int, string) {
	resp, respBody, err := doRequest(client, "GET", cfg.BaseURL+"/health", nil, nil)
	if err != nil {
		return false, 0, "connection error: " + err.Error()
	}
	if resp.StatusCode != 200 {
		return false, resp.StatusCode, fmt.Sprintf("expected 200, got %d. Body: %s", resp.StatusCode, string(respBody))
	}
	return true, resp.StatusCode, "health OK"
}

// T11: Unauthorized request returns 401 with error shape
func testUnauthorizedRequest(cfg config, client *http.Client) (bool, int, string) {
	resp, respBody, err := doRequest(client, "GET", cfg.BaseURL+"/v1/live", nil, map[string]string{
		"Authorization": "Bearer invalid_session_token",
	})
	if err != nil {
		return false, 0, "connection error: " + err.Error()
	}
	if resp.StatusCode != 401 {
		return false, resp.StatusCode, fmt.Sprintf("expected 401, got %d", resp.StatusCode)
	}
	var m map[string]any
	if err := json.Unmarshal(respBody, &m); err != nil {
		return false, resp.StatusCode, "invalid JSON: " + err.Error()
	}
	if _, ok := m["error"]; !ok {
		return false, resp.StatusCode, "401 response missing error field"
	}
	return true, resp.StatusCode, "401 + error shape correct"
}

// T12: Error envelope standard — all error responses include request_id
func testErrorEnvelopeHasRequestID(cfg config, client *http.Client) (bool, int, string) {
	resp, respBody, err := doRequest(client, "GET", cfg.BaseURL+"/v1/live", nil, map[string]string{
		"Authorization": "Bearer bad_token",
	})
	if err != nil {
		return false, 0, "connection error: " + err.Error()
	}
	_ = resp
	var m map[string]any
	if err := json.Unmarshal(respBody, &m); err != nil {
		return false, resp.StatusCode, "invalid JSON: " + err.Error()
	}
	if _, ok := m["request_id"]; !ok {
		return false, resp.StatusCode, "error envelope missing request_id field"
	}
	return true, resp.StatusCode, "error envelope includes request_id"
}

// --- Main ---

func main() {
	cfg := loadConfig()
	client := &http.Client{Timeout: cfg.Timeout}

	fmt.Printf("Roost ↔ Owl Community Addon API Contract Tests\n")
	fmt.Printf("Base URL: %s\n", cfg.BaseURL)
	fmt.Printf("Timestamp: %s\n\n", time.Now().UTC().Format(time.RFC3339))

	// Run tests in dependency order
	run("T1: GET /owl/manifest.json (public discovery)", testManifest, cfg, client)
	run("T2: POST /owl/v1/auth (valid token)", testAuthValidToken, cfg, client)
	run("T3: POST /owl/v1/auth (invalid token → 401 + shape)", testAuthInvalidToken, cfg, client)
	run("T10: GET /health (unauthenticated)", testHealth, cfg, client)
	run("T11: Unauthorized request → 401 + error shape", testUnauthorizedRequest, cfg, client)
	run("T12: Error envelope includes request_id", testErrorEnvelopeHasRequestID, cfg, client)
	run("T4: GET /owl/v1/live (channel list)", testLiveChannels, cfg, client)
	run("T5: GET /owl/v1/live?category=sports (filter)", testLiveChannelsFilter, cfg, client)
	run("T6: GET /owl/v1/epg (with from/to)", testEPG, cfg, client)
	run("T7: GET /owl/v1/epg (missing params → 400)", testEPGMissingParams, cfg, client)
	run("T8: POST /owl/v1/stream/espn (stream URL)", testStreamURL, cfg, client)
	run("T9: GET /owl/v1/vod (placeholder)", testVODPlaceholder, cfg, client)

	// Summary
	pass, fail := 0, 0
	for _, r := range results {
		if r.Pass {
			pass++
		} else {
			fail++
		}
	}
	fmt.Printf("\n--- RESULTS ---\n")
	fmt.Printf("PASS: %d / %d\n", pass, len(results))
	fmt.Printf("FAIL: %d / %d\n", fail, len(results))
	if fail > 0 {
		fmt.Println("\nFailed tests:")
		for _, r := range results {
			if !r.Pass {
				fmt.Printf("  [FAIL] %s (HTTP %d) — %s\n", r.Name, r.Status, r.Notes)
			}
		}
		os.Exit(1)
	}
	fmt.Println("\nAll contract tests passed.")
}
