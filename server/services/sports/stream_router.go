// stream_router.go — Best-source stream routing for live sports games.
// OSG.3.001: Select the highest-priority healthy source for a game and return
// a playable HLS URL. Creates auto-assignments on first routing.
// OSG.4.001: Stale-assignment detection — skip 'down' assignments inline.
package sports

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"net/http"

	"github.com/go-chi/chi/v5"
)

// ErrNoSourceAvailable is returned when no healthy source can be found for a game.
var ErrNoSourceAvailable = errors.New("no stream source available for this game")

// streamResult is the data returned from selectBestSourceForGame.
type streamResult struct {
	ID           string // sports_source_channels.id
	SourceID     string // sports_stream_sources.id
	ChannelURL   string
	SourceType   string
	HealthStatus string
}

// handleGetStream is the HTTP handler for GET /sports/events/{id}/stream.
// Returns the best available stream URL for the requested game.
// OSG.4.001: Stale assignment detection — if the cached assignment points at a
// 'down' source, it bypasses the cache and re-selects.
func (s *Server) handleGetStream(w http.ResponseWriter, r *http.Request) {
	gameID := chi.URLParam(r, "id")

	result, err := selectBestSourceForGame(r.Context(), s.db, gameID)
	if errors.Is(err, ErrNoSourceAvailable) {
		writeError(w, http.StatusNotFound, "no_stream_available",
			"No stream available for this game")
		return
	}
	if err != nil {
		log.Printf("[sports/router] game %s: %v", gameID, err)
		writeError(w, http.StatusInternalServerError, "db_error", "Failed to resolve stream")
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"stream_url":    result.ChannelURL,
		"source_id":     result.SourceID,
		"source_type":   result.SourceType,
		"health_status": result.HealthStatus,
	})
}

// selectBestSourceForGame implements the three-tier routing priority:
//
//  1. Active assignment in sports_game_source_assignments — if source is healthy/degraded.
//     OSG.4.001: If the assigned source is 'down', skip the cache and fall through.
//  2. Best auto-matched channel from sports_source_channels for the game's league.
//  3. ErrNoSourceAvailable if nothing is usable.
//
// When an auto-matched source is selected for a game with no active assignment,
// a new assignment row is inserted (assigned_by='auto') for fast future lookups.
func selectBestSourceForGame(ctx context.Context, db *sql.DB, gameID string) (streamResult, error) {
	// Step 1: Check for an active assignment
	var cached streamResult
	err := db.QueryRowContext(ctx, `
		SELECT sc.id, sc.source_id, sc.channel_url, ss.source_type, ss.health_status
		FROM sports_game_source_assignments a
		JOIN sports_source_channels sc ON sc.id = a.source_channel_id
		JOIN sports_stream_sources   ss ON ss.id = sc.source_id
		WHERE a.game_id = $1 AND a.is_active = true
		ORDER BY a.assigned_at DESC
		LIMIT 1`, gameID,
	).Scan(&cached.ID, &cached.SourceID, &cached.ChannelURL, &cached.SourceType, &cached.HealthStatus)

	if err == nil {
		// OSG.4.001 stale check: bypass if source is down
		if cached.HealthStatus == "down" {
			log.Printf("[sports/router] game %s assigned source %s is down — re-selecting",
				gameID, cached.SourceID)
		} else {
			// Assignment is healthy or degraded — return it directly
			return cached, nil
		}
	} else if err != sql.ErrNoRows {
		return streamResult{}, err
	}

	// Step 2: Auto-match from sports_source_channels for the game's league.
	// Priority: healthy before degraded; within same status, highest match_confidence first.
	var matched streamResult
	err = db.QueryRowContext(ctx, `
		SELECT sc.id, sc.source_id, sc.channel_url, ss.source_type, ss.health_status
		FROM sports_source_channels sc
		JOIN sports_stream_sources  ss ON ss.id = sc.source_id
		WHERE sc.matched_league_id = (
		        SELECT league_id FROM sports_events WHERE id = $1
		      )
		  AND ss.health_status IN ('healthy', 'degraded')
		  AND ss.enabled = true
		ORDER BY
		  CASE ss.health_status WHEN 'healthy' THEN 0 ELSE 1 END,
		  sc.match_confidence DESC
		LIMIT 1`, gameID,
	).Scan(&matched.ID, &matched.SourceID, &matched.ChannelURL, &matched.SourceType, &matched.HealthStatus)

	if err == sql.ErrNoRows {
		return streamResult{}, ErrNoSourceAvailable
	}
	if err != nil {
		return streamResult{}, err
	}

	// Step 3: Persist the auto-selected assignment for fast future lookups.
	go persistAutoAssignment(context.Background(), db, gameID, matched.ID)

	return matched, nil
}

// persistAutoAssignment inserts a new auto assignment row for the game.
// Deactivates any previous (stale/down) assignment first.
// Runs in a goroutine so it never blocks the HTTP response path.
func persistAutoAssignment(ctx context.Context, db *sql.DB, gameID, sourceChannelID string) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		log.Printf("[sports/router] auto-assign tx begin: %v", err)
		return
	}

	// Deactivate any existing active assignments for this game
	_, _ = tx.ExecContext(ctx,
		`UPDATE sports_game_source_assignments SET is_active = false WHERE game_id = $1 AND is_active = true`,
		gameID)

	_, err = tx.ExecContext(ctx, `
		INSERT INTO sports_game_source_assignments (game_id, source_channel_id, assigned_by)
		VALUES ($1, $2, 'auto')`,
		gameID, sourceChannelID)
	if err != nil {
		tx.Rollback()
		log.Printf("[sports/router] auto-assign insert game %s: %v", gameID, err)
		return
	}
	if err := tx.Commit(); err != nil {
		log.Printf("[sports/router] auto-assign commit game %s: %v", gameID, err)
	}
}

// registerStreamRoutes is a no-op placeholder — routes are registered in server.go.
// Kept here for documentation clarity.
func registerStreamRoutes(_ interface{}) {}

// Compile-time check: handleGetStream must satisfy http.HandlerFunc.
var _ http.HandlerFunc = (*Server)(nil).handleGetStream
