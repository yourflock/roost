// main.go — Roost Relay Service.
// Serves HLS playlists and segments to authenticated subscribers.
// Token validation required on every m3u8 and .ts request.
// Port: 8090 (env: RELAY_PORT). Sits behind Nginx which terminates TLS.
//
// Routes:
//   GET /stream/:slug/stream.m3u8   — passthrough playlist (token required)
//   GET /stream/:slug/master.m3u8   — adaptive bitrate master playlist (token required)
//   GET /stream/:slug/:segment      — .ts segment or variant playlist (token required)
//   GET /stream/:slug/key           — AES-128 decryption key (token required, P4-T06)
//   GET /health                     — health check (no auth)
package main

import (
	"database/sql"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/lib/pq"

	relayauth "github.com/yourflock/roost/services/relay/internal/auth"
	"github.com/yourflock/roost/services/relay/internal/sessions"
)

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func main() {
	port := getEnv("RELAY_PORT", "8090")
	segmentDir := getEnv("SEGMENT_DIR", "/var/roost/segments")
	postgresURL := getEnv("POSTGRES_URL", "postgres://roost:roost@localhost:5433/roost_dev?sslmode=disable")
	maxStreams := 2

	log.Printf("[relay] starting on port %s, segments at %s", port, segmentDir)

	db, err := sql.Open("postgres", postgresURL)
	if err != nil {
		log.Fatalf("[relay] db open: %v", err)
	}
	defer db.Close()
	if err := db.Ping(); err != nil {
		log.Fatalf("[relay] db ping: %v", err)
	}

	validator := relayauth.NewValidator(db, nil)
	sessMgr := sessions.NewManager(db, maxStreams)

	mux := http.NewServeMux()

	// CORS middleware wrapper
	cors := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if isAllowedOrigin(origin) {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
			}
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}

	// Health — no auth
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"status":"ok","service":"roost-relay"}`)
	})

	// Passthrough playlist — also serves stream_0.m3u8 for transcoded channels
	mux.Handle("GET /stream/{slug}/stream.m3u8",
		cors(validator.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			slug := r.PathValue("slug")
			sub := relayauth.SubscriberFromContext(r.Context())
			deviceID := deviceIDFrom(r)

			_, err := sessMgr.OnPlaylistRequest(r.Context(), sub.ID, slug, deviceID)
			if err != nil {
				http.Error(w, `{"error":"concurrent stream limit reached"}`, http.StatusTooManyRequests)
				return
			}

			// For passthrough channels: serve stream.m3u8
			// For transcoded channels (which have master.m3u8): serve stream_0.m3u8 as the default quality
			path := filepath.Join(segmentDir, slug, "stream.m3u8")
			if !fileExists(path) {
				// Try first variant (transcoded channel)
				path = filepath.Join(segmentDir, slug, "stream_0.m3u8")
			}
			servefile(w, path, "application/vnd.apple.mpegurl", "no-cache")
		}))))

	// Adaptive bitrate master playlist
	mux.Handle("GET /stream/{slug}/master.m3u8",
		cors(validator.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			slug := r.PathValue("slug")
			servefile(w, filepath.Join(segmentDir, slug, "master.m3u8"), "application/vnd.apple.mpegurl", "no-cache")
		}))))

	// AES key endpoint
	mux.Handle("GET /stream/{slug}/key",
		cors(validator.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			slug := r.PathValue("slug")
			keyPath := filepath.Join(segmentDir, slug, "enc.key")
			data, err := os.ReadFile(keyPath)
			if err != nil {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("Cache-Control", "no-store")
			w.Write(data)
		}))))

	// Segment + variant playlist endpoint
	mux.Handle("GET /stream/{slug}/{segment}",
		cors(validator.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			slug := r.PathValue("slug")
			segment := r.PathValue("segment")
			sub := relayauth.SubscriberFromContext(r.Context())
			deviceID := deviceIDFrom(r)

			// Prevent path traversal
			if strings.Contains(segment, "..") || strings.Contains(segment, "/") {
				w.WriteHeader(http.StatusBadRequest)
				return
			}

			segPath := filepath.Join(segmentDir, slug, segment)

			// Variant playlists (.m3u8) — no bytes tracking, just serve
			if strings.HasSuffix(segment, ".m3u8") {
				servefile(w, segPath, "application/vnd.apple.mpegurl", "no-cache")
				return
			}

			// .ts segments — track bytes served
			fi, err := os.Stat(segPath)
			if err != nil {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			go sessMgr.OnSegmentRequest(sub.ID, slug, deviceID, fi.Size())

			servefile(w, segPath, "video/MP2T", "max-age=60")
		}))))

	srv := &http.Server{
		Addr:         ":" + port,
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
	}

	log.Printf("[relay] listening on :%s", port)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("[relay] server: %v", err)
	}
}

// servefile serves a static file with given content type and cache control.
func servefile(w http.ResponseWriter, path, contentType, cacheControl string) {
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
	w.Header().Set("Cache-Control", cacheControl)
	io.Copy(w, f)
}

// fileExists returns true if the path exists and is a regular file.
func fileExists(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.Mode().IsRegular()
}

// deviceIDFrom extracts the device_id query param, defaulting to "default".
func deviceIDFrom(r *http.Request) string {
	if d := r.URL.Query().Get("device_id"); d != "" {
		return d
	}
	return "default"
}

// isAllowedOrigin checks if the origin is permitted for CORS.
func isAllowedOrigin(origin string) bool {
	allowed := []string{
		"https://owl.yourflock.com",
		"http://localhost",
		"http://localhost:3000",
		"http://localhost:5173",
	}
	for _, a := range allowed {
		if origin == a || strings.HasPrefix(origin, a) {
			return true
		}
	}
	return false
}
