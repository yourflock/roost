// playlist.go — M3U8 playlist generation for Roost Owl API.
//
// M3U8 playlists are the standard "add a source" mechanism for IPTV players.
// When an Owl user enters their Roost subscription token, Owl can fetch this
// endpoint to get a full channel list in a format every player understands.
//
// Endpoint:
//   GET /owl/playlist.m3u8?token=SESSION_TOKEN
//   GET /owl/v1/playlist.m3u8?token=SESSION_TOKEN
//
// The response is a valid M3U8 playlist:
//   - #EXTM3U header with x-tvg-url pointing to our XMLTV EPG feed
//   - One #EXTINF entry per active channel with tvg-id, tvg-name, tvg-logo, group-title
//   - Stream URL for each channel: /owl/v1/stream/{slug}?token=SESSION_TOKEN
//     (pointing back to our relay — never the source URL)
//
// The stream URLs embed the session token as a query parameter so the player
// authenticates automatically when it requests each channel stream. Session tokens
// are 4-hour TTL; players that cache the playlist need to re-fetch after expiry.
//
// EPG source URL: /owl/xmltv.xml?token=SESSION_TOKEN (future endpoint — included
// as a stub so players set it up now and it works when implemented).
//
// Content-Type: application/x-mpegurl (standard for M3U8/HLS playlists)
// Cache-Control: private, max-age=3600 (re-fetch hourly; session tokens are 4h)
package main

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ---- handler: GET /owl/playlist.m3u8 ---------------------------------------

// handlePlaylistM3U8 generates and returns a full M3U8 channel playlist for the
// authenticated session. The token is validated by the requireSession middleware
// before this handler is called.
//
// M3U8 format reference:
//   https://github.com/iptv-org/iptv/blob/master/CONTRIBUTING.md#m3u8-format
func (s *server) handlePlaylistM3U8(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET required")
		return
	}

	// Extract the session token — needed to embed in per-channel stream URLs
	// so the player can authenticate each stream request automatically.
	sessionToken := extractSessionToken(r)

	baseURL := getEnv("ROOST_BASE_URL", "https://roost.yourflock.com")

	// XMLTV EPG source URL (stub — included so players configure it now)
	epgURL := fmt.Sprintf("%s/owl/xmltv.xml?token=%s", baseURL, url.QueryEscape(sessionToken))

	// Fetch all active channels ordered by sort_order (stable channel order)
	rows, err := s.db.QueryContext(r.Context(), `
		SELECT c.slug, c.name, coalesce(c.logo_url,''), coalesce(c.category,''),
		       coalesce(c.country_code,''), coalesce(c.language_code,'en'),
		       coalesce(c.epg_channel_id, c.slug), c.sort_order
		FROM channels c
		WHERE c.is_active = true
		ORDER BY c.sort_order ASC, c.name ASC
	`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query_error", "Failed to fetch channels")
		return
	}
	defer rows.Close()

	type channelEntry struct {
		slug     string
		name     string
		logo     string
		category string
		country  string
		language string
		epgID    string
		order    int
	}

	var channels []channelEntry
	for rows.Next() {
		var ch channelEntry
		if err := rows.Scan(&ch.slug, &ch.name, &ch.logo, &ch.category,
			&ch.country, &ch.language, &ch.epgID, &ch.order); err != nil {
			continue
		}
		channels = append(channels, ch)
	}

	// Build M3U8 content
	var sb strings.Builder

	// M3U8 header — x-tvg-url wires EPG so players can show program guide
	sb.WriteString(fmt.Sprintf("#EXTM3U x-tvg-url=\"%s\"\n", epgURL))
	sb.WriteString(fmt.Sprintf("# Roost IPTV — generated %s\n", time.Now().UTC().Format(time.RFC3339)))
	sb.WriteString("# Subscribe: https://roost.yourflock.com\n")
	sb.WriteString("\n")

	for _, ch := range channels {
		// #EXTINF line: duration (-1 = live), then tvg tags, then display name
		// tvg-id: matches the id attribute in the XMLTV EPG feed
		// tvg-name: display name (shown in guide)
		// tvg-logo: channel logo URL
		// group-title: category for grouping in IPTV players
		extinf := fmt.Sprintf(
			"#EXTINF:-1 tvg-id=\"%s\" tvg-name=\"%s\" tvg-logo=\"%s\" tvg-language=\"%s\" tvg-country=\"%s\" group-title=\"%s\",%s",
			m3uEscape(ch.epgID),
			m3uEscape(ch.name),
			ch.logo, // logo URLs are already properly formed
			m3uEscape(ch.language),
			m3uEscape(strings.ToUpper(ch.country)),
			m3uEscape(ch.category),
			ch.name,
		)
		sb.WriteString(extinf)
		sb.WriteString("\n")

		// Stream URL — points to our relay endpoint with session token embedded
		// Players send GET to this URL when the user selects the channel.
		// The requireSession middleware validates the token, then handleStream
		// generates a signed Cloudflare relay URL and redirects.
		streamURL := fmt.Sprintf("%s/owl/v1/stream/%s?token=%s",
			baseURL, ch.slug, url.QueryEscape(sessionToken))
		sb.WriteString(streamURL)
		sb.WriteString("\n")
	}

	// Write response
	w.Header().Set("Content-Type", "application/x-mpegurl")
	w.Header().Set("Content-Disposition", "attachment; filename=\"roost.m3u8\"")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	w.Header().Set("X-Channel-Count", fmt.Sprintf("%d", len(channels)))
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, sb.String())
}

// m3uEscape escapes characters that are problematic in M3U8 attribute values.
// M3U8 attribute values are quoted strings — commas and quotes need special handling.
// Also strips newlines which would break the line-based format.
func m3uEscape(s string) string {
	// Remove newlines (would break M3U8 line-based format)
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", "")
	// Replace double quotes in attribute values (attribute is already double-quoted)
	s = strings.ReplaceAll(s, `"`, "'")
	return s
}
