// Package cdn provides CDN URL signing and validation for Roost's Cloudflare relay.
// P20.2.001: CDN relay URL signing
//
// Architecture: Stream requests go through Cloudflare Workers (CDN relay). The
// relay sits between Owl clients and the Hetzner origin. Before serving a segment,
// the Worker validates the signed URL. The origin signs URLs with HMAC-SHA256 and
// a shared secret (CDN_HMAC_SECRET). This prevents origin IP exposure and allows
// the CDN layer to enforce time-limited access without a round-trip to origin.
//
// URL format:
//
//	{CDN_RELAY_URL}/stream/{channel_id}/{segment}?token=...&expires=UNIX&sig=HMAC
//
// The sig covers: path + expires, both bound to the secret. Clients cannot forge
// a sig for a different path or extend an expiry without knowing the secret.
package cdn

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"time"
)

const (
	// DefaultTTL is the default URL lifetime for stream segments.
	DefaultTTL = 15 * time.Minute
)

// SignURL generates a time-limited HMAC-SHA256 signed URL for the CDN relay.
//
// Parameters:
//   - cdnBase: base URL of the CDN relay (e.g., "https://stream.yourflock.com")
//   - hmacSecret: shared secret with the Cloudflare Worker (CDN_HMAC_SECRET)
//   - path: the path component to sign (e.g., "/stream/bbc-one/seg001.ts")
//   - expiresAt: Unix timestamp (seconds) when the URL expires
//
// The returned URL is ready to be handed to an Owl client.
func SignURL(cdnBase, hmacSecret, path string, expiresAt int64) (string, error) {
	if hmacSecret == "" {
		return "", fmt.Errorf("cdn: hmac secret must not be empty")
	}
	if path == "" {
		return "", fmt.Errorf("cdn: path must not be empty")
	}

	sig := computeSig(hmacSecret, path, expiresAt)

	base := cdnBase
	// Strip trailing slash so we don't produce double slashes.
	if len(base) > 0 && base[len(base)-1] == '/' {
		base = base[:len(base)-1]
	}

	u, err := url.Parse(base + path)
	if err != nil {
		return "", fmt.Errorf("cdn: invalid base URL %q: %w", cdnBase, err)
	}

	q := u.Query()
	q.Set("expires", fmt.Sprintf("%d", expiresAt))
	q.Set("sig", sig)
	u.RawQuery = q.Encode()

	return u.String(), nil
}

// SignURLWithTTL is a convenience wrapper around SignURL that takes a TTL
// duration instead of an absolute expiry timestamp.
func SignURLWithTTL(cdnBase, hmacSecret, path string, ttl time.Duration) (string, error) {
	expiresAt := time.Now().Add(ttl).Unix()
	return SignURL(cdnBase, hmacSecret, path, expiresAt)
}

// SignStreamURL signs a stream segment URL using DefaultTTL (15 minutes).
// This is the primary entry point for relay service code.
func SignStreamURL(cdnBase, hmacSecret, channelID, segment string) (string, error) {
	path := fmt.Sprintf("/stream/%s/%s", channelID, segment)
	return SignURLWithTTL(cdnBase, hmacSecret, path, DefaultTTL)
}

// ValidateSignature verifies an incoming signed URL sent from the CDN Worker
// back to the origin. Returns true only when the signature is valid and the
// URL has not expired.
//
// Parameters:
//   - secret: shared HMAC secret (CDN_HMAC_SECRET)
//   - path: the URL path that was signed (e.g., "/stream/bbc-one/seg001.ts")
//   - expires: the Unix expiry timestamp from the URL query params
//   - sig: the hex signature from the URL query params
func ValidateSignature(secret, path string, expires int64, sig string) bool {
	if secret == "" || path == "" || sig == "" {
		return false
	}

	// Reject expired tokens first to avoid doing crypto on stale requests.
	if time.Now().Unix() > expires {
		return false
	}

	expected := computeSig(secret, path, expires)
	// Constant-time comparison to prevent timing attacks.
	return hmac.Equal([]byte(expected), []byte(sig))
}

// computeSig computes HMAC-SHA256(secret, path + ":" + expires) and returns
// the lowercase hex string.
func computeSig(secret, path string, expires int64) string {
	msg := fmt.Sprintf("%s:%d", path, expires)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(msg))
	return hex.EncodeToString(mac.Sum(nil))
}
