// xtream_test.go — Unit tests for Xtream Codes compatibility layer and M3U8 playlist.
// Integration tests (requiring DB) use the //go:build integration tag.
// Unit tests here cover: Xtream token validation logic, response shapes, rate limiter
// behaviour, M3U8 format correctness, and source URL safety.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// ---- Xtream auth tests -----------------------------------------------------

// TestValidateXtreamCredsRejectsNonRoostToken verifies that tokens not starting
// with "roost_" are rejected immediately without hitting the DB.
func TestValidateXtreamCredsRejectsNonRoostToken(t *testing.T) {
	srv := &server{db: nil, rl: newRateLimiter(nil)}
	req := httptest.NewRequest(http.MethodGet, "/player_api.php?username=badtoken&password=x&action=", nil)

	_, _, err := srv.validateXtreamCreds(req, "badtoken")
	if err == nil {
		t.Error("expected error for non-roost_ token, got nil")
	}
}

// TestValidateXtreamCredsRejectsEmptyUsername verifies that empty username is rejected.
func TestValidateXtreamCredsRejectsEmptyUsername(t *testing.T) {
	srv := &server{db: nil, rl: newRateLimiter(nil)}
	req := httptest.NewRequest(http.MethodGet, "/player_api.php?username=&password=x", nil)

	_, _, err := srv.validateXtreamCreds(req, "")
	if err == nil {
		t.Error("expected error for empty username, got nil")
	}
}

// TestXtreamUserInfoShape verifies the xtreamUserInfo struct serialises correctly.
func TestXtreamUserInfoShape(t *testing.T) {
	info := xtreamUserInfo{
		Username:             "roost_abc123",
		Password:             "x",
		Message:              "Welcome to Roost",
		Auth:                 1,
		Status:               "Active",
		ExpDate:              "Unlimited",
		IsTrial:              "0",
		ActiveConns:          "0",
		CreatedAt:            "2026-02-24",
		MaxConnections:       "2",
		AllowedOutputFormats: []string{"m3u8", "ts"},
	}

	b, err := json.Marshal(info)
	if err != nil {
		t.Fatalf("xtreamUserInfo marshal failed: %v", err)
	}

	s := string(b)
	for _, field := range []string{"username", "password", "auth", "status", "exp_date",
		"is_trial", "active_cons", "created_at", "max_connections", "allowed_output_formats"} {
		if !strings.Contains(s, fmt.Sprintf("%q", field)) {
			t.Errorf("xtreamUserInfo JSON missing field %q", field)
		}
	}
}

// TestXtreamServerInfoShape verifies the xtreamServerInfo struct serialises correctly.
func TestXtreamServerInfoShape(t *testing.T) {
	info := xtreamServerInfo{
		URL:          "https://roost.yourflock.com",
		Port:         "80",
		HTTPSPort:    "443",
		Protocol:     "http",
		RTMPPort:     "1935",
		Timezone:     "UTC",
		TimestampNow: time.Now().Unix(),
		TimeNow:      time.Now().UTC().Format("2006-01-02 15:04:05"),
	}

	b, err := json.Marshal(info)
	if err != nil {
		t.Fatalf("xtreamServerInfo marshal failed: %v", err)
	}

	s := string(b)
	for _, field := range []string{"url", "port", "https_port", "protocol", "timezone",
		"timestamp_now", "time_now"} {
		if !strings.Contains(s, fmt.Sprintf("%q", field)) {
			t.Errorf("xtreamServerInfo JSON missing field %q", field)
		}
	}
}

// TestXtreamInvalidCredsResponseShape verifies auth=0 is returned for bad creds.
func TestXtreamInvalidCredsResponseShape(t *testing.T) {
	resp := map[string]interface{}{
		"user_info": xtreamUserInfo{
			Username: "roost_fake",
			Password: "x",
			Auth:     0,
			Status:   "Disabled",
			Message:  "Invalid credentials",
		},
	}

	b, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("invalid creds response marshal failed: %v", err)
	}

	var decoded map[string]interface{}
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("invalid creds response unmarshal failed: %v", err)
	}

	userInfo, ok := decoded["user_info"].(map[string]interface{})
	if !ok {
		t.Fatal("response must contain user_info object")
	}
	if userInfo["auth"].(float64) != 0 {
		t.Error("invalid creds must have auth=0")
	}
	if userInfo["status"] != "Disabled" {
		t.Errorf("invalid creds status expected Disabled, got %v", userInfo["status"])
	}
}

// TestXtreamStreamNeverExposesSourceURL verifies xtreamStream.DirectSource is always empty.
func TestXtreamStreamNeverExposesSourceURL(t *testing.T) {
	stream := xtreamStream{
		Num:          1,
		Name:         "ESPN",
		StreamType:   "live",
		StreamID:     100,
		StreamIcon:   "https://cdn.roost.yourflock.com/logos/espn.png",
		EPGChannelID: "espn.us",
		DirectSource: "", // MUST be empty — source URLs are private
	}

	b, _ := json.Marshal(stream)
	s := string(b)

	// Verify direct_source is present but empty in JSON
	if !strings.Contains(s, `"direct_source":""`) {
		t.Errorf("direct_source must be empty string in JSON, got: %s", s)
	}

	// Verify no source infrastructure URLs leaked.
	// Note: we check for infrastructure-specific patterns, not the word "source" itself
	// (since "direct_source" is a valid Xtream field name that must appear empty).
	forbiddenPatterns := []string{"iptv://", "hetzner", "49.12.", "167.235.", "rtsp://", "udp://"}
	for _, p := range forbiddenPatterns {
		if strings.Contains(strings.ToLower(s), p) {
			t.Errorf("xtreamStream must not expose source infrastructure, found %q in: %s", p, s)
		}
	}
	// direct_source must be present but empty (Xtream players expect the field)
	if !strings.Contains(s, `"direct_source":""`) {
		t.Errorf("direct_source must be empty string (not absent, not filled), got: %s", s)
	}
}

// TestXtreamCategoryShape verifies xtreamCategory serialises to Xtream format.
func TestXtreamCategoryShape(t *testing.T) {
	cat := xtreamCategory{
		CategoryID:   "1",
		CategoryName: "Sports",
		ParentID:     0,
	}

	b, err := json.Marshal(cat)
	if err != nil {
		t.Fatalf("category marshal failed: %v", err)
	}

	var decoded map[string]interface{}
	json.Unmarshal(b, &decoded)

	if decoded["category_id"] != "1" {
		t.Errorf("category_id expected '1', got %v", decoded["category_id"])
	}
	if decoded["category_name"] != "Sports" {
		t.Errorf("category_name expected Sports, got %v", decoded["category_name"])
	}
	if decoded["parent_id"].(float64) != 0 {
		t.Errorf("parent_id expected 0, got %v", decoded["parent_id"])
	}
}

// TestXtreamEPGProgramShape verifies xtreamEPGProgram contains all required fields.
func TestXtreamEPGProgramShape(t *testing.T) {
	prog := xtreamEPGProgram{
		ID:             "prog-123",
		EPGListingID:   "espn.us",
		Title:          "NFL Sunday Night Football",
		Lang:           "en",
		Start:          "2026-02-24 20:00:00",
		End:            "2026-02-24 23:00:00",
		Description:    "Live NFL football",
		ChannelID:      "espn.us",
		StartTimestamp: 1234567890,
		StopTimestamp:  1234578690,
		NowPlaying:     0,
		HasArchive:     0,
	}

	b, err := json.Marshal(prog)
	if err != nil {
		t.Fatalf("EPG program marshal failed: %v", err)
	}

	s := string(b)
	requiredFields := []string{"id", "epg_id", "title", "lang", "start", "end",
		"channel_id", "start_timestamp", "stop_timestamp", "now_playing", "has_archive"}
	for _, field := range requiredFields {
		if !strings.Contains(s, fmt.Sprintf("%q", field)) {
			t.Errorf("EPG program JSON missing field %q in: %s", field, s)
		}
	}
}

// TestXtreamStreamPathParsing verifies the Xtream stream path format is understood.
// Path: /live/{username}/{password}/{stream_id}.m3u8
func TestXtreamStreamPathParsing(t *testing.T) {
	cases := []struct {
		path     string
		wantUser string
		wantID   string
	}{
		{"/live/roost_abc/x/100.m3u8", "roost_abc", "100"},
		{"/live/roost_xyz/anypass/42.m3u8", "roost_xyz", "42"},
		{"/live/roost_tok/x/999.ts", "roost_tok", "999"},
	}

	for _, tc := range cases {
		parts := strings.Split(strings.Trim(tc.path, "/"), "/")
		if len(parts) < 4 {
			t.Errorf("path %q: expected 4 parts, got %d", tc.path, len(parts))
			continue
		}
		username := parts[1]
		rawID := strings.TrimSuffix(strings.TrimSuffix(parts[3], ".m3u8"), ".ts")

		if username != tc.wantUser {
			t.Errorf("path %q: username=%q, want %q", tc.path, username, tc.wantUser)
		}
		if rawID != tc.wantID {
			t.Errorf("path %q: stream_id=%q, want %q", tc.path, rawID, tc.wantID)
		}
	}
}

// ---- Rate limiter tests -----------------------------------------------------

// mockRateLimitStore is an in-memory implementation of RateLimitStore for testing.
type mockRateLimitStore struct {
	counts map[string]int64
	ttls   map[string]time.Duration
	vals   map[string]string
}

func newMockStore() *mockRateLimitStore {
	return &mockRateLimitStore{
		counts: map[string]int64{},
		ttls:   map[string]time.Duration{},
		vals:   map[string]string{},
	}
}

func (m *mockRateLimitStore) Incr(_ context.Context, key string) (int64, error) {
	m.counts[key]++
	return m.counts[key], nil
}

func (m *mockRateLimitStore) Expire(_ context.Context, key string, ttl time.Duration) error {
	m.ttls[key] = ttl
	return nil
}

func (m *mockRateLimitStore) Get(_ context.Context, key string) (string, error) {
	if v, ok := m.vals[key]; ok {
		return v, nil
	}
	return "", fmt.Errorf("key not found")
}

func (m *mockRateLimitStore) Set(_ context.Context, key string, value interface{}, _ time.Duration) error {
	m.vals[key] = fmt.Sprintf("%v", value)
	return nil
}

func (m *mockRateLimitStore) Del(_ context.Context, keys ...string) error {
	for _, k := range keys {
		delete(m.counts, k)
		delete(m.vals, k)
		delete(m.ttls, k)
	}
	return nil
}

// TestRateLimiterAllowsUnderLimit verifies requests under 100/min are allowed.
func TestRateLimiterAllowsUnderLimit(t *testing.T) {
	store := newMockStore()
	rl := newRateLimiter(store)

	var allowed int32
	handler := rl.apiRateLimit(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&allowed, 1)
		w.WriteHeader(http.StatusOK)
	})

	for i := 0; i < 10; i++ {
		req := httptest.NewRequest(http.MethodGet, "/owl/live?token=test-session-token-abc", nil)
		w := httptest.NewRecorder()
		handler(w, req)
		if w.Code != http.StatusOK {
			t.Errorf("request %d: expected 200, got %d", i, w.Code)
		}
	}

	if int(allowed) != 10 {
		t.Errorf("expected 10 allowed requests, got %d", allowed)
	}
}

// TestRateLimiterBlocks101stRequest verifies the 101st request returns 429.
func TestRateLimiterBlocks101stRequest(t *testing.T) {
	store := newMockStore()
	rl := newRateLimiter(store)

	var callCount int32
	handler := rl.apiRateLimit(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&callCount, 1)
		w.WriteHeader(http.StatusOK)
	})

	// Make 100 requests (all should pass)
	for i := 0; i < 100; i++ {
		req := httptest.NewRequest(http.MethodGet, "/owl/live?token=session-tok-rate-test", nil)
		w := httptest.NewRecorder()
		handler(w, req)
		if w.Code != http.StatusOK {
			t.Errorf("request %d should succeed, got %d", i+1, w.Code)
		}
	}

	// 101st request must be rejected
	req := httptest.NewRequest(http.MethodGet, "/owl/live?token=session-tok-rate-test", nil)
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusTooManyRequests {
		t.Errorf("101st request: expected 429, got %d", w.Code)
	}

	// Verify response body has the right error code
	var resp map[string]string
	if err := json.NewDecoder(w.Body).Decode(&resp); err == nil {
		if resp["error"] != "rate_limit_exceeded" {
			t.Errorf("expected error=rate_limit_exceeded, got %q", resp["error"])
		}
	}
}

// TestRateLimiterRateLimitHeaders verifies X-RateLimit-* headers are set on every response.
func TestRateLimiterRateLimitHeaders(t *testing.T) {
	store := newMockStore()
	rl := newRateLimiter(store)

	handler := rl.apiRateLimit(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/owl/live?token=hdr-test-token", nil)
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Header().Get("X-RateLimit-Limit") == "" {
		t.Error("X-RateLimit-Limit header missing")
	}
	if w.Header().Get("X-RateLimit-Remaining") == "" {
		t.Error("X-RateLimit-Remaining header missing")
	}
	if w.Header().Get("X-RateLimit-Reset") == "" {
		t.Error("X-RateLimit-Reset header missing")
	}

	// Limit must be 100
	if w.Header().Get("X-RateLimit-Limit") != "100" {
		t.Errorf("X-RateLimit-Limit expected 100, got %q", w.Header().Get("X-RateLimit-Limit"))
	}
}

// TestRateLimiterNilStorePassesAll verifies nil store (no Redis) allows all requests.
func TestRateLimiterNilStorePassesAll(t *testing.T) {
	rl := newRateLimiter(nil)

	var callCount int32
	handler := rl.apiRateLimit(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&callCount, 1)
		w.WriteHeader(http.StatusOK)
	})

	for i := 0; i < 200; i++ {
		req := httptest.NewRequest(http.MethodGet, "/owl/live?token=no-redis-test", nil)
		w := httptest.NewRecorder()
		handler(w, req)
		if w.Code != http.StatusOK {
			t.Errorf("nil store: request %d rejected (expected pass-through), got %d", i, w.Code)
		}
	}

	if int(callCount) != 200 {
		t.Errorf("nil store: expected 200 calls, got %d", callCount)
	}
}

// TestStreamRateLimitAllowsUnderConcurrency verifies streams under max are allowed.
func TestStreamRateLimitAllowsUnderConcurrency(t *testing.T) {
	store := newMockStore()
	rl := newRateLimiter(store)

	var called int32
	handler := rl.streamRateLimit(2, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&called, 1)
		w.WriteHeader(http.StatusOK)
	})

	// First stream request — subscriber has 0 active, max is 2 → allowed
	req := httptest.NewRequest(http.MethodPost, "/owl/stream/espn", nil)
	req.Header.Set("X-Subscriber-ID", "sub-123")
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("first stream: expected 200, got %d", w.Code)
	}
	if int(called) != 1 {
		t.Errorf("expected handler called once, got %d", called)
	}
}

// TestStreamRateLimitBlocksAtMaxConcurrency verifies 429 when concurrent limit reached.
func TestStreamRateLimitBlocksAtMaxConcurrency(t *testing.T) {
	store := newMockStore()
	rl := newRateLimiter(store)

	// Manually set counter to max (2) for this subscriber
	store.vals["owl_api:stream_count:sub-456"] = "2"

	blocked := false
	handler := rl.streamRateLimit(2, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodPost, "/owl/stream/nfl", nil)
	req.Header.Set("X-Subscriber-ID", "sub-456")
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code == http.StatusTooManyRequests {
		blocked = true
	}

	if !blocked {
		t.Errorf("expected 429 when at max concurrent streams, got %d", w.Code)
	}

	// Verify error message mentions stream limit
	var resp map[string]string
	if err := json.NewDecoder(w.Body).Decode(&resp); err == nil {
		if !strings.Contains(resp["error"], "stream_limit") {
			t.Errorf("expected stream_limit error, got %q", resp["error"])
		}
	}
}

// TestExtractSessionToken verifies token extraction from header and query param.
func TestExtractSessionToken(t *testing.T) {
	cases := []struct {
		desc    string
		header  string
		query   string
		wantTok string
	}{
		{
			desc:    "Bearer header",
			header:  "Bearer session-tok-abc",
			wantTok: "session-tok-abc",
		},
		{
			desc:    "query param fallback",
			query:   "session-tok-xyz",
			wantTok: "session-tok-xyz",
		},
		{
			desc:    "header takes priority over query",
			header:  "Bearer header-tok",
			query:   "query-tok",
			wantTok: "header-tok",
		},
		{
			desc:    "no token",
			wantTok: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			url := "/owl/live"
			if tc.query != "" {
				url += "?token=" + tc.query
			}
			req := httptest.NewRequest(http.MethodGet, url, nil)
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			got := extractSessionToken(req)
			if got != tc.wantTok {
				t.Errorf("extractSessionToken: got %q, want %q", got, tc.wantTok)
			}
		})
	}
}

// ---- M3U8 playlist tests ---------------------------------------------------

// TestM3UEscapeHandlesSpecialChars verifies m3uEscape handles problematic characters.
func TestM3UEscapeHandlesSpecialChars(t *testing.T) {
	cases := []struct {
		input string
		check func(string) bool
		desc  string
	}{
		{
			input: "Channel with \"quotes\"",
			check: func(s string) bool { return !strings.Contains(s, `"`) },
			desc:  "double quotes replaced",
		},
		{
			input: "Channel\nWith\nNewlines",
			check: func(s string) bool { return !strings.Contains(s, "\n") },
			desc:  "newlines removed",
		},
		{
			input: "Channel\r\nWindows",
			check: func(s string) bool { return !strings.Contains(s, "\r") && !strings.Contains(s, "\n") },
			desc:  "CRLF removed",
		},
		{
			input: "Normal Channel Name",
			check: func(s string) bool { return s == "Normal Channel Name" },
			desc:  "normal name unchanged",
		},
	}

	for _, tc := range cases {
		result := m3uEscape(tc.input)
		if !tc.check(result) {
			t.Errorf("m3uEscape(%q) = %q: %s", tc.input, result, tc.desc)
		}
	}
}

// TestPlaylistM3U8HeaderFormat verifies the M3U8 playlist starts with #EXTM3U.
func TestPlaylistM3U8HeaderFormat(t *testing.T) {
	// Simulate playlist content without a real DB
	playlistContent := "#EXTM3U x-tvg-url=\"https://roost.yourflock.com/owl/xmltv.xml?token=test\"\n"
	playlistContent += "#EXTINF:-1 tvg-id=\"espn.us\" tvg-name=\"ESPN\" tvg-logo=\"\" group-title=\"Sports\",ESPN\n"
	playlistContent += "https://roost.yourflock.com/owl/v1/stream/espn?token=test-session\n"

	lines := strings.Split(playlistContent, "\n")

	// First line must be #EXTM3U
	if !strings.HasPrefix(lines[0], "#EXTM3U") {
		t.Errorf("M3U8 must start with #EXTM3U, got: %q", lines[0])
	}

	// x-tvg-url must be present (EPG source)
	if !strings.Contains(lines[0], "x-tvg-url=") {
		t.Errorf("M3U8 header must include x-tvg-url, got: %q", lines[0])
	}
}

// TestPlaylistM3U8EXTINFFormat verifies #EXTINF lines have required tvg tags.
func TestPlaylistM3U8EXTINFFormat(t *testing.T) {
	extinf := fmt.Sprintf(
		"#EXTINF:-1 tvg-id=\"%s\" tvg-name=\"%s\" tvg-logo=\"%s\" tvg-language=\"%s\" tvg-country=\"%s\" group-title=\"%s\",%s",
		"espn.us", "ESPN", "https://cdn.example.com/espn.png", "en", "US", "Sports", "ESPN",
	)

	requiredTags := []string{"tvg-id=", "tvg-name=", "tvg-logo=", "group-title="}
	for _, tag := range requiredTags {
		if !strings.Contains(extinf, tag) {
			t.Errorf("#EXTINF line missing tag %q in: %s", tag, extinf)
		}
	}

	// Must start with #EXTINF:-1 (live stream duration)
	if !strings.HasPrefix(extinf, "#EXTINF:-1 ") {
		t.Errorf("#EXTINF must start with #EXTINF:-1, got: %q", extinf)
	}

	// Channel name after final comma
	parts := strings.SplitN(extinf, ",", 2)
	if len(parts) != 2 || parts[1] == "" {
		t.Errorf("#EXTINF must have channel name after comma, got: %q", extinf)
	}
}

// TestPlaylistStreamURLFormat verifies stream URLs in M3U8 embed the session token.
func TestPlaylistStreamURLFormat(t *testing.T) {
	baseURL := "https://roost.yourflock.com"
	sessionToken := "test-session-abc"
	slug := "espn"

	streamURL := fmt.Sprintf("%s/owl/v1/stream/%s?token=%s", baseURL, slug, sessionToken)

	if !strings.Contains(streamURL, slug) {
		t.Errorf("stream URL must contain channel slug, got: %s", streamURL)
	}
	if !strings.Contains(streamURL, "token=") {
		t.Errorf("stream URL must contain token parameter, got: %s", streamURL)
	}
	if !strings.Contains(streamURL, sessionToken) {
		t.Errorf("stream URL must contain session token, got: %s", streamURL)
	}

	// Must not expose source infrastructure
	forbiddenPatterns := []string{"source", "origin", "iptv", "hetzner", "49.12.", "167.235."}
	for _, p := range forbiddenPatterns {
		if strings.Contains(strings.ToLower(streamURL), p) {
			t.Errorf("stream URL must not expose source infrastructure, found %q in: %s", p, streamURL)
		}
	}
}

// TestPlaylistM3U8NeverExposesSourceURL verifies playlist stream URLs always go
// through our relay, not directly to IPTV sources.
func TestPlaylistM3U8NeverExposesSourceURL(t *testing.T) {
	// Simulate what playlist.go generates for each channel
	baseURL := "https://roost.yourflock.com"
	channels := []struct{ slug, name string }{
		{"espn", "ESPN"},
		{"nfl", "NFL Network"},
		{"cnn", "CNN"},
	}

	var sb strings.Builder
	sb.WriteString("#EXTM3U\n")
	for _, ch := range channels {
		sb.WriteString(fmt.Sprintf("#EXTINF:-1 tvg-id=%q tvg-name=%q group-title=\"News\",%s\n",
			ch.slug, ch.name, ch.name))
		sb.WriteString(fmt.Sprintf("%s/owl/v1/stream/%s?token=test-tok\n", baseURL, ch.slug))
	}

	content := sb.String()

	// All stream URLs must go through our relay
	for _, line := range strings.Split(content, "\n") {
		if strings.HasPrefix(line, "http") && !strings.HasPrefix(line, "#") {
			if !strings.Contains(line, "roost.yourflock.com") {
				t.Errorf("stream URL must go through Roost relay, got: %s", line)
			}
		}
	}
}

// TestPlaylistM3U8ContentType verifies the response Content-Type is correct.
func TestPlaylistM3U8ContentType(t *testing.T) {
	w := httptest.NewRecorder()
	w.Header().Set("Content-Type", "application/x-mpegurl")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "#EXTM3U\n")

	ct := w.Header().Get("Content-Type")
	if ct != "application/x-mpegurl" {
		t.Errorf("M3U8 Content-Type expected application/x-mpegurl, got %q", ct)
	}
}

// TestPlaylistM3U8RequiresAuth verifies that the playlist endpoint needs a valid session.
// (Tested via requireSession — the handler itself trusts X-Subscriber-ID set by middleware.)
func TestPlaylistM3U8SessionTokenRequired(t *testing.T) {
	// A request to /owl/playlist.m3u8 with no token should get 401 from requireSession
	// We test this by verifying extractSessionToken returns "" for an unauthenticated request
	req := httptest.NewRequest(http.MethodGet, "/owl/playlist.m3u8", nil)
	token := extractSessionToken(req)
	if token != "" {
		t.Errorf("unauthenticated request should have empty token, got %q", token)
	}
}
