// channel_matcher.go — M3U parser and automated channel-to-league matcher.
// OSG.2.001: Parse source M3U playlists, Jaro-Winkler match channels to leagues,
// upsert sports_source_channels with match scores.
package sports

import (
	"bufio"
	"context"
	"database/sql"
	"fmt"
	"log"
	"math"
	"net/http"
	"strings"
	"time"
)

const (
	// matchThresholdStore is the minimum confidence to store a channel match.
	matchThresholdStore = 0.70
	// matchThresholdAuto is the confidence above which match_confirmed is set true automatically.
	matchThresholdAuto = 0.85
	// maxChannelsPerSource limits M3U parse to prevent OOM on large playlists.
	maxChannelsPerSource = 5000
	// m3uFetchTimeout is the HTTP timeout for fetching the M3U playlist.
	m3uFetchTimeout = 30 * time.Second
)

// sportsBroadcasterKeywords are group-title keywords that suggest a sports channel.
var sportsBroadcasterKeywords = []string{
	"sport", "espn", "fox sports", "nbc sports", "cbs sports", "abc sports",
	"nfl", "nba", "mlb", "nhl", "mls", "bein", "dazn", "sky sports",
	"bt sport", "eurosport", "tennis", "golf", "motorsport", "cricket",
	"rugby", "boxing", "ufc", "wrestling", "racing", "atletico",
}

// parseM3U fetches and parses an M3U playlist URL, returning up to maxChannelsPerSource channels.
// Parses #EXTINF attributes: tvg-name, tvg-id, group-title and the stream URL on the next line.
func parseM3U(ctx context.Context, m3uURL string) ([]RawChannel, error) {
	fetchCtx, cancel := context.WithTimeout(ctx, m3uFetchTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(fetchCtx, http.MethodGet, m3uURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch m3u: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 400 {
		return nil, fmt.Errorf("m3u fetch returned HTTP %d", resp.StatusCode)
	}

	var channels []RawChannel
	truncated := false
	scanner := bufio.NewScanner(resp.Body)

	var pendingChannel *RawChannel
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || line == "#EXTM3U" {
			continue
		}

		if strings.HasPrefix(line, "#EXTINF:") {
			// Parse the #EXTINF line for metadata attributes
			ch := parseExtInfLine(line)
			pendingChannel = &ch
			continue
		}

		if strings.HasPrefix(line, "#") {
			// Other directive — ignore
			continue
		}

		// This is a stream URL line
		if pendingChannel != nil {
			pendingChannel.URL = line
			channels = append(channels, *pendingChannel)
			pendingChannel = nil

			if len(channels) >= maxChannelsPerSource {
				truncated = true
				break
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return channels, fmt.Errorf("scan m3u: %w", err)
	}

	if truncated {
		log.Printf("[sports/matcher] M3U from %s truncated at %d channels (limit)", m3uURL, maxChannelsPerSource)
	}

	return channels, nil
}

// parseExtInfLine parses the attributes from a #EXTINF line.
// Format: #EXTINF:-1 tvg-name="..." tvg-id="..." group-title="...",Display Name
func parseExtInfLine(line string) RawChannel {
	var ch RawChannel

	// Split at first comma to separate attributes from display name
	commaIdx := strings.LastIndex(line, ",")
	displayName := ""
	attrPart := line
	if commaIdx >= 0 {
		displayName = strings.TrimSpace(line[commaIdx+1:])
		attrPart = line[:commaIdx]
	}

	ch.Name = displayName

	// Extract tvg-name (overrides display name if present)
	if v := extractAttr(attrPart, "tvg-name"); v != "" {
		ch.Name = v
	}
	// Fall back to display name if tvg-name was empty
	if ch.Name == "" {
		ch.Name = displayName
	}

	ch.TVGID = extractAttr(attrPart, "tvg-id")
	ch.GroupTitle = extractAttr(attrPart, "group-title")

	return ch
}

// extractAttr extracts a quoted attribute value from an #EXTINF attribute string.
// Handles both single and double quotes.
func extractAttr(s, key string) string {
	needle := key + "="
	idx := strings.Index(strings.ToLower(s), strings.ToLower(needle))
	if idx < 0 {
		return ""
	}
	rest := s[idx+len(needle):]
	if len(rest) == 0 {
		return ""
	}
	quote := rest[0]
	if quote != '"' && quote != '\'' {
		// Unquoted value — read until space
		end := strings.IndexByte(rest, ' ')
		if end < 0 {
			return rest
		}
		return rest[:end]
	}
	end := strings.IndexByte(rest[1:], quote)
	if end < 0 {
		return rest[1:]
	}
	return rest[1 : end+1]
}

// isSportsChannel returns true if the channel's group-title contains any sports keyword.
func isSportsChannel(ch RawChannel) bool {
	groupLower := strings.ToLower(ch.GroupTitle)
	nameLower := strings.ToLower(ch.Name)
	for _, kw := range sportsBroadcasterKeywords {
		if strings.Contains(groupLower, kw) || strings.Contains(nameLower, kw) {
			return true
		}
	}
	return false
}

// runChannelMatch parses the M3U for a source, matches channels to leagues, and upserts results.
// Called on cron and via the /refresh endpoint.
func (s *Server) runChannelMatch(ctx context.Context, sourceID string) error {
	// Fetch source M3U URL
	var m3uURL sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT m3u_url FROM sports_stream_sources WHERE id = $1 AND enabled = true`, sourceID,
	).Scan(&m3uURL)
	if err == sql.ErrNoRows {
		return fmt.Errorf("source %s not found or disabled", sourceID)
	}
	if err != nil {
		return fmt.Errorf("fetch source: %w", err)
	}
	if !m3uURL.Valid || m3uURL.String == "" {
		return fmt.Errorf("source %s has no m3u_url", sourceID)
	}

	start := time.Now()
	channels, err := parseM3U(ctx, m3uURL.String)
	if err != nil {
		return fmt.Errorf("parse m3u: %w", err)
	}

	// Load league names for matching
	leagues, err := s.loadLeagueNames(ctx)
	if err != nil {
		return fmt.Errorf("load leagues: %w", err)
	}

	matched := 0
	for _, ch := range channels {
		if ch.URL == "" {
			continue
		}
		if !isSportsChannel(ch) {
			continue
		}

		bestLeagueID, bestScore := bestLeagueMatch(ch.Name, leagues)

		if bestScore < matchThresholdStore {
			continue
		}

		confirmed := bestScore >= matchThresholdAuto
		var leagueIDParam interface{}
		if bestLeagueID != "" {
			leagueIDParam = bestLeagueID
		}
		groupTitle := nullableString(ch.GroupTitle)
		tvgID := nullableString(ch.TVGID)

		_, upsertErr := s.db.ExecContext(ctx, `
			INSERT INTO sports_source_channels
			  (source_id, channel_name, channel_url, group_title, tvg_id,
			   matched_league_id, match_confidence, match_confirmed)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			ON CONFLICT (source_id, channel_url) DO UPDATE SET
			  channel_name      = EXCLUDED.channel_name,
			  group_title       = EXCLUDED.group_title,
			  tvg_id            = EXCLUDED.tvg_id,
			  matched_league_id = EXCLUDED.matched_league_id,
			  match_confidence  = EXCLUDED.match_confidence,
			  match_confirmed   = EXCLUDED.match_confirmed`,
			sourceID, ch.Name, ch.URL, groupTitle, tvgID,
			leagueIDParam, bestScore, confirmed,
		)
		if upsertErr != nil {
			log.Printf("[sports/matcher] upsert channel %q: %v", ch.Name, upsertErr)
			continue
		}
		matched++
	}

	log.Printf("[sports/matcher] source %s: parsed %d channels, matched %d sports channels in %s",
		sourceID, len(channels), matched, time.Since(start).Round(time.Millisecond))
	return nil
}

// loadLeagueNames returns a map of league ID → league name for all active leagues.
func (s *Server) loadLeagueNames(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name FROM sports_leagues WHERE is_active = true`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	leagues := make(map[string]string)
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			continue
		}
		leagues[id] = name
	}
	return leagues, nil
}

// bestLeagueMatch finds the league with the highest Jaro-Winkler similarity to channelName.
// Returns the league ID and score, or ("", 0) if none exceeds the threshold.
func bestLeagueMatch(channelName string, leagues map[string]string) (string, float64) {
	best := 0.0
	bestID := ""
	nameLower := strings.ToLower(channelName)
	for id, leagueName := range leagues {
		score := jaroWinkler(nameLower, strings.ToLower(leagueName))
		if score > best {
			best = score
			bestID = id
		}
	}
	return bestID, best
}

// ─── Jaro-Winkler implementation ─────────────────────────────────────────────

// jaroWinkler returns the Jaro-Winkler similarity between two strings (0.0–1.0).
func jaroWinkler(s1, s2 string) float64 {
	jaro := jaroSimilarity(s1, s2)
	// Count common prefix up to 4 characters
	prefix := 0
	maxPrefix := 4
	if len(s1) < maxPrefix {
		maxPrefix = len(s1)
	}
	if len(s2) < maxPrefix {
		maxPrefix = len(s2)
	}
	for i := 0; i < maxPrefix; i++ {
		if s1[i] == s2[i] {
			prefix++
		} else {
			break
		}
	}
	const p = 0.1 // standard Winkler prefix scale
	return jaro + float64(prefix)*p*(1-jaro)
}

// jaroSimilarity returns the Jaro similarity between two strings (0.0–1.0).
func jaroSimilarity(s1, s2 string) float64 {
	if s1 == s2 {
		return 1.0
	}
	if len(s1) == 0 || len(s2) == 0 {
		return 0.0
	}

	matchDist := int(math.Max(float64(len(s1)), float64(len(s2)))/2.0) - 1
	if matchDist < 0 {
		matchDist = 0
	}

	s1Matched := make([]bool, len(s1))
	s2Matched := make([]bool, len(s2))

	matches := 0
	transpositions := 0

	for i := 0; i < len(s1); i++ {
		start := i - matchDist
		if start < 0 {
			start = 0
		}
		end := i + matchDist + 1
		if end > len(s2) {
			end = len(s2)
		}
		for j := start; j < end; j++ {
			if s2Matched[j] || s1[i] != s2[j] {
				continue
			}
			s1Matched[i] = true
			s2Matched[j] = true
			matches++
			break
		}
	}

	if matches == 0 {
		return 0.0
	}

	k := 0
	for i := 0; i < len(s1); i++ {
		if !s1Matched[i] {
			continue
		}
		for k < len(s2) && !s2Matched[k] {
			k++
		}
		if k < len(s2) && s1[i] != s2[k] {
			transpositions++
		}
		k++
	}

	m := float64(matches)
	return (m/float64(len(s1)) + m/float64(len(s2)) + (m-float64(transpositions)/2)/m) / 3.0
}

// RunAllSourcesChannelMatch runs runChannelMatch for every enabled source.
// Called on the cron schedule from the main entry point.
func (s *Server) RunAllSourcesChannelMatch(ctx context.Context) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id FROM sports_stream_sources WHERE enabled = true AND m3u_url IS NOT NULL`)
	if err != nil {
		log.Printf("[sports/matcher] list sources: %v", err)
		return
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			continue
		}
		ids = append(ids, id)
	}
	rows.Close()

	for _, id := range ids {
		if err := s.runChannelMatch(ctx, id); err != nil {
			log.Printf("[sports/matcher] source %s: %v", id, err)
		}
	}
}
