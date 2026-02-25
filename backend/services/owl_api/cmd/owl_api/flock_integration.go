// flock_integration.go — Flock integration hooks for the Owl addon API.
// P13-T03: Screen time token check on kids streams
// P13-T05: Watch party status endpoint (/owl/watch-party/:id)
//         Share-to-feed endpoint (/owl/share)
// P13-T06: Now Watching activity reporting on stream start
//
// All Flock calls are gracefully degraded: if the Flock API is unreachable,
// streaming continues without restriction. The goal is zero downtime for
// Roost subscribers when Flock is unavailable.
package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sync"
	"strings"
	"time"
)

// ── Flock client (lightweight, local to owl_api) ──────────────────────────────

type flockClient struct {
	baseURL    string
	httpClient *http.Client

	// In-process balance cache (30s TTL).
	cacheMu sync.RWMutex
	cache   map[string]*flockBalanceCache
}

type flockBalanceCache struct {
	balance   int
	expiresAt time.Time
}

// lastActivity guards the "now watching" rate limiter.
var (
	activityMu       sync.Mutex
	lastActivitySent = make(map[string]time.Time) // flockUserID → last sent time
)

var globalFlockClient *flockClient

func initFlockClient() {
	base := os.Getenv("FLOCK_OAUTH_BASE_URL")
	if base == "" {
		base = "https://yourflock.com"
	}
	globalFlockClient = &flockClient{
		baseURL:    base,
		httpClient: &http.Client{Timeout: 5 * time.Second},
		cache:      make(map[string]*flockBalanceCache),
	}
}

func flockServiceToken() string {
	return os.Getenv("FLOCK_SERVICE_TOKEN")
}

// checkTokenBalance returns the screen time token balance for a Flock user.
// Returns (balance, nil). On any error returns (0, nil) — fail open.
func (fc *flockClient) checkTokenBalance(ctx context.Context, flockUserID string) int {
	fc.cacheMu.RLock()
	if c, ok := fc.cache[flockUserID]; ok && time.Now().Before(c.expiresAt) {
		b := c.balance
		fc.cacheMu.RUnlock()
		return b
	}
	fc.cacheMu.RUnlock()

	url := fmt.Sprintf("%s/api/screen-time/balance?user_id=%s", fc.baseURL, flockUserID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		log.Printf("[flock] checkTokenBalance build error: %v", err)
		return 0
	}
	if tok := flockServiceToken(); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}

	resp, err := fc.httpClient.Do(req)
	if err != nil {
		log.Printf("[flock] checkTokenBalance unreachable (user=%s): %v — fail open", flockUserID, err)
		return 0
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, resp.Body)
		return 0
	}

	var result struct {
		Balance int `json:"balance"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return 0
	}

	fc.cacheMu.Lock()
	fc.cache[flockUserID] = &flockBalanceCache{
		balance:   result.Balance,
		expiresAt: time.Now().Add(30 * time.Second),
	}
	fc.cacheMu.Unlock()

	return result.Balance
}

// consumeToken consumes one screen time token for a Flock user.
// Returns a non-nil error only for 402 (no tokens) — all other failures are
// silently allowed (graceful degradation).
func (fc *flockClient) consumeToken(ctx context.Context, flockUserID, reason string) error {
	url := fmt.Sprintf("%s/api/screen-time/consume", fc.baseURL)
	payload, _ := json.Marshal(map[string]string{"user_id": flockUserID, "reason": reason})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		log.Printf("[flock] consumeToken build error: %v — allowing stream", err)
		return nil
	}
	req.Header.Set("Content-Type", "application/json")
	if tok := flockServiceToken(); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}

	resp, err := fc.httpClient.Do(req)
	if err != nil {
		log.Printf("[flock] consumeToken unreachable (user=%s): %v — allowing stream", flockUserID, err)
		return nil
	}
	defer resp.Body.Close()

	// Invalidate cache
	fc.cacheMu.Lock()
	delete(fc.cache, flockUserID)
	fc.cacheMu.Unlock()

	if resp.StatusCode == http.StatusPaymentRequired {
		return fmt.Errorf("no screen time tokens available")
	}
	io.Copy(io.Discard, resp.Body)
	return nil
}

// reportNowWatching sends a now-watching event to Flock (rate limited: 5 min).
func (fc *flockClient) reportNowWatching(flockUserID, contentTitle, contentType string) {
	activityMu.Lock()
	if last, ok := lastActivitySent[flockUserID]; ok && time.Since(last) < 5*time.Minute {
		activityMu.Unlock()
		return
	}
	lastActivitySent[flockUserID] = time.Now()
	activityMu.Unlock()

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		url := fmt.Sprintf("%s/api/activity/watching", fc.baseURL)
		payload, _ := json.Marshal(map[string]string{
			"user_id":      flockUserID,
			"content":      contentTitle,
			"content_type": contentType,
			"event":        "started",
		})
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
		if err != nil {
			return
		}
		req.Header.Set("Content-Type", "application/json")
		if tok := flockServiceToken(); tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		resp, err := fc.httpClient.Do(req)
		if err != nil {
			log.Printf("[flock] reportNowWatching unreachable (user=%s): %v", flockUserID, err)
			return
		}
		defer resp.Body.Close()
		io.Copy(io.Discard, resp.Body)
	}()
}

// ── Flock user lookup ──────────────────────────────────────────────────────────

// flockSessionInfo holds Flock-related fields for an active subscriber session.
type flockSessionInfo struct {
	FlockUserID string
	IsKids      bool
}

// getFlockSessionInfo returns the flock_user_id and is_kids_profile for the
// current session's active profile. Returns empty struct on any error.
func (s *server) getFlockSessionInfo(ctx context.Context, subscriberID string) flockSessionInfo {
	var flockUserID sql.NullString
	var isKids bool

	// First try to get flock_user_id from the subscriber record
	err := s.db.QueryRowContext(ctx,
		`SELECT flock_user_id FROM subscribers WHERE id = $1`,
		subscriberID).Scan(&flockUserID)
	if err != nil || !flockUserID.Valid {
		return flockSessionInfo{}
	}

	// Check if the active profile is a kids profile
	_ = s.db.QueryRowContext(ctx, `
		SELECT is_kids_profile FROM subscriber_profiles
		WHERE subscriber_id = $1 AND is_active = true
		LIMIT 1
	`, subscriberID).Scan(&isKids)

	return flockSessionInfo{
		FlockUserID: flockUserID.String,
		IsKids:      isKids,
	}
}

// ── Handler: GET /owl/tokens ──────────────────────────────────────────────────

// handleFlockTokens returns the current screen time token balance for the session.
// GET /owl/tokens
// Response: { "balance": N, "is_required": bool }
func (s *server) handleFlockTokens(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET required")
		return
	}

	subscriberID := r.Header.Get("X-Subscriber-ID")
	info := s.getFlockSessionInfo(r.Context(), subscriberID)

	if info.FlockUserID == "" || !info.IsKids {
		// Not linked to Flock or not a kids profile — tokens not required
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"balance":     -1, // -1 = not applicable
			"is_required": false,
		})
		return
	}

	if globalFlockClient == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"balance":     0,
			"is_required": true,
		})
		return
	}

	balance := globalFlockClient.checkTokenBalance(r.Context(), info.FlockUserID)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"balance":     balance,
		"is_required": true,
	})
}

// ── Handler: GET /owl/watch-party/:id ─────────────────────────────────────────

// handleOwlWatchParty returns watch party status for Owl clients.
// GET /owl/watch-party/{id}
// This is a proxy to the billing service's watch party data.
func (s *server) handleOwlWatchParty(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET required")
		return
	}

	// Extract party ID from path: /owl/watch-party/{id} or /owl/v1/watch-party/{id}
	parts := splitPath(r.URL.Path)
	partyID := ""
	for i, part := range parts {
		if part == "watch-party" && i+1 < len(parts) {
			partyID = parts[i+1]
			break
		}
	}
	if partyID == "" {
		writeError(w, http.StatusBadRequest, "missing_party_id", "party ID required")
		return
	}

	// Query DB directly for party status
	var status, contentType, inviteCode string
	var channelSlug sql.NullString
	var participantCount int
	var startedAt time.Time

	err := s.db.QueryRowContext(r.Context(), `
		SELECT wp.status, wp.content_type, wp.invite_code, c.slug,
		       (SELECT COUNT(*) FROM watch_party_participants wpp
		        WHERE wpp.party_id = wp.id AND wpp.left_at IS NULL),
		       wp.started_at
		FROM watch_parties wp
		LEFT JOIN channels c ON c.id = wp.channel_id
		WHERE wp.id = $1
	`, partyID).Scan(&status, &contentType, &inviteCode, &channelSlug,
		&participantCount, &startedAt)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "party_not_found", "watch party not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "party lookup failed")
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"party_id":          partyID,
		"status":            status,
		"content_type":      contentType,
		"invite_code":       inviteCode,
		"channel_slug":      channelSlug.String,
		"participant_count": participantCount,
		"started_at":        startedAt.Format(time.RFC3339),
	})
}

// ── Handler: POST /owl/share ──────────────────────────────────────────────────

// handleOwlShare shares content to the Flock activity feed.
// POST /owl/share
// Body: { "content_title": "...", "content_type": "live|vod", "channel_slug": "..." }
func (s *server) handleOwlShare(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST required")
		return
	}

	subscriberID := r.Header.Get("X-Subscriber-ID")
	info := s.getFlockSessionInfo(r.Context(), subscriberID)

	var req struct {
		ContentTitle string `json:"content_title"`
		ContentType  string `json:"content_type"`
		ChannelSlug  string `json:"channel_slug"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "valid JSON body required")
		return
	}

	if info.FlockUserID == "" {
		// Not linked to Flock — silently succeed
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"shared":false,"reason":"flock_not_linked"}`)
		return
	}

	// Stub: call Flock feed post API
	go func() {
		if globalFlockClient == nil {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		feedURL := fmt.Sprintf("%s/api/feed/post", globalFlockClient.baseURL)
		payload, _ := json.Marshal(map[string]string{
			"flock_user_id": info.FlockUserID,
			"content_title": req.ContentTitle,
			"content_type":  req.ContentType,
			"channel_slug":  req.ChannelSlug,
			"source":        "roost",
		})
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, feedURL,
			bytes.NewReader(payload))
		if err != nil {
			return
		}
		httpReq.Header.Set("Content-Type", "application/json")
		if tok := flockServiceToken(); tok != "" {
			httpReq.Header.Set("Authorization", "Bearer "+tok)
		}
		resp, err := globalFlockClient.httpClient.Do(httpReq)
		if err != nil {
			log.Printf("[flock] share to feed failed (user=%s): %v", info.FlockUserID, err)
			return
		}
		defer resp.Body.Close()
		io.Copy(io.Discard, resp.Body)
	}()

	w.Header().Set("Content-Type", "application/json")
	fmt.Fprint(w, `{"shared":true}`)
}

// ── Path helper ──────────────────────────────────────────────────────────────

func splitPath(path string) []string {
	var parts []string
	for _, p := range strings.Split(path, "/") {
		if p != "" {
			parts = append(parts, p)
		}
	}
	return parts
}
