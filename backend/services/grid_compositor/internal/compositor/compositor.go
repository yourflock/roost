// compositor.go — Multi-channel grid compositor using FFmpeg filter_complex.
//
// Supports grid layouts:
//   - 1x1: single channel (pass-through)
//   - 2x1: side-by-side (2 inputs, horizontal)
//   - 1x2: stacked (2 inputs, vertical)
//   - 2x2: 2×2 grid (4 inputs)
//   - 3x3: 3×3 grid (9 inputs — reduced resolution per cell)
//   - pip: Picture-in-picture (1 main + 1 overlay, bottom-right corner)
//
// Each session runs a single FFmpeg process consuming N input HLS streams
// and producing one composite HLS stream at a fixed output resolution.
// Output: /var/roost/compositor/{session_id}/stream.m3u8
package compositor

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Layout describes the arrangement of input channels in the composite output.
type Layout string

const (
	Layout1x1 Layout = "1x1" // single channel
	Layout2x1 Layout = "2x1" // side-by-side (2 inputs)
	Layout1x2 Layout = "1x2" // stacked (2 inputs)
	Layout2x2 Layout = "2x2" // 2×2 grid (4 inputs)
	Layout3x3 Layout = "3x3" // 3×3 grid (9 inputs)
	LayoutPiP Layout = "pip" // picture-in-picture (2 inputs)
)

// outputResolutions maps layout to composite output resolution (WxH).
var outputResolutions = map[Layout]string{
	Layout1x1: "1280x720",
	Layout2x1: "1280x360", // two 640x360 panels side by side
	Layout1x2: "640x720",  // two 640x360 panels stacked
	Layout2x2: "1280x720", // four 640x360 panels in a 2×2 grid
	Layout3x3: "1280x720", // nine ~426x240 panels in a 3×3 grid
	LayoutPiP: "1280x720", // main 1280x720, overlay 320x180 bottom-right
}

// maxInputs maps layout to the number of input streams required.
var maxInputs = map[Layout]int{
	Layout1x1: 1,
	Layout2x1: 2,
	Layout1x2: 2,
	Layout2x2: 4,
	Layout3x3: 9,
	LayoutPiP: 2,
}

// Session represents a running compositor session.
type Session struct {
	ID           string
	Layout       Layout
	ChannelSlugs []string // ordered list of channel slugs, one per input
	OutputDir    string   // HLS output directory
	CreatedAt    time.Time

	cancel context.CancelFunc
	done   chan struct{}
	err    error
	mu     sync.Mutex
}

// Manager manages compositor sessions.
type Manager struct {
	segmentDir string // where ingest stores segments: {segmentDir}/{slug}/stream.m3u8
	outputBase string // where compositor stores output: {outputBase}/{session_id}/
	mu         sync.RWMutex
	sessions   map[string]*Session
}

// New creates a compositor Manager.
func New(segmentDir, outputBase string) *Manager {
	return &Manager{
		segmentDir: segmentDir,
		outputBase: outputBase,
		sessions:   make(map[string]*Session),
	}
}

// CreateSession starts a new compositor session.
// Returns the session ID and HLS stream URL path.
func (m *Manager) CreateSession(ctx context.Context, layout Layout, channelSlugs []string) (*Session, error) {
	required := maxInputs[layout]
	if len(channelSlugs) != required {
		return nil, fmt.Errorf("layout %s requires exactly %d channel(s), got %d",
			layout, required, len(channelSlugs))
	}
	for _, slug := range channelSlugs {
		m3u8 := filepath.Join(m.segmentDir, slug, "stream.m3u8")
		if _, err := os.Stat(m3u8); err != nil {
			return nil, fmt.Errorf("channel %q has no active HLS stream at %s", slug, m3u8)
		}
	}

	id := uuid.New().String()
	outDir := filepath.Join(m.outputBase, id)
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir output: %w", err)
	}

	sessCtx, cancel := context.WithCancel(ctx)
	sess := &Session{
		ID:           id,
		Layout:       layout,
		ChannelSlugs: channelSlugs,
		OutputDir:    outDir,
		CreatedAt:    time.Now(),
		cancel:       cancel,
		done:         make(chan struct{}),
	}

	m.mu.Lock()
	m.sessions[id] = sess
	m.mu.Unlock()

	go m.run(sessCtx, sess)
	return sess, nil
}

// GetSession returns a session by ID.
func (m *Manager) GetSession(id string) (*Session, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.sessions[id]
	return s, ok
}

// StopSession terminates a compositor session and removes its output.
func (m *Manager) StopSession(id string) error {
	m.mu.Lock()
	sess, ok := m.sessions[id]
	if ok {
		delete(m.sessions, id)
	}
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("session %s not found", id)
	}
	sess.cancel()
	<-sess.done
	go os.RemoveAll(sess.OutputDir)
	return nil
}

// ListSessions returns all active sessions.
func (m *Manager) ListSessions() []*Session {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		out = append(out, s)
	}
	return out
}

// run executes FFmpeg for the session, restarting on non-context errors.
func (m *Manager) run(ctx context.Context, sess *Session) {
	defer close(sess.done)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		args := buildFFmpegArgs(sess, m.segmentDir)
		cmd := exec.CommandContext(ctx, "ffmpeg", args...)
		cmd.Stdout = nil
		cmd.Stderr = nil

		log.Printf("[compositor] session %s starting FFmpeg (%s, %d channels)",
			sess.ID[:8], sess.Layout, len(sess.ChannelSlugs))

		if err := cmd.Start(); err != nil {
			log.Printf("[compositor] FFmpeg start error for session %s: %v", sess.ID[:8], err)
		} else {
			if err := cmd.Wait(); err != nil {
				if ctx.Err() != nil {
					return // cancelled normally
				}
				log.Printf("[compositor] FFmpeg exited for session %s: %v, restarting in 5s", sess.ID[:8], err)
			}
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
	}
}

// buildFFmpegArgs constructs the FFmpeg command for a given layout.
func buildFFmpegArgs(sess *Session, segmentDir string) []string {
	args := []string{"-hide_banner", "-loglevel", "error"}

	// Add all input streams with reconnect flags
	for _, slug := range sess.ChannelSlugs {
		m3u8 := filepath.Join(segmentDir, slug, "stream.m3u8")
		args = append(args,
			"-reconnect", "1",
			"-reconnect_streamed", "1",
			"-reconnect_delay_max", "5",
			"-i", m3u8,
		)
	}

	out := filepath.Join(sess.OutputDir, "stream.m3u8")

	switch sess.Layout {
	case Layout1x1:
		args = append(args,
			"-c:v", "libx264", "-b:v", "2500k",
			"-c:a", "aac", "-b:a", "128k",
			"-f", "hls",
			"-hls_time", "4",
			"-hls_list_size", "10",
			"-hls_flags", "delete_segments+append_list",
			out,
		)

	case Layout2x1:
		// Side-by-side: two 640x360 inputs, hstack
		filter := "[0:v]scale=640:360[a];[1:v]scale=640:360[b];[a][b]hstack=inputs=2[out]"
		args = append(args,
			"-filter_complex", filter,
			"-map", "[out]",
			"-map", "0:a",
			"-c:v", "libx264", "-b:v", "2000k",
			"-c:a", "aac", "-b:a", "128k",
			"-f", "hls",
			"-hls_time", "4",
			"-hls_list_size", "10",
			"-hls_flags", "delete_segments+append_list",
			out,
		)

	case Layout1x2:
		// Stacked: two 640x360 inputs, vstack
		filter := "[0:v]scale=640:360[a];[1:v]scale=640:360[b];[a][b]vstack=inputs=2[out]"
		args = append(args,
			"-filter_complex", filter,
			"-map", "[out]",
			"-map", "0:a",
			"-c:v", "libx264", "-b:v", "2000k",
			"-c:a", "aac", "-b:a", "128k",
			"-f", "hls",
			"-hls_time", "4",
			"-hls_list_size", "10",
			"-hls_flags", "delete_segments+append_list",
			out,
		)

	case Layout2x2:
		// 2×2 grid: four 640×360 inputs → xstack layout
		filter := strings.Join([]string{
			"[0:v]scale=640:360[v0]",
			"[1:v]scale=640:360[v1]",
			"[2:v]scale=640:360[v2]",
			"[3:v]scale=640:360[v3]",
			"[v0][v1][v2][v3]xstack=inputs=4:layout=0_0|w0_0|0_h0|w0_h0[out]",
		}, ";")
		args = append(args,
			"-filter_complex", filter,
			"-map", "[out]",
			"-map", "0:a",
			"-c:v", "libx264", "-b:v", "3000k",
			"-c:a", "aac", "-b:a", "128k",
			"-f", "hls",
			"-hls_time", "4",
			"-hls_list_size", "10",
			"-hls_flags", "delete_segments+append_list",
			out,
		)

	case Layout3x3:
		// 3×3 grid: nine ~426×240 inputs → xstack
		var scaleParts []string
		var stackInputs strings.Builder
		for i := 0; i < 9; i++ {
			scaleParts = append(scaleParts, fmt.Sprintf("[%d:v]scale=426:240[v%d]", i, i))
			stackInputs.WriteString(fmt.Sprintf("[v%d]", i))
		}
		layout3x3 := "0_0|w0_0|w0+w1_0|0_h0|w0_h0|w0+w1_h0|0_h0+h3|w0_h0+h3|w0+w1_h0+h3"
		xstack := fmt.Sprintf("%sxstack=inputs=9:layout=%s[out]", stackInputs.String(), layout3x3)
		filter := strings.Join(scaleParts, ";") + ";" + xstack
		args = append(args,
			"-filter_complex", filter,
			"-map", "[out]",
			"-map", "0:a",
			"-c:v", "libx264", "-b:v", "4000k",
			"-c:a", "aac", "-b:a", "128k",
			"-f", "hls",
			"-hls_time", "4",
			"-hls_list_size", "10",
			"-hls_flags", "delete_segments+append_list",
			out,
		)

	case LayoutPiP:
		// Picture-in-picture: main=1280x720, overlay=320x180 bottom-right
		// overlay_x = 1280-320-20 = 940, overlay_y = 720-180-20 = 520
		filter := "[0:v]scale=1280:720[main];[1:v]scale=320:180[pip];[main][pip]overlay=940:520[out]"
		args = append(args,
			"-filter_complex", filter,
			"-map", "[out]",
			"-map", "0:a",
			"-c:v", "libx264", "-b:v", "3000k",
			"-c:a", "aac", "-b:a", "128k",
			"-f", "hls",
			"-hls_time", "4",
			"-hls_list_size", "10",
			"-hls_flags", "delete_segments+append_list",
			out,
		)

	default:
		// Fallback passthrough of first input
		args = append(args,
			"-c", "copy",
			"-f", "hls",
			"-hls_time", "4",
			"-hls_list_size", "10",
			"-hls_flags", "delete_segments+append_list",
			out,
		)
	}

	return args
}
