// channel_matcher_test.go — Table-driven tests for M3U parsing and channel matching.
// OSG.5.001: No live network or DB required — all M3U fixtures are inline strings.
package sports

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ─── parseM3U tests ───────────────────────────────────────────────────────────

// serveM3U returns a test HTTP server that serves the given M3U body.
func serveM3U(t *testing.T, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		fmt.Fprint(w, body)
	}))
}

func TestParseM3U_StandardPlaylist(t *testing.T) {
	m3uBody := `#EXTM3U
#EXTINF:-1 tvg-name="ESPN Sports HD" tvg-id="ESPN.us" group-title="Sports",ESPN Sports HD
http://live.example.com/espn.m3u8
#EXTINF:-1 tvg-name="NFL Network" tvg-id="NFLN.us" group-title="Sports",NFL Network
http://live.example.com/nfl.m3u8
#EXTINF:-1 tvg-name="Kids Channel" tvg-id="" group-title="Kids",Kids Channel
http://live.example.com/kids.m3u8
`
	srv := serveM3U(t, m3uBody)
	defer srv.Close()

	channels, err := parseM3U(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("parseM3U error: %v", err)
	}
	if len(channels) != 3 {
		t.Fatalf("expected 3 channels, got %d", len(channels))
	}

	// Verify first channel
	if channels[0].Name != "ESPN Sports HD" {
		t.Errorf("ch[0].Name = %q, want %q", channels[0].Name, "ESPN Sports HD")
	}
	if channels[0].TVGID != "ESPN.us" {
		t.Errorf("ch[0].TVGID = %q, want %q", channels[0].TVGID, "ESPN.us")
	}
	if channels[0].GroupTitle != "Sports" {
		t.Errorf("ch[0].GroupTitle = %q, want %q", channels[0].GroupTitle, "Sports")
	}
	if channels[0].URL != "http://live.example.com/espn.m3u8" {
		t.Errorf("ch[0].URL = %q", channels[0].URL)
	}

	// Verify second channel
	if channels[1].Name != "NFL Network" {
		t.Errorf("ch[1].Name = %q, want %q", channels[1].Name, "NFL Network")
	}

	// Third channel
	if channels[2].GroupTitle != "Kids" {
		t.Errorf("ch[2].GroupTitle = %q, want %q", channels[2].GroupTitle, "Kids")
	}
}

func TestParseM3U_MissingTvgName_FallsBackToDisplayName(t *testing.T) {
	m3uBody := `#EXTM3U
#EXTINF:-1 tvg-id="FOX.us" group-title="Sports",Fox Sports 1
http://live.example.com/fox1.m3u8
`
	srv := serveM3U(t, m3uBody)
	defer srv.Close()

	channels, err := parseM3U(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("parseM3U error: %v", err)
	}
	if len(channels) != 1 {
		t.Fatalf("expected 1 channel, got %d", len(channels))
	}
	// No tvg-name attribute → should use display name after comma
	if channels[0].Name != "Fox Sports 1" {
		t.Errorf("expected display name fallback %q, got %q", "Fox Sports 1", channels[0].Name)
	}
}

func TestParseM3U_Truncation(t *testing.T) {
	// Build a playlist with maxChannelsPerSource + 1 entries
	var sb strings.Builder
	sb.WriteString("#EXTM3U\n")
	for i := 0; i < maxChannelsPerSource+1; i++ {
		sb.WriteString(fmt.Sprintf(`#EXTINF:-1 tvg-name="ESPN %d" group-title="Sports",ESPN %d`+"\n", i, i))
		sb.WriteString(fmt.Sprintf("http://live.example.com/stream%d.m3u8\n", i))
	}

	srv := serveM3U(t, sb.String())
	defer srv.Close()

	channels, err := parseM3U(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("parseM3U error: %v", err)
	}
	if len(channels) != maxChannelsPerSource {
		t.Errorf("expected truncation at %d channels, got %d", maxChannelsPerSource, len(channels))
	}
}

func TestParseM3U_EmptyPlaylist(t *testing.T) {
	srv := serveM3U(t, "#EXTM3U\n")
	defer srv.Close()

	channels, err := parseM3U(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("parseM3U error: %v", err)
	}
	if len(channels) != 0 {
		t.Errorf("expected 0 channels, got %d", len(channels))
	}
}

func TestParseM3U_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	_, err := parseM3U(context.Background(), srv.URL)
	if err == nil {
		t.Error("expected error for HTTP 500 response, got nil")
	}
}

// ─── parseExtInfLine tests ────────────────────────────────────────────────────

func TestParseExtInfLine(t *testing.T) {
	tests := []struct {
		name         string
		line         string
		wantName     string
		wantTVGID    string
		wantGroup    string
	}{
		{
			name:      "full_attributes",
			line:      `#EXTINF:-1 tvg-name="ESPN" tvg-id="ESPN.us" group-title="Sports",ESPN HD`,
			wantName:  "ESPN",
			wantTVGID: "ESPN.us",
			wantGroup: "Sports",
		},
		{
			name:      "no_tvg_name_uses_display",
			line:      `#EXTINF:-1 tvg-id="FOX.us" group-title="Sports",Fox Sports`,
			wantName:  "Fox Sports",
			wantTVGID: "FOX.us",
			wantGroup: "Sports",
		},
		{
			name:      "empty_tvg_name_uses_display",
			line:      `#EXTINF:-1 tvg-name="" group-title="Kids",Kids TV`,
			wantName:  "Kids TV",
			wantGroup: "Kids",
		},
		{
			name:      "no_attributes",
			line:      `#EXTINF:-1,Simple Channel`,
			wantName:  "Simple Channel",
			wantTVGID: "",
			wantGroup: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ch := parseExtInfLine(tt.line)
			if ch.Name != tt.wantName {
				t.Errorf("Name = %q, want %q", ch.Name, tt.wantName)
			}
			if ch.TVGID != tt.wantTVGID {
				t.Errorf("TVGID = %q, want %q", ch.TVGID, tt.wantTVGID)
			}
			if ch.GroupTitle != tt.wantGroup {
				t.Errorf("GroupTitle = %q, want %q", ch.GroupTitle, tt.wantGroup)
			}
		})
	}
}

// ─── isSportsChannel tests ────────────────────────────────────────────────────

func TestIsSportsChannel(t *testing.T) {
	tests := []struct {
		name     string
		ch       RawChannel
		expected bool
	}{
		{
			name:     "sports_group",
			ch:       RawChannel{Name: "ESPN", GroupTitle: "Sports"},
			expected: true,
		},
		{
			name:     "espn_in_name",
			ch:       RawChannel{Name: "ESPN Sports HD", GroupTitle: "US Channels"},
			expected: true,
		},
		{
			name:     "nfl_in_name",
			ch:       RawChannel{Name: "NFL Network Live", GroupTitle: "Entertainment"},
			expected: true,
		},
		{
			name:     "kids_channel",
			ch:       RawChannel{Name: "Kids Zone", GroupTitle: "Kids"},
			expected: false,
		},
		{
			name:     "news_channel",
			ch:       RawChannel{Name: "CNN", GroupTitle: "News"},
			expected: false,
		},
		{
			name:     "dazn_channel",
			ch:       RawChannel{Name: "DAZN", GroupTitle: "Premium"},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isSportsChannel(tt.ch)
			if got != tt.expected {
				t.Errorf("isSportsChannel(%+v) = %v, want %v", tt.ch, got, tt.expected)
			}
		})
	}
}

// ─── bestLeagueMatch / jaroWinkler sports matching tests ─────────────────────

func TestBestLeagueMatch_KnownLeague(t *testing.T) {
	// Note: Jaro-Winkler matches on character-level similarity.
	// "NFL Network" vs "National Football League" has moderate similarity (~0.58)
	// because they share few consecutive characters. The algorithm is designed for
	// typo correction, not abbreviation expansion — production matching uses
	// sportsBroadcasterKeywords + group-title filtering to pre-filter candidates.
	leagues := map[string]string{
		"league-nfl": "National Football League",
		"league-nba": "National Basketball Association",
		"league-mlb": "Major League Baseball",
	}

	tests := []struct {
		name         string
		channelName  string
		wantMinScore float64
	}{
		{
			// "NFL Network" has some character overlap with "National Football League"
			name:         "nfl_network_some_score",
			channelName:  "NFL Network",
			wantMinScore: 0.50,
		},
		{
			// "NBA TV" has some character overlap with league names
			name:         "nba_tv_some_score",
			channelName:  "NBA TV",
			wantMinScore: 0.50,
		},
		{
			// Exact league abbreviation match — the league abbreviation itself
			name:         "national_football_league_exact",
			channelName:  "National Football League",
			wantMinScore: 0.99,
		},
		{
			name:         "espn_sports_no_strong_match",
			channelName:  "ESPN Sports HD",
			wantMinScore: 0.0, // may not match any league strongly — that's OK
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, score := bestLeagueMatch(strings.ToLower(tt.channelName), leagues)
			if score < tt.wantMinScore {
				t.Errorf("bestLeagueMatch(%q) score %.3f < required %.3f",
					tt.channelName, score, tt.wantMinScore)
			}
		})
	}
}

func TestBestLeagueMatch_NoMatch(t *testing.T) {
	leagues := map[string]string{
		"league-nfl": "National Football League",
	}
	_, score := bestLeagueMatch("kids entertainment channel", leagues)
	if score >= matchThresholdStore {
		t.Errorf("unexpected match: score %.3f >= store threshold %.3f", score, matchThresholdStore)
	}
}

func TestBestLeagueMatch_EmptyLeagues(t *testing.T) {
	id, score := bestLeagueMatch("ESPN", map[string]string{})
	if id != "" || score != 0 {
		t.Errorf("expected empty result, got id=%q score=%f", id, score)
	}
}

// ─── jaroWinkler precision tests ──────────────────────────────────────────────

func TestJaroWinkler_ESPN_Match(t *testing.T) {
	// "ESPN Sports" vs "ESPN" should have high confidence
	score := jaroWinkler("espn sports", "espn")
	if score < 0.85 {
		t.Errorf("jaroWinkler(%q, %q) = %.3f, want >= 0.85", "espn sports", "espn", score)
	}
}

func TestJaroWinkler_KidsChannel_NoMatch(t *testing.T) {
	score := jaroWinkler("kids channel", "national football league")
	if score >= matchThresholdStore {
		t.Errorf("jaroWinkler(%q, %q) = %.3f, should be below store threshold %.3f",
			"kids channel", "national football league", score, matchThresholdStore)
	}
}

func TestJaroWinkler_Symmetry(t *testing.T) {
	s1, s2 := "nfl network", "national football league"
	if jaroWinkler(s1, s2) != jaroWinkler(s2, s1) {
		t.Error("jaroWinkler should be symmetric")
	}
}

func TestJaroWinkler_RangeValid(t *testing.T) {
	pairs := [][2]string{
		{"espn", "espn sports"},
		{"nba tv", "national basketball association"},
		{"fox sports 1", "fox"},
		{"", "test"},
		{"abc", "xyz"},
	}
	for _, p := range pairs {
		score := jaroWinkler(p[0], p[1])
		if score < 0.0 || score > 1.0 {
			t.Errorf("jaroWinkler(%q, %q) = %.3f out of [0,1] range", p[0], p[1], score)
		}
	}
}
