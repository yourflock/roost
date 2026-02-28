// relay_test.go — Unit tests for CDN URL signing and validation.
// P20.3.001: CDN URL signing round-trip tests
package cdn_test

import (
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/unyeco/roost/internal/cdn"
)

const testSecret = "super-secret-hmac-key-at-least-32-bytes-long"

func TestSignURL_RoundTrip(t *testing.T) {
	// Sign a URL and immediately validate the signature — must succeed.
	signed, err := cdn.SignURL("https://stream.roost.unity.dev", testSecret, "/stream/bbc-one/seg001.ts", time.Now().Add(15*time.Minute).Unix())
	if err != nil {
		t.Fatalf("SignURL failed: %v", err)
	}

	u, err := url.Parse(signed)
	if err != nil {
		t.Fatalf("signed URL is not parseable: %v", err)
	}

	expiresStr := u.Query().Get("expires")
	sigStr := u.Query().Get("sig")

	if expiresStr == "" {
		t.Error("expected 'expires' query param")
	}
	if sigStr == "" {
		t.Error("expected 'sig' query param")
	}

	// ValidateSignature must return true immediately after signing.
	var expires int64
	if _, err := fmt.Sscanf(expiresStr, "%d", &expires); err != nil {
		t.Fatalf("could not parse expires %q: %v", expiresStr, err)
	}

	valid := cdn.ValidateSignature(testSecret, "/stream/bbc-one/seg001.ts", expires, sigStr)
	if !valid {
		t.Error("ValidateSignature returned false on a freshly signed URL")
	}
}

func TestValidateSignature_ExpiredToken(t *testing.T) {
	// A URL that expired 1 second ago must be rejected.
	pastExpiry := time.Now().Add(-time.Second).Unix()
	signed, err := cdn.SignURL("https://stream.roost.unity.dev", testSecret, "/stream/ch1/seg.ts", pastExpiry)
	if err != nil {
		t.Fatalf("SignURL failed: %v", err)
	}

	u, _ := url.Parse(signed)
	sig := u.Query().Get("sig")

	valid := cdn.ValidateSignature(testSecret, "/stream/ch1/seg.ts", pastExpiry, sig)
	if valid {
		t.Error("ValidateSignature should reject expired tokens")
	}
}

func TestValidateSignature_WrongSecret(t *testing.T) {
	expiresAt := time.Now().Add(15 * time.Minute).Unix()
	_, sigStr := signAndExtract(t, testSecret, "/stream/bbc-two/seg002.ts", expiresAt)

	// Different secret — must fail.
	valid := cdn.ValidateSignature("different-secret-also-long-enough", "/stream/bbc-two/seg002.ts", expiresAt, sigStr)
	if valid {
		t.Error("ValidateSignature should reject tokens signed with a different secret")
	}
}

func TestValidateSignature_WrongPath(t *testing.T) {
	expiresAt := time.Now().Add(15 * time.Minute).Unix()
	_, sigStr := signAndExtract(t, testSecret, "/stream/channel-a/seg001.ts", expiresAt)

	// Different path — the sig is bound to the original path.
	valid := cdn.ValidateSignature(testSecret, "/stream/channel-b/seg001.ts", expiresAt, sigStr)
	if valid {
		t.Error("ValidateSignature should reject tokens with a path mismatch")
	}
}

func TestValidateSignature_EmptyInputs(t *testing.T) {
	// Any empty required field must return false.
	cases := []struct {
		name    string
		secret  string
		path    string
		expires int64
		sig     string
	}{
		{"empty secret", "", "/stream/ch/seg.ts", time.Now().Add(time.Hour).Unix(), "abc"},
		{"empty path", testSecret, "", time.Now().Add(time.Hour).Unix(), "abc"},
		{"empty sig", testSecret, "/stream/ch/seg.ts", time.Now().Add(time.Hour).Unix(), ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			valid := cdn.ValidateSignature(tc.secret, tc.path, tc.expires, tc.sig)
			if valid {
				t.Errorf("expected false for %s", tc.name)
			}
		})
	}
}

func TestSignURL_TrailingSlashInBase(t *testing.T) {
	// Trailing slash on base URL must not produce double-slash in output.
	signed, err := cdn.SignURL("https://cdn.roost.unity.dev/", testSecret, "/stream/ch/seg.ts", time.Now().Add(time.Minute).Unix())
	if err != nil {
		t.Fatalf("SignURL failed: %v", err)
	}
	if strings.Contains(signed, "//stream") {
		t.Errorf("double slash in signed URL: %s", signed)
	}
}

func TestSignStreamURL_DefaultTTL(t *testing.T) {
	// SignStreamURL is a convenience wrapper — verify it produces a valid signed URL.
	signed, err := cdn.SignStreamURL("https://stream.roost.unity.dev", testSecret, "bbc-one", "seg001.ts")
	if err != nil {
		t.Fatalf("SignStreamURL failed: %v", err)
	}
	u, err := url.Parse(signed)
	if err != nil {
		t.Fatalf("URL is not parseable: %v", err)
	}
	if u.Query().Get("sig") == "" {
		t.Error("expected sig param from SignStreamURL")
	}
	if u.Query().Get("expires") == "" {
		t.Error("expected expires param from SignStreamURL")
	}
	// Path must contain the channel and segment.
	if !strings.Contains(u.Path, "bbc-one") {
		t.Errorf("channel not in path: %s", u.Path)
	}
	if !strings.Contains(u.Path, "seg001.ts") {
		t.Errorf("segment not in path: %s", u.Path)
	}
}

func TestSignURL_EmptySecret(t *testing.T) {
	_, err := cdn.SignURL("https://stream.roost.unity.dev", "", "/stream/ch/seg.ts", time.Now().Add(time.Minute).Unix())
	if err == nil {
		t.Error("expected error for empty secret")
	}
}

func TestSignURL_EmptyPath(t *testing.T) {
	_, err := cdn.SignURL("https://stream.roost.unity.dev", testSecret, "", time.Now().Add(time.Minute).Unix())
	if err == nil {
		t.Error("expected error for empty path")
	}
}

// --- helpers ---

func signAndExtract(t *testing.T, secret, path string, expiresAt int64) (string, string) {
	t.Helper()
	signed, err := cdn.SignURL("https://stream.roost.unity.dev", secret, path, expiresAt)
	if err != nil {
		t.Fatalf("SignURL failed: %v", err)
	}
	u, _ := url.Parse(signed)
	return u.Query().Get("expires"), u.Query().Get("sig")
}
