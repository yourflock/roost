// main.go — Roost Owl Addon API Service.
// Implements the Community Addon API contract that Owl clients call to discover
// Roost, authenticate subscribers, fetch live channels, EPG, and stream URLs.
// Port: 8091 (env: OWL_API_PORT). Proxied via Nginx behind Cloudflare.
//
// All endpoints are under /owl/* to match the API contract in
// .docs/api/ROOST_OWL_API_CONTRACT.md
//
// Public routes (no auth):
//   GET  /owl/manifest.json  — addon discovery document for Owl
//   GET  /owl/version        — version info
//   GET  /health             — service liveness check
//
// Subscriber routes (require API token → POST /owl/auth first):
//   POST /owl/auth           — validate API token, issue 4-hour session token
//
// Session routes (require Authorization: Bearer {session_token}):
//   GET  /owl/live                  — live channel list (filtered, no source_url)
//   GET  /owl/epg                   — EPG programs for a date window
//   GET  /owl/epg/upcoming          — next N programs per channel
//   POST /owl/stream/:slug          — get signed HLS stream URL for a channel
//   GET  /owl/vod                   — VOD catalog (movies + series)
//   GET  /owl/vod/:id               — content details + stream URL + watch progress
//   GET  /owl/catchup/:channel_slug — list available catchup hours
//   GET  /owl/catchup/:slug/stream  — catchup time-range stream URL
//   GET  /owl/recommendations       — personalized content recommendations
//
// Internal (no external exposure):
//   GET  /internal/sessions/cleanup — prune expired owl_sessions rows
package main

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	_ "github.com/lib/pq"
	goredis "github.com/redis/go-redis/v9"

	rootauth "github.com/yourflock/roost/internal/auth"
	"github.com/yourflock/roost/services/owl_api/audit"
	"github.com/yourflock/roost/services/owl_api/handlers"
	"github.com/yourflock/roost/services/owl_api/middleware"
)

// ---- configuration ----------------------------------------------------------

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func connectDB() (*sql.DB, error) {
	dsn := getEnv("POSTGRES_URL", "postgres://roost:roost@localhost:5433/roost_dev?sslmode=disable")
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(15)
	db.SetMaxIdleConns(5)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return db, db.PingContext(ctx)
}

// ---- response helpers -------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]string{
		"error":      code,
		"message":    msg,
		"request_id": newRequestID(),
	})
}

func newRequestID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// ---- path helpers -----------------------------------------------------------

// pathSegment returns the n-th segment of a URL path (0-indexed after splitting on "/").
// e.g. "/owl/stream/espn" with n=2 → "espn"
func pathSegment(path string, n int) string {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if n >= len(parts) {
		return ""
	}
	return parts[n]
}

// ---- session token management -----------------------------------------------

// sessionRecord is stored in the owl_sessions table.
type sessionRecord struct {
	SubscriberID string
	Plan         string
	DisplayName  string
	MaxStreams    int
	Features     []string
	Region       string
}

// createOwlSession generates a new session token, persists it in DB, and returns it.
// TTL: 4 hours. The session_token is a random UUID — not hashed (short-lived, low risk).
func createOwlSession(ctx context.Context, db *sql.DB, subscriberID, deviceID, platform, clientVersion string) (string, time.Time, error) {
	token := uuid.New().String()
	expiresAt := time.Now().UTC().Add(4 * time.Hour)

	_, err := db.ExecContext(ctx, `
		INSERT INTO owl_sessions (subscriber_id, session_token, device_id, platform, client_version, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6)
	`, subscriberID, token, deviceID, platform, clientVersion, expiresAt)
	if err != nil {
		return "", time.Time{}, err
	}
	return token, expiresAt, nil
}

// validateSession looks up a session token and returns the subscriber_id if valid.
// Expired sessions return sql.ErrNoRows.
func validateSession(ctx context.Context, db *sql.DB, sessionToken string) (string, error) {
	var subscriberID string
	err := db.QueryRowContext(ctx, `
		SELECT subscriber_id FROM owl_sessions
		WHERE session_token = $1 AND expires_at > NOW()
	`, sessionToken).Scan(&subscriberID)
	if err != nil {
		return "", err
	}
	// Touch last_used_at asynchronously
	go db.Exec(`UPDATE owl_sessions SET last_used_at = NOW() WHERE session_token = $1`, sessionToken)
	return subscriberID, nil
}

// planLimits returns (maxConcurrentStreams, features) for a subscriber plan.
func planLimits(plan string) (int, []string) {
	switch strings.ToLower(plan) {
	case "premium":
		return 4, []string{"live", "epg", "vod"}
	case "founding":
		return 4, []string{"live", "epg", "vod"}
	default: // "standard", "basic", anything else
		return 2, []string{"live", "epg"}
	}
}

// ---- stream URL signing -----------------------------------------------------

// signedStreamURL generates a Cloudflare-compatible signed HLS URL.
// For local dev (no CF_SIGNING_KEY), returns a relay URL stub.
func signedStreamURL(channelSlug string) (string, time.Time) {
	baseURL := getEnv("RELAY_BASE_URL", "https://cdn.roost.yourflock.org")
	expiresAt := time.Now().UTC().Add(15 * time.Minute)

	signingKey := os.Getenv("CF_STREAM_SIGNING_KEY")
	if signingKey == "" {
		// Dev mode — return unsigned URL with expiry hint
		return fmt.Sprintf("%s/hls/%s/playlist.m3u8?expires=%d", baseURL, channelSlug, expiresAt.Unix()), expiresAt
	}

	// HMAC-SHA256 sign: "{channelSlug}:{expiresUnix}"
	payload := fmt.Sprintf("%s:%d", channelSlug, expiresAt.Unix())
	mac := hmac.New(sha256.New, []byte(signingKey))
	mac.Write([]byte(payload))
	sig := hex.EncodeToString(mac.Sum(nil))

	url := fmt.Sprintf("%s/hls/%s/playlist.m3u8?expires=%d&sig=%s",
		baseURL, channelSlug, expiresAt.Unix(), sig)
	return url, expiresAt
}

// ---- server -----------------------------------------------------------------

type server struct {
	db           *sql.DB
	port         string
	rl           *rateLimiter // rate limiter (nil = disabled in dev/test)
	adminH       *handlers.AdminHandlers
	auditLog     *audit.Logger
}

func newServer(db *sql.DB, rdb *goredis.Client) *server {
	dataDir := getEnv("ROOST_DATA_DIR", "/var/lib/roost")
	version := getEnv("ROOST_VERSION", "dev")
	al := audit.New(db)
	var rl *rateLimiter
	if rdb != nil {
		// Wire Redis-backed rate limiter; the rateLimiter adapter implements RateLimitStore.
		rl = newRateLimiter(&goRedisRateLimitAdapter{c: rdb})
	} else {
		rl = newRateLimiter(nil)
	}
	return &server{
		db:       db,
		port:     getEnv("OWL_API_PORT", "8091"),
		rl:       rl,
		adminH:   handlers.NewAdminHandlersWithRedis(db, rdb, dataDir, version),
		auditLog: al,
	}
}

// setRateLimitStore wires a Redis-backed store for rate limiting.
// Called from main() after Redis is connected. If not called, rate limiting is disabled.
func (s *server) setRateLimitStore(store RateLimitStore) {
	s.rl = newRateLimiter(store)
}

func (s *server) routes() *http.ServeMux {
	mux := http.NewServeMux()

	// Public — no auth
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/owl/manifest.json", s.handleManifest)
	mux.HandleFunc("/owl/version", s.handleVersion)

	// Subscriber auth — API token in body
	mux.HandleFunc("/owl/auth", s.handleAuth)
	mux.HandleFunc("/owl/v1/auth", s.handleAuth) // v1 prefix alias

	// Session-authenticated routes
	mux.HandleFunc("/owl/live", s.requireSession(s.handleLive))
	mux.HandleFunc("/owl/v1/live", s.requireSession(s.handleLive))
	mux.HandleFunc("/owl/epg", s.requireSession(s.handleEPG))
	mux.HandleFunc("/owl/v1/epg", s.requireSession(s.handleEPG))
	mux.HandleFunc("/owl/epg/upcoming", s.requireSession(s.handleEPGUpcoming))
	mux.HandleFunc("/owl/v1/epg/upcoming", s.requireSession(s.handleEPGUpcoming))
	mux.HandleFunc("/owl/vod", s.requireSession(s.handleVOD))
	mux.HandleFunc("/owl/v1/vod", s.requireSession(s.handleVOD))
	mux.HandleFunc("/owl/vod/", s.requireSession(s.handleVODItem))
	mux.HandleFunc("/owl/v1/vod/", s.requireSession(s.handleVODItem))
	mux.HandleFunc("/owl/catchup/", s.requireSession(s.handleCatchup))
	mux.HandleFunc("/owl/v1/catchup/", s.requireSession(s.handleCatchup))
	mux.HandleFunc("/owl/recommendations", s.requireSession(s.handleRecommendations))
	mux.HandleFunc("/owl/v1/recommendations", s.requireSession(s.handleRecommendations))

	// DVR — cloud recording management (P11-T05-S02)
	mux.HandleFunc("/owl/dvr", s.requireSession(s.handleDVR))
	mux.HandleFunc("/owl/v1/dvr", s.requireSession(s.handleDVR))
	mux.HandleFunc("/owl/dvr/quota", s.requireSession(s.handleDVRQuota))
	mux.HandleFunc("/owl/v1/dvr/quota", s.requireSession(s.handleDVRQuota))
	mux.HandleFunc("/owl/dvr/", s.requireSession(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/play") {
			s.handleDVRPlay(w, r)
		} else {
			s.handleDVRItem(w, r)
		}
	}))
	mux.HandleFunc("/owl/v1/dvr/", s.requireSession(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/play") {
			s.handleDVRPlay(w, r)
		} else {
			s.handleDVRItem(w, r)
		}
	}))

	// Stream URL — POST /owl/stream/{slug} or /owl/v1/stream/{slug}
	// Rate-limited: 100 req/min per session token + concurrent stream limit
	mux.HandleFunc("/owl/stream/", s.rl.apiRateLimit(s.requireSession(s.rl.streamRateLimit(2, s.handleStream))))
	mux.HandleFunc("/owl/v1/stream/", s.rl.apiRateLimit(s.requireSession(s.rl.streamRateLimit(2, s.handleStream))))

	// M3U8 playlist — GET /owl/playlist.m3u8?token=SESSION_TOKEN
	mux.HandleFunc("/owl/playlist.m3u8", s.requireSession(s.handlePlaylistM3U8))
	mux.HandleFunc("/owl/v1/playlist.m3u8", s.requireSession(s.handlePlaylistM3U8))

	// Xtream Codes compatibility layer
	// GET /player_api.php?username=X&password=Y&action=Z
	mux.HandleFunc("/player_api.php", s.handlePlayerAPI)
	// GET /live/:username/:password/:stream_id.m3u8  (Xtream stream redirect)
	mux.HandleFunc("/live/", s.handleXtreamStream)

	// Internal maintenance (firewall-restricted)
	mux.HandleFunc("/internal/sessions/cleanup", s.handleSessionCleanup)


	// ── Flock integration: P13-T03, P13-T05, P13-T06 ──────────────────────────
	// GET  /owl/tokens             — screen time token balance for current session
	// GET  /owl/watch-party/{id}   — watch party status for Owl clients
	// POST /owl/share              — share content to Flock activity feed
	mux.HandleFunc("/owl/tokens", s.requireSession(s.handleFlockTokens))
	mux.HandleFunc("/owl/v1/tokens", s.requireSession(s.handleFlockTokens))
	mux.HandleFunc("/owl/watch-party/", s.requireSession(s.handleOwlWatchParty))
	mux.HandleFunc("/owl/v1/watch-party/", s.requireSession(s.handleOwlWatchParty))
	mux.HandleFunc("/owl/share", s.requireSession(s.handleOwlShare))
	mux.HandleFunc("/owl/v1/share", s.requireSession(s.handleOwlShare))


	// ── Sports Intelligence: P15-T04 ─────────────────────────────────────────
	// GET  /owl/sports/events          — upcoming/live games for subscriber's fav teams
	// GET  /owl/sports/live            — currently live games on accessible channels
	// GET  /owl/sports/teams           — subscriber's favourite teams
	// POST /owl/sports/teams/:id/favorite — add favourite team
	// DELETE /owl/sports/teams/:id/favorite — remove favourite team
	mux.HandleFunc("/owl/sports/events", s.requireSession(s.handleSportsEvents))
	mux.HandleFunc("/owl/v1/sports/events", s.requireSession(s.handleSportsEvents))
	mux.HandleFunc("/owl/sports/live", s.requireSession(s.handleSportsLive))
	mux.HandleFunc("/owl/v1/sports/live", s.requireSession(s.handleSportsLive))
	mux.HandleFunc("/owl/sports/teams", s.requireSession(s.handleGetSportsTeams))
	mux.HandleFunc("/owl/v1/sports/teams", s.requireSession(s.handleGetSportsTeams))
	mux.HandleFunc("/owl/sports/teams/", s.requireSession(s.handleSportsTeamsFavorite))
	mux.HandleFunc("/owl/v1/sports/teams/", s.requireSession(s.handleSportsTeamsFavorite))

	// ── Admin routes (role=admin|owner JWT required) ───────────────────────────
	// All /admin/* routes are gated by RequireAdmin middleware which validates the
	// JWT role claim. No DB calls in the middleware — pure token inspection.
	jwtSecret := []byte(getEnv("JWT_SECRET", "dev-jwt-secret-change-in-production"))

	s.registerAdminRoutes(mux, jwtSecret)

	return mux
}

// registerAdminRoutes wires all /admin/* endpoints onto mux.
// Every route is wrapped with RequireAdmin middleware — a 403 is returned
// for any request with a JWT that doesn't carry role=admin or role=owner.
func (s *server) registerAdminRoutes(mux *http.ServeMux, jwtSecret []byte) {
	h := s.adminH
	al := s.auditLog

	// Convenience: wrap a handler with RequireAdmin
	admin := func(next http.Handler) http.HandlerFunc {
		return middleware.RequireAdmin(jwtSecret, next).ServeHTTP
	}

	// ── Server overview ───────────────────────────────────────────────────────
	// GET  /admin/status       — cpu, ram, disk, active streams, version, uptime
	// GET  /admin/health       — per-service health (postgres, redis, minio) + antboxes
	// GET  /admin/metrics      — raw numeric metrics (prometheus-like snapshot)
	mux.HandleFunc("/admin/status",  admin(http.HandlerFunc(h.Status)))
	mux.HandleFunc("/admin/health",  admin(http.HandlerFunc(h.Health)))
	mux.HandleFunc("/admin/metrics", admin(http.HandlerFunc(h.Metrics)))

	// ── Active streams ────────────────────────────────────────────────────────
	// GET    /admin/streams       — list active streams (from Redis when wired)
	// DELETE /admin/streams/:id   — kill stream (audit logged)
	mux.HandleFunc("/admin/streams", admin(http.HandlerFunc(h.ListActiveStreams)))
	mux.HandleFunc("/admin/streams/", admin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			h.KillStream(w, r, al)
		} else {
			http.NotFound(w, r)
		}
	})))

	// ── User management ───────────────────────────────────────────────────────
	// GET    /admin/users              — list all users for this roost
	// POST   /admin/users/invite       — invite a Flock user
	// PATCH  /admin/users/:id/role     — change role (can't change owner)
	// DELETE /admin/users/:id          — remove user (can't remove owner)
	mux.HandleFunc("/admin/users", admin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			h.ListUsers(w, r)
		} else {
			http.NotFound(w, r)
		}
	})))
	mux.HandleFunc("/admin/users/invite", admin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			h.InviteUser(w, r, al)
		} else {
			http.NotFound(w, r)
		}
	})))
	mux.HandleFunc("/admin/users/", admin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/role") && r.Method == http.MethodPatch {
			h.PatchUserRole(w, r, al)
		} else if r.Method == http.MethodDelete {
			h.DeleteUser(w, r, al)
		} else {
			http.NotFound(w, r)
		}
	})))

	// ── Storage management ────────────────────────────────────────────────────
	// GET    /admin/storage                  — list configured storage paths
	// POST   /admin/storage/add              — add storage path
	// DELETE /admin/storage/:id              — remove storage path (409 if has content)
	// POST   /admin/storage/scan             — trigger content scan job
	// GET    /admin/storage/scan/status      — scan job status
	// GET    /admin/storage/scan/stream      — SSE scan progress
	// GET    /admin/storage/duplicates       — list duplicate content groups
	// DELETE /admin/storage/duplicates       — purge all non-keeper duplicates
	mux.HandleFunc("/admin/storage",             admin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet { h.ListStoragePaths(w, r) } else { http.NotFound(w, r) }
	})))
	mux.HandleFunc("/admin/storage/add",         admin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost { h.AddStoragePath(w, r, al) } else { http.NotFound(w, r) }
	})))
	mux.HandleFunc("/admin/storage/scan",        admin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost { h.TriggerScan(w, r, al) } else { http.NotFound(w, r) }
	})))
	mux.HandleFunc("/admin/storage/scan/status", admin(http.HandlerFunc(h.ScanStatus)))
	mux.HandleFunc("/admin/storage/scan/stream", admin(http.HandlerFunc(h.StorageScanStream)))
	mux.HandleFunc("/admin/storage/duplicates",  admin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:    h.ListDuplicates(w, r)
		case http.MethodDelete: h.PurgeDuplicates(w, r, al)
		default:                http.NotFound(w, r)
		}
	})))
	mux.HandleFunc("/admin/storage/",            admin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete { h.RemoveStoragePath(w, r, al) } else { http.NotFound(w, r) }
	})))

	// ── IPTV source management ────────────────────────────────────────────────
	// GET    /admin/iptv-sources              — list IPTV sources (credentials masked)
	// POST   /admin/iptv-sources              — add source (m3u, xtream, stalker)
	// POST   /admin/iptv-sources/:id/refresh  — trigger channel-list refresh
	// DELETE /admin/iptv-sources/:id          — remove source
	mux.HandleFunc("/admin/iptv-sources", admin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:  h.ListIPTVSources(w, r)
		case http.MethodPost: h.AddIPTVSource(w, r, al)
		default:              http.NotFound(w, r)
		}
	})))
	mux.HandleFunc("/admin/iptv-sources/", admin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/refresh") && r.Method == http.MethodPost {
			h.RefreshIPTVSource(w, r, al)
		} else if r.Method == http.MethodDelete {
			h.DeleteIPTVSource(w, r, al)
		} else {
			http.NotFound(w, r)
		}
	})))

	// ── AntBox device management ──────────────────────────────────────────────
	// GET    /admin/antboxes              — list AntBox devices with status
	// PATCH  /admin/antboxes/:id          — update display name / location
	// DELETE /admin/antboxes/:id          — soft-remove device
	// POST   /admin/antboxes/scan-channels — trigger OTA channel scan
	// GET    /admin/antboxes/:id/signal   — read signal strength from device
	mux.HandleFunc("/admin/antboxes", admin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet { h.ListAntBoxes(w, r) } else { http.NotFound(w, r) }
	})))
	mux.HandleFunc("/admin/antboxes/scan-channels", admin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost { h.TriggerAntBoxChannelScan(w, r, al) } else { http.NotFound(w, r) }
	})))
	mux.HandleFunc("/admin/antboxes/", admin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/signal") {
			h.AntBoxSignal(w, r)
		} else if r.Method == http.MethodPatch {
			h.PatchAntBox(w, r, al)
		} else if r.Method == http.MethodDelete {
			h.DeleteAntBox(w, r, al)
		} else {
			http.NotFound(w, r)
		}
	})))

	// ── Community addon management ────────────────────────────────────────────
	// GET    /admin/addons              — list installed addons
	// POST   /admin/addons/install      — install addon from manifest URL
	// DELETE /admin/addons/:id          — uninstall addon
	// POST   /admin/addons/:id/refresh  — re-fetch manifest + catalog
	mux.HandleFunc("/admin/addons", admin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet { h.ListAddons(w, r) } else { http.NotFound(w, r) }
	})))
	mux.HandleFunc("/admin/addons/install", admin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost { h.InstallAddon(w, r, al) } else { http.NotFound(w, r) }
	})))
	mux.HandleFunc("/admin/addons/", admin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/refresh") && r.Method == http.MethodPost {
			h.RefreshAddon(w, r, al)
		} else if r.Method == http.MethodDelete {
			h.UninstallAddon(w, r, al)
		} else {
			http.NotFound(w, r)
		}
	})))

	// ── DVR schedule management ───────────────────────────────────────────────
	// GET    /admin/dvr/schedule         — upcoming scheduled recordings
	// POST   /admin/dvr/schedule         — schedule a new recording
	// DELETE /admin/dvr/schedule/:id     — cancel a scheduled recording
	// GET    /admin/dvr/recordings       — completed/failed recordings
	// DELETE /admin/dvr/recordings/:id   — delete a recording
	// GET    /admin/dvr/conflicts        — overlapping recording pairs
	// POST   /admin/dvr/series           — create series recording rule
	mux.HandleFunc("/admin/dvr/schedule", admin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:  h.ListDVRSchedule(w, r)
		case http.MethodPost: h.CreateDVRSchedule(w, r, al)
		default:              http.NotFound(w, r)
		}
	})))
	mux.HandleFunc("/admin/dvr/schedule/", admin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete { h.CancelDVRSchedule(w, r, al) } else { http.NotFound(w, r) }
	})))
	mux.HandleFunc("/admin/dvr/recordings", admin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet { h.ListDVRRecordings(w, r) } else { http.NotFound(w, r) }
	})))
	mux.HandleFunc("/admin/dvr/recordings/", admin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete { h.DeleteDVRRecording(w, r, al) } else { http.NotFound(w, r) }
	})))
	mux.HandleFunc("/admin/dvr/conflicts", admin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet { h.ListDVRConflicts(w, r) } else { http.NotFound(w, r) }
	})))
	mux.HandleFunc("/admin/dvr/series", admin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost { h.CreateSeriesRule(w, r, al) } else { http.NotFound(w, r) }
	})))

	// ── Audit log ─────────────────────────────────────────────────────────────
	// GET /admin/audit — paginated audit log (newest-first)
	//   ?limit=N      — max rows (default 100, max 500)
	//   ?action=X     — filter by action prefix (e.g. "storage.")
	//   ?since=T      — only return rows after ISO-8601 timestamp T
	mux.HandleFunc("/admin/audit", admin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet { h.ListAuditLog(w, r) } else { http.NotFound(w, r) }
	})))

	// ── System logs ───────────────────────────────────────────────────────────
	// GET /admin/logs        — paginated application log entries
	//   ?limit=N             — max rows (default 100, max 500)
	//   ?level=warn          — filter by log level (debug/info/warn/error)
	//   ?service=X           — filter by service name
	// GET /admin/logs/stream — SSE stream of live log events (30-min max)
	mux.HandleFunc("/admin/logs", admin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet { h.GetLogs(w, r) } else { http.NotFound(w, r) }
	})))
	mux.HandleFunc("/admin/logs/stream", admin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet { h.LogStream(w, r) } else { http.NotFound(w, r) }
	})))

	// ── Updates ───────────────────────────────────────────────────────────────
	// GET  /admin/updates       — check latest version vs GitHub releases
	// POST /admin/updates/apply — download + swap binary (owner only)
	// POST /admin/restart       — graceful restart via Redis signal (owner only)
	mux.HandleFunc("/admin/updates", admin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet { h.UpdateCheck(w, r) } else { http.NotFound(w, r) }
	})))
	mux.HandleFunc("/admin/updates/apply", admin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost { h.ApplyUpdate(w, r, al) } else { http.NotFound(w, r) }
	})))
	mux.HandleFunc("/admin/restart", admin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost { h.Restart(w, r, al) } else { http.NotFound(w, r) }
	})))
}

// requireSession wraps a handler with session token validation.
// Accepts token via Authorization: Bearer header OR ?token= query param.
func (s *server) requireSession(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Extract token: Authorization header preferred, query param fallback
		token := ""
		if h := r.Header.Get("Authorization"); h != "" {
			parts := strings.SplitN(h, " ", 2)
			if len(parts) == 2 && strings.EqualFold(parts[0], "bearer") {
				token = strings.TrimSpace(parts[1])
			}
		}
		if token == "" {
			token = r.URL.Query().Get("token")
		}
		if token == "" {
			writeError(w, http.StatusUnauthorized, "missing_token", "Authorization header or ?token= required")
			return
		}

		// Validate session in DB
		subscriberID, err := validateSession(r.Context(), s.db, token)
		if err == sql.ErrNoRows {
			writeError(w, http.StatusUnauthorized, "invalid_session", "Session expired or invalid. Call POST /owl/auth to re-authenticate.")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Session validation failed")
			return
		}

		// Inject subscriber_id into request context via header for downstream use
		r.Header.Set("X-Subscriber-ID", subscriberID)
		next(w, r)
	}
}

// ---- handler: GET /health ---------------------------------------------------

func (s *server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET required")
		return
	}

	dbOK := true
	if err := s.db.PingContext(r.Context()); err != nil {
		dbOK = false
	}

	status := "ok"
	code := http.StatusOK
	if !dbOK {
		status = "degraded"
		code = http.StatusServiceUnavailable
	}

	writeJSON(w, code, map[string]interface{}{
		"status":  status,
		"service": "roost-owl-api",
		"db":      dbOK,
		"time":    time.Now().UTC().Format(time.RFC3339),
	})
}

// ---- handler: GET /owl/manifest.json ----------------------------------------

func (s *server) handleManifest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET required")
		return
	}

	baseURL := getEnv("ROOST_BASE_URL", "https://roost.yourflock.org")

	manifest := map[string]interface{}{
		"name":        "Roost",
		"description": "Licensed live TV, sports, and VOD for Owl",
		"icon":        baseURL + "/static/roost-icon.png",
		"version":     "1.0",
		"api_version": "v1",
		"features":    []string{"live", "epg", "vod", "catchup", "recommendations"},
		"auth_url":    "/owl/auth",
		"endpoints": map[string]string{
			"live":            "/owl/v1/live",
			"epg":             "/owl/v1/epg",
			"stream":          "/owl/v1/stream/{channel_id}",
			"vod":             "/owl/v1/vod",
			"vod_item":        "/owl/v1/vod/{id}",
			"catchup":         "/owl/v1/catchup/{channel_slug}",
			"recommendations": "/owl/v1/recommendations",
		},
		"support_url": baseURL + "/support",
		"privacy_url": baseURL + "/privacy",
	}

	// Set cache headers — Owl should cache manifest for 24h
	w.Header().Set("Cache-Control", "public, max-age=86400")
	writeJSON(w, http.StatusOK, manifest)
}

// ---- handler: GET /owl/version ----------------------------------------------

func (s *server) handleVersion(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET required")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"version":     "1.0.0",
		"api_version": "v1",
		"service":     "roost-owl-api",
	})
}

// ---- handler: POST /owl/auth ------------------------------------------------

type authRequest struct {
	Token  string `json:"token"`
	Client struct {
		Platform  string `json:"platform"`
		Version   string `json:"version"`
		DeviceID  string `json:"device_id"`
	} `json:"client"`
}

func (s *server) handleAuth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST required")
		return
	}

	var req authRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "Request body must be valid JSON")
		return
	}
	if req.Token == "" {
		writeError(w, http.StatusBadRequest, "missing_token", "token field required")
		return
	}

	// Validate API token using the internal auth package
	sub, err := rootauth.ValidateAPIToken(r.Context(), s.db, req.Token)
	if err == rootauth.ErrTokenInvalid {
		writeJSON(w, http.StatusUnauthorized, map[string]interface{}{
			"valid":   false,
			"error":   "invalid_token",
			"message": "Token not found or expired. Visit roost.yourflock.org to manage your subscription.",
		})
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Token validation failed")
		return
	}

	// Check subscriber status
	if sub.Status != "active" {
		writeJSON(w, http.StatusPaymentRequired, map[string]interface{}{
			"valid":       false,
			"error":       "subscription_inactive",
			"message":     "Your Roost subscription is inactive. Visit roost.yourflock.org/billing to renew.",
			"billing_url": getEnv("ROOST_BASE_URL", "https://roost.yourflock.org") + "/billing",
		})
		return
	}

	// Look up subscriber plan details
	var plan, displayName, region string
	err = s.db.QueryRowContext(r.Context(), `
		SELECT coalesce(s.plan_slug, 'standard'), coalesce(s.display_name, s.email), coalesce(s.region, 'us')
		FROM subscribers s
		WHERE s.id = $1
	`, sub.ID.String()).Scan(&plan, &displayName, &region)
	if err != nil {
		// Fallback if query fails — use what we have from token
		plan = "standard"
		displayName = sub.DisplayName
		region = "us"
	}

	maxStreams, features := planLimits(plan)

	// Create session token (4-hour TTL, stored in DB)
	sessionToken, expiresAt, err := createOwlSession(
		r.Context(), s.db,
		sub.ID.String(),
		req.Client.DeviceID,
		req.Client.Platform,
		req.Client.Version,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "session_error", "Failed to create session")
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"valid":         true,
		"session_token": sessionToken,
		"expires_at":    expiresAt.Format(time.RFC3339),
		"subscriber": map[string]interface{}{
			"id":                    sub.ID.String(),
			"plan":                  plan,
			"max_concurrent_streams": maxStreams,
			"features":              features,
			"region":                region,
			"display_name":          displayName,
		},
	})
}

// ---- handler: GET /owl/live -------------------------------------------------

func (s *server) handleLive(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET required")
		return
	}

	category := r.URL.Query().Get("category")
	region := r.URL.Query().Get("region")

	// Build query — never expose stream_url (source) to Owl clients
	args := []interface{}{}
	whereClauses := []string{"c.is_active = true"}

	if category != "" {
		args = append(args, category)
		whereClauses = append(whereClauses, fmt.Sprintf("c.category = $%d", len(args)))
	}
	if region != "" {
		args = append(args, region)
		whereClauses = append(whereClauses, fmt.Sprintf("c.country_code = $%d", len(args)))
	}

	// Region-based channel filtering (P14-T02):
	// If the subscriber has region_id set in subscribers table, filter channels via channel_regions.
	// Gracefully degrades if migration 022 has not been applied.
	subscriberID := r.Header.Get("X-Subscriber-ID")
	_, regionFilterSQL, regionArgs := subscriberRegionFilter(s.db, subscriberID, len(args))
	if regionFilterSQL != "" {
		args = append(args, regionArgs...)
		// regionFilterSQL starts with " AND c.id IN (...)" — append directly to where string after join
		whereClauses = append(whereClauses, "1=1") // placeholder so we can append regionFilterSQL after join
	}

	where := strings.Join(whereClauses, " AND ")
	if regionFilterSQL != "" {
		where += regionFilterSQL
	}
	query := fmt.Sprintf(`
		SELECT c.id, c.slug, c.name, c.category, c.logo_url, c.country_code, c.language_code,
		       c.epg_channel_id, c.sort_order,
		       p.title, p.start_time, p.end_time
		FROM channels c
		LEFT JOIN LATERAL (
			SELECT title, start_time, end_time
			FROM epg_programs ep
			WHERE ep.channel_id = c.id
			  AND ep.start_time <= NOW()
			  AND ep.end_time > NOW()
			ORDER BY start_time DESC
			LIMIT 1
		) p ON true
		WHERE %s
		ORDER BY c.sort_order ASC, c.name ASC
	`, where)

	rows, err := s.db.QueryContext(r.Context(), query, args...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query_error", "Failed to fetch channels")
		return
	}
	defer rows.Close()

	type currentProgram struct {
		Title   string `json:"title"`
		StartAt string `json:"start_at"`
		EndAt   string `json:"end_at"`
	}
	type channel struct {
		ID             string          `json:"id"`
		Slug           string          `json:"slug"`
		Name           string          `json:"name"`
		Category       string          `json:"category"`
		Logo           string          `json:"logo"`
		Region         string          `json:"region"`
		Language       string          `json:"language"`
		IsLive         bool            `json:"is_live"`
		StreamURL      string          `json:"stream_url"` // relay URL — NOT source URL
		CurrentProgram *currentProgram `json:"current_program,omitempty"`
	}

	baseURL := getEnv("ROOST_BASE_URL", "https://roost.yourflock.org")
	var channels []channel
	total := 0

	for rows.Next() {
		var id, slug, name string
		var cat, logo, country, lang, epgID sql.NullString
		var sortOrder int
		var progTitle, progStart, progEnd sql.NullString

		if err := rows.Scan(&id, &slug, &name, &cat, &logo, &country, &lang, &epgID, &sortOrder,
			&progTitle, &progStart, &progEnd); err != nil {
			continue
		}

		ch := channel{
			ID:        id,
			Slug:      slug,
			Name:      name,
			Category:  cat.String,
			Logo:      logo.String,
			Region:    country.String,
			Language:  lang.String,
			IsLive:    true,
			StreamURL: fmt.Sprintf("%s/owl/v1/stream/%s", baseURL, slug),
		}

		if progTitle.Valid {
			ch.CurrentProgram = &currentProgram{
				Title:   progTitle.String,
				StartAt: progStart.String,
				EndAt:   progEnd.String,
			}
		}

		channels = append(channels, ch)
		total++
	}

	if channels == nil {
		channels = []channel{}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"channels":   channels,
		"total":      total,
		"updated_at": time.Now().UTC().Format(time.RFC3339),
	})
}

// ---- handler: GET /owl/epg --------------------------------------------------

func (s *server) handleEPG(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET required")
		return
	}

	fromStr := r.URL.Query().Get("from")
	toStr := r.URL.Query().Get("to")
	channelFilter := r.URL.Query().Get("channel_id") // comma-separated slugs

	if fromStr == "" || toStr == "" {
		writeError(w, http.StatusBadRequest, "missing_params", "from and to query params required (ISO 8601)")
		return
	}

	from, err := time.Parse(time.RFC3339, fromStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_from", "from must be ISO 8601 datetime")
		return
	}
	to, err := time.Parse(time.RFC3339, toStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_to", "to must be ISO 8601 datetime")
		return
	}

	// Enforce max 7-day window
	if to.Sub(from) > 7*24*time.Hour {
		writeError(w, http.StatusBadRequest, "range_too_large", "EPG window cannot exceed 7 days")
		return
	}

	// Build query
	args := []interface{}{from, to}
	whereClauses := []string{"ep.start_time >= $1", "ep.end_time <= $2", "c.is_active = true"}

	if channelFilter != "" {
		slugs := strings.Split(channelFilter, ",")
		placeholders := make([]string, len(slugs))
		for i, sl := range slugs {
			args = append(args, strings.TrimSpace(sl))
			placeholders[i] = fmt.Sprintf("$%d", len(args))
		}
		whereClauses = append(whereClauses, fmt.Sprintf("c.slug IN (%s)", strings.Join(placeholders, ",")))
	}

	where := strings.Join(whereClauses, " AND ")
	query := fmt.Sprintf(`
		SELECT c.slug, ep.id, ep.title, coalesce(ep.description,''),
		       ep.start_time, ep.end_time,
		       coalesce(ep.category,''), coalesce(ep.rating,''),
		       ep.is_live, ep.is_new
		FROM epg_programs ep
		JOIN channels c ON c.id = ep.channel_id
		WHERE %s
		ORDER BY c.slug ASC, ep.start_time ASC
	`, where)

	rows, err := s.db.QueryContext(r.Context(), query, args...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query_error", "Failed to fetch EPG")
		return
	}
	defer rows.Close()

	type program struct {
		ID          string `json:"id"`
		Title       string `json:"title"`
		Description string `json:"description"`
		StartAt     string `json:"start_at"`
		EndAt       string `json:"end_at"`
		Category    string `json:"category"`
		Rating      string `json:"rating"`
		IsLive      bool   `json:"is_live"`
		IsNew       bool   `json:"is_new"`
	}

	epgByChannel := map[string][]program{}

	for rows.Next() {
		var slug, id, title, desc, cat, rating string
		var startTime, endTime time.Time
		var isLive, isNew bool

		if err := rows.Scan(&slug, &id, &title, &desc, &startTime, &endTime, &cat, &rating, &isLive, &isNew); err != nil {
			continue
		}

		epgByChannel[slug] = append(epgByChannel[slug], program{
			ID:          id,
			Title:       title,
			Description: desc,
			StartAt:     startTime.UTC().Format(time.RFC3339),
			EndAt:       endTime.UTC().Format(time.RFC3339),
			Category:    cat,
			Rating:      rating,
			IsLive:      isLive,
			IsNew:       isNew,
		})
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"epg":          epgByChannel,
		"generated_at": time.Now().UTC().Format(time.RFC3339),
	})
}

// ---- handler: GET /owl/epg/upcoming -----------------------------------------

func (s *server) handleEPGUpcoming(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET required")
		return
	}

	limitStr := r.URL.Query().Get("limit")
	limit := 3
	if limitStr != "" {
		if n, err := strconv.Atoi(limitStr); err == nil && n > 0 && n <= 20 {
			limit = n
		}
	}

	channelFilter := r.URL.Query().Get("channel_id")

	args := []interface{}{time.Now().UTC(), limit}
	channelWhere := "c.is_active = true"
	if channelFilter != "" {
		slugs := strings.Split(channelFilter, ",")
		placeholders := make([]string, len(slugs))
		for i, sl := range slugs {
			args = append(args, strings.TrimSpace(sl))
			placeholders[i] = fmt.Sprintf("$%d", len(args))
		}
		channelWhere += fmt.Sprintf(" AND c.slug IN (%s)", strings.Join(placeholders, ","))
	}

	query := fmt.Sprintf(`
		SELECT c.slug, ep.id, ep.title, ep.start_time, ep.end_time,
		       coalesce(ep.category,''), ep.is_live
		FROM channels c
		JOIN LATERAL (
			SELECT id, title, start_time, end_time, category, is_live
			FROM epg_programs ep2
			WHERE ep2.channel_id = c.id AND ep2.start_time >= $1
			ORDER BY start_time ASC
			LIMIT $2
		) ep ON true
		WHERE %s
		ORDER BY c.slug ASC, ep.start_time ASC
	`, channelWhere)

	rows, err := s.db.QueryContext(r.Context(), query, args...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query_error", "Failed to fetch upcoming programs")
		return
	}
	defer rows.Close()

	type program struct {
		ID       string `json:"id"`
		Title    string `json:"title"`
		StartAt  string `json:"start_at"`
		EndAt    string `json:"end_at"`
		Category string `json:"category"`
		IsLive   bool   `json:"is_live"`
	}

	upcoming := map[string][]program{}

	for rows.Next() {
		var slug, id, title, cat string
		var startTime, endTime time.Time
		var isLive bool

		if err := rows.Scan(&slug, &id, &title, &startTime, &endTime, &cat, &isLive); err != nil {
			continue
		}

		upcoming[slug] = append(upcoming[slug], program{
			ID:       id,
			Title:    title,
			StartAt:  startTime.UTC().Format(time.RFC3339),
			EndAt:    endTime.UTC().Format(time.RFC3339),
			Category: cat,
			IsLive:   isLive,
		})
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"upcoming":     upcoming,
		"generated_at": time.Now().UTC().Format(time.RFC3339),
	})
}

// ---- handler: POST /owl/stream/:slug ----------------------------------------

func (s *server) handleStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST or GET required")
		return
	}

	// Extract slug from path: /owl/stream/{slug} or /owl/v1/stream/{slug}
	slug := pathSegment(r.URL.Path, 2)
	if slug == "stream" {
		// Path was /owl/v1/stream/{slug} — slug is at index 3
		slug = pathSegment(r.URL.Path, 3)
	}
	if slug == "" {
		writeError(w, http.StatusBadRequest, "missing_slug", "Channel slug required in path")
		return
	}

	subscriberID := r.Header.Get("X-Subscriber-ID")

	// Verify channel exists and is active
	var channelID string
	err := s.db.QueryRowContext(r.Context(), `
		SELECT id FROM channels WHERE slug = $1 AND is_active = true
	`, slug).Scan(&channelID)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "channel_unavailable",
			"This channel is temporarily unavailable.")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Channel lookup failed")
		return
	}

	// Log stream access (non-PII: channel slug only, no subscriber ID in logs per privacy policy)
	log.Printf("[owl_api] stream request: channel=%s", slug)
	// Flock screen time token check for kids profiles (P13-T03)
	if globalFlockClient != nil && subscriberID != "" {
		flockInfo := s.getFlockSessionInfo(r.Context(), subscriberID)
		if flockInfo.IsKids && flockInfo.FlockUserID != "" {
			if err := globalFlockClient.consumeToken(r.Context(), flockInfo.FlockUserID, "roost_stream_"+slug); err != nil {
				writeError(w, http.StatusPaymentRequired, "no_tokens",
					"No screen time tokens available. Complete tasks in Flock to earn more.")
				return
			}
		}
		// P13-T06: Report now-watching activity to Flock
		if flockInfo.FlockUserID != "" {
			globalFlockClient.reportNowWatching(flockInfo.FlockUserID, slug, "live")
		}
	}

	// Generate signed HLS URL
	streamURL, expiresAt := signedStreamURL(slug)

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"stream_url": streamURL,
		"expires_at": expiresAt.Format(time.RFC3339),
		"quality":    "auto",
		"format":     "hls",
		"drm":        nil,
	})
}

// ---- handler: GET /owl/vod --------------------------------------------------

func (s *server) handleVOD(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET required")
		return
	}
	subscriberID := r.Header.Get("X-Subscriber-ID")
	q := r.URL.Query()
	vodType := q.Get("type")   // "movie" | "series" | "" (all)
	genre    := q.Get("genre")
	search   := q.Get("q")
	limit := 50
	if l, _ := strconv.Atoi(q.Get("limit")); l > 0 && l <= 100 {
		limit = l
	}
	offset, _ := strconv.Atoi(q.Get("offset"))
	_ = subscriberID

	args := []interface{}{true}
	where := []string{"is_active = $1"}
	idx := 2
	if vodType != "" {
		where = append(where, fmt.Sprintf("type = $%d", idx))
		args = append(args, vodType)
		idx++
	}
	if genre != "" {
		where = append(where, fmt.Sprintf("genre = $%d", idx))
		args = append(args, genre)
		idx++
	}
	if search != "" {
		where = append(where, fmt.Sprintf("search_vector @@ plainto_tsquery('english', $%d)", idx))
		args = append(args, search)
		idx++
	}
	args = append(args, limit, offset)

	rows, err := s.db.QueryContext(r.Context(), fmt.Sprintf(`
		SELECT id, title, slug, type, genre, rating, release_year, duration_seconds,
		       poster_url
		FROM vod_catalog
		WHERE %s
		ORDER BY sort_order ASC, created_at DESC
		LIMIT $%d OFFSET $%d`,
		strings.Join(where, " AND "), idx, idx+1), args...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "catalog query failed")
		return
	}
	defer rows.Close()

	type vodEntry struct {
		ID              string  `json:"id"`
		Title           string  `json:"title"`
		Slug            string  `json:"slug"`
		Type            string  `json:"type"`
		Genre           *string `json:"genre,omitempty"`
		Rating          *string `json:"rating,omitempty"`
		ReleaseYear     *int    `json:"release_year,omitempty"`
		DurationSeconds *int    `json:"duration_seconds,omitempty"`
		PosterURL       *string `json:"poster_url,omitempty"`
	}
	var items []vodEntry
	for rows.Next() {
		var e vodEntry
		var genre2, rat, poster sql.NullString
		var yr, dur sql.NullInt64
		if err := rows.Scan(&e.ID, &e.Title, &e.Slug, &e.Type,
			&genre2, &rat, &yr, &dur, &poster); err != nil {
			continue
		}
		if genre2.Valid { e.Genre = &genre2.String }
		if rat.Valid   { e.Rating = &rat.String }
		if yr.Valid    { v := int(yr.Int64); e.ReleaseYear = &v }
		if dur.Valid   { v := int(dur.Int64); e.DurationSeconds = &v }
		if poster.Valid { e.PosterURL = &poster.String }
		items = append(items, e)
	}
	if items == nil { items = []vodEntry{} }
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"items": items, "count": len(items), "offset": offset,
	})
}

// ---- handler: GET /owl/vod/:id ----------------------------------------------

func (s *server) handleVODItem(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET required")
		return
	}
	// /owl/vod/{id} or /owl/v1/vod/{id}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	var vodID string
	for i, p := range parts {
		if p == "vod" && i+1 < len(parts) {
			vodID = parts[i+1]
			break
		}
	}
	if vodID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Content ID required")
		return
	}
	subscriberID := r.Header.Get("X-Subscriber-ID")

	// Check content type
	var vodType string
	err := s.db.QueryRowContext(r.Context(),
		`SELECT type FROM vod_catalog WHERE id = $1 AND is_active = true`, vodID).Scan(&vodType)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "not_found", "Content not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "lookup failed")
		return
	}

	// Signed stream URL (source URL never exposed)
	streamURL, expiresAt := signedStreamURL(vodID)

	// Watch progress
	var posSeconds int
	var completed bool
	_ = s.db.QueryRowContext(r.Context(), `
		SELECT position_seconds, completed FROM watch_progress
		WHERE subscriber_id = $1 AND content_type = 'movie' AND content_id = $2`,
		subscriberID, vodID).Scan(&posSeconds, &completed)

	if vodType == "movie" {
		var title, slug string
		var desc, genre, rat, poster, backdrop sql.NullString
		var yr, dur sql.NullInt64
		var skipContentID sql.NullString
		_ = s.db.QueryRowContext(r.Context(), `
			SELECT title, slug, description, genre, rating, release_year, duration_seconds,
			       poster_url, backdrop_url, skip_content_id
			FROM vod_catalog WHERE id = $1`, vodID).Scan(
			&title, &slug, &desc, &genre, &rat, &yr, &dur, &poster, &backdrop, &skipContentID)
		resp := map[string]interface{}{
			"id": vodID, "title": title, "slug": slug, "type": "movie",
			"stream_url":        streamURL,
			"stream_expires_at": expiresAt.Format(time.RFC3339),
			"resume_position":   posSeconds,
			"completed":         completed,
		}
		if desc.Valid     { resp["description"] = desc.String }
		if genre.Valid    { resp["genre"] = genre.String }
		if rat.Valid      { resp["rating"] = rat.String }
		if yr.Valid       { resp["release_year"] = yr.Int64 }
		if dur.Valid      { resp["duration_seconds"] = dur.Int64 }
		if poster.Valid   { resp["poster_url"] = poster.String }
		if backdrop.Valid { resp["backdrop_url"] = backdrop.String }
		// SKIP.6.2 — inferred_rating + scene_summary from skip engine.
		if skipContentID.Valid {
			resp["skip_content_id"] = skipContentID.String
			var inferredRating sql.NullString
			var sceneSummaryJSON []byte
			if err := s.db.QueryRowContext(r.Context(), `
				SELECT inferred_rating, scene_summary
				FROM family_content_rating_overrides
				WHERE content_id = $1`, skipContentID.String,
			).Scan(&inferredRating, &sceneSummaryJSON); err == nil {
				if inferredRating.Valid {
					resp["inferred_rating"] = inferredRating.String
				}
				if len(sceneSummaryJSON) > 0 {
					resp["scene_summary"] = json.RawMessage(sceneSummaryJSON)
				}
			}
		}
		writeJSON(w, http.StatusOK, resp)
		return
	}

	// Series — return seasons + episodes with per-episode progress
	type epResp struct {
		ID              string  `json:"id"`
		EpisodeNumber   int     `json:"episode_number"`
		Title           string  `json:"title"`
		DurationSeconds int     `json:"duration_seconds"`
		StreamURL       string  `json:"stream_url"`
		ResumePosition  int     `json:"resume_position"`
		Completed       bool    `json:"completed"`
	}
	type seasonResp struct {
		ID           string   `json:"id"`
		SeasonNumber int      `json:"season_number"`
		Title        *string  `json:"title,omitempty"`
		Episodes     []epResp `json:"episodes"`
	}

	seasonRows, err := s.db.QueryContext(r.Context(), `
		SELECT id, season_number, title FROM vod_series
		WHERE catalog_id = $1 ORDER BY season_number`, vodID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "seasons query failed")
		return
	}
	defer seasonRows.Close()

	var seasons []seasonResp
	for seasonRows.Next() {
		var se seasonResp
		var title sql.NullString
		if err := seasonRows.Scan(&se.ID, &se.SeasonNumber, &title); err != nil { continue }
		if title.Valid { se.Title = &title.String }

		epRows, err2 := s.db.QueryContext(r.Context(), `
			SELECT e.id, e.episode_number, e.title, e.duration_seconds,
			       COALESCE(wp.position_seconds, 0), COALESCE(wp.completed, false)
			FROM vod_episodes e
			LEFT JOIN watch_progress wp ON wp.content_type = 'episode'
			    AND wp.content_id = e.id AND wp.subscriber_id = $2
			WHERE e.series_id = $1
			ORDER BY e.episode_number`, se.ID, subscriberID)
		if err2 == nil {
			defer epRows.Close()
			for epRows.Next() {
				var ep epResp
				if err := epRows.Scan(&ep.ID, &ep.EpisodeNumber, &ep.Title,
					&ep.DurationSeconds, &ep.ResumePosition, &ep.Completed); err != nil {
					continue
				}
				ep.StreamURL, _ = signedStreamURL(ep.ID)
				se.Episodes = append(se.Episodes, ep)
			}
		}
		if se.Episodes == nil { se.Episodes = []epResp{} }
		seasons = append(seasons, se)
	}
	if seasons == nil { seasons = []seasonResp{} }

	var title, slug string
	var poster sql.NullString
	_ = s.db.QueryRowContext(r.Context(),
		`SELECT title, slug, poster_url FROM vod_catalog WHERE id = $1`, vodID).Scan(
		&title, &slug, &poster)
	resp := map[string]interface{}{
		"id": vodID, "title": title, "slug": slug, "type": "series",
		"seasons": seasons,
	}
	if poster.Valid { resp["poster_url"] = poster.String }
	writeJSON(w, http.StatusOK, resp)
}

// ---- handler: GET /owl/catchup/:channel_slug --------------------------------

func (s *server) handleCatchup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET required")
		return
	}
	// /owl/catchup/{channel_slug}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	var channelSlug string
	for i, p := range parts {
		if p == "catchup" && i+1 < len(parts) {
			channelSlug = parts[i+1]
			break
		}
	}
	if channelSlug == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "channel_slug required")
		return
	}

	// Fetch EPG programs from last 7 days that have catchup recordings
	rows, err := s.db.QueryContext(r.Context(), `
		SELECT ep.title, ep.start_time, ep.end_time, ep.description, ep.category,
		       cr.date, cr.hour, c.slug
		FROM epg_programs ep
		JOIN channels c ON c.id = ep.channel_id AND c.slug = $1
		JOIN catchup_recordings cr ON cr.channel_id = c.id
		    AND DATE(ep.start_time AT TIME ZONE 'UTC') = cr.date
		    AND EXTRACT(HOUR FROM ep.start_time AT TIME ZONE 'UTC')::int = cr.hour
		    AND cr.status IN ('recording', 'complete')
		WHERE ep.start_time >= NOW() - INTERVAL '7 days'
		ORDER BY ep.start_time DESC
		LIMIT 100`, channelSlug)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "catchup query failed")
		return
	}
	defer rows.Close()

	type catchupEntry struct {
		Title       string `json:"title"`
		StartTime   string `json:"start_time"`
		EndTime     string `json:"end_time"`
		Description string `json:"description,omitempty"`
		Category    string `json:"category,omitempty"`
		StreamURL   string `json:"stream_url"`
	}
	catchupBase := getEnv("ROOST_BASE_URL", "https://roost.yourflock.org")
	var entries []catchupEntry
	for rows.Next() {
		var e catchupEntry
		var desc, category sql.NullString
		var date, _ = time.Time{}, 0
		var hour int
		var slug string
		var start, end time.Time
		if err := rows.Scan(&e.Title, &start, &end, &desc, &category,
			&date, &hour, &slug); err != nil {
			continue
		}
		e.StartTime = start.Format(time.RFC3339)
		e.EndTime = end.Format(time.RFC3339)
		if desc.Valid { e.Description = desc.String }
		if category.Valid { e.Category = category.String }
		e.StreamURL = fmt.Sprintf("%s/catchup/%s/playlist.m3u8?start=%s&end=%s",
			catchupBase, slug,
			start.Format(time.RFC3339), end.Format(time.RFC3339))
		entries = append(entries, e)
	}
	if entries == nil { entries = []catchupEntry{} }
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"channel": channelSlug,
		"items":   entries,
		"count":   len(entries),
	})
}

// ---- handler: GET /owl/recommendations --------------------------------------

func (s *server) handleRecommendations(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET required")
		return
	}
	subscriberID := r.Header.Get("X-Subscriber-ID")

	// "For You" — personalized by genre affinity (weighted by watch time)
	forYouRows, err := s.db.QueryContext(r.Context(), `
		WITH genre_affinity AS (
			SELECT c.genre,
			       SUM(wp.position_seconds)::float /
			           GREATEST(SUM(SUM(wp.position_seconds)) OVER (), 1) AS score
			FROM watch_progress wp
			JOIN vod_catalog c ON c.id = wp.content_id AND wp.content_type = 'movie'
			WHERE wp.subscriber_id = $1 AND c.genre IS NOT NULL
			GROUP BY c.genre
		),
		scored AS (
			SELECT vc.id, vc.title, vc.type, vc.genre, vc.poster_url,
			       COALESCE(ga.score, 0) * 0.4 +
			       (SELECT COUNT(*) FROM watch_progress wp2
			        WHERE wp2.content_id = vc.id)::float /
			           GREATEST((SELECT COUNT(*) FROM watch_progress), 1) * 0.3 +
			       CASE WHEN vc.created_at > NOW() - INTERVAL '30 days'
			            THEN 0.2 ELSE 0.0 END AS recommendation_score
			FROM vod_catalog vc
			LEFT JOIN genre_affinity ga ON ga.genre = vc.genre
			WHERE vc.is_active = true
			  AND NOT EXISTS (
			      SELECT 1 FROM watch_progress wp3
			      WHERE wp3.subscriber_id = $1 AND wp3.content_id = vc.id
			        AND wp3.completed = true
			  )
		)
		SELECT id, title, type, genre, poster_url
		FROM scored ORDER BY recommendation_score DESC LIMIT 10`, subscriberID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "recommendations failed")
		return
	}
	defer forYouRows.Close()

	type recItem struct {
		ID        string  `json:"id"`
		Title     string  `json:"title"`
		Type      string  `json:"type"`
		Genre     *string `json:"genre,omitempty"`
		PosterURL *string `json:"poster_url,omitempty"`
	}
	var forYou []recItem
	for forYouRows.Next() {
		var item recItem
		var genre, poster sql.NullString
		if err := forYouRows.Scan(&item.ID, &item.Title, &item.Type, &genre, &poster); err != nil {
			continue
		}
		if genre.Valid { item.Genre = &genre.String }
		if poster.Valid { item.PosterURL = &poster.String }
		forYou = append(forYou, item)
	}

	// "Trending" — most watched this week (no personalization)
	trendRows, err := s.db.QueryContext(r.Context(), `
		SELECT vc.id, vc.title, vc.type, vc.genre, vc.poster_url,
		       COUNT(wp.id) AS watch_count
		FROM vod_catalog vc
		JOIN watch_progress wp ON wp.content_id = vc.id
		WHERE vc.is_active = true
		  AND wp.last_watched_at > NOW() - INTERVAL '7 days'
		GROUP BY vc.id, vc.title, vc.type, vc.genre, vc.poster_url
		ORDER BY watch_count DESC LIMIT 10`)
	if err == nil {
		defer trendRows.Close()
	}
	var trending []recItem
	if err == nil {
		for trendRows.Next() {
			var item recItem
			var genre, poster sql.NullString
			var watchCount int
			if err := trendRows.Scan(&item.ID, &item.Title, &item.Type,
				&genre, &poster, &watchCount); err != nil {
				continue
			}
			if genre.Valid { item.Genre = &genre.String }
			if poster.Valid { item.PosterURL = &poster.String }
			trending = append(trending, item)
		}
	}

	// "Because You Watched" — similar genre to last completed item
	var becauseTrigger string
	var becauseItems []recItem
	var lastWatchedGenre sql.NullString
	var lastWatchedTitle string
	err2 := s.db.QueryRowContext(r.Context(), `
		SELECT c.title, c.genre FROM watch_progress wp
		JOIN vod_catalog c ON c.id = wp.content_id AND wp.content_type = 'movie'
		WHERE wp.subscriber_id = $1 AND wp.completed = true
		ORDER BY wp.last_watched_at DESC LIMIT 1`, subscriberID).
		Scan(&lastWatchedTitle, &lastWatchedGenre)
	if err2 == nil && lastWatchedGenre.Valid {
		becauseTrigger = lastWatchedTitle
		simRows, err3 := s.db.QueryContext(r.Context(), `
			SELECT id, title, type, genre, poster_url
			FROM vod_catalog
			WHERE genre = $1 AND is_active = true
			  AND id NOT IN (
			      SELECT content_id FROM watch_progress WHERE subscriber_id = $2
			  )
			ORDER BY sort_order ASC LIMIT 5`, lastWatchedGenre.String, subscriberID)
		if err3 == nil {
			defer simRows.Close()
			for simRows.Next() {
				var item recItem
				var genre, poster sql.NullString
				if err := simRows.Scan(&item.ID, &item.Title, &item.Type, &genre, &poster); err != nil {
					continue
				}
				if genre.Valid { item.Genre = &genre.String }
				if poster.Valid { item.PosterURL = &poster.String }
				becauseItems = append(becauseItems, item)
			}
		}
	}

	if forYou == nil     { forYou = []recItem{} }
	if trending == nil   { trending = []recItem{} }
	if becauseItems == nil { becauseItems = []recItem{} }

	resp := map[string]interface{}{
		"for_you":  forYou,
		"trending": trending,
	}
	if becauseTrigger != "" {
		resp["because_you_watched"] = map[string]interface{}{
			"title": becauseTrigger,
			"items": becauseItems,
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// ---- handler: POST /internal/sessions/cleanup --------------------------------

func (s *server) handleSessionCleanup(w http.ResponseWriter, r *http.Request) {
	// Only allow from loopback — internal callers only
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	if host != "127.0.0.1" && host != "::1" {
		writeError(w, http.StatusForbidden, "forbidden", "Internal endpoint")
		return
	}

	result, err := s.db.ExecContext(r.Context(),
		`DELETE FROM owl_sessions WHERE expires_at < NOW() - INTERVAL '1 hour'`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "cleanup_error", err.Error())
		return
	}
	deleted, _ := result.RowsAffected()
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"deleted": deleted,
		"time":    time.Now().UTC().Format(time.RFC3339),
	})
}

// ---- main -------------------------------------------------------------------

func main() {
	db, err := connectDB()
	if err != nil {
		log.Fatalf("[owl_api] database connection failed: %v", err)
	}
	defer db.Close()

	// Wire the global DB for token validation caching (package-level in auth package)
	rootauth.SetDB(db)
	// Initialize Flock client for screen time, activity, and watch party features
	initFlockClient()

	// Connect Redis if REDIS_URL is set. Degrades gracefully when absent (dev mode).
	var rdb *goredis.Client
	if redisURL := getEnv("REDIS_URL", ""); redisURL != "" {
		rdb = goredis.NewClient(&goredis.Options{Addr: redisURL})
		log.Printf("[owl_api] Redis connected: %s", redisURL)
	} else {
		log.Printf("[owl_api] REDIS_URL not set — rate limiting and SSE pub/sub disabled")
	}

	srv := newServer(db, rdb)
	port := srv.port
	addr := ":" + port

	log.Printf("[owl_api] starting on %s", addr)
	log.Printf("[owl_api] endpoints: manifest, auth, live, epg, epg/upcoming, stream/:slug, vod, playlist.m3u8, player_api.php, /live/ (Xtream)")

	httpSrv := &http.Server{
		Addr:         addr,
		Handler:      srv.routes(),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	if err := httpSrv.ListenAndServe(); err != nil {
		log.Fatalf("[owl_api] server error: %v", err)
	}
}
