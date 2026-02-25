// main.go — Roost Grid Compositor Service.
// Creates multi-channel composite HLS streams using FFmpeg filter_complex.
// Supports 2x2, 1x2, 2x1, 3x3 grid layouts and PiP (picture-in-picture).
// Port: 8100 (env: COMPOSITOR_PORT).
//
// Routes:
//   POST   /compositor/sessions            — create new grid session
//   GET    /compositor/sessions            — list active sessions
//   GET    /compositor/sessions/:id        — session status + stream URL
//   DELETE /compositor/sessions/:id        — stop and clean up session
//   GET    /compositor/sessions/:id/stream.m3u8 — HLS playlist (no auth — behind relay)
//   GET    /compositor/sessions/:id/:segment    — serve HLS segments
//   GET    /health
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/yourflock/roost/services/grid_compositor/internal/compositor"
)

type config struct {
	Port       string
	SegmentDir string
	OutputBase string
}

func loadConfig() config {
	return config{
		Port:       getEnv("COMPOSITOR_PORT", "8100"),
		SegmentDir: getEnv("SEGMENT_DIR", "/var/roost/segments"),
		OutputBase: getEnv("COMPOSITOR_OUTPUT_DIR", "/var/roost/compositor"),
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

type handler struct {
	cfg config
	mgr *compositor.Manager
}

// POST /compositor/sessions
// Body: {"layout":"2x2","channels":["slug1","slug2","slug3","slug4"]}
func (h *handler) handleCreate(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Layout   string   `json:"layout"`
		Channels []string `json:"channels"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON")
		return
	}
	if input.Layout == "" {
		input.Layout = "2x2"
	}
	layout := compositor.Layout(input.Layout)

	sess, err := h.mgr.CreateSession(r.Context(), layout, input.Channels)
	if err != nil {
		writeError(w, http.StatusBadRequest, "create_error", err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"id":         sess.ID,
		"layout":     string(sess.Layout),
		"channels":   sess.ChannelSlugs,
		"stream_url": fmt.Sprintf("/compositor/sessions/%s/stream.m3u8", sess.ID),
		"created_at": sess.CreatedAt.Format(time.RFC3339),
	})
}

// GET /compositor/sessions
func (h *handler) handleList(w http.ResponseWriter, r *http.Request) {
	sessions := h.mgr.ListSessions()
	type item struct {
		ID        string   `json:"id"`
		Layout    string   `json:"layout"`
		Channels  []string `json:"channels"`
		StreamURL string   `json:"stream_url"`
		CreatedAt string   `json:"created_at"`
	}
	var items []item
	for _, s := range sessions {
		items = append(items, item{
			ID:        s.ID,
			Layout:    string(s.Layout),
			Channels:  s.ChannelSlugs,
			StreamURL: fmt.Sprintf("/compositor/sessions/%s/stream.m3u8", s.ID),
			CreatedAt: s.CreatedAt.Format(time.RFC3339),
		})
	}
	if items == nil {
		items = []item{}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"sessions": items, "count": len(items)})
}

// GET /compositor/sessions/:id
func (h *handler) handleGet(w http.ResponseWriter, r *http.Request) {
	id := pathSegment(r.URL.Path, 2)
	sess, ok := h.mgr.GetSession(id)
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "session not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"id":         sess.ID,
		"layout":     string(sess.Layout),
		"channels":   sess.ChannelSlugs,
		"stream_url": fmt.Sprintf("/compositor/sessions/%s/stream.m3u8", sess.ID),
		"created_at": sess.CreatedAt.Format(time.RFC3339),
	})
}

// DELETE /compositor/sessions/:id
func (h *handler) handleStop(w http.ResponseWriter, r *http.Request) {
	id := pathSegment(r.URL.Path, 2)
	if err := h.mgr.StopSession(id); err != nil {
		writeError(w, http.StatusNotFound, "not_found", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "stopped", "id": id})
}

// GET /compositor/sessions/:id/stream.m3u8
func (h *handler) handlePlaylist(w http.ResponseWriter, r *http.Request) {
	id := pathSegment(r.URL.Path, 2)
	sess, ok := h.mgr.GetSession(id)
	if !ok {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}
	playlistPath := filepath.Join(sess.OutputDir, "stream.m3u8")
	serveFile(w, playlistPath, "application/vnd.apple.mpegurl", "no-cache")
}

// GET /compositor/sessions/:id/:segment (.ts files)
func (h *handler) handleSegment(w http.ResponseWriter, r *http.Request) {
	id := pathSegment(r.URL.Path, 2)
	seg := pathSegment(r.URL.Path, 3)
	if strings.Contains(seg, "..") || strings.Contains(seg, "/") {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	sess, ok := h.mgr.GetSession(id)
	if !ok {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}
	segPath := filepath.Join(sess.OutputDir, seg)
	serveFile(w, segPath, "video/MP2T", "max-age=60")
}

func (h *handler) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":          "ok",
		"service":         "roost-grid-compositor",
		"active_sessions": len(h.mgr.ListSessions()),
	})
}

func serveFile(w http.ResponseWriter, path, contentType, cache string) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", cache)
	_, _ = io.Copy(w, f)
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]string{"error": code, "message": msg})
}

func pathSegment(path string, n int) string {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if n >= len(parts) {
		return ""
	}
	return parts[n]
}

func main() {
	cfg := loadConfig()
	if err := os.MkdirAll(cfg.OutputBase, 0o755); err != nil {
		log.Fatalf("[compositor] cannot create output dir: %v", err)
	}

	mgr := compositor.New(cfg.SegmentDir, cfg.OutputBase)
	h := &handler{cfg: cfg, mgr: mgr}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", h.handleHealth)
	mux.Handle("GET /metrics", promhttp.Handler())

	// Session CRUD
	mux.HandleFunc("POST /compositor/sessions", h.handleCreate)
	mux.HandleFunc("GET /compositor/sessions", h.handleList)

	// Session-level routes (catch-all for :id sub-paths)
	mux.HandleFunc("/compositor/sessions/", func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		// /compositor/sessions/:id
		if len(parts) == 3 {
			switch r.Method {
			case http.MethodGet:
				h.handleGet(w, r)
			case http.MethodDelete:
				h.handleStop(w, r)
			default:
				writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET or DELETE")
			}
			return
		}
		// /compositor/sessions/:id/:file
		if len(parts) == 4 && r.Method == http.MethodGet {
			if strings.HasSuffix(parts[3], ".m3u8") {
				h.handlePlaylist(w, r)
			} else if strings.HasSuffix(parts[3], ".ts") {
				h.handleSegment(w, r)
			} else {
				writeError(w, http.StatusNotFound, "not_found", "unknown resource")
			}
			return
		}
		http.NotFound(w, r)
	})

	addr := ":" + cfg.Port
	log.Printf("[compositor] starting on %s, segments at %s, output at %s",
		addr, cfg.SegmentDir, cfg.OutputBase)

	srv := &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  120 * time.Second,
	}
	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("[compositor] server error: %v", err)
	}
}
