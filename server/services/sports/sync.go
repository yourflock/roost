// sync.go — Background sync goroutines and TheSportsDB integration.
// P15-T02: dailySync + gameDay30sPoller + league/team logo sync.
package sports

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"time"
)

const (
	theSportsDBBase = "https://www.thesportsdb.com/api/v1/json/3"
	httpTimeout     = 15 * time.Second
)

// DailySync runs a ticker every 24h, syncing schedules for all active leagues.
// Intended to be run as a background goroutine.
func (s *Server) DailySync(ctx context.Context) {
	// Run once immediately on startup
	s.syncAllLeagues(ctx)

	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.syncAllLeagues(ctx)
		}
	}
}

// GameDay30sPoller polls live scores every 30 seconds for in-progress events.
// Intended to be run as a background goroutine.
func (s *Server) GameDay30sPoller(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.pollLiveScores(ctx); err != nil {
				log.Printf("[sports] live score poll error: %v", err)
			}
		}
	}
}

// syncAllLeagues fetches the schedule for every active league from TheSportsDB.
func (s *Server) syncAllLeagues(ctx context.Context) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, abbreviation, thesportsdb_id FROM sports_leagues WHERE is_active = true AND thesportsdb_id IS NOT NULL`)
	if err != nil {
		log.Printf("[sports] syncAllLeagues db error: %v", err)
		return
	}
	defer rows.Close()

	for rows.Next() {
		var id, abbr string
		var tsdbID sql.NullString
		if err := rows.Scan(&id, &abbr, &tsdbID); err != nil {
			continue
		}
		if !tsdbID.Valid {
			continue
		}
		if err := s.SyncLeagueSchedule(ctx, abbr, tsdbID.String); err != nil {
			log.Printf("[sports] sync league %s error: %v", abbr, err)
		} else {
			log.Printf("[sports] synced league %s", abbr)
		}
	}
}

// SyncLeagueSchedule fetches upcoming events for a league from TheSportsDB and upserts them.
func (s *Server) SyncLeagueSchedule(ctx context.Context, leagueAbbr, theSportsDBLeagueID string) error {
	url := fmt.Sprintf("%s/eventsnextleague.php?id=%s", theSportsDBBase, theSportsDBLeagueID)

	client := &http.Client{Timeout: httpTimeout}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("fetch events: %w", err)
	}
	defer resp.Body.Close()

	var result TheSportsDBEventsResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}

	// Look up league id from abbreviation
	var leagueID string
	err = s.db.QueryRowContext(ctx, `SELECT id FROM sports_leagues WHERE abbreviation = $1`, leagueAbbr).Scan(&leagueID)
	if err != nil {
		return fmt.Errorf("league not found: %s: %w", leagueAbbr, err)
	}

	for _, ev := range result.Events {
		if ev.IDEvent == "" || ev.DateEvent == "" {
			continue
		}

		// Parse scheduled time: DateEvent = "2026-09-08", StrTime = "17:20:00"
		timeStr := ev.DateEvent + "T"
		if ev.StrTime != "" {
			timeStr += ev.StrTime
		} else {
			timeStr += "00:00:00"
		}
		scheduledTime, err := time.Parse("2006-01-02T15:04:05", timeStr)
		if err != nil {
			log.Printf("[sports] parse time %q: %v", timeStr, err)
			continue
		}

		// Resolve home/away team IDs (may not exist yet — use NULL if not found)
		homeTeamID := s.resolveTeamID(ctx, leagueID, ev.IDHomeTeam)
		awayTeamID := s.resolveTeamID(ctx, leagueID, ev.IDAwayTeam)

		// Determine status
		status := "scheduled"
		switch ev.StrStatus {
		case "Match Finished", "FT", "AET", "PEN":
			status = "final"
		case "In Progress", "HT", "1H", "2H", "ET", "P":
			status = "live"
		case "Postponed":
			status = "postponed"
		case "Cancelled", "Abandoned":
			status = "cancelled"
		}

		homeScore := 0
		awayScore := 0
		if ev.IntHomeScore != nil {
			if v, err := strconv.Atoi(*ev.IntHomeScore); err == nil {
				homeScore = v
			}
		}
		if ev.IntAwayScore != nil {
			if v, err := strconv.Atoi(*ev.IntAwayScore); err == nil {
				awayScore = v
			}
		}

		_, err = s.db.ExecContext(ctx, `
			INSERT INTO sports_events
			  (league_id, home_team_id, away_team_id, season, season_type, week,
			   venue, scheduled_time, status, home_score, away_score, thesportsdb_event_id)
			VALUES ($1, $2, $3, $4, 'regular', $5, $6, $7, $8, $9, $10, $11)
			ON CONFLICT (thesportsdb_event_id) DO UPDATE SET
			  status = EXCLUDED.status,
			  home_score = EXCLUDED.home_score,
			  away_score = EXCLUDED.away_score,
			  updated_at = now()
			WHERE sports_events.thesportsdb_event_id IS NOT NULL`,
			leagueID, homeTeamID, awayTeamID, ev.StrSeason, ev.IntRound,
			nullableString(ev.StrVenue), scheduledTime.UTC(), status, homeScore, awayScore, ev.IDEvent)
		if err != nil {
			log.Printf("[sports] upsert event %s: %v", ev.IDEvent, err)
		}
	}
	return nil
}

// SyncTeamLogos fetches team logo URLs from TheSportsDB and updates sports_teams.
func (s *Server) SyncTeamLogos(ctx context.Context, leagueID string) error {
	// Get thesportsdb_id for the league
	var tsdbLeagueID sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT thesportsdb_id FROM sports_leagues WHERE id = $1`, leagueID).Scan(&tsdbLeagueID)
	if err != nil || !tsdbLeagueID.Valid {
		return fmt.Errorf("league not found or missing thesportsdb_id: %w", err)
	}

	url := fmt.Sprintf("%s/lookup_all_teams.php?id=%s", theSportsDBBase, tsdbLeagueID.String)
	client := &http.Client{Timeout: httpTimeout}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("fetch teams: %w", err)
	}
	defer resp.Body.Close()

	var result TheSportsDBTeamsResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}

	for _, t := range result.Teams {
		if t.StrTeamBadge == "" {
			continue
		}
		_, err := s.db.ExecContext(ctx, `
			UPDATE sports_teams SET logo_url = $1, updated_at = now()
			WHERE league_id = $2 AND thesportsdb_id = $3`,
			t.StrTeamBadge, leagueID, t.IDTeam)
		if err != nil {
			log.Printf("[sports] update logo for team %s: %v", t.IDTeam, err)
		}
	}
	return nil
}

// pollLiveScores updates home_score/away_score/period/status for currently live events.
func (s *Server) pollLiveScores(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `
		SELECT se.id, se.thesportsdb_event_id, sl.thesportsdb_id
		FROM sports_events se
		JOIN sports_leagues sl ON sl.id = se.league_id
		WHERE se.status = 'live'
		  AND se.thesportsdb_event_id IS NOT NULL`)
	if err != nil {
		return fmt.Errorf("query live events: %w", err)
	}
	defer rows.Close()

	type liveRow struct {
		eventID   string
		tsdbEvent string
		tsdbLeague string
	}
	var liveRows []liveRow
	for rows.Next() {
		var lr liveRow
		if err := rows.Scan(&lr.eventID, &lr.tsdbEvent, &lr.tsdbLeague); err != nil {
			continue
		}
		liveRows = append(liveRows, lr)
	}
	rows.Close()

	// Group by league to minimize API calls
	leagueEvents := map[string][]liveRow{}
	for _, lr := range liveRows {
		leagueEvents[lr.tsdbLeague] = append(leagueEvents[lr.tsdbLeague], lr)
	}

	client := &http.Client{Timeout: httpTimeout}
	for tsdbLeagueID, events := range leagueEvents {
		url := fmt.Sprintf("%s/eventslive.php?id=%s", theSportsDBBase, tsdbLeagueID)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			continue
		}
		resp, err := client.Do(req)
		if err != nil {
			log.Printf("[sports] live scores fetch for league %s: %v", tsdbLeagueID, err)
			continue
		}

		var result TheSportsDBEventsResponse
		decodeErr := json.NewDecoder(resp.Body).Decode(&result)
		resp.Body.Close()
		if decodeErr != nil {
			continue
		}

		// Build a lookup map
		liveMap := map[string]TheSportsDBEvent{}
		for _, ev := range result.Events {
			liveMap[ev.IDEvent] = ev
		}

		for _, lr := range events {
			ev, ok := liveMap[lr.tsdbEvent]
			if !ok {
				continue
			}
			homeScore := 0
			awayScore := 0
			if ev.IntHomeScore != nil {
				if v, err := strconv.Atoi(*ev.IntHomeScore); err == nil {
					homeScore = v
				}
			}
			if ev.IntAwayScore != nil {
				if v, err := strconv.Atoi(*ev.IntAwayScore); err == nil {
					awayScore = v
				}
			}
			status := "live"
			switch ev.StrStatus {
			case "Match Finished", "FT", "AET", "PEN":
				status = "final"
			case "Postponed":
				status = "postponed"
			}
			_, err := s.db.ExecContext(ctx, `
				UPDATE sports_events
				SET home_score = $1, away_score = $2, status = $3, period = $4, updated_at = now()
				WHERE id = $5`,
				homeScore, awayScore, status, nullableString(ev.StrStatus), lr.eventID)
			if err != nil {
				log.Printf("[sports] update live score for %s: %v", lr.eventID, err)
			}
		}
	}
	return nil
}

// resolveTeamID looks up a sports_team by its thesportsdb_id within a league.
// Returns nil if not found (team not yet seeded).
func (s *Server) resolveTeamID(ctx context.Context, leagueID, tsdbTeamID string) *string {
	if tsdbTeamID == "" {
		return nil
	}
	var id string
	err := s.db.QueryRowContext(ctx,
		`SELECT id FROM sports_teams WHERE league_id = $1 AND thesportsdb_id = $2`,
		leagueID, tsdbTeamID).Scan(&id)
	if err != nil {
		return nil
	}
	return &id
}

// nullableString returns nil for empty strings.
func nullableString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
