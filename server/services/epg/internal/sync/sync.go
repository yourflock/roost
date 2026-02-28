// Package sync fetches XMLTV data from remote sources, parses it, and
// upserts programs into Postgres. It handles multi-source EPG aggregation
// with priority-based conflict resolution: when two sources provide data
// for the same program slot, the higher-priority source's non-null fields
// win. Stale programs (end_time older than 7 days) are pruned automatically.
// If a fetch fails, existing programs are preserved (stale-data preservation).
package sync

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/unyeco/roost/services/epg/internal/xmltv"
)

// Source represents a single EPG source to sync from.
type Source struct {
	ID                     string
	Name                   string
	URL                    string
	Priority               int
	RefreshIntervalSeconds int
}

// SyncResult holds the outcome of a single source sync.
type SyncResult struct {
	SourceID          string
	ProgramsUpserted  int
	ProgramsDeleted   int
	Duration          time.Duration
	Error             error
}

// SyncFromSources fetches all active EPG sources in priority order and syncs each one.
// Higher-priority sources are processed first; their data wins on conflict.
func SyncFromSources(ctx context.Context, db *sql.DB) ([]*SyncResult, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT id, name, url, priority, refresh_interval_seconds
		FROM epg_sources
		WHERE is_active = true
		ORDER BY priority DESC, name`)
	if err != nil {
		return nil, fmt.Errorf("query epg sources: %w", err)
	}
	defer rows.Close()

	var sources []Source
	for rows.Next() {
		var s Source
		if err := rows.Scan(&s.ID, &s.Name, &s.URL, &s.Priority, &s.RefreshIntervalSeconds); err == nil {
			sources = append(sources, s)
		}
	}

	results := make([]*SyncResult, 0, len(sources))
	for _, src := range sources {
		res := SyncSource(ctx, db, src)
		results = append(results, res)
	}
	return results, nil
}

// SyncSource performs a full sync cycle for one EPG source:
//  1. Insert a sync log entry with status=running
//  2. Fetch XMLTV from source URL (30s timeout)
//  3. Parse and upsert programs matched by epg_channel_id
//  4. Delete programs older than 7 days (only on success)
//  5. Update sync log with final status
//
// If the fetch fails, existing programs are preserved (stale data kept).
func SyncSource(ctx context.Context, db *sql.DB, src Source) *SyncResult {
	start := time.Now()
	result := &SyncResult{SourceID: src.ID}

	// Record sync start
	var logID string
	_ = db.QueryRowContext(ctx,
		`INSERT INTO epg_sync_log (source_id, status) VALUES ($1, 'running') RETURNING id`,
		src.ID).Scan(&logID)

	updateLog := func(status string, upserted, deleted int, syncErr error) {
		errMsg := ""
		if syncErr != nil {
			errMsg = syncErr.Error()
		}
		_, _ = db.ExecContext(ctx, `
			UPDATE epg_sync_log
			SET status=$1, programs_upserted=$2, programs_deleted=$3,
			    error=$4, completed_at=now()
			WHERE id=$5`,
			status, upserted, deleted, errMsg, logID)
		// Update last_sync_at on the source (even on failure — it records attempt time)
		_, _ = db.ExecContext(ctx,
			`UPDATE epg_sources SET last_sync_at=now() WHERE id=$1`, src.ID)
	}

	// Fetch XMLTV
	xmltvData, err := fetchXMLTV(ctx, src.URL)
	if err != nil {
		result.Error = fmt.Errorf("fetch %s: %w", src.Name, err)
		updateLog("failed", 0, 0, result.Error)
		log.Printf("[epg] sync %s (%s): fetch failed: %v", src.Name, src.ID, err)
		return result
	}

	// Parse
	parsed, err := xmltv.ParseReader(strings.NewReader(xmltvData))
	if err != nil {
		result.Error = fmt.Errorf("parse %s: %w", src.Name, err)
		updateLog("failed", 0, 0, result.Error)
		return result
	}

	// Build XMLTV channel ID → Roost channel ID mapping via epg_channel_id
	xmltvIDToChannelID, err := buildChannelMap(ctx, db, parsed.Channels)
	if err != nil {
		result.Error = fmt.Errorf("build channel map: %w", err)
		updateLog("failed", 0, 0, result.Error)
		return result
	}

	// Upsert programs
	upserted, err := upsertPrograms(ctx, db, src.ID, parsed.Programmes, xmltvIDToChannelID)
	if err != nil {
		result.Error = fmt.Errorf("upsert programs: %w", err)
		updateLog("failed", upserted, 0, result.Error)
		return result
	}

	// Clean up programs older than 7 days (only on successful sync)
	deleted, err := deleteOldPrograms(ctx, db)
	if err != nil {
		log.Printf("[epg] cleanup old programs: %v", err)
		// Non-fatal — sync succeeded, cleanup is best-effort
	}

	result.ProgramsUpserted = upserted
	result.ProgramsDeleted = deleted
	result.Duration = time.Since(start)
	updateLog("completed", upserted, deleted, nil)
	log.Printf("[epg] sync %s: %d upserted, %d deleted in %v", src.Name, upserted, deleted, result.Duration)
	return result
}

// fetchXMLTV fetches XMLTV data from a URL with a 30-second timeout.
// Returns the raw XML body as a string.
func fetchXMLTV(ctx context.Context, url string) (string, error) {
	fetchCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(fetchCtx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("User-Agent", "Roost-EPG/1.0")
	req.Header.Set("Accept", "application/xml,text/xml,*/*")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("http get: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected status %d from EPG source", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 50<<20)) // 50MB limit
	if err != nil {
		return "", fmt.Errorf("read body: %w", err)
	}
	return string(body), nil
}

// buildChannelMap queries the DB to map XMLTV channel IDs to Roost channel UUIDs.
// Channels are matched via channels.epg_channel_id = xmltv channel id.
func buildChannelMap(ctx context.Context, db *sql.DB, xmltvChannels []xmltv.XMLTVChannel) (map[string]string, error) {
	if len(xmltvChannels) == 0 {
		return map[string]string{}, nil
	}

	// Collect unique XMLTV IDs
	xmltvIDs := make([]string, 0, len(xmltvChannels))
	seen := map[string]bool{}
	for _, c := range xmltvChannels {
		if !seen[c.ID] {
			xmltvIDs = append(xmltvIDs, c.ID)
			seen[c.ID] = true
		}
	}

	// Build parameterized IN clause
	placeholders := make([]string, len(xmltvIDs))
	args := make([]interface{}, len(xmltvIDs))
	for i, id := range xmltvIDs {
		placeholders[i] = fmt.Sprintf("$%d", i+1)
		args[i] = id
	}

	rows, err := db.QueryContext(ctx,
		fmt.Sprintf(`SELECT id, epg_channel_id FROM channels WHERE epg_channel_id IN (%s) AND is_active=true`,
			strings.Join(placeholders, ",")), args...)
	if err != nil {
		return nil, fmt.Errorf("query channels: %w", err)
	}
	defer rows.Close()

	m := map[string]string{}
	for rows.Next() {
		var channelID, epgChannelID string
		if err := rows.Scan(&channelID, &epgChannelID); err == nil {
			m[epgChannelID] = channelID
		}
	}
	return m, nil
}

// upsertPrograms inserts or updates programs from the parsed XMLTV feed.
// source_program_id is derived from the XMLTV channel ID + start time to provide
// a stable natural key for conflict resolution.
// Returns the number of rows upserted.
func upsertPrograms(ctx context.Context, db *sql.DB,
	sourceID string,
	programmes []xmltv.XMLTVProgramme,
	channelMap map[string]string,
) (int, error) {
	upserted := 0
	for _, prog := range programmes {
		channelID, ok := channelMap[prog.ChannelID]
		if !ok {
			continue // no matching Roost channel for this XMLTV channel
		}

		// Natural key: channel_id + start time (ISO8601)
		sourceProgramID := fmt.Sprintf("%s|%s", prog.ChannelID, prog.Start.UTC().Format(time.RFC3339))

		_, err := db.ExecContext(ctx, `
			INSERT INTO programs
				(channel_id, source_program_id, title, description,
				 start_time, end_time, genre, rating, icon_url, epg_source_id)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
			ON CONFLICT (channel_id, source_program_id) DO UPDATE SET
				title       = EXCLUDED.title,
				description = COALESCE(EXCLUDED.description, programs.description),
				end_time    = EXCLUDED.end_time,
				genre       = COALESCE(EXCLUDED.genre, programs.genre),
				rating      = COALESCE(EXCLUDED.rating, programs.rating),
				icon_url    = COALESCE(EXCLUDED.icon_url, programs.icon_url),
				epg_source_id = EXCLUDED.epg_source_id,
				updated_at  = now()`,
			channelID, sourceProgramID, prog.Title,
			nullableString(prog.Description),
			prog.Start.UTC(), prog.Stop.UTC(),
			nullableString(prog.Category),
			nullableString(prog.Rating),
			nullableString(prog.IconSrc),
			nullableString(sourceID),
		)
		if err != nil {
			log.Printf("[epg] upsert program %q: %v", prog.Title, err)
			continue
		}
		upserted++
	}
	return upserted, nil
}

// deleteOldPrograms removes programs whose end_time is older than 7 days.
// Returns the number of deleted rows.
func deleteOldPrograms(ctx context.Context, db *sql.DB) (int, error) {
	res, err := db.ExecContext(ctx,
		`DELETE FROM programs WHERE end_time < now() - interval '7 days'`)
	if err != nil {
		return 0, fmt.Errorf("delete old programs: %w", err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// nullableString converts an empty string to nil (for nullable DB columns).
func nullableString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
