// Package auth provides JWT generation, validation, and related utilities
// for Roost subscriber authentication.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// Claims represents JWT claims for a Roost subscriber.
// Hasura naming convention: x-hasura-* fields are consumed by Hasura's
// JWT auth mode to enforce row-level permissions.
type Claims struct {
	jwt.RegisteredClaims
	HasuraClaims  HasuraClaims `json:"https://hasura.io/jwt/claims"`
	EmailVerified bool         `json:"email_verified"`
}

// HasuraClaims contains the Hasura-specific claims embedded in the JWT.
type HasuraClaims struct {
	SubscriberID string   `json:"x-hasura-subscriber-id"`
	DefaultRole  string   `json:"x-hasura-default-role"`
	AllowedRoles []string `json:"x-hasura-allowed-roles"`
}

// GenerateAccessToken creates a signed JWT access token for the given subscriber.
// The token contains Hasura role claims so GraphQL queries are automatically
// scoped to the subscriber. Access tokens are short-lived (15 minutes).
// Every token receives a unique jti (JWT ID) for revocation support (P22.2).
func GenerateAccessToken(subscriberID uuid.UUID, emailVerified bool) (string, error) {
	secret := os.Getenv("AUTH_JWT_SECRET")
	if secret == "" {
		return "", errors.New("AUTH_JWT_SECRET not set")
	}

	expiry := 15 * time.Minute
	if v := os.Getenv("AUTH_JWT_EXPIRY"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			expiry = d
		}
	}

	now := time.Now()
	claims := Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			ID:        uuid.NewString(), // jti — unique per token for revocation
			Subject:   subscriberID.String(),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(expiry)),
			Issuer:    "roost",
		},
		HasuraClaims: HasuraClaims{
			SubscriberID: subscriberID.String(),
			DefaultRole:  "subscriber",
			AllowedRoles: []string{"subscriber"},
		},
		EmailVerified: emailVerified,
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(secret))
}

// ValidateAccessToken parses and validates a JWT access token.
// Returns the parsed claims or an error if the token is invalid/expired.
// Revocation check against the DB is NOT done here — callers that need it
// should call IsRevoked(ctx, db, claims.ID) after validation.
func ValidateAccessToken(tokenStr string) (*Claims, error) {
	secret := os.Getenv("AUTH_JWT_SECRET")
	if secret == "" {
		return nil, errors.New("AUTH_JWT_SECRET not set")
	}

	token, err := jwt.ParseWithClaims(tokenStr, &Claims{}, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return []byte(secret), nil
	})
	if err != nil {
		return nil, err
	}

	claims, ok := token.Claims.(*Claims)
	if !ok || !token.Valid {
		return nil, errors.New("invalid token claims")
	}
	return claims, nil
}

// GenerateRefreshToken creates a cryptographically random refresh token (32 bytes, hex-encoded).
// The raw token is returned for transmission; the caller must hash and store it.
func GenerateRefreshToken() (raw string, hash string, err error) {
	b := make([]byte, 32)
	if _, err = rand.Read(b); err != nil {
		return "", "", fmt.Errorf("failed to generate refresh token: %w", err)
	}
	raw = hex.EncodeToString(b)
	hash = HashToken(raw)
	return raw, hash, nil
}

// GenerateSecureToken creates a cryptographically random token with the given prefix.
// Format: {prefix}{32 random bytes hex}. Returns raw token and its SHA-256 hash.
// The raw token is shown to the user once; only the hash is stored.
func GenerateSecureToken(prefix string) (raw string, hash string, err error) {
	b := make([]byte, 32)
	if _, err = rand.Read(b); err != nil {
		return "", "", fmt.Errorf("failed to generate token: %w", err)
	}
	raw = prefix + hex.EncodeToString(b)
	hash = HashToken(raw)
	return raw, hash, nil
}

// HashToken computes the SHA-256 hex digest of a token string.
// Used to convert raw tokens into storage-safe hashes.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
