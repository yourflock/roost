// mode_test.go — Unit tests for RequirePublicMode middleware.
// P20.3.001: Mode middleware unit tests
package middleware_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/yourflock/roost/internal/middleware"
)

// handlerOK is a trivial next handler that writes 200 OK.
var handlerOK = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"ok":true}`))
})

func TestRequirePublicMode_PrivateModeBlocks(t *testing.T) {
	// When ROOST_MODE=private, protected routes must return 403 with JSON body.
	isPublic := func() bool { return false }
	handler := middleware.RequirePublicMode(isPublic, handlerOK)

	req := httptest.NewRequest(http.MethodGet, "/billing/checkout", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", rr.Code)
	}

	// Body must be valid JSON with the error fields.
	var body map[string]string
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("response body is not valid JSON: %v", err)
	}
	if body["error"] == "" {
		t.Error("expected 'error' field in JSON body")
	}
	if body["mode"] != "private" {
		t.Errorf("expected mode=private in body, got %q", body["mode"])
	}
	if body["docs"] == "" {
		t.Error("expected 'docs' field in JSON body")
	}

	// Content-Type must be application/json.
	ct := rr.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("expected Content-Type application/json, got %q", ct)
	}
}

func TestRequirePublicMode_PublicModeAllows(t *testing.T) {
	// When ROOST_MODE=public, the next handler must be called and its response passed through.
	isPublic := func() bool { return true }
	handler := middleware.RequirePublicMode(isPublic, handlerOK)

	req := httptest.NewRequest(http.MethodGet, "/billing/checkout", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rr.Code)
	}

	var body map[string]bool
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("response body is not valid JSON: %v", err)
	}
	if !body["ok"] {
		t.Error("expected ok=true from downstream handler")
	}
}

func TestRequirePublicMode_MethodAgnostic(t *testing.T) {
	// The middleware should block any HTTP method in private mode.
	isPublic := func() bool { return false }
	handler := middleware.RequirePublicMode(isPublic, handlerOK)

	methods := []string{http.MethodGet, http.MethodPost, http.MethodPatch, http.MethodDelete}
	for _, method := range methods {
		req := httptest.NewRequest(method, "/addon/manifest", nil)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusForbidden {
			t.Errorf("method %s: expected 403, got %d", method, rr.Code)
		}
	}
}

func TestRequirePublicMode_DynamicToggle(t *testing.T) {
	// The isPublicMode function is called per-request, so toggling it should
	// affect subsequent requests without restarting the handler.
	publicMode := false
	isPublic := func() bool { return publicMode }
	handler := middleware.RequirePublicMode(isPublic, handlerOK)

	// First request: private mode → 403.
	req1 := httptest.NewRequest(http.MethodGet, "/billing/plans", nil)
	rr1 := httptest.NewRecorder()
	handler.ServeHTTP(rr1, req1)
	if rr1.Code != http.StatusForbidden {
		t.Errorf("expected 403 when private, got %d", rr1.Code)
	}

	// Toggle to public.
	publicMode = true

	// Second request: public mode → 200.
	req2 := httptest.NewRequest(http.MethodGet, "/billing/plans", nil)
	rr2 := httptest.NewRecorder()
	handler.ServeHTTP(rr2, req2)
	if rr2.Code != http.StatusOK {
		t.Errorf("expected 200 when public, got %d", rr2.Code)
	}
}
