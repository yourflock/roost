// ingest_test.go — Unit tests for ingest service config loading, pipeline creation,
// and health check logic.
package ingest_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/unyeco/roost/services/ingest/internal/config"
	"github.com/unyeco/roost/services/ingest/internal/encryption"
	"github.com/unyeco/roost/services/ingest/internal/pipeline"
)

// --- Config tests ---

func TestConfigDefaults(t *testing.T) {
	os.Unsetenv("INGEST_PORT")
	os.Unsetenv("SEGMENT_DIR")
	os.Unsetenv("MAX_RESTARTS")
	os.Unsetenv("CHANNEL_POLL_INTERVAL")
	os.Unsetenv("RESTART_WINDOW")

	cfg := config.Load()

	if cfg.IngestPort != "8094" {
		t.Errorf("IngestPort: want 8094, got %q", cfg.IngestPort)
	}
	if cfg.SegmentDir != "/var/roost/segments" {
		t.Errorf("SegmentDir: want /var/roost/segments, got %q", cfg.SegmentDir)
	}
	if cfg.MaxRestarts != 5 {
		t.Errorf("MaxRestarts: want 5, got %d", cfg.MaxRestarts)
	}
	if cfg.ChannelPollInterval != 30*time.Second {
		t.Errorf("ChannelPollInterval: want 30s, got %s", cfg.ChannelPollInterval)
	}
	if cfg.RestartWindow != 5*time.Minute {
		t.Errorf("RestartWindow: want 5m, got %s", cfg.RestartWindow)
	}
}

func TestConfigFromEnv(t *testing.T) {
	os.Setenv("INGEST_PORT", "9090")
	os.Setenv("SEGMENT_DIR", "/tmp/segs")
	os.Setenv("MAX_RESTARTS", "3")
	os.Setenv("CHANNEL_POLL_INTERVAL", "10s")
	os.Setenv("RESTART_WINDOW", "2m")
	defer func() {
		os.Unsetenv("INGEST_PORT")
		os.Unsetenv("SEGMENT_DIR")
		os.Unsetenv("MAX_RESTARTS")
		os.Unsetenv("CHANNEL_POLL_INTERVAL")
		os.Unsetenv("RESTART_WINDOW")
	}()

	cfg := config.Load()
	if cfg.IngestPort != "9090" {
		t.Errorf("IngestPort from env: want 9090, got %q", cfg.IngestPort)
	}
	if cfg.SegmentDir != "/tmp/segs" {
		t.Errorf("SegmentDir from env: want /tmp/segs, got %q", cfg.SegmentDir)
	}
	if cfg.MaxRestarts != 3 {
		t.Errorf("MaxRestarts from env: want 3, got %d", cfg.MaxRestarts)
	}
	if cfg.ChannelPollInterval != 10*time.Second {
		t.Errorf("ChannelPollInterval from env: want 10s, got %s", cfg.ChannelPollInterval)
	}
	if cfg.RestartWindow != 2*time.Minute {
		t.Errorf("RestartWindow from env: want 2m, got %s", cfg.RestartWindow)
	}
}

// --- Pipeline creation tests ---

func TestNewManagerCreation(t *testing.T) {
	mgr := pipeline.NewManager("/tmp/segs", 5, 5*time.Minute, nil)
	if mgr == nil {
		t.Fatal("NewManager returned nil")
	}
	if mgr.ActiveCount() != 0 {
		t.Errorf("ActiveCount: want 0 initially, got %d", mgr.ActiveCount())
	}
}

func TestBuildFFmpegArgsPassthrough(t *testing.T) {
	ch := pipeline.Channel{
		Slug:      "test-channel",
		SourceURL: "http://example.com/stream",
		BitrateConfig: pipeline.BitrateConfig{
			Mode: "passthrough",
		},
	}
	args := pipeline.BuildFFmpegArgs(ch, "/tmp/segs")

	found := false
	for i, a := range args {
		if a == "-c" && i+1 < len(args) && args[i+1] == "copy" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("passthrough args missing -c copy: %v", args)
	}

	lastArg := args[len(args)-1]
	expected := "/tmp/segs/test-channel/stream.m3u8"
	if lastArg != expected {
		t.Errorf("passthrough: last arg want %q, got %q", expected, lastArg)
	}
}

func TestBuildFFmpegArgsTranscode(t *testing.T) {
	ch := pipeline.Channel{
		Slug:      "hd-channel",
		SourceURL: "http://example.com/live",
		BitrateConfig: pipeline.BitrateConfig{
			Mode:     "transcode",
			Variants: []string{"720p", "1080p"},
		},
	}
	args := pipeline.BuildFFmpegArgs(ch, "/tmp/segs")

	found264 := false
	for _, a := range args {
		if a == "libx264" {
			found264 = true
			break
		}
	}
	if !found264 {
		t.Errorf("transcode args missing libx264: %v", args)
	}

	foundVSM := false
	for _, a := range args {
		if a == "-var_stream_map" {
			foundVSM = true
			break
		}
	}
	if !foundVSM {
		t.Errorf("transcode args missing -var_stream_map: %v", args)
	}
}

func TestBuildFFmpegArgsEncrypt(t *testing.T) {
	ch := pipeline.Channel{
		Slug:      "enc-channel",
		SourceURL: "http://example.com/stream",
		BitrateConfig: pipeline.BitrateConfig{
			Mode:    "passthrough",
			Encrypt: true,
		},
	}
	args := pipeline.BuildFFmpegArgs(ch, "/tmp/segs")

	foundKey := false
	for i, a := range args {
		if a == "-hls_key_info_file" && i+1 < len(args) {
			foundKey = true
			if args[i+1] != "/tmp/segs/enc-channel/enc.keyinfo" {
				t.Errorf("keyinfo path: want /tmp/segs/enc-channel/enc.keyinfo, got %q", args[i+1])
			}
			break
		}
	}
	if !foundKey {
		t.Errorf("encrypt args missing -hls_key_info_file: %v", args)
	}
}

func TestManagerSyncAndStop(t *testing.T) {
	mgr := pipeline.NewManager(t.TempDir(), 5, 5*time.Minute, nil)
	mgr.StopAll()
	if mgr.ActiveCount() != 0 {
		t.Errorf("ActiveCount after StopAll: want 0, got %d", mgr.ActiveCount())
	}
}

func TestHealthCallbackFired(t *testing.T) {
	var mu sync.Mutex
	updates := make([]string, 0)

	mgr := pipeline.NewManager(t.TempDir(), 2, 1*time.Second, func(slug, status string) {
		mu.Lock()
		updates = append(updates, slug+":"+status)
		mu.Unlock()
	})

	mgr.Sync([]pipeline.Channel{
		{Slug: "test", SourceURL: "http://fake/stream", IsActive: true, BitrateConfig: pipeline.BitrateConfig{Mode: "passthrough"}},
	})

	// Wait for at least one health update
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(updates)
		mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	mgr.StopAll()

	mu.Lock()
	got := len(updates)
	mu.Unlock()

	if got == 0 {
		t.Error("expected at least one health callback, got none")
	}
}

// --- Encryption tests ---

func TestGenerateChannelKey(t *testing.T) {
	key, err := encryption.GenerateChannelKey("test-channel", "20260224")
	if err != nil {
		t.Fatalf("GenerateChannelKey: %v", err)
	}
	if len(key) != 16 {
		t.Errorf("key length: want 16, got %d", len(key))
	}

	// Keys should be random — two generated keys should differ
	key2, _ := encryption.GenerateChannelKey("test-channel", "20260224")
	if string(key) == string(key2) {
		t.Error("two generated keys should not be identical (random generation)")
	}
}

func TestKeyManagerWriteAndRead(t *testing.T) {
	dir := t.TempDir()
	km := encryption.NewKeyManager(nil, dir) // no Redis

	slug := "test-ch"
	key, err := km.GetCurrentKey(context.Background(), slug)
	if err != nil {
		t.Fatalf("GetCurrentKey: %v", err)
	}
	if len(key) != 16 {
		t.Errorf("key length: want 16, got %d", len(key))
	}

	// Second call should return the same key (disk cache)
	key2, err := km.GetCurrentKey(context.Background(), slug)
	if err != nil {
		t.Fatalf("GetCurrentKey (2nd call): %v", err)
	}
	if string(key) != string(key2) {
		t.Error("second GetCurrentKey call should return same key as first")
	}

	// Key file should exist on disk
	keyPath := filepath.Join(dir, slug, "enc.key")
	data, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("enc.key not found on disk: %v", err)
	}
	if string(data) != string(key) {
		t.Error("disk key does not match returned key")
	}
}

func TestKeyInfoFileContent(t *testing.T) {
	dir := t.TempDir()
	km := encryption.NewKeyManager(nil, dir)
	slug := "encrypted-ch"

	if err := km.WriteKeyInfo(context.Background(), slug); err != nil {
		t.Fatalf("WriteKeyInfo: %v", err)
	}

	// Verify enc.keyinfo file content
	keyInfoPath := filepath.Join(dir, slug, "enc.keyinfo")
	content, err := os.ReadFile(keyInfoPath)
	if err != nil {
		t.Fatalf("enc.keyinfo not found: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(string(content)), "\n")
	if len(lines) < 2 {
		t.Fatalf("keyinfo: want at least 2 lines, got %d: %q", len(lines), content)
	}

	// Line 1: key URI (relative)
	if !strings.HasPrefix(lines[0], "/stream/") {
		t.Errorf("keyinfo line 1 (URI): want /stream/..., got %q", lines[0])
	}
	if !strings.Contains(lines[0], slug) {
		t.Errorf("keyinfo URI should contain slug %q: got %q", slug, lines[0])
	}

	// Line 2: key file path
	if !strings.HasSuffix(lines[1], "enc.key") {
		t.Errorf("keyinfo line 2 (path): want ...enc.key, got %q", lines[1])
	}
}

// --- P11 Quality Ladder tests ---

// TestBuildFFmpegArgsFullLadder verifies the full 4-variant quality ladder args.
func TestBuildFFmpegArgsFullLadder(t *testing.T) {
	ch := pipeline.Channel{
		Slug:      "sports",
		SourceURL: "http://example.com/live.m3u8",
		BitrateConfig: pipeline.BitrateConfig{
			Mode:     "transcode",
			Variants: []string{"360p", "480p", "720p", "1080p"},
		},
	}
	args := pipeline.BuildFFmpegArgs(ch, "/var/roost/segments")
	joined := strings.Join(args, " ")

	// All 4 bitrates must appear
	for _, kv := range []string{"800k", "1500k", "2500k", "5000k"} {
		if !strings.Contains(joined, kv) {
			t.Errorf("expected bitrate %s in full ladder args", kv)
		}
	}
	// All 4 resolutions must appear
	for _, res := range []string{"640x360", "854x480", "1280x720", "1920x1080"} {
		if !strings.Contains(joined, res) {
			t.Errorf("expected resolution %s in full ladder args", res)
		}
	}
	// master_pl_name must be set
	if !strings.Contains(joined, "master.m3u8") {
		t.Error("expected master.m3u8 in full ladder args")
	}
	// var_stream_map must include all 4 streams
	if !strings.Contains(joined, "v:3,a:3") {
		t.Error("expected v:3,a:3 in var_stream_map for 4-variant output")
	}
}

// TestBuildFFmpegArgsLightweightLadder verifies 2-variant (720p+1080p) config.
func TestBuildFFmpegArgsLightweightLadder(t *testing.T) {
	ch := pipeline.Channel{
		Slug:      "news",
		SourceURL: "http://example.com/live.m3u8",
		BitrateConfig: pipeline.BitrateConfig{
			Mode:     "transcode",
			Variants: []string{"720p", "1080p"},
		},
	}
	args := pipeline.BuildFFmpegArgs(ch, "/var/roost/segments")
	joined := strings.Join(args, " ")

	if !strings.Contains(joined, "2500k") {
		t.Error("expected 720p bitrate (2500k) in lightweight ladder")
	}
	if !strings.Contains(joined, "5000k") {
		t.Error("expected 1080p bitrate (5000k) in lightweight ladder")
	}
	// 360p/480p must NOT appear
	if strings.Contains(joined, "800k") {
		t.Error("360p bitrate (800k) should not appear in 720p+1080p-only ladder")
	}
}

// TestBuildFFmpegArgsSingleVariant verifies single variant outputs simple m3u8.
func TestBuildFFmpegArgsSingleVariant(t *testing.T) {
	ch := pipeline.Channel{
		Slug:      "lowbw",
		SourceURL: "http://example.com/live.m3u8",
		BitrateConfig: pipeline.BitrateConfig{
			Mode:     "transcode",
			Variants: []string{"360p"},
		},
	}
	args := pipeline.BuildFFmpegArgs(ch, "/var/roost/segments")
	joined := strings.Join(args, " ")

	// Single variant: no var_stream_map, no master.m3u8
	if strings.Contains(joined, "var_stream_map") {
		t.Error("single variant should not use var_stream_map")
	}
	if strings.Contains(joined, "master.m3u8") {
		t.Error("single variant should not produce master.m3u8")
	}
	// Output goes to stream.m3u8
	if !strings.Contains(joined, "stream.m3u8") {
		t.Error("single variant should output stream.m3u8")
	}
}

// TestBuildMasterPlaylist verifies full ladder produces master.m3u8 in output.
// BuildMasterPlaylist is tested indirectly via BuildFFmpegArgs (which sets -master_pl_name).
func TestBuildMasterPlaylistIndirect(t *testing.T) {
	ch := pipeline.Channel{
		Slug:      "test-master",
		SourceURL: "http://example.com/live.m3u8",
		BitrateConfig: pipeline.BitrateConfig{
			Mode:     "transcode",
			Variants: []string{"720p", "1080p"},
		},
	}
	args := pipeline.BuildFFmpegArgs(ch, "/tmp/segments")
	found := false
	for _, a := range args {
		if a == "master.m3u8" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected master.m3u8 in multi-variant args, got: %v", args)
	}
}

// TestPassthroughUnchanged verifies passthrough channels are not affected by ladder additions.
func TestPassthroughUnchanged(t *testing.T) {
	ch := pipeline.Channel{
		Slug:      "passthru",
		SourceURL: "http://example.com/live.ts",
		BitrateConfig: pipeline.BitrateConfig{
			Mode: "passthrough",
		},
	}
	args := pipeline.BuildFFmpegArgs(ch, "/var/roost/segments")
	joined := strings.Join(args, " ")

	// Must copy, not transcode
	if !strings.Contains(joined, "-c copy") {
		t.Error("passthrough should use -c copy")
	}
	// Must not transcode
	if strings.Contains(joined, "libx264") {
		t.Error("passthrough should not use libx264")
	}
}
