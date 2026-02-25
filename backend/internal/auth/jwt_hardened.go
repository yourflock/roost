// jwt_hardened.go — Hardened JWT validation with strict claim enforcement (P22.2.001).
//
// Enforcements beyond the standard ValidateAccessToken:
//   - Reject alg:none tokens (no unsigned JWTs)
//   - Require exp claim (reject tokens without expiry)
//   - Require iat claim and reject future-issued tokens (clock skew > 5 min)
//   - Require iss claim matching ROOST_JWT_ISSUER env var
//   - Support key rotation via ROOST_JWT_SIGNING_KEY + ROOST_JWT_PREV_KEY_1/2
package auth

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	// maxClockSkew is the maximum allowed difference between iat and now().
	// Rejects tokens issued more than 5 minutes in the future.
	maxClockSkew = 5 * time.Minute
)

// ValidateHardened performs strict JWT validation with all P22.2.001 enforcements.
// Attempts verification with the active signing key, then falls back to previous
// keys in order (supports zero-downtime key rotation).
//
// Returns parsed Claims on success, or a descriptive error.
func ValidateHardened(tokenStr string) (*Claims, error) {
	keys := loadSigningKeys()
	if len(keys) == 0 {
		return nil, errors.New("jwt: no signing keys configured")
	}

	var lastErr error
	for _, key := range keys {
		claims, err := parseHardened(tokenStr, key)
		if err == nil {
			return claims, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

// parseHardened attempts to parse and validate the token with the given key,
// applying all hardened checks.
func parseHardened(tokenStr string, key []byte) (*Claims, error) {
	issuer := getIssuer()

	token, err := jwt.ParseWithClaims(tokenStr, &Claims{}, func(t *jwt.Token) (interface{}, error) {
		// ENFORCEMENT 1: Reject alg:none (unsigned tokens).
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("jwt: unexpected signing method %v — alg:none not accepted", t.Header["alg"])
		}
		return key, nil
	},
		// Use the configured issuer for validation.
		jwt.WithIssuedAt(),
		jwt.WithIssuer(issuer),
	)
	if err != nil {
		return nil, fmt.Errorf("jwt: parse failed: %w", err)
	}

	claims, ok := token.Claims.(*Claims)
	if !ok || !token.Valid {
		return nil, errors.New("jwt: invalid claims")
	}

	// ENFORCEMENT 2: exp must be present (jwt library enforces this, but be explicit).
	if claims.ExpiresAt == nil {
		return nil, errors.New("jwt: missing exp claim")
	}

	// ENFORCEMENT 3: iat must be present and not in the future.
	if claims.IssuedAt == nil {
		return nil, errors.New("jwt: missing iat claim")
	}
	if time.Until(claims.IssuedAt.Time) > maxClockSkew {
		return nil, fmt.Errorf("jwt: iat is %v in the future (max skew %v)", time.Until(claims.IssuedAt.Time), maxClockSkew)
	}

	// ENFORCEMENT 4: iss must match ROOST_JWT_ISSUER (already checked by jwt.WithIssuer above,
	// but explicitly verify in case the library version differs).
	if claims.Issuer != issuer {
		return nil, fmt.Errorf("jwt: issuer mismatch: got %q, expected %q", claims.Issuer, issuer)
	}

	return claims, nil
}

// getIssuer returns the expected JWT issuer from env, falling back to "roost".
func getIssuer() string {
	if v := os.Getenv("ROOST_JWT_ISSUER"); v != "" {
		return v
	}
	return "roost"
}

// loadSigningKeys returns all configured signing keys in priority order:
// [active_key, prev_key_1, prev_key_2].
// Keys are loaded from environment variables at call time (not cached) so
// rotation takes effect without restart.
func loadSigningKeys() [][]byte {
	var keys [][]byte

	// Primary active key.
	if k := os.Getenv("ROOST_JWT_SIGNING_KEY"); k != "" {
		keys = append(keys, []byte(k))
	} else if k := os.Getenv("AUTH_JWT_SECRET"); k != "" {
		// Fall back to legacy variable for backwards compatibility.
		keys = append(keys, []byte(k))
	}

	// Previous keys for zero-downtime rotation.
	if k := os.Getenv("ROOST_JWT_PREV_KEY_1"); k != "" {
		keys = append(keys, []byte(k))
	}
	if k := os.Getenv("ROOST_JWT_PREV_KEY_2"); k != "" {
		keys = append(keys, []byte(k))
	}

	return keys
}
