// middleware.go — HTTP middleware for auth enforcement.
// Provides Bearer token extraction and subscriber context injection.
package auth

import (
	"context"
	"net/http"
	"strings"

	"github.com/google/uuid"
)

// contextKey is an unexported type to avoid context key collisions.
type contextKey string

const claimsKey contextKey = "auth_claims"

// RequireAuth is an HTTP middleware that validates the Bearer JWT in the
// Authorization header. On success, injects the parsed claims into the
// request context. On failure, responds with 401 JSON.
func RequireAuth(next http.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tokenStr := extractBearerToken(r)
		if tokenStr == "" {
			writeError(w, http.StatusUnauthorized, "missing_token", "Authorization header required")
			return
		}

		claims, err := ValidateAccessToken(tokenStr)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "invalid_token", "Invalid or expired token")
			return
		}

		ctx := context.WithValue(r.Context(), claimsKey, claims)
		next.ServeHTTP(w, r.WithContext(ctx))
	}
}

// RequireVerifiedEmail extends RequireAuth by also requiring email_verified = true.
// Use this for token generation and streaming features.
func RequireVerifiedEmail(next http.Handler) http.HandlerFunc {
	return RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims := ClaimsFromContext(r.Context())
		if claims == nil || !claims.EmailVerified {
			writeError(w, http.StatusForbidden, "email_not_verified",
				"Email verification required. Check your inbox or request a new verification email.")
			return
		}
		next.ServeHTTP(w, r)
	}))
}

// ClaimsFromContext extracts JWT claims from the request context.
// Returns nil if RequireAuth middleware was not applied.
func ClaimsFromContext(ctx context.Context) *Claims {
	if c, ok := ctx.Value(claimsKey).(*Claims); ok {
		return c
	}
	return nil
}

// SubscriberIDFromContext extracts the subscriber UUID from JWT claims in context.
// Returns uuid.Nil if not authenticated.
func SubscriberIDFromContext(ctx context.Context) uuid.UUID {
	claims := ClaimsFromContext(ctx)
	if claims == nil {
		return uuid.Nil
	}
	id, _ := uuid.Parse(claims.HasuraClaims.SubscriberID)
	return id
}

// extractBearerToken pulls the token from "Authorization: Bearer <token>".
// Returns empty string if header is missing or malformed.
func extractBearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if h == "" {
		return ""
	}
	parts := strings.SplitN(h, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "bearer") {
		return ""
	}
	return strings.TrimSpace(parts[1])
}
