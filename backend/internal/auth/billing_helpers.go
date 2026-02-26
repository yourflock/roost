// billing_helpers.go — Auth helpers for the billing service.
// Provides ValidateJWT as a simpler wrapper used by billing handlers
// that don't use the full RequireAuth middleware.
package auth

import (
	"fmt"
	"net/http"
	"strings"
)

// ValidateJWT extracts and validates the Bearer JWT from an HTTP request.
// Returns the parsed Claims (with Subject = subscriber UUID string) or an error.
// This is a lightweight alternative to the RequireAuth middleware for billing handlers.
func ValidateJWT(r *http.Request) (*Claims, error) {
	h := r.Header.Get("Authorization")
	if h == "" {
		return nil, fmt.Errorf("missing Authorization header")
	}
	parts := strings.SplitN(h, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "bearer") {
		return nil, fmt.Errorf("authorization header must be 'Bearer <token>'")
	}
	tokenStr := strings.TrimSpace(parts[1])
	return ValidateAccessToken(tokenStr)
}
