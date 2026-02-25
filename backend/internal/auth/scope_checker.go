// scope_checker.go — JWT scope enforcement middleware (P22.2.003).
//
// Defines Roost API scopes and provides gin-compatible middleware for scope checks.
// Scopes are stored in the JWT claims under the "scopes" field.
//
// Scope assignment:
//   - Subscribers at signup: [stream:read, catalog:read, billing:read]
//   - Billing write actions (upgrade/cancel): [billing:write] added on demand
//   - Admin panel users: [admin]
//   - Internal service-to-service: [internal]
//
// Note: Roost uses net/http (not gin). These are net/http middleware funcs.
// The gin naming convention in the task spec is followed in spirit —
// middleware functions return http.HandlerFunc-compatible wrappers.
package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
)

// Scope constants define all valid Roost API scopes.
const (
	ScopeStreamRead    = "stream:read"
	ScopeCatalogRead   = "catalog:read"
	ScopeCatalogWrite  = "catalog:write"
	ScopeBillingRead   = "billing:read"
	ScopeBillingWrite  = "billing:write"
	ScopeAdmin         = "admin"
	ScopeInternal      = "internal"
)

// ScopedClaims extends Claims with a Scopes field for fine-grained authorization.
type ScopedClaims struct {
	Claims
	Scopes []string `json:"scopes"`
}

// HasScope reports whether the claims contain the given scope.
func (sc *ScopedClaims) HasScope(scope string) bool {
	for _, s := range sc.Scopes {
		if s == scope {
			return true
		}
	}
	return false
}

// HasAnyScope reports whether the claims contain any of the given scopes.
func (sc *ScopedClaims) HasAnyScope(scopes ...string) bool {
	for _, required := range scopes {
		if sc.HasScope(required) {
			return true
		}
	}
	return false
}

// scopedClaimsKey is the context key for ScopedClaims.
type scopedClaimsKey struct{}

// ScopedClaimsFromContext extracts ScopedClaims from context.
// Returns nil if not present (RequireScope middleware not applied).
func ScopedClaimsFromContext(ctx context.Context) *ScopedClaims {
	if c, ok := ctx.Value(scopedClaimsKey{}).(*ScopedClaims); ok {
		return c
	}
	return nil
}

// RequireScope returns an HTTP middleware that enforces the given scope.
// The token must have been validated by RequireAuth upstream.
// The scopes claim is extracted from the JWT and checked against the required scope.
// Missing scope → 403 Forbidden.
func RequireScope(scope string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			sc := scopedClaimsFromRequest(r)
			if sc == nil {
				writeScopeError(w, "authentication required", http.StatusUnauthorized)
				return
			}
			if !sc.HasScope(scope) {
				writeScopeError(w, "insufficient scope: "+scope+" required", http.StatusForbidden)
				return
			}
			ctx := context.WithValue(r.Context(), scopedClaimsKey{}, sc)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// RequireAnyScope returns an HTTP middleware that enforces at least one of the given scopes.
// OR semantics — the token must have at least one of the listed scopes.
func RequireAnyScope(scopes ...string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			sc := scopedClaimsFromRequest(r)
			if sc == nil {
				writeScopeError(w, "authentication required", http.StatusUnauthorized)
				return
			}
			if !sc.HasAnyScope(scopes...) {
				writeScopeError(w, "insufficient scope: one of ["+strings.Join(scopes, ", ")+"] required", http.StatusForbidden)
				return
			}
			ctx := context.WithValue(r.Context(), scopedClaimsKey{}, sc)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// scopedClaimsFromRequest extracts ScopedClaims from the request.
// First checks context (if already placed by upstream middleware).
// If not found, extracts directly from the Bearer token.
func scopedClaimsFromRequest(r *http.Request) *ScopedClaims {
	// Check if already in context (set by an earlier middleware invocation).
	if sc := ScopedClaimsFromContext(r.Context()); sc != nil {
		return sc
	}

	// Fall back to reading from the base Claims in context.
	base := ClaimsFromContext(r.Context())
	if base == nil {
		return nil
	}

	// Wrap in ScopedClaims (scopes field will be empty if not present in token).
	return &ScopedClaims{Claims: *base}
}

// writeScopeError writes a JSON scope error response.
func writeScopeError(w http.ResponseWriter, message string, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"error":   "scope_error",
		"message": message,
	})
}

// DefaultSubscriberScopes returns the default scopes assigned to new subscribers.
func DefaultSubscriberScopes() []string {
	return []string{ScopeStreamRead, ScopeCatalogRead, ScopeBillingRead}
}
