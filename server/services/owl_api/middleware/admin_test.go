package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

var testSecret = []byte("test-secret-at-least-32-chars-long")

func makeToken(t *testing.T, role string, roostID string, secret []byte, expiry time.Duration) string {
	t.Helper()
	claims := jwt.MapClaims{
		"flock_user_id": "user_001",
		"roost_id":      roostID,
		"exp":           time.Now().Add(expiry).Unix(),
	}
	if role != "" {
		claims["role"] = role
	}
	tok, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(secret)
	if err != nil {
		t.Fatalf("failed to make token: %v", err)
	}
	return tok
}

func TestRequireAdmin(t *testing.T) {
	okHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify claims injected into context
		claims := AdminClaimsFromCtx(r.Context())
		if claims.FlockUserID != "user_001" {
			t.Errorf("expected flock_user_id=user_001, got %s", claims.FlockUserID)
		}
		w.WriteHeader(http.StatusOK)
	})
	mw := RequireAdmin(testSecret, okHandler)

	tests := []struct {
		name       string
		authHeader string
		wantStatus int
	}{
		{
			name:       "missing Authorization header",
			authHeader: "",
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "wrong role (member)",
			authHeader: "Bearer " + makeToken(t, "member", "roost_001", testSecret, time.Hour),
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "wrong role (guest)",
			authHeader: "Bearer " + makeToken(t, "guest", "roost_001", testSecret, time.Hour),
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "no role claim",
			authHeader: "Bearer " + makeToken(t, "", "roost_001", testSecret, time.Hour),
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "valid owner token",
			authHeader: "Bearer " + makeToken(t, "owner", "roost_001", testSecret, time.Hour),
			wantStatus: http.StatusOK,
		},
		{
			name:       "valid admin token",
			authHeader: "Bearer " + makeToken(t, "admin", "roost_001", testSecret, time.Hour),
			wantStatus: http.StatusOK,
		},
		{
			name:       "expired token",
			authHeader: "Bearer " + makeToken(t, "owner", "roost_001", testSecret, -time.Hour),
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "tampered signature (wrong secret)",
			authHeader: "Bearer " + makeToken(t, "owner", "roost_001", []byte("wrong-secret"), time.Hour),
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "not a bearer token",
			authHeader: "Basic dXNlcjpwYXNz",
			wantStatus: http.StatusForbidden,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/admin/status", nil)
			if tc.authHeader != "" {
				req.Header.Set("Authorization", tc.authHeader)
			}
			rr := httptest.NewRecorder()
			mw.ServeHTTP(rr, req)
			if rr.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d", rr.Code, tc.wantStatus)
			}
		})
	}
}
