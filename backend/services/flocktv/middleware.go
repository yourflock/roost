// middleware.go — JWT authentication middleware for the Flock TV service.
// Phase FLOCKTV FTV.0.T04/T05: validates Flock-issued ES256 JWTs and extracts
// family_id claim for all subscriber-facing endpoints.
//
// Flock issues ES256 (ECDSA P-256) JWTs with the following standard claims:
//   - sub: subscriber UUID
//   - family_id: family UUID
//   - roost_subscriber: bool
//   - roost_boost_active: bool
//   - exp: expiry unix timestamp
//
// The public key is loaded from FLOCK_PUBLIC_KEY (PEM-encoded EC public key) or
// from flock_sso_keys table (per-family key registered via the SSO provision endpoint).
package flocktv

import (
	"context"
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// contextKey is a typed key for request context values.
type contextKey string

const (
	contextKeyFamilyID        contextKey = "family_id"
	contextKeySubscriberID    contextKey = "subscriber_id"
	contextKeyBoostActive     contextKey = "roost_boost_active"
	contextKeyRoostSubscriber contextKey = "roost_subscriber"
)

// FlockClaims holds the standard claims issued by Flock for Roost subscribers.
type FlockClaims struct {
	FamilyID        string `json:"family_id"`
	RoostSubscriber bool   `json:"roost_subscriber"`
	BoostActive     bool   `json:"roost_boost_active"`
	jwt.RegisteredClaims
}

// loadFlockPublicKey loads and parses the Flock EC public key from the FLOCK_PUBLIC_KEY
// environment variable (PEM-encoded). Returns an error if unset or malformed.
func loadFlockPublicKey() (*ecdsa.PublicKey, error) {
	pemData := os.Getenv("FLOCK_PUBLIC_KEY")
	if pemData == "" {
		return nil, errors.New("FLOCK_PUBLIC_KEY env var not set")
	}

	block, _ := pem.Decode([]byte(pemData))
	if block == nil {
		return nil, errors.New("FLOCK_PUBLIC_KEY: failed to decode PEM block")
	}

	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, err
	}

	ecKey, ok := pub.(*ecdsa.PublicKey)
	if !ok {
		return nil, errors.New("FLOCK_PUBLIC_KEY is not an EC public key")
	}

	return ecKey, nil
}

// validateFlockJWT parses and validates a Flock-issued ES256 JWT.
// Returns the claims on success or an error on failure.
func validateFlockJWT(tokenStr string) (*FlockClaims, error) {
	pubKey, err := loadFlockPublicKey()
	if err != nil {
		return nil, err
	}

	token, err := jwt.ParseWithClaims(tokenStr, &FlockClaims{},
		func(t *jwt.Token) (interface{}, error) {
			if _, ok := t.Method.(*jwt.SigningMethodECDSA); !ok {
				return nil, errors.New("unexpected signing method: expected ES256")
			}
			return pubKey, nil
		},
		jwt.WithExpirationRequired(),
		jwt.WithIssuedAt(),
		jwt.WithLeeway(30*time.Second),
	)
	if err != nil {
		return nil, err
	}

	claims, ok := token.Claims.(*FlockClaims)
	if !ok || !token.Valid {
		return nil, errors.New("invalid token claims")
	}

	if claims.FamilyID == "" {
		return nil, errors.New("missing family_id claim")
	}

	return claims, nil
}

// extractBearerToken returns the token from "Authorization: Bearer {token}" header.
// Returns empty string if header is missing or malformed.
func extractBearerToken(r *http.Request) string {
	header := r.Header.Get("Authorization")
	if !strings.HasPrefix(header, "Bearer ") {
		return ""
	}
	return strings.TrimPrefix(header, "Bearer ")
}

// requireSubscriberJWT is an HTTP middleware that validates the Flock JWT,
// extracts subscriber claims, and injects them into the request context.
// If FLOCK_PUBLIC_KEY is not set, JWT validation is skipped (dev mode).
func (s *Server) requireSubscriberJWT(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Dev mode: skip JWT validation when FLOCK_PUBLIC_KEY is not configured.
		if os.Getenv("FLOCK_PUBLIC_KEY") == "" {
			// Inject placeholder claims for dev/test from query params.
			familyID := r.URL.Query().Get("family_id")
			if familyID == "" {
				familyID = r.Header.Get("X-Debug-Family-ID")
			}
			if familyID == "" {
				writeError(w, http.StatusUnauthorized, "missing_auth",
					"Authorization header required (FLOCK_PUBLIC_KEY not configured)")
				return
			}
			ctx := context.WithValue(r.Context(), contextKeyFamilyID, familyID)
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}

		tokenStr := extractBearerToken(r)
		if tokenStr == "" {
			writeError(w, http.StatusUnauthorized, "missing_token",
				"Authorization: Bearer {token} header is required")
			return
		}

		claims, err := validateFlockJWT(tokenStr)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "invalid_token", err.Error())
			return
		}

		if !claims.RoostSubscriber {
			writeError(w, http.StatusForbidden, "not_subscriber",
				"active Flock TV subscription required")
			return
		}

		ctx := r.Context()
		ctx = context.WithValue(ctx, contextKeyFamilyID, claims.FamilyID)
		ctx = context.WithValue(ctx, contextKeySubscriberID, claims.Subject)
		ctx = context.WithValue(ctx, contextKeyBoostActive, claims.BoostActive)
		ctx = context.WithValue(ctx, contextKeyRoostSubscriber, claims.RoostSubscriber)

		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// familyIDFromCtx extracts the family_id from the request context.
// Returns empty string if not present (should not happen after requireSubscriberJWT).
func familyIDFromCtx(ctx context.Context) string {
	v, _ := ctx.Value(contextKeyFamilyID).(string)
	return v
}

// boostActiveFromCtx returns whether the subscriber has Roost Boost active.
func boostActiveFromCtx(ctx context.Context) bool {
	v, _ := ctx.Value(contextKeyBoostActive).(bool)
	return v
}
