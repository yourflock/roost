// sports_stream.go — Sports-aware streaming endpoints for the Owl Addon API.
// P15-T04: Subscriber sports preferences and game-annotated stream responses.
package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// ---- types ------------------------------------------------------------------

// sportsEventAnnotation is embedded in stream responses when a channel is
// currently airing a known sports event.
type sportsEventAnnotation struct {
	ID            string    `json:"id"`
	LeagueName    string    `json:"league_name"`
	LeagueAbbr    string    `json:"league_abbreviation"`
	HomeTeam      string    `json:"home_team,omitempty"`
	AwayTeam      string    `json:"away_team,omitempty"`
	HomeScore     int       `json:"home_score"`
	AwayScore     int       `json:"away_score"`
	Status        string    `json:"status"`
	Period        *string   `json:"period,omitempty"`
	ScheduledTime time.Time `json:"scheduled_time"`
	IsPrimary     bool      `json:"is_primary_broadcast"`
}

// upcomingSportsEvent is returned by GET /owl/sports/events.
type upcomingSportsEvent struct {
	ID            string    `json:"id"`
	LeagueAbbr    string    `json:"league_abbreviation"`
	HomeTeam      string    `json:"home_team,omitempty"`
	AwayTeam      string    `json:"away_team,omitempty"`
	HomeTeamLogo  *string   `json:"home_team_logo,omitempty"`
	AwayTeamLogo  *string   `json:"away_team_logo,omitempty"`
	ScheduledTime time.Time `json:"scheduled_time"`
	Status        string    `json:"status"`
	HomeScore     int       `json:"home_score"`
	AwayScore     int       `json:"away_score"`
	ChannelSlug   *string   `json:"channel_slug,omitempty"`
	ChannelName   *string   `json:"channel_name,omitempty"`
}

// favTeam is a subscriber's favourite team.
type favTeam struct {
	ID                string  `json:"id"`
	TeamID            string  `json:"team_id"`
	TeamName          string  `json:"team_name"`
	TeamAbbreviation  string  `json:"team_abbreviation"`
	TeamLogoURL       *string `json:"team_logo_url,omitempty"`
	LeagueName        string  `json:"league_name"`
	LeagueAbbr        string  `json:"league_abbreviation"`
	NotificationLevel string  `json:"notification_level"`
	AutoDVR           bool    `json:"auto_dvr"`
}

// ---- handlers ---------------------------------------------------------------

// GET /owl/sports/events — upcoming/live events for subscriber's favourite teams.
func (s *server) handleSportsEvents(w http.ResponseWriter, r *http.Request) {
	subscriberID := r.Header.Get("X-Subscriber-ID")
	if subscriberID == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Session required")
		return
	}

	rows, err := s.db.QueryContext(r.Context(), `
		SELECT DISTINCT se.id, sl.abbreviation,
		       ht.name AS home_team, at.name AS away_team,
		       ht.logo_url, at.logo_url,
		       se.scheduled_time, se.status, se.home_score, se.away_score,
		       c.slug AS channel_slug, c.name AS channel_name
		FROM subscriber_sports_preferences ssp
		JOIN sports_events se ON (se.home_team_id = ssp.team_id OR se.away_team_id = ssp.team_id)
		JOIN sports_leagues sl ON sl.id = se.league_id
		LEFT JOIN sports_teams ht ON ht.id = se.home_team_id
		LEFT JOIN sports_teams at ON at.id = se.away_team_id
		LEFT JOIN sports_channel_mappings scm ON scm.event_id = se.id AND scm.is_primary = true
		LEFT JOIN channels c ON c.id = scm.channel_id
		WHERE ssp.subscriber_id = $1
		  AND se.scheduled_time >= now() - interval '3 hours'
		  AND se.scheduled_time <= now() + interval '7 days'
		  AND se.status != 'cancelled'
		ORDER BY se.scheduled_time
		LIMIT 50`, subscriberID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Failed to get sports events")
		return
	}
	defer rows.Close()

	events := []upcomingSportsEvent{}
	for rows.Next() {
		var ev upcomingSportsEvent
		var homeTeam, awayTeam sql.NullString
		if err := rows.Scan(
			&ev.ID, &ev.LeagueAbbr,
			&homeTeam, &awayTeam,
			&ev.HomeTeamLogo, &ev.AwayTeamLogo,
			&ev.ScheduledTime, &ev.Status, &ev.HomeScore, &ev.AwayScore,
			&ev.ChannelSlug, &ev.ChannelName,
		); err != nil {
			continue
		}
		if homeTeam.Valid {
			ev.HomeTeam = homeTeam.String
		}
		if awayTeam.Valid {
			ev.AwayTeam = awayTeam.String
		}
		events = append(events, ev)
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"events": events})
}

// GET /owl/sports/live — currently live games on channels the subscriber can access.
func (s *server) handleSportsLive(w http.ResponseWriter, r *http.Request) {
	subscriberID := r.Header.Get("X-Subscriber-ID")
	if subscriberID == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Session required")
		return
	}

	rows, err := s.db.QueryContext(r.Context(), `
		SELECT se.id, sl.abbreviation,
		       ht.name AS home_team, at.name AS away_team,
		       se.home_score, se.away_score, se.status, se.period,
		       se.scheduled_time, c.slug, c.name
		FROM sports_events se
		JOIN sports_leagues sl ON sl.id = se.league_id
		LEFT JOIN sports_teams ht ON ht.id = se.home_team_id
		LEFT JOIN sports_teams at ON at.id = se.away_team_id
		JOIN sports_channel_mappings scm ON scm.event_id = se.id
		JOIN channels c ON c.id = scm.channel_id
		JOIN subscription_channels sc2 ON sc2.channel_id = c.id
		JOIN subscriptions sub ON sub.id = sc2.subscription_id
		                       AND sub.subscriber_id = $1
		                       AND sub.status IN ('active', 'trialing')
		WHERE se.status = 'live'
		ORDER BY se.scheduled_time
		LIMIT 30`, subscriberID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Failed to get live sports")
		return
	}
	defer rows.Close()

	events := []map[string]interface{}{}
	for rows.Next() {
		var id, leagueAbbr, status, channelSlug, channelName string
		var homeTeam, awayTeam sql.NullString
		var period *string
		var homeScore, awayScore int
		var scheduledTime time.Time
		if err := rows.Scan(&id, &leagueAbbr, &homeTeam, &awayTeam,
			&homeScore, &awayScore, &status, &period, &scheduledTime,
			&channelSlug, &channelName); err != nil {
			continue
		}
		ev := map[string]interface{}{
			"id":             id,
			"league":         leagueAbbr,
			"home_score":     homeScore,
			"away_score":     awayScore,
			"status":         status,
			"scheduled_time": scheduledTime,
			"channel_slug":   channelSlug,
			"channel_name":   channelName,
		}
		if homeTeam.Valid {
			ev["home_team"] = homeTeam.String
		}
		if awayTeam.Valid {
			ev["away_team"] = awayTeam.String
		}
		if period != nil {
			ev["period"] = *period
		}
		events = append(events, ev)
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"events": events, "count": len(events)})
}

// GET /owl/sports/teams — subscriber's favourite teams.
func (s *server) handleGetSportsTeams(w http.ResponseWriter, r *http.Request) {
	subscriberID := r.Header.Get("X-Subscriber-ID")
	if subscriberID == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Session required")
		return
	}

	rows, err := s.db.QueryContext(r.Context(), `
		SELECT ssp.id, ssp.team_id, st.name, st.abbreviation, st.logo_url,
		       sl.name, sl.abbreviation, ssp.notification_level, ssp.auto_dvr
		FROM subscriber_sports_preferences ssp
		JOIN sports_teams st ON st.id = ssp.team_id
		JOIN sports_leagues sl ON sl.id = st.league_id
		WHERE ssp.subscriber_id = $1
		ORDER BY sl.sort_order, st.name`, subscriberID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Failed to get favourite teams")
		return
	}
	defer rows.Close()

	teams := []favTeam{}
	for rows.Next() {
		var t favTeam
		if err := rows.Scan(&t.ID, &t.TeamID, &t.TeamName, &t.TeamAbbreviation,
			&t.TeamLogoURL, &t.LeagueName, &t.LeagueAbbr, &t.NotificationLevel, &t.AutoDVR); err != nil {
			continue
		}
		teams = append(teams, t)
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"teams": teams})
}

// handleSportsTeamsFavorite dispatches POST/DELETE /owl/sports/teams/:id/favorite.
func (s *server) handleSportsTeamsFavorite(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		s.handleAddFavoriteTeam(w, r)
	case http.MethodDelete:
		s.handleRemoveFavoriteTeam(w, r)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST or DELETE required")
	}
}

// POST /owl/sports/teams/:id/favorite — add a team to subscriber's favourites.
func (s *server) handleAddFavoriteTeam(w http.ResponseWriter, r *http.Request) {
	subscriberID := r.Header.Get("X-Subscriber-ID")
	if subscriberID == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Session required")
		return
	}
	teamID := extractSportsTeamID(r.URL.Path)

	var body struct {
		NotificationLevel string `json:"notification_level"`
		AutoDVR           *bool  `json:"auto_dvr"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.NotificationLevel == "" {
		body.NotificationLevel = "all"
	}
	autoDVR := true
	if body.AutoDVR != nil {
		autoDVR = *body.AutoDVR
	}

	_, err := s.db.ExecContext(r.Context(), `
		INSERT INTO subscriber_sports_preferences (subscriber_id, team_id, notification_level, auto_dvr)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (subscriber_id, COALESCE(profile_id, '00000000-0000-0000-0000-000000000000'::uuid), team_id)
		DO UPDATE SET notification_level = EXCLUDED.notification_level, auto_dvr = EXCLUDED.auto_dvr`,
		subscriberID, teamID, body.NotificationLevel, autoDVR)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Failed to add favourite team")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "added", "team_id": teamID})
}

// DELETE /owl/sports/teams/:id/favorite — remove a team from subscriber's favourites.
func (s *server) handleRemoveFavoriteTeam(w http.ResponseWriter, r *http.Request) {
	subscriberID := r.Header.Get("X-Subscriber-ID")
	if subscriberID == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Session required")
		return
	}
	teamID := extractSportsTeamID(r.URL.Path)

	_, err := s.db.ExecContext(r.Context(), `
		DELETE FROM subscriber_sports_preferences
		WHERE subscriber_id = $1 AND team_id = $2`, subscriberID, teamID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Failed to remove favourite team")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "removed", "team_id": teamID})
}

// getSportsEventForChannel looks up any sports event currently mapped to a channel.
// Returns nil if no event is live or starting soon on this channel.
func (s *server) getSportsEventForChannel(channelID string) *sportsEventAnnotation {
	now := time.Now().UTC()
	var ev sportsEventAnnotation
	var homeTeam, awayTeam sql.NullString
	err := s.db.QueryRow(`
		SELECT se.id, sl.name, sl.abbreviation,
		       ht.name, at.name,
		       se.home_score, se.away_score, se.status, se.period,
		       se.scheduled_time, scm.is_primary
		FROM sports_channel_mappings scm
		JOIN sports_events se ON se.id = scm.event_id
		JOIN sports_leagues sl ON sl.id = se.league_id
		LEFT JOIN sports_teams ht ON ht.id = se.home_team_id
		LEFT JOIN sports_teams at ON at.id = se.away_team_id
		WHERE scm.channel_id = $1
		  AND scm.start_time <= $2 AND scm.end_time >= $2
		  AND se.status IN ('live', 'scheduled')
		ORDER BY scm.is_primary DESC, se.scheduled_time
		LIMIT 1`, channelID, now).Scan(
		&ev.ID, &ev.LeagueName, &ev.LeagueAbbr,
		&homeTeam, &awayTeam,
		&ev.HomeScore, &ev.AwayScore, &ev.Status, &ev.Period,
		&ev.ScheduledTime, &ev.IsPrimary)
	if err != nil {
		return nil
	}
	if homeTeam.Valid {
		ev.HomeTeam = homeTeam.String
	}
	if awayTeam.Valid {
		ev.AwayTeam = awayTeam.String
	}
	return &ev
}

// extractSportsTeamID pulls the team UUID from paths like:
//   /owl/sports/teams/{id}/favorite
func extractSportsTeamID(path string) string {
	// Strip trailing /favorite
	path = strings.TrimSuffix(path, "/favorite")
	// Strip leading /owl/sports/teams/
	parts := strings.Split(path, "/")
	if len(parts) > 0 {
		return parts[len(parts)-1]
	}
	return ""
}
