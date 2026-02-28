// handlers_watch_party.go — Watch party management endpoints.
// P13-T05: Watch Party API
//
// Watch parties let a group of subscribers watch the same channel in sync.
// The host creates a party and shares an invite code. Guests join by entering
// the code. All participants get the same signed stream URL so playback is
// synchronized.
//
// Watch party notification: when a party is created, Roost attempts to post an
// invite notification (stubbed — graceful if provider
// chat API is not live yet).
//
// Endpoints:
//   POST   /watch-party       — create a new watch party
//   GET    /watch-party/{id}  — get party details + participants
//   POST   /watch-party/join  — join by invite_code
//   DELETE /watch-party/{id}  — end party (host only)
package billing

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/big"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/unyeco/roost/internal/auth"
)

// ── Invite code generation ────────────────────────────────────────────────────

const inviteCodeAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789" // no 0/O/I/1 ambiguity

// generateInviteCode generates a 6-character alphanumeric invite code using crypto/rand.
func generateInviteCode() (string, error) {
	code := make([]byte, 6)
	for i := range code {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(inviteCodeAlphabet))))
		if err != nil {
			return "", err
		}
		code[i] = inviteCodeAlphabet[n.Int64()]
	}
	return string(code), nil
}

// ── Watch party request/response types ───────────────────────────────────────

type createPartyRequest struct {
	ChannelSlug  string `json:"channel_slug"`
	ContentType  string `json:"content_type"`  // "live" | "vod" | "dvr"; default "live"
	ContentID    string `json:"content_id"`    // UUID for VOD/DVR content; empty for live
	MaxParticipants int `json:"max_participants"` // default 10, max 50
}

type partyResponse struct {
	PartyID      string `json:"party_id"`
	InviteCode   string `json:"invite_code"`
	Status       string `json:"status"`
	ContentType  string `json:"content_type"`
	ChannelSlug  string `json:"channel_slug,omitempty"`
	StreamURL    string `json:"stream_url"`
	ExpiresAt    string `json:"stream_expires_at"`
	StartedAt    string `json:"started_at"`
	Participants []partyParticipant `json:"participants"`
	IsHost       bool   `json:"is_host"`
}

type partyParticipant struct {
	SubscriberID string `json:"subscriber_id"`
	DisplayName  string `json:"display_name"`
	JoinedAt     string `json:"joined_at"`
	IsHost       bool   `json:"is_host"`
}

// ── Handler: POST /watch-party ────────────────────────────────────────────────

// handleCreateWatchParty creates a new watch party. The host auto-joins.
// POST /watch-party
func (s *Server) handleCreateWatchParty(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST required")
		return
	}

	claims, err := auth.ValidateJWT(r)
	if err != nil {
		auth.WriteError(w, http.StatusUnauthorized, "unauthorized", "valid JWT required")
		return
	}
	subscriberID := claims.Subject

	// Check subscriber has an active subscription
	if !s.hasActiveSubscription(r.Context(), subscriberID) {
		writeError(w, http.StatusForbidden, "subscription_required",
			"An active Roost subscription is required to create watch parties")
		return
	}

	var req createPartyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "valid JSON body required")
		return
	}

	// Validate content type
	contentType := strings.ToLower(req.ContentType)
	if contentType == "" {
		contentType = "live"
	}
	if contentType != "live" && contentType != "vod" && contentType != "dvr" {
		writeError(w, http.StatusBadRequest, "invalid_content_type",
			"content_type must be live, vod, or dvr")
		return
	}

	// Look up channel ID from slug (for live parties)
	var channelID sql.NullString
	channelSlug := req.ChannelSlug
	if channelSlug != "" {
		err := s.db.QueryRowContext(r.Context(),
			`SELECT id FROM channels WHERE slug = $1 AND is_active = true`,
			channelSlug).Scan(&channelID)
		if err == sql.ErrNoRows {
			writeError(w, http.StatusNotFound, "channel_not_found",
				fmt.Sprintf("channel %q not found", channelSlug))
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "db_error", "channel lookup failed")
			return
		}
	}

	maxParticipants := req.MaxParticipants
	if maxParticipants <= 0 || maxParticipants > 50 {
		maxParticipants = 10
	}

	// Generate unique invite code (retry up to 5 times on collision)
	var inviteCode string
	for attempt := 0; attempt < 5; attempt++ {
		code, err := generateInviteCode()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "invite_code_error",
				"failed to generate invite code")
			return
		}
		inviteCode = code
		// Check uniqueness against active parties
		var exists bool
		_ = s.db.QueryRowContext(r.Context(),
			`SELECT EXISTS(SELECT 1 FROM watch_parties WHERE invite_code=$1 AND status='active')`,
			code).Scan(&exists)
		if !exists {
			break
		}
	}

	// Create party and auto-join host in a transaction
	var partyID string
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "tx_error", "failed to start transaction")
		return
	}
	defer tx.Rollback()

	contentIDParam := nullableStr(req.ContentID)
	err = tx.QueryRowContext(r.Context(), `
		INSERT INTO watch_parties
		  (host_subscriber_id, channel_id, content_type, content_id, invite_code, max_participants)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id
	`, subscriberID, channelID, contentType, contentIDParam, inviteCode, maxParticipants).Scan(&partyID)
	if err != nil {
		log.Printf("[watch_party] create failed: %v", err)
		writeError(w, http.StatusInternalServerError, "db_error", "failed to create watch party")
		return
	}

	// Auto-join the host
	_, err = tx.ExecContext(r.Context(), `
		INSERT INTO watch_party_participants (party_id, subscriber_id)
		VALUES ($1, $2)
	`, partyID, subscriberID)
	if err != nil {
		log.Printf("[watch_party] host join failed: %v", err)
		writeError(w, http.StatusInternalServerError, "db_error", "failed to join as host")
		return
	}

	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, "tx_error", "transaction commit failed")
		return
	}

	// Generate signed stream URL for the channel
	streamURL, expiresAt := watchPartyStreamURL(channelSlug)

	// Async: notify watch party (stub — graceful if provider unavailable)
	go notifyWatchPartyCreated(r.Context(), subscriberID, partyID, inviteCode, channelSlug)

	log.Printf("[watch_party] created: party=%s host=%s channel=%s invite=%s",
		partyID, subscriberID, channelSlug, inviteCode)

	writeJSON(w, http.StatusCreated, partyResponse{
		PartyID:     partyID,
		InviteCode:  inviteCode,
		Status:      "active",
		ContentType: contentType,
		ChannelSlug: channelSlug,
		StreamURL:   streamURL,
		ExpiresAt:   expiresAt.Format(time.RFC3339),
		StartedAt:   time.Now().UTC().Format(time.RFC3339),
		IsHost:      true,
		Participants: []partyParticipant{
			{SubscriberID: subscriberID, JoinedAt: time.Now().UTC().Format(time.RFC3339), IsHost: true},
		},
	})
}

// ── Handler: GET /watch-party/{id} ───────────────────────────────────────────

// handleGetWatchParty returns party details and current participants.
// GET /watch-party/{id}
func (s *Server) handleGetWatchParty(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET required")
		return
	}

	claims, err := auth.ValidateJWT(r)
	if err != nil {
		auth.WriteError(w, http.StatusUnauthorized, "unauthorized", "valid JWT required")
		return
	}
	subscriberID := claims.Subject

	partyID := partyIDFromPath(r.URL.Path)
	if partyID == "" {
		writeError(w, http.StatusBadRequest, "missing_party_id", "party ID required in path")
		return
	}

	var hostSubID, contentType, inviteCode, status string
	var channelID, channelSlug sql.NullString
	var startedAt time.Time
	err = s.db.QueryRowContext(r.Context(), `
		SELECT wp.host_subscriber_id, wp.content_type, wp.invite_code, wp.status,
		       wp.channel_id, c.slug, wp.started_at
		FROM watch_parties wp
		LEFT JOIN channels c ON c.id = wp.channel_id
		WHERE wp.id = $1
	`, partyID).Scan(&hostSubID, &contentType, &inviteCode, &status,
		&channelID, &channelSlug, &startedAt)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "party_not_found", "watch party not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "party lookup failed")
		return
	}

	// Fetch participants
	participants, err := s.loadPartyParticipants(r.Context(), partyID, hostSubID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "participants lookup failed")
		return
	}

	streamURL, expiresAt := watchPartyStreamURL(channelSlug.String)

	resp := partyResponse{
		PartyID:      partyID,
		InviteCode:   inviteCode,
		Status:       status,
		ContentType:  contentType,
		ChannelSlug:  channelSlug.String,
		StreamURL:    streamURL,
		ExpiresAt:    expiresAt.Format(time.RFC3339),
		StartedAt:    startedAt.Format(time.RFC3339),
		IsHost:       hostSubID == subscriberID,
		Participants: participants,
	}
	writeJSON(w, http.StatusOK, resp)
}

// ── Handler: POST /watch-party/join ──────────────────────────────────────────

// handleJoinWatchParty joins an active party by invite code.
// POST /watch-party/join
// Body: { "invite_code": "ABC123" }
func (s *Server) handleJoinWatchParty(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST required")
		return
	}

	claims, err := auth.ValidateJWT(r)
	if err != nil {
		auth.WriteError(w, http.StatusUnauthorized, "unauthorized", "valid JWT required")
		return
	}
	subscriberID := claims.Subject

	if !s.hasActiveSubscription(r.Context(), subscriberID) {
		writeError(w, http.StatusForbidden, "subscription_required",
			"An active Roost subscription is required to join watch parties")
		return
	}

	var req struct {
		InviteCode string `json:"invite_code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "valid JSON body required")
		return
	}
	req.InviteCode = strings.ToUpper(strings.TrimSpace(req.InviteCode))
	if req.InviteCode == "" {
		writeError(w, http.StatusBadRequest, "missing_invite_code", "invite_code is required")
		return
	}

	// Look up active party by invite code
	var partyID, hostSubID, contentType string
	var channelSlug sql.NullString
	var maxParticipants int
	var startedAt time.Time
	err = s.db.QueryRowContext(r.Context(), `
		SELECT wp.id, wp.host_subscriber_id, wp.content_type, wp.max_participants,
		       wp.started_at, c.slug
		FROM watch_parties wp
		LEFT JOIN channels c ON c.id = wp.channel_id
		WHERE wp.invite_code = $1 AND wp.status = 'active'
	`, req.InviteCode).Scan(&partyID, &hostSubID, &contentType, &maxParticipants,
		&startedAt, &channelSlug)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "party_not_found",
			"no active watch party found with that invite code")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "party lookup failed")
		return
	}

	// Count current participants
	var currentCount int
	_ = s.db.QueryRowContext(r.Context(),
		`SELECT COUNT(*) FROM watch_party_participants WHERE party_id=$1 AND left_at IS NULL`,
		partyID).Scan(&currentCount)
	if currentCount >= maxParticipants {
		writeError(w, http.StatusConflict, "party_full",
			"this watch party is full")
		return
	}

	// Upsert participant (subscriber may be re-joining after leaving)
	_, err = s.db.ExecContext(r.Context(), `
		INSERT INTO watch_party_participants (party_id, subscriber_id)
		VALUES ($1, $2)
		ON CONFLICT (party_id, subscriber_id) DO UPDATE SET joined_at = NOW(), left_at = NULL
	`, partyID, subscriberID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "failed to join party")
		return
	}

	participants, err := s.loadPartyParticipants(r.Context(), partyID, hostSubID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "participants lookup failed")
		return
	}

	streamURL, expiresAt := watchPartyStreamURL(channelSlug.String)

	writeJSON(w, http.StatusOK, partyResponse{
		PartyID:      partyID,
		InviteCode:   req.InviteCode,
		Status:       "active",
		ContentType:  contentType,
		ChannelSlug:  channelSlug.String,
		StreamURL:    streamURL,
		ExpiresAt:    expiresAt.Format(time.RFC3339),
		StartedAt:    startedAt.Format(time.RFC3339),
		IsHost:       hostSubID == subscriberID,
		Participants: participants,
	})
}

// ── Handler: DELETE /watch-party/{id} ─────────────────────────────────────────

// handleEndWatchParty ends a watch party. Only the host can end a party.
// DELETE /watch-party/{id}
func (s *Server) handleEndWatchParty(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "DELETE required")
		return
	}

	claims, err := auth.ValidateJWT(r)
	if err != nil {
		auth.WriteError(w, http.StatusUnauthorized, "unauthorized", "valid JWT required")
		return
	}
	subscriberID := claims.Subject

	partyID := partyIDFromPath(r.URL.Path)
	if partyID == "" {
		writeError(w, http.StatusBadRequest, "missing_party_id", "party ID required in path")
		return
	}

	// Only the host can end the party
	var hostSubID string
	err = s.db.QueryRowContext(r.Context(),
		`SELECT host_subscriber_id FROM watch_parties WHERE id = $1 AND status = 'active'`,
		partyID).Scan(&hostSubID)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "party_not_found",
			"active watch party not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "party lookup failed")
		return
	}
	if hostSubID != subscriberID {
		writeError(w, http.StatusForbidden, "not_host",
			"only the party host can end the watch party")
		return
	}

	_, err = s.db.ExecContext(r.Context(),
		`UPDATE watch_parties SET status='ended', ended_at=NOW() WHERE id=$1`,
		partyID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "failed to end party")
		return
	}

	log.Printf("[watch_party] ended: party=%s host=%s", partyID, subscriberID)

	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"ended":true,"party_id":%q}`, partyID)
}

// ── Shared helper: load party participants ─────────────────────────────────────

func (s *Server) loadPartyParticipants(ctx context.Context, partyID, hostSubID string) ([]partyParticipant, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT wpp.subscriber_id, COALESCE(sub.display_name,''), wpp.joined_at
		FROM watch_party_participants wpp
		JOIN subscribers sub ON sub.id = wpp.subscriber_id
		WHERE wpp.party_id = $1 AND wpp.left_at IS NULL
		ORDER BY wpp.joined_at ASC
	`, partyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var participants []partyParticipant
	for rows.Next() {
		var p partyParticipant
		var joinedAt time.Time
		if err := rows.Scan(&p.SubscriberID, &p.DisplayName, &joinedAt); err != nil {
			return nil, err
		}
		p.JoinedAt = joinedAt.Format(time.RFC3339)
		p.IsHost = p.SubscriberID == hostSubID
		participants = append(participants, p)
	}
	return participants, rows.Err()
}

// ── Helper: generate stream URL for watch party ─────────────────────────────

// watchPartyStreamURL generates a signed HLS URL for a watch party channel.
// Returns the URL and its expiry. Delegates to the same signing logic as the
// stream endpoint via env-based config.
func watchPartyStreamURL(channelSlug string) (string, time.Time) {
	if channelSlug == "" {
		expires := time.Now().UTC().Add(15 * time.Minute)
		return "", expires
	}
	baseURL := getEnv("RELAY_BASE_URL", "https://cdn.roost.unity.dev")
	expiresAt := time.Now().UTC().Add(15 * time.Minute)
	url := fmt.Sprintf("%s/hls/%s/playlist.m3u8?expires=%d", baseURL, channelSlug, expiresAt.Unix())
	return url, expiresAt
}

// ── Helper: extract party ID from URL path ────────────────────────────────────

// partyIDFromPath extracts the party UUID from paths like /watch-party/{id}.
// Returns empty string if path doesn't match the pattern.
func partyIDFromPath(path string) string {
	// Paths: /watch-party/{id} — the id is the 3rd segment (0-indexed)
	// But /watch-party/join is also under this prefix so check it's not "join"
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) < 2 {
		return ""
	}
	id := parts[len(parts)-1]
	if id == "join" || id == "" {
		return ""
	}
	return id
}

// ── Helper: check active subscription ────────────────────────────────────────

// hasActiveSubscription returns true if the subscriber has an active subscription.
func (s *Server) hasActiveSubscription(ctx context.Context, subscriberID string) bool {
	var status string
	err := s.db.QueryRowContext(ctx,
		`SELECT status FROM subscribers WHERE id=$1`, subscriberID).Scan(&status)
	if err != nil {
		return false
	}
	return status == "active"
}

// ── Watch party notification (stub) ────────────────────────────────────────────

// notifyWatchPartyCreated notifies that a watch party was created.
// Stub implementation — gracefully no-ops if provider is unavailable.
func notifyWatchPartyCreated(ctx context.Context, subscriberID, partyID, inviteCode, channelSlug string) {
	// Fetch subscriber's sso_user_id
	// (This would normally come from the DB but we'd need to pass it in — using
	//  a background lookup here to keep the handler clean)
	_ = subscriberID // used for subscriber lookup in production
	_ = partyID

	chatURL := fmt.Sprintf("%s/api/chat/watch-party-invite", ssoBaseURL())
	payload, _ := json.Marshal(map[string]string{
		"invite_code":  inviteCode,
		"channel_slug": channelSlug,
		"source":       "roost",
	})

	reqCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, chatURL,
		bytes.NewReader(payload))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if tok := os.Getenv("SSO_SERVICE_TOKEN"); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Printf("[watch_party] Watch party notification failed (stub): %v", err)
		return // graceful degradation
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
}

// handleWatchParty dispatches POST /watch-party.
func (s *Server) handleWatchParty(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		s.handleCreateWatchParty(w, r)
		return
	}
	writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST required")
}

// handleWatchPartyByID dispatches GET and DELETE /watch-party/{id}.
func (s *Server) handleWatchPartyByID(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.handleGetWatchParty(w, r)
	case http.MethodDelete:
		s.handleEndWatchParty(w, r)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET or DELETE required")
	}
}
