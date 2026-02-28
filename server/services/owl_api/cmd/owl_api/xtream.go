// xtream.go — Xtream Codes compatibility layer for Roost Owl API.
//
// Many popular IPTV players (TiviMate, Kodi IPTV Simple Client, Perfect Player,
// IPTV Smarters, etc.) expect the Xtream Codes API format. This file adds that
// compatibility surface to the Roost service so those players work without any
// Owl app requirement.
//
// Authentication: Xtream players send the subscriber's Roost API token as the
// "username" field. The password field is accepted but ignored — the API token
// is the only credential verified. The token must start with "roost_" and exist
// as an active record in api_tokens joined to an active subscriber.
//
// Endpoints added:
//   GET  /player_api.php?username=X&password=Y&action=get_live_categories
//   GET  /player_api.php?username=X&password=Y&action=get_live_streams
//   GET  /player_api.php?username=X&password=Y&action=get_epg_info_id&stream_id=N
//   GET  /live/:username/:password/:stream_id.m3u8
//
// Stream IDs in Xtream format are integer channel IDs, mapped from our UUID-based
// channel table via a stable integer sort_order column. Xtream players cache these
// IDs so they must be stable across restarts.
//
// Security: source stream URLs are NEVER returned. The /live/ endpoint validates
// the token and redirects to the Cloudflare-signed relay URL (15-min expiry).
// The player_api.php stream URLs point back to our /live/ endpoint.
package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	rootauth "github.com/unyeco/roost/internal/auth"
)

// ---- Xtream auth / types ---------------------------------------------------

// xtreamUserInfo is the subscriber profile returned on login.
type xtreamUserInfo struct {
	Username             string   `json:"username"`
	Password             string   `json:"password"`
	Message              string   `json:"message"`
	Auth                 int      `json:"auth"` // 1 = valid, 0 = invalid
	Status               string   `json:"status"`
	ExpDate              string   `json:"exp_date"`
	IsTrial              string   `json:"is_trial"`
	ActiveConns          string   `json:"active_cons"`
	CreatedAt            string   `json:"created_at"`
	MaxConnections       string   `json:"max_connections"`
	AllowedOutputFormats []string `json:"allowed_output_formats"`
}

// xtreamServerInfo is the service metadata returned on login.
type xtreamServerInfo struct {
	URL          string `json:"url"`
	Port         string `json:"port"`
	HTTPSPort    string `json:"https_port"`
	Protocol     string `json:"protocol"`
	RTMPPort     string `json:"rtmp_port"`
	Timezone     string `json:"timezone"`
	TimestampNow int64  `json:"timestamp_now"`
	TimeNow      string `json:"time_now"`
}

// xtreamCategory represents a channel category in Xtream format.
type xtreamCategory struct {
	CategoryID   string `json:"category_id"`
	CategoryName string `json:"category_name"`
	ParentID     int    `json:"parent_id"`
}

// xtreamStream represents one live channel in Xtream format.
type xtreamStream struct {
	Num               int    `json:"num"`
	Name              string `json:"name"`
	StreamType        string `json:"stream_type"`
	StreamID          int    `json:"stream_id"`
	StreamIcon        string `json:"stream_icon"`
	EPGChannelID      string `json:"epg_channel_id"`
	Added             string `json:"added"`
	IsAdult           string `json:"is_adult"`
	CategoryID        string `json:"category_id"`
	CategoryIds       []int  `json:"category_ids"`
	CustomSID         string `json:"custom_sid"`
	TVArchive         int    `json:"tv_archive"`
	DirectSource      string `json:"direct_source"` // always empty — never expose source
	TVArchiveDuration int    `json:"tv_archive_duration"`
}

// xtreamEPGProgram is one EPG entry in Xtream format.
type xtreamEPGProgram struct {
	ID             string `json:"id"`
	EPGListingID   string `json:"epg_id"`
	Title          string `json:"title"`
	Lang           string `json:"lang"`
	Start          string `json:"start"`
	End            string `json:"end"`
	Description    string `json:"description"`
	ChannelID      string `json:"channel_id"`
	StartTimestamp int64  `json:"start_timestamp"`
	StopTimestamp  int64  `json:"stop_timestamp"`
	NowPlaying     int    `json:"now_playing"`
	HasArchive     int    `json:"has_archive"`
}

// ---- validateXtreamCreds ---------------------------------------------------

// validateXtreamCreds validates the Xtream "username" field as a Roost API token.
// The password field is accepted but not verified — API token is the sole credential.
// Returns the subscriber_id and plan if valid, error if not found/inactive.
//
// Xtream convention: username = the raw API token (e.g. "roost_abc123...")
// This lets IPTV players connect using their Roost subscription token directly.
func (s *server) validateXtreamCreds(r *http.Request, username string) (subscriberID string, plan string, err error) {
	if username == "" || !strings.HasPrefix(username, "roost_") {
		return "", "", fmt.Errorf("invalid credentials")
	}

	// Hash the token to match how it's stored in the api_tokens table.
	// rootauth.HashToken produces SHA-256 hex — same as token creation.
	tokenHash := rootauth.HashToken(username)

	err = s.db.QueryRowContext(r.Context(), `
		SELECT s.id, coalesce(s.plan_slug, 'standard')
		FROM api_tokens t
		JOIN subscribers s ON s.id = t.subscriber_id
		WHERE t.token_hash = $1
		  AND t.is_active = true
		  AND s.status = 'active'
	`, tokenHash).Scan(&subscriberID, &plan)
	if err == sql.ErrNoRows {
		return "", "", fmt.Errorf("invalid credentials")
	}
	if err != nil {
		return "", "", fmt.Errorf("db error: %w", err)
	}
	return subscriberID, plan, nil
}

// ---- handler: GET /player_api.php ------------------------------------------

// handlePlayerAPI is the main Xtream Codes dispatch endpoint.
// Players hit this with ?username=X&password=Y&action=Z
// When action is absent (login check), returns user_info + server_info JSON.
func (s *server) handlePlayerAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET or POST required")
		return
	}

	q := r.URL.Query()
	username := q.Get("username")
	password := q.Get("password")
	action := q.Get("action")

	// Validate credentials for all actions
	subscriberID, plan, err := s.validateXtreamCreds(r, username)
	if err != nil {
		// Xtream players expect auth=0 in JSON, not an HTTP error status
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"user_info": xtreamUserInfo{
				Username: username,
				Password: password,
				Auth:     0,
				Status:   "Disabled",
				Message:  "Invalid credentials",
			},
		})
		return
	}

	switch action {
	case "get_live_categories":
		s.xtreamLiveCategories(w, r)
	case "get_live_streams":
		s.xtreamLiveStreams(w, r, subscriberID, username)
	case "get_epg_info_id":
		streamIDStr := q.Get("stream_id")
		s.xtreamEPGByStreamID(w, r, streamIDStr)
	default:
		// No action = login check — return user_info + server_info
		s.xtreamLoginResponse(w, r, username, password, subscriberID, plan)
	}
}

// xtreamLoginResponse returns the standard Xtream login response shape.
func (s *server) xtreamLoginResponse(w http.ResponseWriter, r *http.Request, username, password, subscriberID, plan string) {
	baseURL := getEnv("ROOST_BASE_URL", "https://roost.unity.dev")
	maxStreams, _ := planLimits(plan)
	_ = subscriberID // used for future active connections count

	resp := map[string]interface{}{
		"user_info": xtreamUserInfo{
			Username:             username,
			Password:             password,
			Message:              "Welcome to Roost",
			Auth:                 1,
			Status:               "Active",
			ExpDate:              "Unlimited",
			IsTrial:              "0",
			ActiveConns:          "0",
			CreatedAt:            time.Now().Format("2006-01-02"),
			MaxConnections:       fmt.Sprintf("%d", maxStreams),
			AllowedOutputFormats: []string{"m3u8", "ts"},
		},
		"server_info": xtreamServerInfo{
			URL:          baseURL,
			Port:         "80",
			HTTPSPort:    "443",
			Protocol:     "http",
			RTMPPort:     "1935",
			Timezone:     "UTC",
			TimestampNow: time.Now().Unix(),
			TimeNow:      time.Now().UTC().Format("2006-01-02 15:04:05"),
		},
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// xtreamLiveCategories returns all distinct channel categories in Xtream format.
func (s *server) xtreamLiveCategories(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.QueryContext(r.Context(), `
		SELECT DISTINCT category, count(*) as cnt
		FROM channels
		WHERE is_active = true AND category IS NOT NULL AND category != ''
		GROUP BY category
		ORDER BY category ASC
	`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query_error", "Failed to fetch categories")
		return
	}
	defer rows.Close()

	var cats []xtreamCategory
	i := 1
	for rows.Next() {
		var cat string
		var cnt int
		if err := rows.Scan(&cat, &cnt); err != nil {
			continue
		}
		cats = append(cats, xtreamCategory{
			CategoryID:   fmt.Sprintf("%d", i),
			CategoryName: cat,
			ParentID:     0,
		})
		i++
	}

	if cats == nil {
		cats = []xtreamCategory{}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(cats)
}

// xtreamLiveStreams returns all active channels in Xtream format.
// Stream IDs are stable integers from the sort_order column.
// Stream URLs are served via /live/:username/:password/:stream_id.m3u8 (redirect endpoint).
func (s *server) xtreamLiveStreams(w http.ResponseWriter, r *http.Request, subscriberID, username string) {
	_ = subscriberID // available for future per-subscriber channel filtering

	// Build a stable category_id lookup from the active channel set
	catRows, err := s.db.QueryContext(r.Context(), `
		SELECT DISTINCT category FROM channels
		WHERE is_active = true AND category IS NOT NULL AND category != ''
		ORDER BY category ASC
	`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query_error", "Failed to fetch categories")
		return
	}
	catMap := map[string]string{}
	idx := 1
	for catRows.Next() {
		var cat string
		if catRows.Scan(&cat) == nil {
			catMap[cat] = fmt.Sprintf("%d", idx)
			idx++
		}
	}
	catRows.Close()

	rows, err := s.db.QueryContext(r.Context(), `
		SELECT c.sort_order, c.name, c.slug, coalesce(c.logo_url,''),
		       coalesce(c.epg_channel_id,''), coalesce(c.category,'')
		FROM channels c
		WHERE c.is_active = true
		ORDER BY c.sort_order ASC
	`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query_error", "Failed to fetch streams")
		return
	}
	defer rows.Close()

	var streams []xtreamStream
	num := 1
	for rows.Next() {
		var sortOrder int
		var name, slug, logo, epgID, category string
		if err := rows.Scan(&sortOrder, &name, &slug, &logo, &epgID, &category); err != nil {
			continue
		}

		catID := catMap[category]
		catIDInt := 0
		if catID != "" {
			fmt.Sscanf(catID, "%d", &catIDInt)
		}

		streams = append(streams, xtreamStream{
			Num:               num,
			Name:              name,
			StreamType:        "live",
			StreamID:          sortOrder,
			StreamIcon:        logo,
			EPGChannelID:      epgID,
			Added:             fmt.Sprintf("%d", time.Now().Unix()),
			IsAdult:           "0",
			CategoryID:        catID,
			CategoryIds:       []int{catIDInt},
			CustomSID:         slug,
			TVArchive:         0,
			DirectSource:      "", // NEVER expose source URL
			TVArchiveDuration: 0,
		})
		num++
	}

	if streams == nil {
		streams = []xtreamStream{}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(streams)
}

// xtreamEPGByStreamID returns EPG programs for a specific channel by stream_id (sort_order).
// Returns the last 4h and next 48h of programming (up to 100 entries).
func (s *server) xtreamEPGByStreamID(w http.ResponseWriter, r *http.Request, streamIDStr string) {
	if streamIDStr == "" {
		writeError(w, http.StatusBadRequest, "missing_stream_id", "stream_id required")
		return
	}

	var streamID int
	if _, err := fmt.Sscanf(streamIDStr, "%d", &streamID); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_stream_id", "stream_id must be integer")
		return
	}

	// Look up channel by sort_order (= Xtream stream_id)
	var channelID string
	var epgChannelID sql.NullString
	err := s.db.QueryRowContext(r.Context(), `
		SELECT id, epg_channel_id FROM channels WHERE sort_order = $1 AND is_active = true
	`, streamID).Scan(&channelID, &epgChannelID)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "channel_not_found", "Channel not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Channel lookup failed")
		return
	}

	from := time.Now().UTC().Add(-4 * time.Hour)
	to := time.Now().UTC().Add(48 * time.Hour)

	rows, err := s.db.QueryContext(r.Context(), `
		SELECT ep.id, ep.title, coalesce(ep.description,''), ep.start_time, ep.end_time,
		       coalesce(ep.language_code, 'en')
		FROM epg_programs ep
		WHERE ep.channel_id = $1
		  AND ep.start_time >= $2
		  AND ep.end_time <= $3
		ORDER BY ep.start_time ASC
		LIMIT 100
	`, channelID, from, to)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query_error", "EPG query failed")
		return
	}
	defer rows.Close()

	var programs []xtreamEPGProgram
	now := time.Now().UTC()

	for rows.Next() {
		var id, title, desc, lang string
		var startTime, endTime time.Time

		if err := rows.Scan(&id, &title, &desc, &startTime, &endTime, &lang); err != nil {
			continue
		}

		nowPlaying := 0
		if startTime.Before(now) && endTime.After(now) {
			nowPlaying = 1
		}

		epgChID := ""
		if epgChannelID.Valid {
			epgChID = epgChannelID.String
		}

		programs = append(programs, xtreamEPGProgram{
			ID:             id,
			EPGListingID:   epgChID,
			Title:          title,
			Lang:           lang,
			Start:          startTime.UTC().Format("2006-01-02 15:04:05"),
			End:            endTime.UTC().Format("2006-01-02 15:04:05"),
			Description:    desc,
			ChannelID:      epgChID,
			StartTimestamp: startTime.Unix(),
			StopTimestamp:  endTime.Unix(),
			NowPlaying:     nowPlaying,
			HasArchive:     0,
		})
	}

	if programs == nil {
		programs = []xtreamEPGProgram{}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"epg_listings": programs,
	})
}

// ---- handler: GET /live/:username/:password/:stream_id.m3u8 ----------------

// handleXtreamStream validates the Xtream credentials, looks up the channel
// by stream_id (sort_order), generates a signed relay URL, and HTTP 302-redirects.
// The player follows the redirect to the Cloudflare-signed HLS playlist.
// Source URLs are NEVER returned — the redirect is always to our CDN relay.
func (s *server) handleXtreamStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET required")
		return
	}

	// Path: /live/{username}/{password}/{stream_id}.m3u8
	// Split on "/" — parts[0]="live", parts[1]=username, parts[2]=password, parts[3]="{id}.m3u8"
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 4 {
		writeError(w, http.StatusBadRequest, "invalid_path", "Expected /live/:username/:password/:stream_id.m3u8")
		return
	}

	username := parts[1]
	// parts[2] is password — accepted for Xtream compatibility but not checked
	streamIDRaw := strings.TrimSuffix(parts[3], ".m3u8")
	streamIDRaw = strings.TrimSuffix(streamIDRaw, ".ts")

	var streamID int
	if _, err := fmt.Sscanf(streamIDRaw, "%d", &streamID); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_stream_id", "stream_id must be integer")
		return
	}

	// Validate API token (Xtream username field)
	if _, _, err := s.validateXtreamCreds(r, username); err != nil {
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "Invalid username/password")
		return
	}

	// Look up channel slug by sort_order (= Xtream stream_id)
	var slug string
	err := s.db.QueryRowContext(r.Context(), `
		SELECT slug FROM channels WHERE sort_order = $1 AND is_active = true
	`, streamID).Scan(&slug)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "channel_unavailable", "Channel not found or unavailable")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Channel lookup failed")
		return
	}

	// Generate Cloudflare-signed CDN relay URL (15-min expiry)
	streamURL, _ := signedStreamURL(slug)

	// 302-redirect to signed CDN relay — player follows, source never exposed
	http.Redirect(w, r, streamURL, http.StatusFound)
}
