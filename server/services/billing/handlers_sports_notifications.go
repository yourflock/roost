// handlers_sports_notifications.go — Sports notification preferences and pre-game alerts.
// P15-T06: Subscriber sport preference management and pre-game notification stub.
package billing

import (
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
	"time"
)

// ---- types ------------------------------------------------------------------

type sportsPrefRequest struct {
	TeamID            string `json:"team_id"`
	NotificationLevel string `json:"notification_level"` // "all", "game_start", "score_changes", "none"
	AutoDVR           *bool  `json:"auto_dvr"`
}

type sportsPref struct {
	ID                string    `json:"id"`
	TeamID            string    `json:"team_id"`
	TeamName          string    `json:"team_name"`
	LeagueName        string    `json:"league_name"`
	NotificationLevel string    `json:"notification_level"`
	AutoDVR           bool      `json:"auto_dvr"`
	CreatedAt         time.Time `json:"created_at"`
}

type myGame struct {
	EventID       string    `json:"event_id"`
	LeagueName    string    `json:"league_name"`
	HomeTeam      string    `json:"home_team,omitempty"`
	AwayTeam      string    `json:"away_team,omitempty"`
	ScheduledTime time.Time `json:"scheduled_time"`
	Status        string    `json:"status"`
	HomeScore     int       `json:"home_score"`
	AwayScore     int       `json:"away_score"`
	ChannelSlug   *string   `json:"channel_slug,omitempty"`
}

// ---- handlers ---------------------------------------------------------------

// POST /sports/preferences — save subscriber's favourite teams + notification prefs.
func (s *Server) handleSportsPreferences(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST required")
		return
	}
	subscriberID := r.Header.Get("X-Subscriber-ID")
	if subscriberID == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Subscriber session required")
		return
	}

	var req sportsPrefRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "Invalid request body")
		return
	}
	if req.TeamID == "" {
		writeError(w, http.StatusBadRequest, "missing_team", "team_id is required")
		return
	}
	if req.NotificationLevel == "" {
		req.NotificationLevel = "all"
	}
	autoDVR := true
	if req.AutoDVR != nil {
		autoDVR = *req.AutoDVR
	}

	if s.db == nil {
		writeError(w, http.StatusServiceUnavailable, "db_unavailable", "Database not connected")
		return
	}
	_, err := s.db.ExecContext(r.Context(), `
		INSERT INTO subscriber_sports_preferences
		  (subscriber_id, team_id, notification_level, auto_dvr)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (subscriber_id, COALESCE(profile_id, '00000000-0000-0000-0000-000000000000'::uuid), team_id)
		DO UPDATE SET notification_level = EXCLUDED.notification_level, auto_dvr = EXCLUDED.auto_dvr`,
		subscriberID, req.TeamID, req.NotificationLevel, autoDVR)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Failed to save preference")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"saved"}`))
}

// GET /sports/preferences — get subscriber's sports preferences.
func (s *Server) handleGetSportsPreferences(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET required")
		return
	}
	subscriberID := r.Header.Get("X-Subscriber-ID")
	if subscriberID == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Subscriber session required")
		return
	}

	if s.db == nil {
		writeError(w, http.StatusServiceUnavailable, "db_unavailable", "Database not connected")
		return
	}
	rows, err := s.db.QueryContext(r.Context(), `
		SELECT ssp.id, ssp.team_id, st.name, sl.name,
		       ssp.notification_level, ssp.auto_dvr, ssp.created_at
		FROM subscriber_sports_preferences ssp
		JOIN sports_teams st ON st.id = ssp.team_id
		JOIN sports_leagues sl ON sl.id = st.league_id
		WHERE ssp.subscriber_id = $1
		ORDER BY sl.sort_order, st.name`, subscriberID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Failed to get preferences")
		return
	}
	defer rows.Close()

	prefs := []sportsPref{}
	for rows.Next() {
		var p sportsPref
		if err := rows.Scan(&p.ID, &p.TeamID, &p.TeamName, &p.LeagueName,
			&p.NotificationLevel, &p.AutoDVR, &p.CreatedAt); err != nil {
			continue
		}
		prefs = append(prefs, p)
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"preferences": prefs})
}

// GET /sports/my-games — upcoming/live games for subscriber's teams.
func (s *Server) handleMyGames(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET required")
		return
	}
	subscriberID := r.Header.Get("X-Subscriber-ID")
	if subscriberID == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Subscriber session required")
		return
	}

	if s.db == nil {
		writeError(w, http.StatusServiceUnavailable, "db_unavailable", "Database not connected")
		return
	}
	rows, err := s.db.QueryContext(r.Context(), `
		SELECT DISTINCT se.id, sl.name,
		       ht.name, at.name,
		       se.scheduled_time, se.status, se.home_score, se.away_score,
		       c.slug
		FROM subscriber_sports_preferences ssp
		JOIN sports_events se ON (se.home_team_id = ssp.team_id OR se.away_team_id = ssp.team_id)
		JOIN sports_leagues sl ON sl.id = se.league_id
		LEFT JOIN sports_teams ht ON ht.id = se.home_team_id
		LEFT JOIN sports_teams at ON at.id = se.away_team_id
		LEFT JOIN sports_channel_mappings scm ON scm.event_id = se.id AND scm.is_primary = true
		LEFT JOIN channels c ON c.id = scm.channel_id
		WHERE ssp.subscriber_id = $1
		  AND se.scheduled_time >= now() - interval '3 hours'
		  AND se.status != 'cancelled'
		ORDER BY se.scheduled_time
		LIMIT 30`, subscriberID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "Failed to get games")
		return
	}
	defer rows.Close()

	games := []myGame{}
	for rows.Next() {
		var g myGame
		var homeTeam, awayTeam sql.NullString
		if err := rows.Scan(&g.EventID, &g.LeagueName,
			&homeTeam, &awayTeam,
			&g.ScheduledTime, &g.Status, &g.HomeScore, &g.AwayScore,
			&g.ChannelSlug); err != nil {
			continue
		}
		if homeTeam.Valid {
			g.HomeTeam = homeTeam.String
		}
		if awayTeam.Valid {
			g.AwayTeam = awayTeam.String
		}
		games = append(games, g)
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"games": games})
}

// ---- background notifier stub -----------------------------------------------

// sportsPregameNotifier checks every 15 minutes for games starting in the next
// 30 minutes and queues push notifications for subscribers who have opted in.
//
// TODO: implement actual push delivery (APNS/FCM/web push) when push provider is
// configured. Currently logs intent only.
func (s *Server) sportsPregameNotifier() {
	ticker := time.NewTicker(15 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		s.checkPregameAlerts()
	}
}

func (s *Server) checkPregameAlerts() {
	window := time.Now().Add(30 * time.Minute)
	rows, err := s.db.Query(`
		SELECT se.id, sl.name, ht.name, at.name, se.scheduled_time,
		       ssp.subscriber_id, ssp.notification_level
		FROM subscriber_sports_preferences ssp
		JOIN sports_events se ON (se.home_team_id = ssp.team_id OR se.away_team_id = ssp.team_id)
		JOIN sports_leagues sl ON sl.id = se.league_id
		LEFT JOIN sports_teams ht ON ht.id = se.home_team_id
		LEFT JOIN sports_teams at ON at.id = se.away_team_id
		WHERE se.status = 'scheduled'
		  AND se.scheduled_time BETWEEN now() AND $1
		  AND ssp.notification_level IN ('all', 'game_start')`, window)
	if err != nil {
		log.Printf("[sports-notifier] query error: %v", err)
		return
	}
	defer rows.Close()

	count := 0
	for rows.Next() {
		var eventID, leagueName, subscriberID, notifLevel string
		var homeTeam, awayTeam sql.NullString
		var scheduledTime time.Time
		if err := rows.Scan(&eventID, &leagueName, &homeTeam, &awayTeam,
			&scheduledTime, &subscriberID, &notifLevel); err != nil {
			continue
		}
		// TODO: implement push notification delivery (APNS/FCM/web push)
		// For now, just log the intent.
		home := ""
		if homeTeam.Valid {
			home = homeTeam.String
		}
		away := ""
		if awayTeam.Valid {
			away = awayTeam.String
		}
		log.Printf("[sports-notifier] TODO: notify subscriber %s — %s %s vs %s at %s",
			subscriberID, leagueName, home, away,
			scheduledTime.Format("15:04"))
		count++
	}
	if count > 0 {
		log.Printf("[sports-notifier] %d pre-game alerts to deliver (push not yet implemented)", count)
	}
}
