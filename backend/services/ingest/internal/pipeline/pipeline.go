// pipeline.go — FFmpeg pipeline manager for HLS stream ingest.
// Manages per-channel FFmpeg processes: spawn, monitor, restart on failure,
// and graceful shutdown. Source URLs are never logged in full.
package pipeline

import (
	"context"
	"fmt"
	"log"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

// Channel represents a live channel to be ingested.
type Channel struct {
	ID            string
	Slug          string
	SourceURL     string
	SourceType    string // "hls", "rtmp", "mpegts"
	BitrateConfig BitrateConfig
	IsActive      bool
}

// BitrateConfig controls transcoding behavior.
// Supported modes:
//   - "passthrough"  — copy stream as-is (default)
//   - "transcode"    — encode to one or more variants
//
// Variants (only used when mode=="transcode"):
//   - "360p"  — 640×360,  800 kbps video
//   - "480p"  — 854×480,  1500 kbps video
//   - "720p"  — 1280×720, 2500 kbps video
//   - "1080p" — 1920×1080,5000 kbps video
//   (all variants use AAC 128 kbps audio)
//
// Encrypt: wrap passthrough or first variant with AES-128.
type BitrateConfig struct {
	Mode     string   `json:"mode"`     // "passthrough" or "transcode"
	Variants []string `json:"variants"` // e.g. ["360p","480p","720p","1080p"]
	Encrypt  bool     `json:"encrypt"`  // enable AES-128 encryption
}

// processState tracks a running FFmpeg instance and its restart history.
type processState struct {
	cmd      *exec.Cmd
	channel  Channel
	restarts []time.Time // timestamps of recent restarts
	cancel   context.CancelFunc
	mu       sync.Mutex
}

// Manager manages all active FFmpeg pipelines.
type Manager struct {
	segmentDir    string
	maxRestarts   int
	restartWindow time.Duration
	setHealth     func(slug, status string)

	mu       sync.RWMutex
	channels map[string]*processState // keyed by channel slug
}

// NewManager creates a pipeline manager.
// setHealthFn is called whenever a channel's health status changes.
func NewManager(segmentDir string, maxRestarts int, restartWindow time.Duration, setHealthFn func(slug, status string)) *Manager {
	if setHealthFn == nil {
		setHealthFn = func(slug, status string) {}
	}
	return &Manager{
		segmentDir:    segmentDir,
		maxRestarts:   maxRestarts,
		restartWindow: restartWindow,
		setHealth:     setHealthFn,
		channels:      make(map[string]*processState),
	}
}

// Sync reconciles the manager's running processes against the provided channel list.
// New channels are started; channels no longer active are stopped.
func (m *Manager) Sync(channels []Channel) {
	desired := make(map[string]Channel)
	for _, ch := range channels {
		if ch.IsActive {
			desired[ch.Slug] = ch
		}
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Stop channels no longer wanted
	for slug, state := range m.channels {
		if _, ok := desired[slug]; !ok {
			log.Printf("[ingest] stopping removed channel %q", slug)
			m.stopLocked(state)
			delete(m.channels, slug)
		}
	}

	// Start new channels
	for slug, ch := range desired {
		if _, running := m.channels[slug]; !running {
			log.Printf("[ingest] starting channel %q", slug)
			m.startLocked(ch)
		}
	}
}

// StopAll sends SIGTERM to every running FFmpeg process and waits up to 10s.
func (m *Manager) StopAll() {
	m.mu.Lock()
	states := make([]*processState, 0, len(m.channels))
	for _, s := range m.channels {
		states = append(states, s)
	}
	m.channels = make(map[string]*processState)
	m.mu.Unlock()

	var wg sync.WaitGroup
	for _, s := range states {
		wg.Add(1)
		go func(ps *processState) {
			defer wg.Done()
			m.stopLocked(ps)
		}(s)
	}

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		log.Println("[ingest] graceful shutdown timed out, killing remaining processes")
		for _, s := range states {
			s.mu.Lock()
			if s.cmd != nil && s.cmd.Process != nil {
				s.cmd.Process.Kill()
			}
			s.mu.Unlock()
		}
	}
}

// ActiveCount returns the number of currently running pipelines.
func (m *Manager) ActiveCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.channels)
}

// startLocked starts a new FFmpeg process for the channel. Must hold m.mu.
func (m *Manager) startLocked(ch Channel) {
	ctx, cancel := context.WithCancel(context.Background())
	state := &processState{
		channel: ch,
		cancel:  cancel,
	}
	m.channels[ch.Slug] = state
	go m.runLoop(ctx, state)
}

// stopLocked sends SIGTERM to the process and cancels its context.
func (m *Manager) stopLocked(state *processState) {
	state.cancel()
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.cmd != nil && state.cmd.Process != nil {
		state.cmd.Process.Signal(syscall.SIGTERM)
	}
}

// runLoop manages the lifecycle of a single FFmpeg process, restarting on failure.
func (m *Manager) runLoop(ctx context.Context, state *processState) {
	slug := state.channel.Slug

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		if m.tooManyRestarts(state) {
			log.Printf("[ingest] channel %q exceeded restart limit (%d in %s), marking unhealthy",
				slug, m.maxRestarts, m.restartWindow)
			m.setHealth(slug, "unhealthy")
			return
		}

		outDir := filepath.Join(m.segmentDir, slug)
		if err := os.MkdirAll(outDir, 0o755); err != nil {
			log.Printf("[ingest] cannot create segment dir for %q: %v", slug, err)
			time.Sleep(5 * time.Second)
			continue
		}

		args := BuildFFmpegArgs(state.channel, m.segmentDir)
		cmd := exec.CommandContext(ctx, "ffmpeg", args...)
		// Suppress stdout/stderr to avoid leaking source URLs in logs
		cmd.Stdout = nil
		cmd.Stderr = nil

		state.mu.Lock()
		state.cmd = cmd
		state.mu.Unlock()

		log.Printf("[ingest] starting FFmpeg for channel %q (source: %s)", slug, safeLogURL(state.channel.SourceURL))
		m.setHealth(slug, "starting")

		if err := cmd.Start(); err != nil {
			log.Printf("[ingest] FFmpeg start error for %q: %v", slug, err)
		} else {
			m.setHealth(slug, "healthy")
			cmd.Wait()
		}

		select {
		case <-ctx.Done():
			return
		default:
		}

		state.mu.Lock()
		state.restarts = append(state.restarts, time.Now())
		state.mu.Unlock()

		log.Printf("[ingest] FFmpeg exited for %q, restarting in 5s", slug)
		m.setHealth(slug, "restarting")

		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
	}
}

// tooManyRestarts returns true if the channel has exceeded maxRestarts within restartWindow.
func (m *Manager) tooManyRestarts(state *processState) bool {
	state.mu.Lock()
	defer state.mu.Unlock()

	cutoff := time.Now().Add(-m.restartWindow)
	recent := 0
	for _, t := range state.restarts {
		if t.After(cutoff) {
			recent++
		}
	}
	return recent >= m.maxRestarts
}

// variantSpec describes a single quality variant for transcoded output.
type variantSpec struct {
	name       string // e.g. "360p"
	resolution string // e.g. "640x360"
	videoBitrate string // e.g. "800k"
}

// knownVariants is the canonical ordered list of quality variants.
// Order matters: lower quality first so adaptive players start with the smallest.
var knownVariants = []variantSpec{
	{name: "360p",  resolution: "640x360",   videoBitrate: "800k"},
	{name: "480p",  resolution: "854x480",   videoBitrate: "1500k"},
	{name: "720p",  resolution: "1280x720",  videoBitrate: "2500k"},
	{name: "1080p", resolution: "1920x1080", videoBitrate: "5000k"},
}

// variantBandwidthBPS returns the nominal HLS BANDWIDTH value (bits/s) for a variant.
// Includes video + audio (128 kbps).
var variantBandwidthBPS = map[string]int{
	"360p":  800_000  + 128_000,
	"480p":  1_500_000 + 128_000,
	"720p":  2_500_000 + 128_000,
	"1080p": 5_000_000 + 128_000,
}

// variantResolutionStr maps variant name → HLS RESOLUTION attribute value.
var variantResolutionStr = map[string]string{
	"360p":  "640x360",
	"480p":  "854x480",
	"720p":  "1280x720",
	"1080p": "1920x1080",
}

// selectedVariants filters knownVariants to those requested in cfg.Variants.
// Returns knownVariants in canonical order if Variants is empty or ["all"].
func selectedVariants(cfg BitrateConfig) []variantSpec {
	if len(cfg.Variants) == 0 {
		return knownVariants
	}
	want := make(map[string]bool, len(cfg.Variants))
	for _, v := range cfg.Variants {
		want[v] = true
	}
	if want["all"] {
		return knownVariants
	}
	var out []variantSpec
	for _, v := range knownVariants {
		if want[v.name] {
			out = append(out, v)
		}
	}
	if len(out) == 0 {
		return knownVariants // fallback: all
	}
	return out
}

// BuildFFmpegArgs returns the FFmpeg argument list for a channel.
// Handles:
//   - passthrough (copy, default)
//   - AES-128 encrypted passthrough
//   - single-variant transcode (mode=transcode, 1 variant)
//   - multi-variant transcode (mode=transcode, 2-4 variants) → master playlist + variant playlists
func BuildFFmpegArgs(ch Channel, segmentDir string) []string {
	outDir := filepath.Join(segmentDir, ch.Slug)
	m3u8 := filepath.Join(outDir, "stream.m3u8")

	// Base input args — reconnect flags for live stream resilience
	base := []string{
		"-hide_banner", "-loglevel", "error",
		"-reconnect", "1",
		"-reconnect_streamed", "1",
		"-reconnect_delay_max", "5",
		"-i", ch.SourceURL,
	}

	switch {
	case ch.BitrateConfig.Encrypt && ch.BitrateConfig.Mode != "transcode":
		// AES-128 encrypted passthrough
		keyInfoFile := filepath.Join(outDir, "enc.keyinfo")
		return append(base,
			"-c", "copy",
			"-f", "hls",
			"-hls_time", "4",
			"-hls_list_size", "10",
			"-hls_flags", "delete_segments+append_list",
			"-hls_key_info_file", keyInfoFile,
			m3u8,
		)

	case ch.BitrateConfig.Mode == "transcode":
		variants := selectedVariants(ch.BitrateConfig)
		if len(variants) == 1 {
			// Single-variant encode — simple output
			v := variants[0]
			return append(base,
				"-c:v", "libx264", "-b:v", v.videoBitrate, "-s", v.resolution,
				"-c:a", "aac", "-b:a", "128k",
				"-f", "hls",
				"-hls_time", "4",
				"-hls_list_size", "10",
				"-hls_flags", "delete_segments+append_list",
				m3u8,
			)
		}
		// Multi-variant encode — generates stream_0.m3u8 … stream_N.m3u8 + master.m3u8
		return buildMultiVariantArgs(base, variants, outDir)

	default:
		// Passthrough (default)
		return append(base,
			"-c", "copy",
			"-f", "hls",
			"-hls_time", "4",
			"-hls_list_size", "10",
			"-hls_flags", "delete_segments+append_list",
			m3u8,
		)
	}
}

// buildMultiVariantArgs builds FFmpeg args for multi-variant (ABR) HLS output.
// Produces: stream_0.m3u8 (lowest quality) … stream_N.m3u8 (highest quality) + master.m3u8
func buildMultiVariantArgs(base []string, variants []variantSpec, outDir string) []string {
	args := make([]string, len(base))
	copy(args, base)

	// -map 0:v -map 0:a for each variant
	for range variants {
		args = append(args, "-map", "0:v", "-map", "0:a")
	}

	// Per-stream encoding parameters
	for i, v := range variants {
		args = append(args,
			fmt.Sprintf("-c:v:%d", i), "libx264",
			fmt.Sprintf("-b:v:%d", i), v.videoBitrate,
			fmt.Sprintf("-s:v:%d", i), v.resolution,
			fmt.Sprintf("-c:a:%d", i), "aac",
			fmt.Sprintf("-b:a:%d", i), "128k",
		)
	}

	// Build var_stream_map: "v:0,a:0 v:1,a:1 ..."
	varMap := ""
	for i := range variants {
		if i > 0 {
			varMap += " "
		}
		varMap += fmt.Sprintf("v:%d,a:%d", i, i)
	}

	variantPattern := filepath.Join(outDir, "stream_%v.m3u8")
	args = append(args,
		"-f", "hls",
		"-hls_time", "4",
		"-hls_list_size", "10",
		"-hls_flags", "delete_segments+append_list",
		"-var_stream_map", varMap,
		"-master_pl_name", "master.m3u8",
		variantPattern,
	)
	return args
}

// BuildMasterPlaylist generates a HLS master playlist string for the given variants.
// The relay serves this as /stream/:slug/master.m3u8 when a channel is in transcode mode.
func BuildMasterPlaylist(variants []variantSpec) string {
	out := "#EXTM3U\n#EXT-X-VERSION:3\n"
	for i, v := range variants {
		bw := variantBandwidthBPS[v.name]
		res := variantResolutionStr[v.name]
		out += fmt.Sprintf("#EXT-X-STREAM-INF:BANDWIDTH=%d,RESOLUTION=%s\nstream_%d.m3u8\n", bw, res, i)
	}
	return out
}

// safeLogURL returns a URL safe for logging: scheme://host/... (path truncated).
func safeLogURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "[unparseable url]"
	}
	return fmt.Sprintf("%s://%s/...", u.Scheme, u.Host)
}

// VariantSpecExported is an exported alias for variantSpec — used by tests.
type VariantSpecExported = variantSpec

// HealthFileMonitor checks whether a channel's HLS output is actively being updated.
// Returns "healthy" if the stream.m3u8 file was modified within the last 30 seconds,
// "stale" if it exists but hasn't been updated recently, or "offline" if missing.
func HealthFileMonitor(segmentDir, slug string) string {
	m3u8 := filepath.Join(segmentDir, slug, "stream.m3u8")
	fi, err := os.Stat(m3u8)
	if err != nil {
		// Also check for transcoded output (master.m3u8 or stream_0.m3u8)
		alt := filepath.Join(segmentDir, slug, "master.m3u8")
		if _, err2 := os.Stat(alt); err2 != nil {
			return "offline"
		}
		fi, _ = os.Stat(alt)
	}
	if fi == nil {
		return "offline"
	}
	if time.Since(fi.ModTime()) > 30*time.Second {
		return "stale"
	}
	return "healthy"
}
