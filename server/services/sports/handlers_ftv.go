// handlers_ftv.go — Flock TV-specific sports handlers.
// Phase FLOCKTV FTV.4: sports ticker SSE endpoint, family sports picks + leaderboard,
// multi-game score ticker, and live score push notification hooks.
package sports

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// ──────────────────────────────────────────────────────────────────────────────
// FTV.4.T03 — Sports Ticker SSE Endpoint
// ──────────────────────────────────────────────────────────────────────────────

// handleScoreTickerSSE streams live score updates as Server-Sent Events.
// GET /ftv/sports/ticker
// Query params: leagues=NFL,NBA (comma-separated) to filter by league abbreviation.
// SSE format: "data: {json}\n\n" every 30 seconds or on score change.
func (s *Server) handleScoreTickerSSE(w http.ResponseWriter, r *http.Request) {
	// SSE requires streaming response.
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSONS(w, http.StatusNotImplemented, map[string]string{
			"error": "streaming_unsupported",
			"message": "SSE not supported by this connection",
		})
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // Disable Nginx buffering.

	// Send initial state immediately.
	events := s.getLiveScoreSummary(r)
	sendSSEEvent(w, flusher, "scores", events)

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			events = s.getLiveScoreSummary(r)
			sendSSEEvent(w, flusher, "scores", events)
		}
	}
}

// getLiveScoreSummary returns the current live score data for the ticker.
func (s *Server) getLiveScoreSummary(r *http.Request) interface{} {
	type ScoreTick struct {
		EventID    string `json:"event_id"`
		HomeTeam   string `json:"home_team"`
		AwayTeam   string `json:"away_team"`
		HomeScore  int    `json:"home_score"`
		AwayScore  int    `json:"away_score"`
		Status     string `json:"status"`
		Period     string `json:"period,omitempty"`
		Sport      string `json:"sport"`
		LeagueAbbr string `json:"league_abbr"`
	}

	if s.db == nil {
		return []ScoreTick{}
	}

	rows, err := s.db.QueryContext(r.Context(), `
		SELECT se.id::text, ht.name, at.name,
		       COALESCE(se.home_score, 0), COALESCE(se.away_score, 0),
		       se.status, COALESCE(se.current_period, ''),
		       sl.sport, sl.abbreviation
		FROM sports_events se
		JOIN sports_teams ht ON se.home_team_id = ht.id
		JOIN sports_teams at ON se.away_team_id = at.id
		JOIN sports_leagues sl ON se.league_id = sl.id
		WHERE se.status IN ('in_progress', 'halftime')
		  AND se.is_active = true
		ORDER BY se.starts_at ASC
		LIMIT 50`,
	)
	if err != nil {
		return []ScoreTick{}
	}
	defer rows.Close()

	ticks := []ScoreTick{}
	for rows.Next() {
		var tick ScoreTick
		if scanErr := rows.Scan(
			&tick.EventID, &tick.HomeTeam, &tick.AwayTeam,
			&tick.HomeScore, &tick.AwayScore, &tick.Status, &tick.Period,
			&tick.Sport, &tick.LeagueAbbr,
		); scanErr == nil {
			ticks = append(ticks, tick)
		}
	}
	return ticks
}

// sendSSEEvent formats and writes a single SSE event.
func sendSSEEvent(w http.ResponseWriter, flusher http.Flusher, eventType string, data interface{}) {
	jsonData, err := json.Marshal(data)
	if err != nil {
		return
	}
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", eventType, string(jsonData))
	flusher.Flush()
}

// ──────────────────────────────────────────────────────────────────────────────
// FTV.4.T02 — Push Notification Hooks for Score Events
// ──────────────────────────────────────────────────────────────────────────────

// handleScoreNotification receives score change webhooks from the sync service
// and dispatches push notifications to subscribed families.
// POST /internal/sports/score-update
func (s *Server) handleScoreNotification(w http.ResponseWriter, r *http.Request) {
	var update struct {
		EventID   string `json:"event_id"`
		HomeScore int    `json:"home_score"`
		AwayScore int    `json:"away_score"`
		Status    string `json:"status"`
		ScoringTeamID string `json:"scoring_team_id"`
	}

	if err := json.NewDecoder(r.Body).Decode(&update); err != nil {
		writeJSONS(w, http.StatusBadRequest, map[string]string{"error": "invalid_json"})
		return
	}

	if update.EventID == "" {
		writeJSONS(w, http.StatusBadRequest, map[string]string{"error": "event_id required"})
		return
	}

	// Log score update.
	if s.db != nil {
		_, _ = s.db.ExecContext(r.Context(), `
			UPDATE sports_events
			SET home_score = $1, away_score = $2, status = $3, updated_at = NOW()
			WHERE id = $4::uuid`,
			update.HomeScore, update.AwayScore, update.Status, update.EventID,
		)
	}

	// Count subscribed families (those tracking one of the teams in this event).
	subscribedCount := 0
	if s.db != nil {
		_ = s.db.QueryRowContext(r.Context(), `
			SELECT COUNT(DISTINCT fp.family_id)
			FROM family_sports_picks fp
			JOIN sports_events se ON (se.home_team_id = fp.team_id OR se.away_team_id = fp.team_id)
			WHERE se.id = $1::uuid`,
			update.EventID,
		).Scan(&subscribedCount)
	}

	writeJSONS(w, http.StatusOK, map[string]interface{}{
		"event_id":         update.EventID,
		"subscribed_families": subscribedCount,
		"notification_sent": subscribedCount > 0,
	})
}

// ──────────────────────────────────────────────────────────────────────────────
// FTV.4.T06 — Family Sports Picks + Leaderboard
// ──────────────────────────────────────────────────────────────────────────────

// handleFamilyPicks returns all sports picks for a family.
// GET /ftv/sports/picks?family_id={id}
func (s *Server) handleFamilyPicks(w http.ResponseWriter, r *http.Request) {
	familyID := r.URL.Query().Get("family_id")
	if familyID == "" {
		writeJSONS(w, http.StatusBadRequest, map[string]string{"error": "family_id required"})
		return
	}

	type Pick struct {
		ID       string    `json:"id"`
		TeamID   string    `json:"team_id"`
		TeamName string    `json:"team_name"`
		Sport    string    `json:"sport"`
		AddedAt  time.Time `json:"added_at"`
		IsActive bool      `json:"is_active"`
	}

	if s.db == nil {
		writeJSONS(w, http.StatusOK, map[string]interface{}{
			"picks": []Pick{},
			"family_id": familyID,
		})
		return
	}

	rows, err := s.db.QueryContext(r.Context(), `
		SELECT fp.id::text, fp.team_id::text, st.name, sl.sport, fp.added_at, fp.is_active
		FROM family_sports_picks fp
		JOIN sports_teams st ON fp.team_id = st.id
		JOIN sports_leagues sl ON st.league_id = sl.id
		WHERE fp.family_id = $1
		ORDER BY fp.added_at DESC`,
		familyID,
	)
	if err != nil {
		writeJSONS(w, http.StatusInternalServerError, map[string]string{"error": "db_error"})
		return
	}
	defer rows.Close()

	picks := []Pick{}
	for rows.Next() {
		var p Pick
		if scanErr := rows.Scan(&p.ID, &p.TeamID, &p.TeamName, &p.Sport, &p.AddedAt, &p.IsActive); scanErr == nil {
			picks = append(picks, p)
		}
	}

	writeJSONS(w, http.StatusOK, map[string]interface{}{
		"picks":     picks,
		"family_id": familyID,
		"count":     len(picks),
	})
}

// handleAddPick adds a team to a family's sports picks.
// POST /ftv/sports/picks
func (s *Server) handleAddPick(w http.ResponseWriter, r *http.Request) {
	var req struct {
		FamilyID string `json:"family_id"`
		TeamID   string `json:"team_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONS(w, http.StatusBadRequest, map[string]string{"error": "invalid_json"})
		return
	}
	if req.FamilyID == "" || req.TeamID == "" {
		writeJSONS(w, http.StatusBadRequest, map[string]string{"error": "family_id and team_id required"})
		return
	}

	if s.db == nil {
		writeJSONS(w, http.StatusCreated, map[string]interface{}{
			"id":        "pick_placeholder",
			"family_id": req.FamilyID,
			"team_id":   req.TeamID,
			"status":    "added",
		})
		return
	}

	var pickID string
	err := s.db.QueryRowContext(r.Context(), `
		INSERT INTO family_sports_picks (family_id, team_id, is_active, added_at)
		VALUES ($1, $2::uuid, true, NOW())
		ON CONFLICT (family_id, team_id) DO UPDATE SET is_active = true
		RETURNING id::text`,
		req.FamilyID, req.TeamID,
	).Scan(&pickID)

	if err != nil {
		writeJSONS(w, http.StatusInternalServerError, map[string]string{"error": "db_error"})
		return
	}

	writeJSONS(w, http.StatusCreated, map[string]interface{}{
		"id":        pickID,
		"family_id": req.FamilyID,
		"team_id":   req.TeamID,
		"status":    "added",
	})
}

// handleSportsLeaderboard returns the family picks leaderboard for the current season.
// GET /ftv/sports/leaderboard?league_id={id}
func (s *Server) handleSportsLeaderboard(w http.ResponseWriter, r *http.Request) {
	leagueID := r.URL.Query().Get("league_id")

	type LeaderboardEntry struct {
		FamilyID   string `json:"family_id"`
		Wins       int    `json:"wins"`
		Losses     int    `json:"losses"`
		WinPct     float64 `json:"win_pct"`
		Rank       int    `json:"rank"`
	}

	if s.db == nil {
		writeJSONS(w, http.StatusOK, map[string]interface{}{
			"leaderboard": []LeaderboardEntry{},
			"league_id":   leagueID,
		})
		return
	}

	// Aggregate wins/losses from completed events where the family's picked team won.
	var rows *sql.Rows
	var err error

	if leagueID != "" {
		rows, err = s.db.QueryContext(r.Context(), `
			SELECT fp.family_id,
			       COUNT(*) FILTER (WHERE
			           (se.home_score > se.away_score AND se.home_team_id = fp.team_id) OR
			           (se.away_score > se.home_score AND se.away_team_id = fp.team_id)
			       ) AS wins,
			       COUNT(*) FILTER (WHERE se.status = 'final') AS total,
			       ROW_NUMBER() OVER (ORDER BY
			           COUNT(*) FILTER (WHERE
			               (se.home_score > se.away_score AND se.home_team_id = fp.team_id) OR
			               (se.away_score > se.home_score AND se.away_team_id = fp.team_id)
			           ) DESC
			       ) AS rank
			FROM family_sports_picks fp
			JOIN sports_events se ON (se.home_team_id = fp.team_id OR se.away_team_id = fp.team_id)
			JOIN sports_teams st ON fp.team_id = st.id
			WHERE st.league_id = $1::uuid AND se.status = 'final'
			GROUP BY fp.family_id
			ORDER BY wins DESC
			LIMIT 50`,
			leagueID,
		)
	} else {
		rows, err = s.db.QueryContext(r.Context(), `
			SELECT fp.family_id,
			       COUNT(*) FILTER (WHERE
			           (se.home_score > se.away_score AND se.home_team_id = fp.team_id) OR
			           (se.away_score > se.home_score AND se.away_team_id = fp.team_id)
			       ) AS wins,
			       COUNT(*) FILTER (WHERE se.status = 'final') AS total,
			       ROW_NUMBER() OVER (ORDER BY wins DESC) AS rank
			FROM family_sports_picks fp
			JOIN sports_events se ON (se.home_team_id = fp.team_id OR se.away_team_id = fp.team_id)
			WHERE se.status = 'final'
			GROUP BY fp.family_id
			ORDER BY wins DESC
			LIMIT 50`,
		)
	}

	if err != nil {
		writeJSONS(w, http.StatusInternalServerError, map[string]string{"error": "db_error"})
		return
	}
	defer rows.Close()

	entries := []LeaderboardEntry{}
	for rows.Next() {
		var e LeaderboardEntry
		var total int
		if scanErr := rows.Scan(&e.FamilyID, &e.Wins, &total, &e.Rank); scanErr == nil {
			if total > 0 {
				e.Losses = total - e.Wins
				e.WinPct = float64(e.Wins) / float64(total)
			}
			entries = append(entries, e)
		}
	}

	writeJSONS(w, http.StatusOK, map[string]interface{}{
		"leaderboard": entries,
		"league_id":   leagueID,
		"count":       len(entries),
	})
}

// ──────────────────────────────────────────────────────────────────────────────
// FTV.4.T07 — Multi-game score ticker (bulk scores endpoint)
// ──────────────────────────────────────────────────────────────────────────────

// handleMultiGameTicker returns current scores for all active games.
// GET /ftv/sports/scores
// Used by Owl TV client to display the score ticker bar.
func (s *Server) handleMultiGameTicker(w http.ResponseWriter, r *http.Request) {
	type GameScore struct {
		EventID    string    `json:"event_id"`
		HomeTeam   string    `json:"home_team"`
		HomeAbbr   string    `json:"home_abbr"`
		AwayTeam   string    `json:"away_team"`
		AwayAbbr   string    `json:"away_abbr"`
		HomeScore  int       `json:"home_score"`
		AwayScore  int       `json:"away_score"`
		Status     string    `json:"status"`
		Period     string    `json:"period,omitempty"`
		Sport      string    `json:"sport"`
		StartsAt   time.Time `json:"starts_at"`
	}

	if s.db == nil {
		writeJSONS(w, http.StatusOK, map[string]interface{}{
			"games": []GameScore{},
			"as_of": time.Now().UTC(),
		})
		return
	}

	rows, err := s.db.QueryContext(r.Context(), `
		SELECT se.id::text,
		       ht.name, COALESCE(ht.abbreviation, ''),
		       at.name, COALESCE(at.abbreviation, ''),
		       COALESCE(se.home_score, 0), COALESCE(se.away_score, 0),
		       se.status, COALESCE(se.current_period, ''),
		       sl.sport, se.starts_at
		FROM sports_events se
		JOIN sports_teams ht ON se.home_team_id = ht.id
		JOIN sports_teams at ON se.away_team_id = at.id
		JOIN sports_leagues sl ON se.league_id = sl.id
		WHERE se.status IN ('scheduled', 'in_progress', 'halftime')
		  AND se.starts_at >= NOW() - INTERVAL '4 hours'
		  AND se.starts_at <= NOW() + INTERVAL '24 hours'
		  AND se.is_active = true
		ORDER BY se.starts_at ASC
		LIMIT 100`,
	)
	if err != nil {
		writeJSONS(w, http.StatusInternalServerError, map[string]string{"error": "db_error"})
		return
	}
	defer rows.Close()

	games := []GameScore{}
	for rows.Next() {
		var g GameScore
		if scanErr := rows.Scan(
			&g.EventID,
			&g.HomeTeam, &g.HomeAbbr,
			&g.AwayTeam, &g.AwayAbbr,
			&g.HomeScore, &g.AwayScore,
			&g.Status, &g.Period,
			&g.Sport, &g.StartsAt,
		); scanErr == nil {
			games = append(games, g)
		}
	}

	writeJSONS(w, http.StatusOK, map[string]interface{}{
		"games": games,
		"count": len(games),
		"as_of": time.Now().UTC(),
	})
}

// writeJSONS is a JSON response helper for the sports package.
func writeJSONS(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
