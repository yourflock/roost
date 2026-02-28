// coppa.go — COPPA compliance middleware (P22.4.001).
//
// COPPA (Children's Online Privacy Protection Act) applies when a request comes
// from a subscriber with kid_profile:true in their JWT claims. Roost receives
// this via SSO JWT claims.
//
// Enforcements for kid profiles:
//  1. Block all /billing/ endpoints (no purchases from kid accounts)
//  2. Block content with adult ratings (R, NC-17, TV-MA, UNRATED)
//  3. Enforce parental_controls claim from JWT for additional restrictions
//  4. No analytics tracking (middleware skips analytics for kid profiles)
//
// Non-kid profiles: middleware is a no-op.
package middleware

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
)

// kidProfileKey is the context key for the kid profile flag.
type kidProfileKey struct{}

// IsKidProfile returns true if the current request is from a kid profile.
func IsKidProfile(ctx context.Context) bool {
	v, _ := ctx.Value(kidProfileKey{}).(bool)
	return v
}

// blockedKidRatings lists content ratings that must not be served to kid profiles.
var blockedKidRatings = map[string]bool{
	"R":       true,
	"NC-17":   true,
	"TV-MA":   true,
	"UNRATED": true,
}

// IsBlockedRatingForKids returns true if the given content rating is blocked for kids.
func IsBlockedRatingForKids(rating string) bool {
	return blockedKidRatings[strings.ToUpper(rating)]
}

// COPPAGuard is HTTP middleware that enforces COPPA restrictions.
//
// It reads the kid_profile claim from the JWT (via Authorization header raw decode —
// full validation must be done by RequireAuth upstream).
//
// For kid profiles:
//   - /billing/* → 403 Forbidden
//   - /gdpr/me → 403 (parents use /coppa/child/:id instead)
//   - Sets IsKidProfile(ctx)=true for downstream handlers to check
//
// For non-kid profiles: passes through unchanged.
func COPPAGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		isKid := extractKidProfileClaim(r)

		// Inject kid profile flag into context regardless of value.
		ctx := context.WithValue(r.Context(), kidProfileKey{}, isKid)

		if isKid {
			path := r.URL.Path

			// Block all billing endpoints for kid profiles.
			if strings.HasPrefix(path, "/billing/") {
				writeCOPPAError(w, "Purchases are not available for child accounts.")
				return
			}

			// Block self-service GDPR erasure for kids (parents use /coppa/child/:id).
			if path == "/gdpr/me" {
				writeCOPPAError(w, "Use the parental account to delete child data at /coppa/child/:id.")
				return
			}
		}

		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// writeCOPPAError writes a 403 COPPA restriction response.
func writeCOPPAError(w http.ResponseWriter, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusForbidden)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"error":   "coppa_restriction",
		"message": message,
	})
}

// extractKidProfileClaim reads the kid_profile boolean from the JWT payload
// without verifying the signature (signature verification is done upstream
// by RequireAuth). Returns false if the claim is absent or unparseable.
func extractKidProfileClaim(r *http.Request) bool {
	h := r.Header.Get("Authorization")
	if h == "" {
		return false
	}
	parts := strings.SplitN(h, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "bearer") {
		return false
	}
	tokenStr := strings.TrimSpace(parts[1])
	if tokenStr == "" {
		return false
	}

	segments := strings.Split(tokenStr, ".")
	if len(segments) != 3 {
		return false
	}

	payload, err := base64.RawURLEncoding.DecodeString(segments[1])
	if err != nil {
		return false
	}

	var claims struct {
		KidProfile bool `json:"kid_profile"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return false
	}
	return claims.KidProfile
}
