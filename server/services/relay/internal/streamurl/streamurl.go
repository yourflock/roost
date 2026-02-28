// Package streamurl provides mode-aware HLS stream URL generation for the relay service.
// P20.2.002: Stream URL routing based on mode
//
// Private mode: serve direct HLS URLs from origin (no signing required).
// Public mode: sign URLs through CDN relay using HMAC-SHA256.
//
// This package is consumed by the relay service's segment-serving handlers to
// produce the correct URL type based on ROOST_MODE at startup time.
package streamurl

import (
	"fmt"
	"time"

	"github.com/yourflock/roost/internal/cdn"
)

// Mode mirrors config.Mode to avoid a circular import between the relay service
// and the root module's config package.
type Mode string

const (
	ModePrivate Mode = "private"
	ModePublic  Mode = "public"
)

// Builder generates stream URLs appropriate for the current Roost mode.
// Construct once at startup; safe for concurrent use.
type Builder struct {
	mode       Mode
	originBase string // e.g., "http://localhost:8090" (private) or "https://stream.yourflock.org" (public CDN bypass)
	cdnBase    string // e.g., "https://stream.yourflock.org" (public CDN relay)
	hmacSecret string
}

// NewBuilder returns a Builder configured for the given mode and environment.
//
// Parameters:
//   - mode: "private" or "public" (from ROOST_MODE env var)
//   - originBase: the direct origin HLS base URL (used in private mode)
//   - cdnBase: the CDN relay base URL (used in public mode; signed URLs)
//   - hmacSecret: HMAC-SHA256 secret for URL signing (required in public mode)
func NewBuilder(mode Mode, originBase, cdnBase, hmacSecret string) *Builder {
	return &Builder{
		mode:       mode,
		originBase: originBase,
		cdnBase:    cdnBase,
		hmacSecret: hmacSecret,
	}
}

// SegmentURL returns the URL that an authenticated Owl client should use to
// fetch the given HLS segment of a channel.
//
// Private mode: returns a direct origin URL — no signing, no CDN hop.
//
//	Example: http://localhost:8090/stream/bbc-one/seg001.ts
//
// Public mode: returns a CDN relay URL with a 15-minute HMAC signature.
//
//	Example: https://stream.yourflock.org/stream/bbc-one/seg001.ts?expires=...&sig=...
func (b *Builder) SegmentURL(channelSlug, segment string) (string, error) {
	path := fmt.Sprintf("/stream/%s/%s", channelSlug, segment)

	if b.mode == ModePublic {
		expiresAt := time.Now().Add(cdn.DefaultTTL).Unix()
		return cdn.SignURL(b.cdnBase, b.hmacSecret, path, expiresAt)
	}

	// Private mode: direct origin URL — no signing.
	base := b.originBase
	if len(base) > 0 && base[len(base)-1] == '/' {
		base = base[:len(base)-1]
	}
	return base + path, nil
}

// PlaylistURL returns the URL for a channel's m3u8 playlist file.
// Private mode: direct origin URL.
// Public mode: CDN-signed URL with same 15-minute TTL.
func (b *Builder) PlaylistURL(channelSlug, playlist string) (string, error) {
	path := fmt.Sprintf("/stream/%s/%s", channelSlug, playlist)

	if b.mode == ModePublic {
		expiresAt := time.Now().Add(cdn.DefaultTTL).Unix()
		return cdn.SignURL(b.cdnBase, b.hmacSecret, path, expiresAt)
	}

	base := b.originBase
	if len(base) > 0 && base[len(base)-1] == '/' {
		base = base[:len(base)-1]
	}
	return base + path, nil
}

// IsPublic reports whether this builder is operating in public mode.
func (b *Builder) IsPublic() bool {
	return b.mode == ModePublic
}
