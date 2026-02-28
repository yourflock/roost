// m3u_parser.go — M3U playlist fetcher and parser for family IPTV sources.
//
// Thin wrapper around the existing ingest/providers M3U parser logic, adapted
// for the streams service. Fetches an M3U URL and returns structured Channel
// records. The parser is streaming (bufio.Scanner) for memory efficiency on
// large playlists (5000+ entries).
//
// Supported M3U attributes on #EXTINF lines:
//
//	tvg-id, tvg-name, tvg-logo, group-title
//
// Raw credential handling: This layer never persists stream URLs — callers
// receive them only for validation counts. Actual stream proxying is done
// server-side via the relay service; source URLs never reach clients.
package streams

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// Channel is a parsed M3U channel entry.
type Channel struct {
	Name       string
	LogoURL    string
	GroupTitle string
	TvgID      string
	StreamURL  string
}

// attrRE extracts key="value" or key=value from #EXTINF lines.
var attrRE = regexp.MustCompile(`([\w-]+)=(?:"([^"]*?)"|([^\s,]+))`)

// ParseM3U fetches an M3U playlist from m3uURL and returns all channels.
// Streams the response body line-by-line — memory use is O(1) per entry.
// Context deadline is respected: use a 30-60s timeout for network fetches.
func ParseM3U(ctx context.Context, m3uURL string) ([]Channel, error) {
	client := &http.Client{Timeout: 60 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m3uURL, nil)
	if err != nil {
		return nil, fmt.Errorf("m3u build request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("m3u fetch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("m3u fetch: HTTP %d from %s", resp.StatusCode, m3uURL)
	}

	return parseM3UBody(resp)
}

// parseM3UBody parses an M3U response body into Channel entries.
func parseM3UBody(resp *http.Response) ([]Channel, error) {
	scanner := bufio.NewScanner(resp.Body)
	// Increase buffer for long lines (some M3U lines are very wide).
	buf := make([]byte, 0, 256*1024)
	scanner.Buffer(buf, 1024*1024)

	var channels []Channel
	var pending *Channel
	firstLine := true

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		if firstLine {
			firstLine = false
			if !strings.HasPrefix(line, "#EXTM3U") {
				return nil, fmt.Errorf("m3u: not a valid M3U file (first line: %q)", line)
			}
			continue
		}

		if strings.HasPrefix(line, "#EXTINF:") {
			ch := parseExtinf(line)
			pending = &ch
			continue
		}

		if strings.HasPrefix(line, "#") {
			// Other directive — reset pending.
			pending = nil
			continue
		}

		// Non-comment, non-empty line = stream URL.
		if !strings.HasPrefix(line, "http://") && !strings.HasPrefix(line, "https://") {
			pending = nil
			continue
		}

		ch := Channel{StreamURL: line}
		if pending != nil {
			ch.Name = pending.Name
			ch.LogoURL = pending.LogoURL
			ch.GroupTitle = pending.GroupTitle
			ch.TvgID = pending.TvgID
		}
		if ch.Name == "" {
			ch.Name = line
		}
		channels = append(channels, ch)
		pending = nil
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("m3u scan: %w", err)
	}
	return channels, nil
}

// parseExtinf extracts channel metadata from an #EXTINF line.
func parseExtinf(line string) Channel {
	attrs := make(map[string]string)
	matches := attrRE.FindAllStringSubmatch(line, -1)
	for _, m := range matches {
		key := strings.ToLower(m[1])
		val := m[2]
		if val == "" {
			val = m[3]
		}
		attrs[key] = val
	}

	name := ""
	if idx := strings.LastIndex(line, ","); idx != -1 {
		name = strings.TrimSpace(line[idx+1:])
	}
	if name == "" {
		name = firstNonEmpty(attrs["tvg-name"], attrs["name"])
	}

	return Channel{
		Name:       name,
		LogoURL:    attrs["tvg-logo"],
		GroupTitle: attrs["group-title"],
		TvgID:      attrs["tvg-id"],
	}
}

// firstNonEmpty returns the first non-empty string from candidates.
func firstNonEmpty(candidates ...string) string {
	for _, s := range candidates {
		if s != "" {
			return s
		}
	}
	return ""
}
