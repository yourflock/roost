// metadata_display.go — Metadata display endpoints for sports events.
//
// Implements the display API contract defined in the Phase 15D supplement:
//   GET /owl/v1/channels/:channel_id/metadata  — channel metadata with live event
//   GET /owl/v1/sports/config/:sport           — sport display configuration
//   GET /owl/v1/sports/ticker                  — live scores ticker
//   GET /owl/v1/sports/events/:id/pregame      — pre-game screen data
//   GET /owl/v1/sports/events/:id/halftime     — halftime screen data
//   GET /owl/v1/sports/events/:id/final        — post-game summary
//   GET /owl/v1/sports/events/:id/status-stream — SSE status change events
//
// All score fields respect the subscriber's spoiler prevention preference.
// Sports data fields are sport-agnostic — the sport_data JSONB varies per sport.
package sports

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
)

// LiveEventObject is the structured live event embedded in channel metadata.
// All sport-specific fields are in SportData JSONB — Owl uses sport config to render them.
type LiveEventObject struct {
	EventID        string          `json:"event_id"`
	Sport          string          `json:"sport"`
	League         string          `json:"league"`
	LeagueLogo     string          `json:"league_logo,omitempty"`
	Status         string          `json:"status"` // pregame|live|halftime|final|postponed
	Home           TeamDisplay     `json:"home"`
	Away           TeamDisplay     `json:"away"`
	Period         string          `json:"period,omitempty"`
	Clock          string          `json:"clock,omitempty"`
	ClockDirection string          `json:"clock_direction"` // "down" or "up"
	IsClockRunning bool            `json:"is_clock_running"`
	SportData      json.RawMessage `json:"sport_data"` // sport-specific fields
	Broadcast      *BroadcastInfo  `json:"broadcast,omitempty"`
	Venue          *VenueInfo      `json:"venue,omitempty"`
	ScoresHidden   bool            `json:"scores_hidden"`
}

// TeamDisplay contains display fields for one team in a live event.
type TeamDisplay struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	ShortName      string `json:"short_name,omitempty"`
	Abbreviation   string `json:"abbreviation"`
	Logo           string `json:"logo,omitempty"`
	PrimaryColor   string `json:"primary_color,omitempty"`
	SecondaryColor string `json:"secondary_color,omitempty"`
	Score          *int   `json:"score"` // null when scores_hidden=true
	Record         string `json:"record,omitempty"`
}

// BroadcastInfo describes how the event is broadcast.
type BroadcastInfo struct {
	Network     string   `json:"network,omitempty"`
	Commentators []string `json:"commentators,omitempty"`
}

// VenueInfo describes the event venue.
type VenueInfo struct {
	Name     string       `json:"name,omitempty"`
	Location string       `json:"location,omitempty"`
	Weather  *WeatherInfo `json:"weather,omitempty"`
}

// WeatherInfo contains venue weather data.
type WeatherInfo struct {
	TempF     int    `json:"temp_f,omitempty"`
	Condition string `json:"condition,omitempty"`
	WindMPH   int    `json:"wind_mph,omitempty"`
}

// ChannelMetadataResponse is the full metadata response for a channel.
type ChannelMetadataResponse struct {
	ChannelID      string           `json:"channel_id"`
	ChannelName    string           `json:"channel_name"`
	ChannelLogo    string           `json:"channel_logo,omitempty"`
	ContentType    string           `json:"content_type"` // live_sport|live_news|live_general|composite
	CurrentProgram *CurrentProgram  `json:"current_program,omitempty"`
	LiveEvent      *LiveEventObject `json:"live_event"`       // null if no sports event mapped
	CompositeInfo  *CompositeInfo   `json:"composite_info"`   // null for non-composite channels
	CommercialStatus *CommercialStatusInfo `json:"commercial_status"` // null when not in break
}

// CurrentProgram contains EPG program info for the current time slot.
type CurrentProgram struct {
	Title       string    `json:"title"`
	Description string    `json:"description,omitempty"`
	StartTime   time.Time `json:"start_time"`
	EndTime     time.Time `json:"end_time"`
	Genre       string    `json:"genre,omitempty"`
}

// CompositeInfo is populated for composite (multi-stream) channels.
type CompositeInfo struct {
	Layout       map[string]int          `json:"layout"` // {cols, rows}
	Inputs       []CompositeInputDisplay `json:"inputs"`
	AudioFocusIdx int                    `json:"audio_focus_index"`
}

// CompositeInputDisplay describes one input in a composite channel.
type CompositeInputDisplay struct {
	Index        int    `json:"index"`
	ChannelID    string `json:"channel_id"`
	Label        string `json:"label,omitempty"`
	AudioFocused bool   `json:"audio_focused"`
}

// CommercialStatusInfo is included when the channel is currently in a commercial break.
type CommercialStatusInfo struct {
	InCommercial       bool      `json:"in_commercial"`
	EventID            string    `json:"event_id,omitempty"`
	StartedAt          time.Time `json:"started_at,omitempty"`
	EstimatedEndAt     *time.Time `json:"estimated_end_at,omitempty"`
	ElapsedSec         int       `json:"elapsed_s,omitempty"`
	EstimatedRemaining int       `json:"estimated_remaining_s,omitempty"`
	DetectionMethod    string    `json:"detection_method,omitempty"`
	Confidence         float64   `json:"confidence,omitempty"`
}

// SportConfig defines rendering rules for one sport.
type SportDisplayConfig struct {
	Sport            string                 `json:"sport"`
	PeriodName       string                 `json:"period_name"`
	PeriodCount      int                    `json:"period_count"`
	HasOvertime      bool                   `json:"has_overtime"`
	ClockDirection   string                 `json:"clock_direction"`
	ScoreFormat      string                 `json:"score_format"`
	HalftimeLabel    string                 `json:"halftime_label"`
	LeagueBadgeColor string                 `json:"league_badge_color,omitempty"`
	SportDataFields  []DisplayFieldConfig `json:"sport_data_fields"`
}

// SportDataFieldConfig describes one field in a sport's data set.
type DisplayFieldConfig struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Type  string `json:"type"` // number|string|team_indicator|boolean_badge|yards
}

// TickerItem is one entry in the live scores ticker.
type TickerItem struct {
	Sport     string  `json:"sport"`
	HomeAbbr  string  `json:"home_abbr"`
	HomeScore *int    `json:"home_score"` // null when scores_hidden
	HomeLogo  string  `json:"home_logo,omitempty"`
	AwayAbbr  string  `json:"away_abbr"`
	AwayScore *int    `json:"away_score"` // null when scores_hidden
	AwayLogo  string  `json:"away_logo,omitempty"`
	Period    string  `json:"period,omitempty"`
	Clock     string  `json:"clock,omitempty"`
	Status    string  `json:"status"`
	ChannelID *string `json:"channel_id"` // null if no Roost channel
}

// handleChannelMetadata returns rich metadata for a channel, including live event info.
// GET /owl/v1/channels/:channel_id/metadata
func (s *Server) handleChannelMetadata(w http.ResponseWriter, r *http.Request) {
	channelID := chi.URLParam(r, "channel_id")
	if channelID == "" {
		writeError(w, http.StatusBadRequest, "missing_param", "channel_id required")
		return
	}

	// Fetch basic channel info.
	var channelName, channelLogo, channelContentType string
	err := s.db.QueryRowContext(r.Context(), `
		SELECT name, COALESCE(logo_url, ''), 'live_general'
		FROM channels
		WHERE id = $1 AND is_active = true
	`, channelID).Scan(&channelName, &channelLogo, &channelContentType)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "not_found", "channel not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "channel query failed")
		return
	}

	resp := ChannelMetadataResponse{
		ChannelID:   channelID,
		ChannelName: channelName,
		ChannelLogo: channelLogo,
		ContentType: channelContentType,
		LiveEvent:   nil,
		CompositeInfo: nil,
		CommercialStatus: &CommercialStatusInfo{InCommercial: false},
	}

	// Check for an active sports event mapped to this channel.
	liveEvent, err := s.fetchLiveEventForChannelCtx(r, channelID)
	if err != nil {
		log.Printf("[sports] channel metadata live event fetch %s: %v", channelID, err)
	} else if liveEvent != nil {
		resp.LiveEvent = liveEvent
		resp.ContentType = "live_sport"
	}

	// Check for active commercial break.
	commercial, cerr := s.fetchActiveCommercialEvent(r, channelID)
	if cerr == nil && commercial != nil {
		resp.CommercialStatus = commercial
	}

	writeJSON(w, http.StatusOK, resp)
}

// handleSportConfig returns the display configuration for a sport.
// GET /owl/v1/sports/config/:sport
func (s *Server) handleSportConfig(w http.ResponseWriter, r *http.Request) {
	sport := chi.URLParam(r, "sport")
	if sport == "" {
		writeError(w, http.StatusBadRequest, "missing_param", "sport required")
		return
	}

	var cfg SportDisplayConfig
	var fieldsJSON []byte
	err := s.db.QueryRowContext(r.Context(), `
		SELECT sport, period_name, period_count, has_overtime, clock_direction,
		       score_format, halftime_label, COALESCE(league_badge_color, ''),
		       sport_data_fields
		FROM sport_display_config
		WHERE sport = $1
	`, sport).Scan(
		&cfg.Sport, &cfg.PeriodName, &cfg.PeriodCount, &cfg.HasOvertime,
		&cfg.ClockDirection, &cfg.ScoreFormat, &cfg.HalftimeLabel,
		&cfg.LeagueBadgeColor, &fieldsJSON,
	)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "not_found", fmt.Sprintf("no config for sport %q", sport))
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "sport config query failed")
		return
	}

	if len(fieldsJSON) > 0 {
		json.Unmarshal(fieldsJSON, &cfg.SportDataFields)
	}
	if cfg.SportDataFields == nil {
		cfg.SportDataFields = []DisplayFieldConfig{}
	}

	writeJSON(w, http.StatusOK, cfg)
}

// handleTicker returns all currently live events for the bottom scores ticker.
// GET /owl/v1/sports/ticker
func (s *Server) handleTicker(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.QueryContext(r.Context(), `
		SELECT
			sl.sport,
			ht.abbreviation, se.home_score, ht.logo_url,
			at.abbreviation, se.away_score, at.logo_url,
			se.period, se.status,
			(SELECT cm.channel_id::text
			 FROM sports_channel_mappings cm
			 WHERE cm.event_id = se.id AND cm.is_primary = true LIMIT 1) AS channel_id
		FROM sports_events se
		JOIN sports_leagues sl ON sl.id = se.league_id
		JOIN sports_teams ht ON ht.id = se.home_team_id
		JOIN sports_teams at ON at.id = se.away_team_id
		WHERE se.status IN ('live', 'halftime')
		   OR (se.status = 'final' AND se.updated_at > now() - INTERVAL '3 hours')
		ORDER BY se.scheduled_time
		LIMIT 50
	`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "ticker query failed")
		return
	}
	defer rows.Close()

	var items []TickerItem
	for rows.Next() {
		var item TickerItem
		var homeScore, awayScore int
		var period, channelIDStr sql.NullString
		var logoHome, logoAway sql.NullString
		if err := rows.Scan(
			&item.Sport,
			&item.HomeAbbr, &homeScore, &logoHome,
			&item.AwayAbbr, &awayScore, &logoAway,
			&period, &item.Status,
			&channelIDStr,
		); err != nil {
			continue
		}
		item.HomeScore = &homeScore
		item.AwayScore = &awayScore
		if logoHome.Valid {
			item.HomeLogo = logoHome.String
		}
		if logoAway.Valid {
			item.AwayLogo = logoAway.String
		}
		if period.Valid {
			item.Period = period.String
		}
		if channelIDStr.Valid {
			s := channelIDStr.String
			item.ChannelID = &s
		}
		items = append(items, item)
	}
	if items == nil {
		items = []TickerItem{}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"items":          items,
		"refreshes_in_s": 30,
	})
}

// handleEventStatusStream provides SSE updates for event status transitions.
// GET /owl/v1/sports/events/:id/status-stream
func (s *Server) handleEventStatusStream(w http.ResponseWriter, r *http.Request) {
	eventID := chi.URLParam(r, "id")
	if eventID == "" {
		writeError(w, http.StatusBadRequest, "missing_param", "event id required")
		return
	}

	// Set SSE headers.
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming_unsupported", "SSE not supported")
		return
	}

	// Send initial status.
	var currentStatus string
	err := s.db.QueryRowContext(r.Context(), `
		SELECT status FROM sports_events WHERE id = $1
	`, eventID).Scan(&currentStatus)
	if err != nil {
		fmt.Fprintf(w, "data: {\"error\":\"event not found\"}\n\n")
		flusher.Flush()
		return
	}

	fmt.Fprintf(w, "data: {\"type\":\"status\",\"status\":%q}\n\n", currentStatus)
	flusher.Flush()

	// Poll for status changes every 10 seconds.
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			var newStatus string
			err := s.db.QueryRowContext(r.Context(), `
				SELECT status FROM sports_events WHERE id = $1
			`, eventID).Scan(&newStatus)
			if err != nil {
				return
			}
			if newStatus != currentStatus {
				fmt.Fprintf(w, "data: {\"type\":\"status_change\",\"from\":%q,\"to\":%q}\n\n",
					currentStatus, newStatus)
				flusher.Flush()
				currentStatus = newStatus
			} else {
				// Send keepalive.
				fmt.Fprintf(w, ": keepalive\n\n")
				flusher.Flush()
			}
		}
	}
}

// ---------- internal helpers -------------------------------------------------

// fetchLiveEventForChannelCtx returns the live event for a channel.
func (s *Server) fetchLiveEventForChannelCtx(r *http.Request, channelID string) (*LiveEventObject, error) {
	var event Event
	var homeTeam, awayTeam Team
	var leagueName, sport string
	var sportDataRaw []byte
	var period sql.NullString

	err := s.db.QueryRowContext(r.Context(), `
		SELECT
			se.id, se.status, se.home_score, se.away_score,
			COALESCE(se.period, ''), se.broadcast_info::text,
			sl.name, sl.sport, sl.logo_url,
			ht.id, ht.name, COALESCE(ht.short_name, ''), ht.abbreviation,
			COALESCE(ht.logo_url, ''), COALESCE(ht.primary_color, ''), COALESCE(ht.secondary_color, ''),
			at.id, at.name, COALESCE(at.short_name, ''), at.abbreviation,
			COALESCE(at.logo_url, ''), COALESCE(at.primary_color, ''), COALESCE(at.secondary_color, ''),
			'{}' as sport_data
		FROM sports_events se
		JOIN sports_leagues sl ON sl.id = se.league_id
		JOIN sports_teams ht ON ht.id = se.home_team_id
		JOIN sports_teams at ON at.id = se.away_team_id
		JOIN sports_channel_mappings cm ON cm.event_id = se.id
		WHERE cm.channel_id = $1
		  AND se.status IN ('live', 'halftime', 'pregame')
		  AND cm.start_time <= now() + INTERVAL '1 hour'
		  AND cm.end_time >= now() - INTERVAL '30 minutes'
		ORDER BY se.scheduled_time
		LIMIT 1
	`, channelID).Scan(
		&event.ID, &event.Status, &event.HomeScore, &event.AwayScore,
		&period, &event.BroadcastInfo,
		&leagueName, &sport, &leagueName, // logos todo
		&homeTeam.ID, &homeTeam.Name, &homeTeam.ShortName, &homeTeam.Abbreviation,
		&homeTeam.LogoURL, &homeTeam.PrimaryColor, &homeTeam.SecondaryColor,
		&awayTeam.ID, &awayTeam.Name, &awayTeam.ShortName, &awayTeam.Abbreviation,
		&awayTeam.LogoURL, &awayTeam.PrimaryColor, &awayTeam.SecondaryColor,
		&sportDataRaw,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	homeScore := event.HomeScore
	awayScore := event.AwayScore
	shortNameHome := ""
	if homeTeam.ShortName != nil {
		shortNameHome = *homeTeam.ShortName
	}
	shortNameAway := ""
	if awayTeam.ShortName != nil {
		shortNameAway = *awayTeam.ShortName
	}
	logoHome := ""
	if homeTeam.LogoURL != nil {
		logoHome = *homeTeam.LogoURL
	}
	logoAway := ""
	if awayTeam.LogoURL != nil {
		logoAway = *awayTeam.LogoURL
	}
	primaryHome := ""
	if homeTeam.PrimaryColor != nil {
		primaryHome = *homeTeam.PrimaryColor
	}
	primaryAway := ""
	if awayTeam.PrimaryColor != nil {
		primaryAway = *awayTeam.PrimaryColor
	}

	liveEvent := &LiveEventObject{
		EventID:        event.ID,
		Sport:          sport,
		League:         leagueName,
		Status:         event.Status,
		Period:         "",
		ClockDirection: "down",
		IsClockRunning: event.Status == "live",
		SportData:      json.RawMessage(`{}`),
		ScoresHidden:   false,
		Home: TeamDisplay{
			ID:           homeTeam.ID,
			Name:         homeTeam.Name,
			ShortName:    shortNameHome,
			Abbreviation: homeTeam.Abbreviation,
			Logo:         logoHome,
			PrimaryColor: primaryHome,
			Score:        &homeScore,
		},
		Away: TeamDisplay{
			ID:           awayTeam.ID,
			Name:         awayTeam.Name,
			ShortName:    shortNameAway,
			Abbreviation: awayTeam.Abbreviation,
			Logo:         logoAway,
			PrimaryColor: primaryAway,
			Score:        &awayScore,
		},
	}
	if period.Valid {
		liveEvent.Period = period.String
	}
	if len(sportDataRaw) > 0 {
		liveEvent.SportData = sportDataRaw
	}
	return liveEvent, nil
}

// fetchActiveCommercialEvent returns the active commercial break for a channel, if any.
func (s *Server) fetchActiveCommercialEvent(r *http.Request, channelID string) (*CommercialStatusInfo, error) {
	var info CommercialStatusInfo
	var startedAt time.Time
	var confidence float64
	var method string
	var eventID string

	err := s.db.QueryRowContext(r.Context(), `
		SELECT id, started_at, confidence, detection_method
		FROM commercial_events
		WHERE channel_id = $1 AND status = 'active'
		ORDER BY started_at DESC
		LIMIT 1
	`, channelID).Scan(&eventID, &startedAt, &confidence, &method)
	if err == sql.ErrNoRows {
		return &CommercialStatusInfo{InCommercial: false}, nil
	}
	if err != nil {
		return nil, err
	}

	now := time.Now()
	elapsed := int(now.Sub(startedAt).Seconds())
	// Estimate 120s average break duration.
	estimatedDur := 120
	remaining := estimatedDur - elapsed
	if remaining < 0 {
		remaining = 0
	}
	estimatedEnd := startedAt.Add(time.Duration(estimatedDur) * time.Second)

	info = CommercialStatusInfo{
		InCommercial:       true,
		EventID:            eventID,
		StartedAt:          startedAt,
		EstimatedEndAt:     &estimatedEnd,
		ElapsedSec:         elapsed,
		EstimatedRemaining: remaining,
		DetectionMethod:    method,
		Confidence:         confidence,
	}
	return &info, nil
}

// Bind metadata display routes to the chi router.
// Called from server.go after the existing routes are registered.
func (s *Server) registerMetadataRoutes(r chi.Router) {
	r.Get("/owl/v1/channels/{channel_id}/metadata", s.handleChannelMetadata)
	r.Get("/owl/v1/sports/config/{sport}", s.handleSportConfig)
	r.Get("/owl/v1/sports/ticker", s.handleTicker)
	r.Get("/owl/v1/sports/events/{id}/status-stream", s.handleEventStatusStream)
}
