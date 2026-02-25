// Package handlers — unit tests for Roost admin HTTP handlers.
//
// These tests cover:
//   - Pure helper functions (computeAntBoxStatus, isValidUUID, extractPathID,
//     maskIPTVConfig, isValidHTTPSURL, validateAddonManifestURL)
//   - HTTP handlers that do not require a real DB (Status, ListActiveStreams,
//     KillStream, ScanStatus)
//
// Handlers that require a live Postgres connection are covered by integration
// tests (//go:build integration tag) in admin_integration_test.go.
//
// Run unit tests: go test ./services/owl_api/handlers/... -v
package handlers

import (
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	_ "github.com/lib/pq"
	"github.com/yourflock/roost/services/owl_api/audit"
	"github.com/yourflock/roost/services/owl_api/middleware"
)

// ── JWT helpers for tests ─────────────────────────────────────────────────────

var testAdminSecret = []byte("test-admin-handler-secret-32bytes!!")

// buildTestJWT creates a signed HS256 JWT for handler testing.
// Mirrors the structure expected by middleware.RequireAdmin.
func buildTestJWT(userID, role, roostID string, secret []byte) string {
	b64url := func(data []byte) string {
		return base64.RawURLEncoding.EncodeToString(data)
	}
	header := b64url([]byte(`{"alg":"HS256","typ":"JWT"}`))
	payload := b64url([]byte(fmt.Sprintf(
		`{"flock_user_id":%q,"role":%q,"roost_id":%q,"exp":%d}`,
		userID, role, roostID, time.Now().Add(time.Hour).Unix(),
	)))
	sigInput := header + "." + payload
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(sigInput))
	return sigInput + "." + b64url(mac.Sum(nil))
}

// injectAdminClaims returns a copy of r with AdminClaims in context.
// It does this by running the real RequireAdmin middleware with a known-good token,
// capturing the enriched context via an intercepting handler.
func injectAdminClaims(r *http.Request, role, roostID, userID string) *http.Request {
	token := buildTestJWT(userID, role, roostID, testAdminSecret)

	var enrichedReq *http.Request
	interceptor := http.HandlerFunc(func(_ http.ResponseWriter, req *http.Request) {
		enrichedReq = req
	})

	probe := httptest.NewRequest(r.Method, r.URL.String(), r.Body)
	probe.Header.Set("Authorization", "Bearer "+token)
	// Copy other relevant headers
	for k, v := range r.Header {
		probe.Header[k] = v
	}
	probe.Header.Set("Authorization", "Bearer "+token) // ensure it's set after copy

	rr := httptest.NewRecorder()
	middleware.RequireAdmin(testAdminSecret, interceptor).ServeHTTP(rr, probe)

	if enrichedReq == nil {
		// Middleware rejected the token — should not happen with our test tokens
		panic("injectAdminClaims: RequireAdmin rejected test token (role=" + role + ")")
	}
	return enrichedReq
}

// noopAuditLogger returns an audit.Logger backed by a nil DB.
// All Log() calls are fire-and-forget goroutines that will fail silently.
// Acceptable for unit tests that only check HTTP status + response shape.
func noopAuditLogger() *audit.Logger {
	return audit.New(nil)
}

// openNullDB opens a *sql.DB handle without connecting to a real database.
// Safe for handlers that don't execute DB queries (e.g. Status on macOS where
// /proc/stat is unavailable, ListActiveStreams which reads from Redis).
func openNullDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("postgres",
		"postgres://test:test@localhost:5432/test?sslmode=disable&connect_timeout=1")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// ptr returns a pointer to v (Go generics, 1.18+).
func ptr[T any](v T) *T { return &v }

// ── computeAntBoxStatus ───────────────────────────────────────────────────────

func TestComputeAntBoxStatus(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name     string
		lastSeen *time.Time
		want     string
	}{
		{"nil → offline", nil, "offline"},
		{"5s ago → online", ptr(now.Add(-5 * time.Second)), "online"},
		{"90s ago → online (under 2min threshold)", ptr(now.Add(-90 * time.Second)), "online"},
		{"3min ago → stale (over 2min, under 10min)", ptr(now.Add(-3 * time.Minute)), "stale"},
		{"9m59s ago → stale", ptr(now.Add(-9*time.Minute - 59*time.Second)), "stale"},
		{"11min ago → offline (over 10min)", ptr(now.Add(-11 * time.Minute)), "offline"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := computeAntBoxStatus(tc.lastSeen)
			if got != tc.want {
				t.Errorf("computeAntBoxStatus(%v) = %q, want %q", tc.lastSeen, got, tc.want)
			}
		})
	}
}

// ── isValidUUID ───────────────────────────────────────────────────────────────

func TestIsValidUUID(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"550e8400-e29b-41d4-a716-446655440000", true},
		{"00000000-0000-0000-0000-000000000000", true},
		{"AAAAAAAA-BBBB-CCCC-DDDD-EEEEEEEEEEEE", true},   // uppercase hex is valid
		{"", false},
		{"not-a-uuid", false},
		{"550e8400-e29b-41d4-a716-44665544000", false},   // 35 chars
		{"550e8400-e29b-41d4-a716-4466554400000", false}, // 37 chars
		{"550e8400xe29b-41d4-a716-446655440000", false},  // wrong separator at pos 8
		{"550e8400-e29b-41d4-a716-44665544000g", false},  // non-hex char 'g'
		{"../etc/passwd-0000-0000-0000-000000000", false},
	}
	for _, tc := range tests {
		t.Run(fmt.Sprintf("input=%q", tc.input), func(t *testing.T) {
			got := isValidUUID(tc.input)
			if got != tc.want {
				t.Errorf("isValidUUID(%q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}

// ── extractPathID ─────────────────────────────────────────────────────────────

func TestExtractPathID(t *testing.T) {
	tests := []struct {
		path   string
		prefix string
		suffix string
		want   string
	}{
		{"/admin/users/abc-123", "/admin/users/", "", "abc-123"},
		{"/admin/users/abc-123/role", "/admin/users/", "/role", "abc-123"},
		{"/admin/storage/xyz-456", "/admin/storage/", "", "xyz-456"},
		{"/admin/antboxes/box-789/signal", "/admin/antboxes/", "/signal", "box-789"},
		{"/admin/users/", "/admin/users/", "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.path, func(t *testing.T) {
			got := extractPathID(tc.path, tc.prefix, tc.suffix)
			if got != tc.want {
				t.Errorf("extractPathID(%q, %q, %q) = %q, want %q",
					tc.path, tc.prefix, tc.suffix, got, tc.want)
			}
		})
	}
}

// ── maskIPTVConfig ────────────────────────────────────────────────────────────

func TestMaskIPTVConfig(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		checks func(t *testing.T, m map[string]interface{})
	}{
		{
			"password field is replaced with ***",
			`{"host":"example.com","username":"user","password":"s3cr3t"}`,
			func(t *testing.T, m map[string]interface{}) {
				if m["password"] != "***" {
					t.Errorf("password = %v, want ***", m["password"])
				}
				if m["host"] != "example.com" {
					t.Error("non-sensitive field 'host' should be preserved")
				}
			},
		},
		{
			"mac address is anonymised",
			`{"portal":"http://example.com","mac":"AA:BB:CC:DD:EE:FF"}`,
			func(t *testing.T, m map[string]interface{}) {
				if m["mac"] != "XX:XX:XX:XX:XX:XX" {
					t.Errorf("mac = %v, want XX:XX:XX:XX:XX:XX", m["mac"])
				}
			},
		},
		{
			"non-sensitive m3u config is unchanged",
			`{"url":"https://example.com/playlist.m3u8"}`,
			func(t *testing.T, m map[string]interface{}) {
				if m["url"] != "https://example.com/playlist.m3u8" {
					t.Errorf("url should be unchanged, got %v", m["url"])
				}
			},
		},
		{
			"invalid JSON returns empty object",
			`not-json`,
			func(t *testing.T, m map[string]interface{}) {
				if len(m) != 0 {
					t.Errorf("expected empty map for invalid JSON, got %v", m)
				}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			raw := maskIPTVConfig([]byte(tc.input))
			var m map[string]interface{}
			if err := json.Unmarshal(raw, &m); err != nil {
				m = map[string]interface{}{}
			}
			tc.checks(t, m)
		})
	}
}

// ── isValidHTTPSURL ───────────────────────────────────────────────────────────

func TestIsValidHTTPSURL(t *testing.T) {
	tests := []struct {
		url  string
		want bool
	}{
		{"https://example.com/playlist.m3u8", true},
		{"http://example.com/playlist.m3u8", true},
		{"ftp://example.com/file.m3u8", false},
		{"", false},
		{"not-a-url", false},
		{"://missing-scheme.com", false},
	}
	for _, tc := range tests {
		t.Run(tc.url, func(t *testing.T) {
			got := isValidHTTPSURL(tc.url)
			if got != tc.want {
				t.Errorf("isValidHTTPSURL(%q) = %v, want %v", tc.url, got, tc.want)
			}
		})
	}
}

// ── validateAddonManifestURL ──────────────────────────────────────────────────

func TestValidateAddonManifestURL(t *testing.T) {
	tests := []struct {
		url     string
		wantErr bool
	}{
		{"https://example.com/manifest.json", false},
		{"http://example.com/manifest.json", true},       // must be HTTPS
		{"https://localhost/manifest.json", true},         // loopback blocked
		{"https://127.0.0.1/manifest.json", true},         // loopback blocked
		{"https://192.168.1.1/manifest.json", true},       // LAN blocked
		{"https://10.0.0.1/manifest.json", true},          // LAN blocked
		{"https://172.16.0.1/manifest.json", true},        // LAN blocked
		{"not-a-url", true},
		{"", true},
	}
	for _, tc := range tests {
		t.Run(fmt.Sprintf("url=%q", tc.url), func(t *testing.T) {
			err := validateAddonManifestURL(tc.url)
			if (err != nil) != tc.wantErr {
				t.Errorf("validateAddonManifestURL(%q): got err=%v, wantErr=%v", tc.url, err, tc.wantErr)
			}
		})
	}
}

// ── AdminHandlers.Status ──────────────────────────────────────────────────────

// TestStatusReturnsRequiredFields verifies the Status handler emits all JSON
// fields expected by the Owl admin UI. On macOS (no /proc/stat), numeric
// metrics fall back to zero — but the fields must still be present.
func TestStatusReturnsRequiredFields(t *testing.T) {
	h := NewAdminHandlers(openNullDB(t), "/tmp", "test-v0.1")

	req := injectAdminClaims(
		httptest.NewRequest(http.MethodGet, "/admin/status", nil),
		"owner", "roost_001", "user_001",
	)
	rr := httptest.NewRecorder()
	h.Status(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("Status() = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("response is not valid JSON: %v", err)
	}

	required := []string{
		"version", "uptime_seconds",
		"cpu_percent", "ram_used_bytes", "ram_total_bytes",
		"disk_used_bytes", "disk_total_bytes",
		"active_streams", "active_recordings",
	}
	for _, field := range required {
		if _, ok := resp[field]; !ok {
			t.Errorf("Status response missing required field %q", field)
		}
	}
}

// TestStatusVersionReflectsConstructorArg verifies the version string propagates.
func TestStatusVersionReflectsConstructorArg(t *testing.T) {
	h := NewAdminHandlers(openNullDB(t), "/tmp", "v9.9.9-test")

	req := injectAdminClaims(
		httptest.NewRequest(http.MethodGet, "/admin/status", nil),
		"admin", "roost_002", "user_002",
	)
	rr := httptest.NewRecorder()
	h.Status(rr, req)

	var resp map[string]interface{}
	_ = json.NewDecoder(rr.Body).Decode(&resp)
	if got := resp["version"]; got != "v9.9.9-test" {
		t.Errorf("version = %v, want v9.9.9-test", got)
	}
}

// TestStatusUptimeIsNonNegative verifies uptime_seconds is >= 0.
func TestStatusUptimeIsNonNegative(t *testing.T) {
	h := NewAdminHandlers(openNullDB(t), "/tmp", "dev")

	req := injectAdminClaims(
		httptest.NewRequest(http.MethodGet, "/admin/status", nil),
		"owner", "roost_001", "user_001",
	)
	rr := httptest.NewRecorder()
	h.Status(rr, req)

	var resp map[string]interface{}
	_ = json.NewDecoder(rr.Body).Decode(&resp)
	uptime, _ := resp["uptime_seconds"].(float64)
	if uptime < 0 {
		t.Errorf("uptime_seconds = %v, want >= 0", uptime)
	}
}

// ── AdminHandlers.ListActiveStreams ───────────────────────────────────────────

// TestListActiveStreamsReturnsEmptyArray verifies the response is [] not null.
// JSON.parse(null) succeeds but .map() on null throws in the Owl UI.
func TestListActiveStreamsReturnsEmptyArray(t *testing.T) {
	h := NewAdminHandlers(openNullDB(t), "/tmp", "dev")

	req := injectAdminClaims(
		httptest.NewRequest(http.MethodGet, "/admin/streams", nil),
		"owner", "roost_001", "user_001",
	)
	rr := httptest.NewRecorder()
	h.ListActiveStreams(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("ListActiveStreams() = %d, want 200", rr.Code)
	}

	var arr []interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &arr); err != nil {
		t.Fatalf("response is not a valid JSON array: %v; body=%q", err, rr.Body.String())
	}
	if arr == nil {
		t.Error("expected non-nil empty array [], got JSON null")
	}
}

// ── AdminHandlers.KillStream ──────────────────────────────────────────────────

// TestKillStreamReturns200WithStatus verifies KillStream always returns 200 with
// {"stream_id":"...","status":"kill_sent"} — kill is fire-and-forget via Redis.
func TestKillStreamReturns200WithStatus(t *testing.T) {
	h := NewAdminHandlers(openNullDB(t), "/tmp", "dev")
	al := noopAuditLogger()

	req := injectAdminClaims(
		httptest.NewRequest(http.MethodDelete, "/admin/streams/test-stream-id", nil),
		"owner", "roost_001", "user_001",
	)
	rr := httptest.NewRecorder()
	h.KillStream(rr, req, al)

	if rr.Code != http.StatusOK {
		t.Fatalf("KillStream() = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}

	var resp map[string]string
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("response not valid JSON: %v", err)
	}
	if resp["status"] != "kill_sent" {
		t.Errorf("status = %q, want kill_sent", resp["status"])
	}
}

// ── AdminHandlers.ScanStatus ──────────────────────────────────────────────────

// TestScanStatusReturnsRequiredFields verifies the scan status endpoint returns
// all fields needed for the Owl storage UI progress indicator.
func TestScanStatusReturnsRequiredFields(t *testing.T) {
	h := NewAdminHandlers(openNullDB(t), "/tmp", "dev")

	req := injectAdminClaims(
		httptest.NewRequest(http.MethodGet, "/admin/storage/scan/status", nil),
		"admin", "roost_001", "user_001",
	)
	rr := httptest.NewRecorder()
	h.ScanStatus(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("ScanStatus() = %d, want 200", rr.Code)
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("response not valid JSON: %v", err)
	}

	required := []string{"running", "job_id", "files_scanned", "files_found", "errors", "percent_complete"}
	for _, field := range required {
		if _, ok := resp[field]; !ok {
			t.Errorf("ScanStatus response missing field %q", field)
		}
	}
}
