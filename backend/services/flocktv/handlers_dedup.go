// handlers_dedup.go — Channel deduplication and content acquisition queue management.
// Phase FLOCKTV FTV.1.T02 / FTV.2.T02/T03: normalises channel names from IPTV contributions,
// deduplicates them into canonical_channels entries, and provides the internal channel
// dedup API called by the IPTV contribution validation job.
package flocktv

import (
	"net/http"
	"regexp"
	"strings"
	"unicode"
)

// normalizeChannelName strips punctuation, lowercases, and collapses whitespace.
// Used to produce the normalized_name key for canonical channel deduplication.
// "CNN International" → "cnn international"
// "CNN-International (HD)" → "cnn international hd"
func normalizeChannelName(name string) string {
	// Lowercase everything.
	name = strings.ToLower(name)

	// Remove common resolution suffixes that are not part of the channel identity.
	hdRe := regexp.MustCompile(`\s*(hd|fhd|uhd|4k|sd)\s*$`)
	name = hdRe.ReplaceAllString(name, "")

	// Strip non-alphanumeric characters except spaces.
	var b strings.Builder
	for _, r := range name {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		} else if unicode.IsSpace(r) || r == '-' || r == '_' {
			b.WriteRune(' ')
		}
	}
	name = b.String()

	// Collapse multiple spaces.
	spaceRe := regexp.MustCompile(`\s+`)
	name = spaceRe.ReplaceAllString(name, " ")

	return strings.TrimSpace(name)
}

// ChannelDedupRequest is the body for POST /internal/flocktv/channels/dedup.
// Called by the IPTV contribution validation job with parsed channel data.
type ChannelDedupRequest struct {
	ContributionID string    `json:"contribution_id"`
	Channels       []RawChannel `json:"channels"`
}

// RawChannel is a single channel entry from an M3U or Xtream source.
type RawChannel struct {
	TvgID    string `json:"tvg_id"`    // from M3U tvg-id attribute
	Name     string `json:"name"`      // raw channel name
	GroupTitle string `json:"group_title"` // M3U group-title
	Country  string `json:"country"`
	LogoURL  string `json:"logo_url"`
	StreamURL string `json:"stream_url"` // resolved stream URL
}

// handleChannelDedup normalises and deduplicates channels from a contribution.
// POST /internal/flocktv/channels/dedup
// Protected by X-Flock-Internal-Secret.
func (s *Server) handleChannelDedup(w http.ResponseWriter, r *http.Request) {
	if !checkInternalSecret(w, r) {
		return
	}

	var req ChannelDedupRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	if req.ContributionID == "" {
		writeError(w, http.StatusBadRequest, "missing_field", "contribution_id is required")
		return
	}
	if len(req.Channels) == 0 {
		writeError(w, http.StatusBadRequest, "missing_channels", "channels array is empty")
		return
	}

	added := 0
	updated := 0
	skipped := 0

	for _, ch := range req.Channels {
		if ch.Name == "" {
			skipped++
			continue
		}

		normalizedName := normalizeChannelName(ch.Name)
		if normalizedName == "" {
			skipped++
			continue
		}

		if s.db == nil {
			added++
			continue
		}

		// Upsert canonical channel — keyed by normalized_name OR tvg_id (if present).
		var canonicalID string
		var wasNew bool

		if ch.TvgID != "" {
			// Try tvg_id match first (strongest dedup key).
			dbErr := s.db.QueryRowContext(r.Context(),
				`SELECT id FROM canonical_channels WHERE tvg_id = $1`,
				ch.TvgID,
			).Scan(&canonicalID)
			if dbErr != nil {
				// No existing entry by tvg_id — insert or match by normalized_name.
				dbErr = s.db.QueryRowContext(r.Context(), `
					INSERT INTO canonical_channels
					  (tvg_id, normalized_name, canonical_name, country, category, logo_url,
					   active_source_count, ingest_active, created_at, updated_at)
					VALUES ($1, $2, $3, $4, $5, $6, 0, false, NOW(), NOW())
					ON CONFLICT (normalized_name) DO UPDATE
					  SET tvg_id = COALESCE(canonical_channels.tvg_id, EXCLUDED.tvg_id),
					      logo_url = COALESCE(canonical_channels.logo_url, EXCLUDED.logo_url),
					      updated_at = NOW()
					RETURNING id, (xmax = 0) AS was_inserted`,
					ch.TvgID, normalizedName, ch.Name,
					nullableString(ch.Country), nullableString(ch.GroupTitle), nullableString(ch.LogoURL),
				).Scan(&canonicalID, &wasNew)
				if dbErr != nil {
					s.logger.Warn("canonical channel upsert failed", "name", ch.Name, "error", dbErr.Error())
					skipped++
					continue
				}
				if wasNew {
					added++
				} else {
					updated++
				}
			}
		} else {
			// No tvg_id — use normalized_name as sole dedup key.
			dbErr := s.db.QueryRowContext(r.Context(), `
				INSERT INTO canonical_channels
				  (normalized_name, canonical_name, country, category, logo_url,
				   active_source_count, ingest_active, created_at, updated_at)
				VALUES ($1, $2, $3, $4, $5, 0, false, NOW(), NOW())
				ON CONFLICT (normalized_name) DO UPDATE
				  SET logo_url = COALESCE(canonical_channels.logo_url, EXCLUDED.logo_url),
				      updated_at = NOW()
				RETURNING id, (xmax = 0) AS was_inserted`,
				normalizedName, ch.Name,
				nullableString(ch.Country), nullableString(ch.GroupTitle), nullableString(ch.LogoURL),
			).Scan(&canonicalID, &wasNew)
			if dbErr != nil {
				s.logger.Warn("canonical channel insert failed", "name", ch.Name, "error", dbErr.Error())
				skipped++
				continue
			}
			if wasNew {
				added++
			} else {
				updated++
			}
		}

		// Register this contribution's stream URL as a channel source.
		if canonicalID != "" && ch.StreamURL != "" {
			_, _ = s.db.ExecContext(r.Context(), `
				INSERT INTO channel_sources
				  (canonical_channel_id, contribution_id, source_url, priority, health_score, created_at)
				VALUES ($1, $2, $3,
				  (SELECT COUNT(*) FROM channel_sources WHERE canonical_channel_id = $1),
				  100, NOW())
				ON CONFLICT (canonical_channel_id, contribution_id) DO UPDATE
				  SET source_url = EXCLUDED.source_url, last_checked = NOW()`,
				canonicalID, req.ContributionID, ch.StreamURL,
			)
			// Update active_source_count.
			_, _ = s.db.ExecContext(r.Context(), `
				UPDATE canonical_channels
				SET active_source_count = (
				  SELECT COUNT(*) FROM channel_sources WHERE canonical_channel_id = $1
				), updated_at = NOW()
				WHERE id = $1`,
				canonicalID,
			)
		}
	}

	// Update channel_count on the contribution.
	if s.db != nil {
		_, _ = s.db.ExecContext(r.Context(), `
			UPDATE iptv_contributions
			SET channel_count = $1, last_verified = NOW(), updated_at = NOW()
			WHERE id = $2`,
			len(req.Channels)-skipped, req.ContributionID,
		)
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"contribution_id": req.ContributionID,
		"total_channels":  len(req.Channels),
		"added":           added,
		"updated":         updated,
		"skipped":         skipped,
	})
}

// nullableString converts an empty string to nil for DB nullable columns.
func nullableString(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}
