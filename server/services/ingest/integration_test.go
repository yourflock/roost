//go:build integration

// integration_test.go — End-to-end integration tests for the ingest + relay pipeline.
//
// Requirements:
//   - FFmpeg installed (exec.LookPath check — test skipped if missing)
//   - Running Postgres (POSTGRES_URL env or default localhost:5433/roost_dev)
//
// Run: go test -tags integration -v -timeout 60s ./...
package ingest_test

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/lib/pq"
	"github.com/google/uuid"

	"github.com/unyeco/roost/services/ingest/internal/pipeline"
	"github.com/unyeco/roost/services/relay/internal/sessions"
)

// TestIngestRelayPipeline starts a test FFmpeg source, runs the ingest pipeline,
// then verifies the relay serves a valid HLS playlist and MPEG-TS segment.
func TestIngestRelayPipeline(t *testing.T) {
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not found in PATH — skipping integration test")
	}
	t.Logf("using FFmpeg at %s", ffmpegPath)

	tmpDir := t.TempDir()
	db := openIntegrationDB(t)
	defer db.Close()

	// Start test FFmpeg source
	sourceDir := filepath.Join(tmpDir, "source")
	os.MkdirAll(sourceDir, 0o755)
	sourceM3U8 := filepath.Join(sourceDir, "stream.m3u8")

	sourceCmd := exec.Command("ffmpeg",
		"-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc2=size=320x180:rate=10",
		"-f", "lavfi", "-i", "sine=frequency=220",
		"-t", "30",
		"-c:v", "libx264", "-preset", "ultrafast", "-tune", "zerolatency",
		"-c:a", "aac",
		"-f", "hls", "-hls_time", "2", "-hls_list_size", "5",
		"-hls_flags", "delete_segments+append_list",
		sourceM3U8,
	)
	if err := sourceCmd.Start(); err != nil {
		t.Skipf("cannot start FFmpeg source: %v", err)
	}
	t.Cleanup(func() { sourceCmd.Process.Kill(); sourceCmd.Wait() })

	// Wait for source HLS (up to 20s)
	waitForFile(t, sourceM3U8, 20*time.Second, "test source HLS")

	// Run ingest pipeline
	segmentDir := filepath.Join(tmpDir, "segments")
	channelSlug := "inttest-" + randHex(4)

	mgr := pipeline.NewManager(segmentDir, 3, time.Minute, func(slug, status string) {
		t.Logf("[health] %s: %s", slug, status)
	})
	mgr.Sync([]pipeline.Channel{{
		Slug:          channelSlug,
		SourceURL:     sourceM3U8,
		SourceType:    "hls",
		BitrateConfig: pipeline.BitrateConfig{Mode: "passthrough"},
		IsActive:      true,
	}})
	t.Cleanup(mgr.StopAll)

	outM3U8 := filepath.Join(segmentDir, channelSlug, "stream.m3u8")
	waitForFile(t, outM3U8, 15*time.Second, "ingest HLS")

	// Insert subscriber + token
	subID, rawToken := insertTestSubscriber(t, db)

	// Start in-process relay
	relaySrv := buildInProcessRelay(t, db, segmentDir, 2)
	defer relaySrv.Close()

	// Test 1: GET stream.m3u8 with valid token
	m3u8URL := fmt.Sprintf("%s/stream/%s/stream.m3u8?token=%s&device_id=dev1", relaySrv.URL, channelSlug, rawToken)
	resp := doGet(t, m3u8URL)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("m3u8 GET: want 200, got %d: %s", resp.StatusCode, body)
	}
	m3u8Body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(m3u8Body), "#EXTM3U") {
		t.Errorf("m3u8 missing #EXTM3U header")
	}
	t.Logf("m3u8 OK: %d bytes", len(m3u8Body))

	// Test 2: Fetch first .ts segment, verify MPEG-TS sync byte
	firstSeg := firstTSSegment(string(m3u8Body))
	if firstSeg == "" {
		t.Log("no .ts segment in m3u8 yet — skipping segment byte check")
	} else {
		segURL := fmt.Sprintf("%s/stream/%s/%s?token=%s&device_id=dev1", relaySrv.URL, channelSlug, firstSeg, rawToken)
		segResp := doGet(t, segURL)
		defer segResp.Body.Close()
		if segResp.StatusCode != http.StatusOK {
			t.Fatalf("segment GET: want 200, got %d", segResp.StatusCode)
		}
		segData, _ := io.ReadAll(segResp.Body)
		if len(segData) > 0 && segData[0] != 0x47 {
			t.Errorf("segment sync byte: want 0x47, got 0x%02X", segData[0])
		} else if len(segData) > 0 {
			t.Logf("segment OK: %d bytes, sync byte 0x47", len(segData))
		}
	}

	// Test 3: stream_sessions row created
	var n int
	db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM stream_sessions WHERE subscriber_id = $1`, subID,
	).Scan(&n)
	if n == 0 {
		t.Error("expected stream_sessions row to exist after m3u8 request")
	}

	// Test 4: unauthenticated request returns 403
	unauthed := doGet(t, fmt.Sprintf("%s/stream/%s/stream.m3u8", relaySrv.URL, channelSlug))
	unauthed.Body.Close()
	if unauthed.StatusCode != http.StatusForbidden {
		t.Errorf("unauthed request: want 403, got %d", unauthed.StatusCode)
	}
}

// TestConcurrentStreamLimit verifies the relay enforces per-subscriber concurrent stream limits.
func TestConcurrentStreamLimit(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not found — skipping")
	}

	tmpDir := t.TempDir()
	db := openIntegrationDB(t)
	defer db.Close()

	// Create fake segments so relay can serve them
	for _, ch := range []string{"ch-a", "ch-b", "ch-c"} {
		dir := filepath.Join(tmpDir, ch)
		os.MkdirAll(dir, 0o755)
		os.WriteFile(filepath.Join(dir, "stream.m3u8"), []byte("#EXTM3U\n#EXT-X-VERSION:3\n"), 0o644)
	}

	_, rawToken := insertTestSubscriber(t, db)
	relaySrv := buildInProcessRelay(t, db, tmpDir, 2) // limit = 2
	defer relaySrv.Close()

	// First two streams: should succeed
	for i, ch := range []string{"ch-a", "ch-b"} {
		url := fmt.Sprintf("%s/stream/%s/stream.m3u8?token=%s&device_id=dev%d", relaySrv.URL, ch, rawToken, i)
		resp := doGet(t, url)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("stream %d (%s): want 200, got %d", i+1, ch, resp.StatusCode)
		}
	}

	// Let session timestamps settle
	time.Sleep(100 * time.Millisecond)

	// Third stream from different device: should be rate-limited
	url := fmt.Sprintf("%s/stream/ch-c/stream.m3u8?token=%s&device_id=dev99", relaySrv.URL, rawToken)
	resp := doGet(t, url)
	resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Errorf("third stream: want 429, got %d", resp.StatusCode)
	}
}

// --- Helpers ---

func waitForFile(t *testing.T, path string, timeout time.Duration, label string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			t.Logf("%s ready: %s", label, path)
			return
		}
		time.Sleep(300 * time.Millisecond)
	}
	t.Fatalf("%s did not appear within %s: %s", label, timeout, path)
}

func firstTSSegment(m3u8 string) string {
	for _, line := range strings.Split(m3u8, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasSuffix(line, ".ts") {
			return line
		}
	}
	return ""
}

func doGet(t *testing.T, url string) *http.Response {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	return resp
}

func randHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func hashAPIToken(raw string) string {
	h := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(h[:])
}

func openIntegrationDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("POSTGRES_URL")
	if dsn == "" {
		dsn = "postgres://roost:roost@localhost:5433/roost_dev?sslmode=disable"
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("db open: %v", err)
	}
	if err := db.Ping(); err != nil {
		t.Skipf("DB not available: %v", err)
	}
	return db
}

func insertTestSubscriber(t *testing.T, db *sql.DB) (uuid.UUID, string) {
	t.Helper()
	subID := uuid.New()
	email := fmt.Sprintf("inttest-%s@example.com", randHex(4))
	_, err := db.ExecContext(context.Background(), `
		INSERT INTO subscribers (id, email, display_name, email_verified, status)
		VALUES ($1, $2, 'Integration Tester', true, 'active')
	`, subID, email)
	if err != nil {
		t.Fatalf("insert subscriber: %v", err)
	}
	t.Cleanup(func() {
		db.ExecContext(context.Background(), `DELETE FROM api_tokens WHERE subscriber_id = $1`, subID)
		db.ExecContext(context.Background(), `DELETE FROM stream_sessions WHERE subscriber_id = $1`, subID)
		db.ExecContext(context.Background(), `DELETE FROM subscribers WHERE id = $1`, subID)
	})

	rawToken := "roost_" + randHex(32)
	tokenHash := hashAPIToken(rawToken)
	_, err = db.ExecContext(context.Background(), `
		INSERT INTO api_tokens (subscriber_id, token_hash, token_prefix, is_active)
		VALUES ($1, $2, $3, true)
	`, subID, tokenHash, rawToken[:12])
	if err != nil {
		t.Fatalf("insert token: %v", err)
	}

	return subID, rawToken
}

// buildInProcessRelay creates a test HTTP server that mimics the relay service.
// Uses the real sessions.Manager for concurrent stream enforcement.
func buildInProcessRelay(t *testing.T, db *sql.DB, segmentDir string, maxStreams int) *httptest.Server {
	t.Helper()
	sessMgr := sessions.NewManager(db, maxStreams)
	mux := http.NewServeMux()

	mux.HandleFunc("/stream/", func(w http.ResponseWriter, r *http.Request) {
		parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/stream/"), "/", 2)
		if len(parts) != 2 {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		slug, file := parts[0], parts[1]

		token := r.URL.Query().Get("token")
		if token == "" {
			w.WriteHeader(http.StatusForbidden)
			return
		}

		var subID uuid.UUID
		err := db.QueryRowContext(r.Context(), `
			SELECT s.id FROM api_tokens t
			JOIN subscribers s ON s.id = t.subscriber_id
			WHERE t.token_hash = $1 AND t.is_active = true AND s.status = 'active'
		`, hashAPIToken(token)).Scan(&subID)
		if err != nil {
			w.WriteHeader(http.StatusForbidden)
			return
		}

		deviceID := r.URL.Query().Get("device_id")
		if deviceID == "" {
			deviceID = "default"
		}

		if file == "stream.m3u8" || file == "master.m3u8" {
			_, err := sessMgr.OnPlaylistRequest(r.Context(), subID, slug, deviceID)
			if err != nil {
				http.Error(w, `{"error":"concurrent stream limit reached"}`, http.StatusTooManyRequests)
				return
			}
		}

		path := filepath.Join(segmentDir, slug, file)
		f, err := os.Open(path)
		if err != nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		defer f.Close()

		fi, _ := f.Stat()
		if file != "stream.m3u8" && file != "master.m3u8" && fi != nil {
			go sessMgr.OnSegmentRequest(subID, slug, deviceID, fi.Size())
		}

		if strings.HasSuffix(file, ".m3u8") {
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			w.Header().Set("Cache-Control", "no-cache")
		} else {
			w.Header().Set("Content-Type", "video/MP2T")
			w.Header().Set("Cache-Control", "max-age=60")
		}
		io.Copy(w, f)
	})

	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"status":"ok"}`)
	})

	return httptest.NewServer(mux)
}
