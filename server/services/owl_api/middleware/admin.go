// Package middleware provides HTTP middleware for the Roost owl_api service.
package middleware

import (
	"context"
	"net/http"
	"strings"

	"github.com/golang-jwt/jwt/v5"
)

// adminCtxKey is an unexported type to prevent collisions with other context keys.
// Using a struct type (not string) means no other package can accidentally overwrite it.
type adminCtxKey struct{}

// AdminClaims holds the verified identity of an admin caller, injected into the
// request context by RequireAdmin. Downstream handlers retrieve it via AdminClaimsFromCtx.
type AdminClaims struct {
	UserID string
	Role        string // "owner" or "admin"
	RoostID     string // roost_id JWT claim — ties this token to a specific Roost server
}

// RequireAdmin rejects requests whose JWT role claim is not "owner" or "admin".
// Returns 403 (not 401) for all rejection cases to avoid leaking endpoint existence.
//
// On success: injects AdminClaims into r.Context() and calls next.
// On failure: writes {"error":"forbidden"} with status 403 and does not call next.
//
// The middleware is pure (no DB calls). The role is read directly from the JWT claim,
// which was injected at token issuance time by the Roost auth service.
func RequireAdmin(jwtSecret []byte, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader := r.Header.Get("Authorization")
		if !strings.HasPrefix(authHeader, "Bearer ") {
			forbidden(w)
			return
		}
		tokenStr := strings.TrimPrefix(authHeader, "Bearer ")

		claims := jwt.MapClaims{}
		token, err := jwt.ParseWithClaims(tokenStr, claims, func(t *jwt.Token) (interface{}, error) {
			// Enforce HMAC signing — reject alg:none and asymmetric algorithms
			if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, jwt.ErrSignatureInvalid
			}
			return jwtSecret, nil
		})
		if err != nil || !token.Valid {
			// Do not include parse error details in response — prevents oracle attacks
			forbidden(w)
			return
		}

		role, _ := claims["role"].(string)
		if role != "owner" && role != "admin" {
			forbidden(w)
			return
		}

		userID, _ := claims["user_id"].(string)
		roostID, _ := claims["roost_id"].(string)

		ctx := context.WithValue(r.Context(), adminCtxKey{}, AdminClaims{
			UserID: userID,
			Role:        role,
			RoostID:     roostID,
		})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// AdminClaimsFromCtx retrieves the AdminClaims injected by RequireAdmin.
// Panics with a descriptive message if called outside an admin-protected handler —
// this is intentional: it turns a wiring mistake into a clear panic at dev time
// rather than a silent security hole in production.
func AdminClaimsFromCtx(ctx context.Context) AdminClaims {
	claims, ok := ctx.Value(adminCtxKey{}).(AdminClaims)
	if !ok {
		panic("middleware.AdminClaimsFromCtx: called outside admin-protected handler — RequireAdmin middleware not applied")
	}
	return claims
}

func forbidden(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusForbidden)
	_, _ = w.Write([]byte(`{"error":"forbidden"}`))
}
