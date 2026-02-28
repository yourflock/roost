package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// ---- timeRangePlaylist validation -------------------------------------------

func TestTimeRangePlaylistInvalidRange(t *testing.T) {
	cfg := loadConfig()
	cfg.StorageDir = t.TempDir()
	a := newArchiver(cfg, nil)

	// end before start
	start := time.Now().UTC()
	end := start.Add(-time.Hour)
	_, err := a.timeRangePlaylist("test-channel", start, end)
	if err == nil {
		t.Error("expected error for end < start")
	}
}

func TestTimeRangePlaylistTooLong(t *testing.T) {
	cfg := loadConfig()
	cfg.StorageDir = t.TempDir()
	a := newArchiver(cfg, nil)

	start := time.Now().UTC()
	end := start.Add(9 * time.Hour) // over 8-hour limit
	_, err := a.timeRangePlaylist("test-channel", start, end)
	if err == nil {
		t.Error("expected error for range > 8 hours")
	}
}

func TestTimeRangePlaylistValidRange(t *testing.T) {
	cfg := loadConfig()
	cfg.StorageDir = t.TempDir()
	a := newArchiver(cfg, nil)

	// Valid range — no segments archived, but playlist structure should be valid
	start := time.Now().UTC().Add(-2 * time.Hour)
	end := time.Now().UTC()
	playlist, err := a.timeRangePlaylist("test-channel", start, end)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasPrefix(playlist, "#EXTM3U") {
		t.Error("expected playlist to start with #EXTM3U")
	}
	if !strings.Contains(playlist, "#EXT-X-ENDLIST") {
		t.Error("expected playlist to contain #EXT-X-ENDLIST")
	}
	if !strings.Contains(playlist, "#EXT-X-PLAYLIST-TYPE:VOD") {
		t.Error("expected VOD playlist type")
	}
}

// ---- health endpoint --------------------------------------------------------

func TestHandleHealth(t *testing.T) {
	cfg := loadConfig()
	hs := &handlerSet{cfg: cfg, db: nil, archiver: newArchiver(cfg, nil)}

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	hs.handleHealth(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("health: expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "roost-catchup") {
		t.Errorf("health: expected service name in body, got: %s", body)
	}
}

// ---- config defaults --------------------------------------------------------

func TestConfigDefaults(t *testing.T) {
	cfg := loadConfig()
	if cfg.RetentionDays != 7 {
		t.Errorf("expected default retention 7 days, got %d", cfg.RetentionDays)
	}
	if cfg.Port != "8098" {
		t.Errorf("expected default port 8098, got %s", cfg.Port)
	}
	if cfg.PollInterval <= 0 {
		t.Error("expected positive poll interval")
	}
}

// ---- generateHourPlaylist ---------------------------------------------------

func TestGenerateHourPlaylist(t *testing.T) {
	cfg := loadConfig()
	dir := t.TempDir()
	cfg.StorageDir = dir
	a := newArchiver(cfg, nil)

	// Create fake .ts segment files
	for i := 0; i < 3; i++ {
		segName := strings.Repeat("0", i+1) + "segment.ts"
		segPath := dir + "/" + segName
		if err := os.WriteFile(segPath, []byte("fake-ts-data"), 0o644); err != nil {
			t.Fatalf("failed to create test segment: %v", err)
		}
	}

	// Generate playlist
	err := a.generateHourPlaylist(nil, "test-channel", "2026-02-24", "14", dir)
	if err != nil {
		t.Fatalf("generateHourPlaylist: %v", err)
	}

	// Verify playlist was created
	playlistPath := dir + "/playlist.m3u8"
	contentBytes, err := os.ReadFile(playlistPath)
	if err != nil {
		t.Fatalf("playlist not created: %v", err)
	}
	content := string(contentBytes)

	if !strings.HasPrefix(content, "#EXTM3U") {
		t.Error("playlist should start with #EXTM3U")
	}
	if !strings.Contains(content, "#EXT-X-ENDLIST") {
		t.Error("playlist should end with #EXT-X-ENDLIST")
	}
	if !strings.Contains(content, ".ts") {
		t.Error("playlist should reference .ts segments")
	}
	if !strings.Contains(content, "#EXTINF:8.000,") {
		t.Error("playlist should contain #EXTINF markers")
	}
}

// ---- copyFile ---------------------------------------------------------------

func TestCopyFile(t *testing.T) {
	dir := t.TempDir()
	src := dir + "/source.ts"
	dst := dir + "/dest.ts"
	data := []byte("fake-segment-data-12345")
	if err := os.WriteFile(src, data, 0o644); err != nil {
		t.Fatalf("create source: %v", err)
	}
	if err := copyFile(src, dst); err != nil {
		t.Fatalf("copyFile: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read dest: %v", err)
	}
	if string(got) != string(data) {
		t.Errorf("copied content mismatch: got %q, want %q", got, data)
	}
}
