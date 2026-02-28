// handlers.go — HTTP handlers for the sports service.
// P15-T02: Public and admin endpoints for leagues, teams, events, channel mappings.
package sports

import (
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

// handleListLeagues returns all active sports leagues.
func (s *Server) handleListLeagues(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.QueryContext(r.Context(), `
		SELECT id, name, abbreviation, sport, country_code, logo_url,
		       season_structure::text, thesportsdb_id, is_active, sort_order, created_at, updated_at
		FROM sports_leagues
		WHERE is_active = true
		ORDER BY sort_order, name`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Failed to list leagues")
		return
	}
	defer rows.Close()

	leagues := []League{}
	for rows.Next() {
		var l League
		var seasonStruct string
		if err := rows.Scan(&l.ID, &l.Name, &l.Abbreviation, &l.Sport,
			&l.CountryCode, &l.LogoURL, &seasonStruct,
			&l.TheSportsDBID, &l.IsActive, &l.SortOrder, &l.CreatedAt, &l.UpdatedAt); err != nil {
			continue
		}
		l.SeasonStructure = seasonStruct
		leagues = append(leagues, l)
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"leagues": leagues})
}

// handleListTeams returns all active teams for a league.
func (s *Server) handleListTeams(w http.ResponseWriter, r *http.Request) {
	leagueID := chi.URLParam(r, "id")
	rows, err := s.db.QueryContext(r.Context(), `
		SELECT id, league_id, name, short_name, abbreviation, city, venue, logo_url,
		       primary_color, secondary_color, conference, division,
		       thesportsdb_id, is_active, created_at, updated_at
		FROM sports_teams
		WHERE league_id = $1 AND is_active = true
		ORDER BY conference, division, name`, leagueID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Failed to list teams")
		return
	}
	defer rows.Close()

	teams := []Team{}
	for rows.Next() {
		var t Team
		if err := rows.Scan(&t.ID, &t.LeagueID, &t.Name, &t.ShortName,
			&t.Abbreviation, &t.City, &t.Venue, &t.LogoURL,
			&t.PrimaryColor, &t.SecondaryColor, &t.Conference, &t.Division,
			&t.TheSportsDBID, &t.IsActive, &t.CreatedAt, &t.UpdatedAt); err != nil {
			continue
		}
		teams = append(teams, t)
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"teams": teams})
}

// handleListEvents returns filtered sports events.
// Query params: league (abbreviation), week, season, status
func (s *Server) handleListEvents(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	leagueAbbr := q.Get("league")
	week := q.Get("week")
	season := q.Get("season")
	status := q.Get("status")

	var conds []string
	var args []interface{}
	argIdx := 1

	if leagueAbbr != "" {
		conds = append(conds, "sl.abbreviation = $"+itos(argIdx))
		args = append(args, leagueAbbr)
		argIdx++
	}
	if week != "" {
		conds = append(conds, "se.week = $"+itos(argIdx))
		args = append(args, week)
		argIdx++
	}
	if season != "" {
		conds = append(conds, "se.season = $"+itos(argIdx))
		args = append(args, season)
		argIdx++
	}
	if status != "" {
		conds = append(conds, "se.status = $"+itos(argIdx))
		args = append(args, status)
		argIdx++
	}

	where := ""
	if len(conds) > 0 {
		where = "WHERE " + strings.Join(conds, " AND ")
	}

	rows, err := s.db.QueryContext(r.Context(), `
		SELECT se.id, se.league_id, se.home_team_id, se.away_team_id,
		       se.season, se.season_type, se.week, se.venue,
		       se.scheduled_time, se.actual_start_time, se.status,
		       se.home_score, se.away_score, se.period,
		       se.period_scores::text, se.broadcast_info::text,
		       se.thesportsdb_event_id, se.created_at, se.updated_at
		FROM sports_events se
		JOIN sports_leagues sl ON sl.id = se.league_id
		`+where+`
		ORDER BY se.scheduled_time
		LIMIT 200`, args...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Failed to list events")
		return
	}
	defer rows.Close()

	events := scanEvents(rows)
	writeJSON(w, http.StatusOK, map[string]interface{}{"events": events})
}

// handleGetEvent returns a single event with its channel mappings.
func (s *Server) handleGetEvent(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	row := s.db.QueryRowContext(r.Context(), `
		SELECT id, league_id, home_team_id, away_team_id,
		       season, season_type, week, venue,
		       scheduled_time, actual_start_time, status,
		       home_score, away_score, period,
		       period_scores::text, broadcast_info::text,
		       thesportsdb_event_id, created_at, updated_at
		FROM sports_events WHERE id = $1`, id)

	var ev Event
	err := row.Scan(&ev.ID, &ev.LeagueID, &ev.HomeTeamID, &ev.AwayTeamID,
		&ev.Season, &ev.SeasonType, &ev.Week, &ev.Venue,
		&ev.ScheduledTime, &ev.ActualStartTime, &ev.Status,
		&ev.HomeScore, &ev.AwayScore, &ev.Period,
		&ev.PeriodScores, &ev.BroadcastInfo,
		&ev.TheSportsDBEventID, &ev.CreatedAt, &ev.UpdatedAt)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "not_found", "Event not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Failed to get event")
		return
	}

	// Fetch channel mappings
	mappingRows, err := s.db.QueryContext(r.Context(), `
		SELECT id, event_id, channel_id, start_time, end_time, is_primary, notes, created_at
		FROM sports_channel_mappings WHERE event_id = $1 ORDER BY is_primary DESC, start_time`, id)
	if err == nil {
		defer mappingRows.Close()
		for mappingRows.Next() {
			var m ChannelMapping
			if err := mappingRows.Scan(&m.ID, &m.EventID, &m.ChannelID,
				&m.StartTime, &m.EndTime, &m.IsPrimary, &m.Notes, &m.CreatedAt); err == nil {
				ev.ChannelMappings = append(ev.ChannelMappings, m)
			}
		}
	}

	writeJSON(w, http.StatusOK, ev)
}

// handleLiveEvents returns all currently live or scheduled-soon events.
func (s *Server) handleLiveEvents(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.QueryContext(r.Context(), `
		SELECT se.id, se.league_id, se.home_team_id, se.away_team_id,
		       se.season, se.season_type, se.week, se.venue,
		       se.scheduled_time, se.actual_start_time, se.status,
		       se.home_score, se.away_score, se.period,
		       se.period_scores::text, se.broadcast_info::text,
		       se.thesportsdb_event_id, se.created_at, se.updated_at
		FROM sports_events se
		WHERE se.status = 'live'
		   OR (se.status = 'scheduled' AND se.scheduled_time <= now() + interval '30 minutes')
		ORDER BY se.scheduled_time`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Failed to get live events")
		return
	}
	defer rows.Close()

	events := scanEvents(rows)
	writeJSON(w, http.StatusOK, map[string]interface{}{"events": events, "count": len(events)})
}

// handleCreateLeague creates a new league (admin only).
func (s *Server) handleCreateLeague(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name            string `json:"name"`
		Abbreviation    string `json:"abbreviation"`
		Sport           string `json:"sport"`
		CountryCode     string `json:"country_code"`
		TheSportsDBID   string `json:"thesportsdb_id"`
		SeasonStructure string `json:"season_structure"`
		SortOrder       int    `json:"sort_order"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "Invalid request body")
		return
	}
	if req.Name == "" || req.Abbreviation == "" || req.Sport == "" {
		writeError(w, http.StatusBadRequest, "missing_fields", "name, abbreviation, sport required")
		return
	}
	if req.SeasonStructure == "" {
		req.SeasonStructure = "{}"
	}

	var id string
	err := s.db.QueryRowContext(r.Context(), `
		INSERT INTO sports_leagues (name, abbreviation, sport, country_code, thesportsdb_id, season_structure, sort_order)
		VALUES ($1, $2, $3, NULLIF($4,''), NULLIF($5,''), $6::jsonb, $7)
		RETURNING id`,
		req.Name, req.Abbreviation, req.Sport, req.CountryCode,
		req.TheSportsDBID, req.SeasonStructure, req.SortOrder).Scan(&id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Failed to create league")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"id": id})
}

// handleCreateTeam creates a new team (admin only).
func (s *Server) handleCreateTeam(w http.ResponseWriter, r *http.Request) {
	var req struct {
		LeagueID       string `json:"league_id"`
		Name           string `json:"name"`
		ShortName      string `json:"short_name"`
		Abbreviation   string `json:"abbreviation"`
		City           string `json:"city"`
		Venue          string `json:"venue"`
		PrimaryColor   string `json:"primary_color"`
		SecondaryColor string `json:"secondary_color"`
		Conference     string `json:"conference"`
		Division       string `json:"division"`
		TheSportsDBID  string `json:"thesportsdb_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "Invalid request body")
		return
	}
	if req.LeagueID == "" || req.Name == "" || req.Abbreviation == "" {
		writeError(w, http.StatusBadRequest, "missing_fields", "league_id, name, abbreviation required")
		return
	}

	var id string
	err := s.db.QueryRowContext(r.Context(), `
		INSERT INTO sports_teams (league_id, name, short_name, abbreviation, city, venue,
		                          primary_color, secondary_color, conference, division, thesportsdb_id)
		VALUES ($1, $2, NULLIF($3,''), $4, NULLIF($5,''), NULLIF($6,''),
		        NULLIF($7,''), NULLIF($8,''), NULLIF($9,''), NULLIF($10,''), NULLIF($11,''))
		RETURNING id`,
		req.LeagueID, req.Name, req.ShortName, req.Abbreviation,
		req.City, req.Venue, req.PrimaryColor, req.SecondaryColor,
		req.Conference, req.Division, req.TheSportsDBID).Scan(&id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Failed to create team")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"id": id})
}

// handleCreateEvent creates a new sports event (admin only).
func (s *Server) handleCreateEvent(w http.ResponseWriter, r *http.Request) {
	var req struct {
		LeagueID      string `json:"league_id"`
		HomeTeamID    string `json:"home_team_id"`
		AwayTeamID    string `json:"away_team_id"`
		Season        string `json:"season"`
		SeasonType    string `json:"season_type"`
		Week          string `json:"week"`
		Venue         string `json:"venue"`
		ScheduledTime string `json:"scheduled_time"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "Invalid request body")
		return
	}
	if req.LeagueID == "" || req.ScheduledTime == "" || req.Season == "" {
		writeError(w, http.StatusBadRequest, "missing_fields", "league_id, season, scheduled_time required")
		return
	}
	scheduledTime, err := time.Parse(time.RFC3339, req.ScheduledTime)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_time", "scheduled_time must be RFC3339")
		return
	}
	if req.SeasonType == "" {
		req.SeasonType = "regular"
	}

	var id string
	err = s.db.QueryRowContext(r.Context(), `
		INSERT INTO sports_events (league_id, home_team_id, away_team_id, season, season_type, week, venue, scheduled_time)
		VALUES ($1, NULLIF($2,'')::uuid, NULLIF($3,'')::uuid, $4, $5, NULLIF($6,''), NULLIF($7,''), $8)
		RETURNING id`,
		req.LeagueID, req.HomeTeamID, req.AwayTeamID,
		req.Season, req.SeasonType, req.Week, req.Venue, scheduledTime.UTC()).Scan(&id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Failed to create event")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"id": id})
}

// handleChannelMapping maps a sports event to a channel.
func (s *Server) handleChannelMapping(w http.ResponseWriter, r *http.Request) {
	eventID := chi.URLParam(r, "id")
	var req struct {
		ChannelID string `json:"channel_id"`
		StartTime string `json:"start_time"`
		EndTime   string `json:"end_time"`
		IsPrimary bool   `json:"is_primary"`
		Notes     string `json:"notes"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "Invalid request body")
		return
	}
	startTime, err := time.Parse(time.RFC3339, req.StartTime)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_time", "start_time must be RFC3339")
		return
	}
	endTime, err := time.Parse(time.RFC3339, req.EndTime)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_time", "end_time must be RFC3339")
		return
	}

	var id string
	err = s.db.QueryRowContext(r.Context(), `
		INSERT INTO sports_channel_mappings (event_id, channel_id, start_time, end_time, is_primary, notes)
		VALUES ($1, NULLIF($2,'')::uuid, $3, $4, $5, NULLIF($6,''))
		RETURNING id`,
		eventID, req.ChannelID, startTime.UTC(), endTime.UTC(), req.IsPrimary, req.Notes).Scan(&id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Failed to create channel mapping")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"id": id})
}

// handleAdminSync triggers a manual schedule sync for all leagues.
func (s *Server) handleAdminSync(w http.ResponseWriter, r *http.Request) {
	go s.syncAllLeagues(r.Context())
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "sync_started"})
}

// scanEvents scans query rows into a slice of Event.
func scanEvents(rows *sql.Rows) []Event {
	events := []Event{}
	for rows.Next() {
		var ev Event
		if err := rows.Scan(
			&ev.ID, &ev.LeagueID, &ev.HomeTeamID, &ev.AwayTeamID,
			&ev.Season, &ev.SeasonType, &ev.Week, &ev.Venue,
			&ev.ScheduledTime, &ev.ActualStartTime, &ev.Status,
			&ev.HomeScore, &ev.AwayScore, &ev.Period,
			&ev.PeriodScores, &ev.BroadcastInfo,
			&ev.TheSportsDBEventID, &ev.CreatedAt, &ev.UpdatedAt); err != nil {
			log.Printf("[sports] scan event: %v", err)
			continue
		}
		events = append(events, ev)
	}
	return events
}

