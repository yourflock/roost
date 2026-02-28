// m3u_provider.go — M3U playlist ingest provider.
//
// Fetches an M3U playlist from a URL, parses #EXTINF entries, and returns
// a channel list. The parser is streaming (bufio.Scanner) so large playlists
// (5000+ entries) do not require loading the entire file into memory.
//
// Supported M3U attributes on #EXTINF lines:
//   tvg-id, tvg-name, tvg-logo, group-title, tvg-chno, tvg-language, tvg-country
//
// EPG URL extraction:
//   #EXTM3U url-tvg="..." and x-tvg-url="..." are extracted from the header line.
//
// Config keys:
//   url  — HTTP(S) URL returning the M3U playlist
package providers

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// m3uProvider implements IngestProvider for M3U playlist sources.
type m3uProvider struct {
	url    string
	client *http.Client
}

// newM3UProvider validates config and returns an m3uProvider.
func newM3UProvider(config map[string]string) (*m3uProvider, error) {
	url, ok := config["url"]
	if !ok || strings.TrimSpace(url) == "" {
		return nil, fmt.Errorf("m3u provider requires config key 'url'")
	}
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return nil, fmt.Errorf("m3u provider url must start with http:// or https://")
	}
	return &m3uProvider{
		url: url,
		client: &http.Client{
			Timeout: 60 * time.Second,
		},
	}, nil
}

func (p *m3uProvider) Type() string { return "m3u" }

func (p *m3uProvider) Validate(config map[string]string) error {
	_, err := newM3UProvider(config)
	return err
}

// GetChannels fetches and parses the M3U playlist, returning all channels.
// The playlist is streamed line-by-line — memory usage is O(1) per entry.
func (p *m3uProvider) GetChannels(ctx context.Context) ([]IngestChannel, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.url, nil)
	if err != nil {
		return nil, fmt.Errorf("m3u build request: %w", err)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("m3u fetch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("m3u fetch: HTTP %d", resp.StatusCode)
	}
	return parseM3U(resp.Body)
}

// GetStreamURL returns the stream URL for a channel identified by its tvg-id or
// stream URL (used as fallback ID when no tvg-id is present).
// For M3U providers the URL is the channel's stream URL directly.
func (p *m3uProvider) GetStreamURL(_ context.Context, channelID string) (string, error) {
	// For M3U providers channelID is the stream URL itself (set in IngestChannel.StreamURL).
	// Callers that need the URL have it already via GetChannels.
	// This method is retained for interface compliance and future use.
	if channelID == "" {
		return "", fmt.Errorf("m3u: empty channelID")
	}
	return channelID, nil
}

// HealthCheck verifies the playlist URL is reachable by issuing a HEAD request.
func (p *m3uProvider) HealthCheck(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, p.url, nil)
	if err != nil {
		return fmt.Errorf("m3u health: %w", err)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("m3u health: %w", err)
	}
	resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("m3u health: HTTP %d", resp.StatusCode)
	}
	return nil
}

// ---------- parser -----------------------------------------------------------

// attrRE matches key="value" or key=value pairs in an #EXTINF line.
var attrRE = regexp.MustCompile(`([\w-]+)=(?:"([^"]*?)"|([^\s,]+))`)

// extinfDurationRE matches the duration number after #EXTINF:.
var extinfDurationRE = regexp.MustCompile(`^#EXTINF:\s*(-?\d+\.?\d*)`)

// epgURLRE matches url-tvg or x-tvg-url attributes on the #EXTM3U header line.
var epgURLRE = regexp.MustCompile(`(?:url-tvg|x-tvg-url)="([^"]+)"`)

// ParsedM3U is the result of parsing a complete M3U file.
type ParsedM3U struct {
	Channels []IngestChannel
	EPGURLs  []string // EPG source URLs found in the header
}

// parseM3U reads an M3U playlist from r and returns parsed channels.
// Malformed lines are logged and skipped — the parser is tolerant.
func parseM3U(r io.Reader) ([]IngestChannel, error) {
	scanner := bufio.NewScanner(r)
	// Extend default 64 KB buffer — some M3U lines are very long.
	buf := make([]byte, 0, 256*1024)
	scanner.Buffer(buf, 1024*1024)

	var channels []IngestChannel
	var pendingAttrs map[string]string // attributes from the #EXTINF line
	firstLine := true

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		if firstLine {
			firstLine = false
			if !strings.HasPrefix(line, "#EXTM3U") {
				return nil, fmt.Errorf("not a valid M3U file: first line %q", line)
			}
			// header line: ignore EPG URLs here (handled by caller with ParseM3UFull)
			pendingAttrs = nil
			continue
		}

		if strings.HasPrefix(line, "#EXTINF:") {
			pendingAttrs = parseEXTINFAttrs(line)
			continue
		}

		if strings.HasPrefix(line, "#") {
			// Other directive — skip.
			pendingAttrs = nil
			continue
		}

		// Non-comment, non-empty line after #EXTINF = stream URL.
		if !strings.HasPrefix(line, "http://") && !strings.HasPrefix(line, "https://") {
			// Not an HTTP URL (could be rtsp:// or relative path) — skip silently.
			pendingAttrs = nil
			continue
		}

		ch := IngestChannel{
			StreamURL: line,
		}
		if pendingAttrs != nil {
			ch.ID = firstNonEmpty(pendingAttrs["tvg-id"], line)
			ch.Name = firstNonEmpty(pendingAttrs["tvg-name"], pendingAttrs["name"])
			ch.LogoURL = pendingAttrs["tvg-logo"]
			ch.Category = pendingAttrs["group-title"]
			ch.TvgID = pendingAttrs["tvg-id"]
		} else {
			ch.ID = line
		}

		if ch.Name == "" {
			ch.Name = ch.ID
		}

		channels = append(channels, ch)
		pendingAttrs = nil
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("m3u scan: %w", err)
	}
	return channels, nil
}

// ParseM3UFull returns channels and any EPG URLs from the header.
func ParseM3UFull(r io.Reader) (ParsedM3U, error) {
	scanner := bufio.NewScanner(r)
	buf := make([]byte, 0, 256*1024)
	scanner.Buffer(buf, 1024*1024)

	var result ParsedM3U
	var pendingAttrs map[string]string
	firstLine := true

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		if firstLine {
			firstLine = false
			if strings.HasPrefix(line, "#EXTM3U") {
				// Extract EPG URLs from header attributes.
				matches := epgURLRE.FindAllStringSubmatch(line, -1)
				for _, m := range matches {
					if len(m) > 1 && m[1] != "" {
						result.EPGURLs = append(result.EPGURLs, m[1])
					}
				}
			}
			continue
		}

		if strings.HasPrefix(line, "#EXTINF:") {
			pendingAttrs = parseEXTINFAttrs(line)
			continue
		}

		if strings.HasPrefix(line, "#") {
			pendingAttrs = nil
			continue
		}

		if !strings.HasPrefix(line, "http://") && !strings.HasPrefix(line, "https://") {
			pendingAttrs = nil
			continue
		}

		ch := IngestChannel{
			StreamURL: line,
		}
		if pendingAttrs != nil {
			ch.ID = firstNonEmpty(pendingAttrs["tvg-id"], line)
			ch.Name = firstNonEmpty(pendingAttrs["tvg-name"], pendingAttrs["name"])
			ch.LogoURL = pendingAttrs["tvg-logo"]
			ch.Category = pendingAttrs["group-title"]
			ch.TvgID = pendingAttrs["tvg-id"]
		} else {
			ch.ID = line
		}
		if ch.Name == "" {
			ch.Name = ch.ID
		}
		result.Channels = append(result.Channels, ch)
		pendingAttrs = nil
	}

	if err := scanner.Err(); err != nil {
		return result, fmt.Errorf("m3u scan: %w", err)
	}
	return result, nil
}

// parseEXTINFAttrs extracts key=value attributes from an #EXTINF line.
// Returns a map of attribute name → value.
func parseEXTINFAttrs(line string) map[string]string {
	attrs := make(map[string]string)
	// Extract quoted and unquoted attribute values.
	matches := attrRE.FindAllStringSubmatch(line, -1)
	for _, m := range matches {
		key := strings.ToLower(m[1])
		val := m[2] // quoted value
		if val == "" {
			val = m[3] // unquoted value
		}
		attrs[key] = val
	}
	// Also extract the display name after the last comma on the line.
	if idx := strings.LastIndex(line, ","); idx != -1 {
		name := strings.TrimSpace(line[idx+1:])
		if name != "" && attrs["tvg-name"] == "" {
			attrs["name"] = name
		}
	}
	return attrs
}

// firstNonEmpty returns the first non-empty string from the candidates.
func firstNonEmpty(candidates ...string) string {
	for _, s := range candidates {
		if s != "" {
			return s
		}
	}
	return ""
}
