package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// ---- slugify ----------------------------------------------------------------

func TestSlugify(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"The Shawshank Redemption", "the-shawshank-redemption"},
		{"La La Land!", "la-la-land"},
		{"2001: A Space Odyssey", "2001-a-space-odyssey"},
		{"  Leading Spaces  ", "leading-spaces"},
		{"UPPER CASE", "upper-case"},
	}
	for _, tc := range cases {
		got := slugify(tc.input)
		if got != tc.want {
			t.Errorf("slugify(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

// ---- nullStr / nullInt ------------------------------------------------------

func TestNullStr(t *testing.T) {
	if nullStr("") != nil {
		t.Error("nullStr(\"\") should return nil")
	}
	if nullStr("hello") != "hello" {
		t.Error("nullStr(\"hello\") should return \"hello\"")
	}
}

func TestNullInt(t *testing.T) {
	if nullInt(0) != nil {
		t.Error("nullInt(0) should return nil")
	}
	if nullInt(42) != 42 {
		t.Error("nullInt(42) should return 42")
	}
}

// ---- signedStreamURL --------------------------------------------------------

func TestSignedStreamURL(t *testing.T) {
	t.Setenv("STREAM_HMAC_SECRET", "test-secret")
	t.Setenv("RELAY_BASE_URL", "https://stream.test.com")

	url, expiry := signedStreamURL("abc-123")
	if url == "" {
		t.Error("signedStreamURL: expected non-empty URL")
	}
	if expiry.IsZero() {
		t.Error("signedStreamURL: expected non-zero expiry")
	}
	// Verify URL contains expected components
	if len(url) < 50 {
		t.Errorf("signedStreamURL: URL seems too short: %s", url)
	}
	// Must contain sig= and exp= parameters
	if !containsParam(url, "sig") {
		t.Error("signedStreamURL: URL missing sig parameter")
	}
	if !containsParam(url, "exp") {
		t.Error("signedStreamURL: URL missing exp parameter")
	}
}

func containsParam(url, param string) bool {
	return len(url) > 0 && (contains(url, "?"+param+"=") || contains(url, "&"+param+"="))
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && containsStr(s, sub))
}

func containsStr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// ---- health endpoint --------------------------------------------------------

func TestHandleHealth(t *testing.T) {
	srv := &server{artworkDir: t.TempDir()}

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()

	srv.routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("health: expected 200, got %d", rec.Code)
	}

	var resp map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("health: invalid JSON: %v", err)
	}
	if resp["status"] != "ok" {
		t.Errorf("health: expected status=ok, got %q", resp["status"])
	}
	if resp["service"] != "roost-vod" {
		t.Errorf("health: expected service=roost-vod, got %q", resp["service"])
	}
}

// ---- unauthorized access to admin endpoints --------------------------------

func TestAdminRoutesRequireAuth(t *testing.T) {
	srv := &server{artworkDir: t.TempDir()}
	mux := srv.routes()

	adminRoutes := []struct {
		method string
		path   string
	}{
		{"POST", "/admin/vod/movies"},
		{"GET", "/admin/vod/movies"},
		{"POST", "/admin/vod/import"},
	}

	for _, tc := range adminRoutes {
		req := httptest.NewRequest(tc.method, tc.path, bytes.NewBufferString("{}"))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)

		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s: expected 401 without auth, got %d",
				tc.method, tc.path, rec.Code)
		}
	}
}

// ---- subscriber routes require session token --------------------------------

func TestSubscriberRoutesRequireSession(t *testing.T) {
	srv := &server{artworkDir: t.TempDir()}
	mux := srv.routes()

	subscriberRoutes := []struct {
		method string
		path   string
	}{
		{"GET", "/vod/catalog"},
		{"GET", "/vod/continue-watching"},
	}

	for _, tc := range subscriberRoutes {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)

		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s: expected 401 without session token, got %d",
				tc.method, tc.path, rec.Code)
		}
	}
}

// ---- watch progress calculation --------------------------------------------

func TestCompletedCalculation(t *testing.T) {
	// 90% threshold
	cases := []struct {
		position int
		duration int
		wantDone bool
	}{
		{900, 1000, true},  // exactly 90%
		{899, 1000, false}, // just under 90%
		{950, 1000, true},  // 95%
		{0, 1000, false},   // not started
		{100, 1000, false}, // 10%
	}
	for _, tc := range cases {
		got := tc.position > 0 &&
			float64(tc.position)/float64(tc.duration) >= 0.9
		if got != tc.wantDone {
			t.Errorf("pos=%d dur=%d: completed=%v, want %v",
				tc.position, tc.duration, got, tc.wantDone)
		}
	}
}

// ---- pathSegment ------------------------------------------------------------

func TestPathSegment(t *testing.T) {
	cases := []struct {
		path string
		n    int
		want string
	}{
		{"/admin/vod/movies/abc-123", 3, "abc-123"},
		{"/admin/vod/series/def-456", 3, "def-456"},
		{"/vod/progress/movie/xyz-789", 3, "xyz-789"},
		{"/vod/progress/movie/xyz-789", 2, "movie"},
	}
	for _, tc := range cases {
		got := pathSegment(tc.path, tc.n)
		if got != tc.want {
			t.Errorf("pathSegment(%q, %d) = %q, want %q", tc.path, tc.n, got, tc.want)
		}
	}
}
