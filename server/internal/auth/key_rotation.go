// key_rotation.go — JWT signing key rotation support (P22.2.001).
//
// Key rotation allows changing the signing secret without invalidating all
// active tokens. The rotation process:
//
//  1. Generate a new key: set ROOST_JWT_SIGNING_KEY=<new_key>
//  2. Move the old key to ROOST_JWT_PREV_KEY_1 (keep it for up to 15 min TTL)
//  3. Deploy — new tokens are signed with the new key; old tokens still validate
//  4. After all old tokens expire (max 15 min access token TTL), clear ROOST_JWT_PREV_KEY_1
//
// For emergency rotation (compromise), set ROOST_JWT_PREV_KEY_1="" to immediately
// invalidate all tokens signed with the old key (forces re-login).
package auth

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
)

// KeyRotationStatus describes the current key rotation configuration.
type KeyRotationStatus struct {
	HasActiveKey   bool
	HasPrevKey1    bool
	HasPrevKey2    bool
	ActiveKeyHash  string // first 8 hex chars for identification (not the full key)
	PrevKey1Hash   string
	PrevKey2Hash   string
	Issuer         string
}

// GetKeyRotationStatus returns the current state of JWT signing key configuration.
// Safe to log — only returns key prefixes, never full keys.
func GetKeyRotationStatus() KeyRotationStatus {
	active := os.Getenv("ROOST_JWT_SIGNING_KEY")
	if active == "" {
		active = os.Getenv("AUTH_JWT_SECRET")
	}
	prev1 := os.Getenv("ROOST_JWT_PREV_KEY_1")
	prev2 := os.Getenv("ROOST_JWT_PREV_KEY_2")

	return KeyRotationStatus{
		HasActiveKey:  active != "",
		HasPrevKey1:   prev1 != "",
		HasPrevKey2:   prev2 != "",
		ActiveKeyHash: keyPrefix(active),
		PrevKey1Hash:  keyPrefix(prev1),
		PrevKey2Hash:  keyPrefix(prev2),
		Issuer:        getIssuer(),
	}
}

// GenerateSigningKey generates a new cryptographically random 256-bit signing key.
// The returned string is suitable for use as ROOST_JWT_SIGNING_KEY.
// This is a utility function for key generation during rotation — not called at runtime.
func GenerateSigningKey() (string, error) {
	b := make([]byte, 32) // 256 bits
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("key rotation: failed to generate random bytes: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// keyPrefix returns the first 8 hex characters of a key for safe logging.
// Returns "none" if the key is empty.
func keyPrefix(key string) string {
	if key == "" {
		return "none"
	}
	h := make([]byte, 4)
	copy(h, []byte(key))
	prefix := fmt.Sprintf("%x", h)
	if len(prefix) > 8 {
		return prefix[:8]
	}
	return prefix
}
