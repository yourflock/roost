// hls_provider.go — Direct HLS URL ingest provider.
//
// Wraps a single HLS stream URL (or a small M3U playlist of HLS URLs) as an
// IngestProvider. On HealthCheck it fetches the manifest and verifies it is a
// valid M3U8 file with at least one segment or variant reference.
//
// Config keys:
//   url  — direct HLS manifest URL (.m3u8)
//   name — channel name (optional; defaults to URL host)
package providers

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// hlsProvider implements IngestProvider for direct HLS URL sources.
type hlsProvider struct {
	streamURL string
	name      string
	client    *http.Client
}

// newHLSProvider validates config and returns an hlsProvider.
func newHLSProvider(config map[string]string) (*hlsProvider, error) {
	streamURL := strings.TrimSpace(config["url"])
	if streamURL == "" {
		return nil, fmt.Errorf("hls provider requires config key 'url'")
	}
	if !strings.HasPrefix(streamURL, "http://") && !strings.HasPrefix(streamURL, "https://") {
		return nil, fmt.Errorf("hls provider url must start with http:// or https://")
	}

	name := config["name"]
	if name == "" {
		if u, err := url.Parse(streamURL); err == nil {
			name = u.Host
		} else {
			name = streamURL
		}
	}

	return &hlsProvider{
		streamURL: streamURL,
		name:      name,
		client: &http.Client{
			Timeout: 15 * time.Second,
		},
	}, nil
}

func (p *hlsProvider) Type() string { return "hls" }

func (p *hlsProvider) Validate(config map[string]string) error {
	_, err := newHLSProvider(config)
	return err
}

// GetChannels returns a single IngestChannel for this HLS URL.
func (p *hlsProvider) GetChannels(_ context.Context) ([]IngestChannel, error) {
	ch := IngestChannel{
		ID:        p.streamURL, // URL as the stable identifier
		Name:      p.name,
		StreamURL: p.streamURL,
	}
	return []IngestChannel{ch}, nil
}

// GetStreamURL returns the HLS URL for the given channelID.
// For HLS providers, channelID is the stream URL.
func (p *hlsProvider) GetStreamURL(_ context.Context, channelID string) (string, error) {
	if channelID == "" {
		return p.streamURL, nil
	}
	return channelID, nil
}

// HealthCheck fetches the HLS manifest and verifies it contains at least one
// segment or variant playlist reference.
func (p *hlsProvider) HealthCheck(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.streamURL, nil)
	if err != nil {
		return fmt.Errorf("hls health: %w", err)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("hls health fetch: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("hls health: HTTP %d", resp.StatusCode)
	}

	hasSegment, err := m3u8HasSegment(resp.Body)
	if err != nil {
		return fmt.Errorf("hls health parse: %w", err)
	}
	if !hasSegment {
		return fmt.Errorf("hls health: manifest has no segments or variants")
	}
	return nil
}

// m3u8HasSegment scans an M3U8 body and returns true if it contains at least
// one non-comment, non-empty line (a segment or variant playlist URL).
func m3u8HasSegment(r io.Reader) (bool, error) {
	scanner := bufio.NewScanner(r)
	firstLine := true
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			if firstLine && !strings.HasPrefix(line, "#EXTM3U") {
				return false, fmt.Errorf("not a valid M3U8: missing #EXTM3U header")
			}
			firstLine = false
			continue
		}
		// Non-comment, non-empty line = a segment or variant URL.
		return true, nil
	}
	return false, scanner.Err()
}
