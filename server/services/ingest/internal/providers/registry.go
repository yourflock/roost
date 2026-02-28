// registry.go — ProviderRegistry manages multiple ingest source types.
//
// Supported providers:
//   - M3U playlist (bulk import from URL)
//   - Xtream Codes API (host + username + password)
//   - Direct HLS URL (single channel or small playlist)
//
// Credentials are stored encrypted in the database. The registry decrypts them
// on read and constructs stream URLs server-side — raw credentials are never
// returned to any API caller or written to logs.
package providers

import (
	"context"
	"fmt"
)

// IngestChannel represents a single channel from a provider.
type IngestChannel struct {
	ID        string // provider-scoped unique ID (e.g. xtream stream_id or M3U tvg-id)
	Name      string
	LogoURL   string
	Category  string
	TvgID     string
	StreamURL string // assembled server-side; never logged
}

// IngestProvider is the interface all provider implementations satisfy.
type IngestProvider interface {
	// Type returns the provider type string ("m3u", "xtream", "hls").
	Type() string

	// Validate checks that the config map has all required keys and values.
	// Returns a descriptive error if any required field is missing or malformed.
	Validate(config map[string]string) error

	// GetChannels fetches the full channel list from the provider.
	// For M3U providers this fetches and parses the playlist.
	// For Xtream providers this calls get_live_streams.
	// Results are returned in the order the provider delivers them.
	GetChannels(ctx context.Context) ([]IngestChannel, error)

	// GetStreamURL returns a playable URL for the given channel ID.
	// For Xtream this constructs the HLS URL from credentials + stream ID.
	// For HLS/M3U this returns the URL directly from the channel record.
	// The returned URL must not be passed to clients — it is for ingest only.
	GetStreamURL(ctx context.Context, channelID string) (string, error)

	// HealthCheck performs a lightweight connectivity test (e.g. HEAD request
	// or server info call). Returns nil if the provider is reachable.
	HealthCheck(ctx context.Context) error
}

// NewProvider constructs the correct IngestProvider for the given type and config.
// Supported types: "m3u", "xtream", "hls".
// The config map is type-specific (see individual provider docs).
func NewProvider(providerType string, config map[string]string) (IngestProvider, error) {
	switch providerType {
	case "m3u":
		return newM3UProvider(config)
	case "xtream":
		return newXtreamProvider(config)
	case "hls":
		return newHLSProvider(config)
	default:
		return nil, fmt.Errorf("unsupported provider type %q; supported: m3u, xtream, hls", providerType)
	}
}
