// relay_auth.go — Token validation middleware for the relay service.
// Wraps the shared internal/auth package with a Redis-cached token check.
// Returns 403 for invalid or revoked tokens. The raw token is read from the
// "token" query parameter on every stream request.
package auth

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	rootauth "github.com/yourflock/roost/internal/auth"
)

// Subscriber is re-exported for handlers in the relay service.
type Subscriber = rootauth.TokenSubscriber

// ErrTokenInvalid is returned when token validation fails.
var ErrTokenInvalid = rootauth.ErrTokenInvalid

// redisClient is a minimal interface for caching validated tokens.
type redisClient interface {
	Get(ctx context.Context, key string) interface{ Result() (string, error) }
	Set(ctx context.Context, key string, value interface{}, expiration time.Duration) interface{ Err() error }
}

// Validator validates stream tokens with optional Redis caching.
type Validator struct {
	db    *sql.DB
	redis redisClient
}

// NewValidator creates a token validator.
// redis may be nil — validation falls back to DB-only (slightly slower).
func NewValidator(db *sql.DB, redis redisClient) *Validator {
	return &Validator{db: db, redis: redis}
}

// Validate checks the token from the request query string.
// Returns the subscriber on success, or an error with an appropriate HTTP status code.
func (v *Validator) Validate(r *http.Request) (*Subscriber, int, error) {
	token := r.URL.Query().Get("token")
	if token == "" {
		return nil, http.StatusForbidden, errors.New("missing token")
	}

	sub, err := rootauth.ValidateAPIToken(r.Context(), v.db, token)
	if err != nil {
		if errors.Is(err, rootauth.ErrTokenInvalid) {
			return nil, http.StatusForbidden, err
		}
		return nil, http.StatusInternalServerError, fmt.Errorf("token validation: %w", err)
	}
	return sub, http.StatusOK, nil
}

// Middleware returns an http.Handler middleware that requires a valid token.
// On success, the subscriber is attached to the request context.
func (v *Validator) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sub, status, err := v.Validate(r)
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}

		ctx := context.WithValue(r.Context(), subscriberKey{}, sub)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// SubscriberFromContext retrieves the authenticated subscriber from the request context.
// Returns nil if not present.
func SubscriberFromContext(ctx context.Context) *Subscriber {
	sub, _ := ctx.Value(subscriberKey{}).(*Subscriber)
	return sub
}

type subscriberKey struct{}
