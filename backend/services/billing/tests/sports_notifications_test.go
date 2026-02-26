// sports_notifications_test.go — Tests for sports notification preferences in billing service.
// P15-T08: Preferences CRUD and my-games filter auth/validation tests.
package tests

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	billingsvc "github.com/yourflock/roost/services/billing"
)

// newSportsTestServer creates a billing server for sports notification tests.
// Uses a nil DB — tests only exercise input validation (no DB queries needed).
func newSportsTestServer() *billingsvc.Server {
	return billingsvc.NewServer(nil, nil, nil, nil)
}

// makeSportsMux creates a registered mux for the sports test server.
func makeSportsMux(srv *billingsvc.Server) *http.ServeMux {
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)
	return mux
}

func TestSportsPreferences_MissingSubscriberID(t *testing.T) {
	srv := newSportsTestServer()
	mux := makeSportsMux(srv)

	body := map[string]interface{}{"team_id": "some-uuid", "notification_level": "all"}
	bodyBytes, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/sports/preferences", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	errObj, _ := resp["error"].(map[string]interface{})
	if errObj == nil || errObj["code"] != "unauthorized" {
		t.Errorf("expected error.code=unauthorized, got %v", resp["error"])
	}
}

func TestGetSportsPreferences_MissingSubscriberID(t *testing.T) {
	srv := newSportsTestServer()
	mux := makeSportsMux(srv)

	req := httptest.NewRequest(http.MethodGet, "/sports/preferences", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

func TestMyGames_MissingSubscriberID(t *testing.T) {
	srv := newSportsTestServer()
	mux := makeSportsMux(srv)

	req := httptest.NewRequest(http.MethodGet, "/sports/my-games", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

func TestSportsPreferences_MissingTeamID(t *testing.T) {
	srv := newSportsTestServer()
	mux := makeSportsMux(srv)

	// Has X-Subscriber-ID but missing team_id
	body := map[string]interface{}{"notification_level": "all"}
	bodyBytes, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/sports/preferences", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Subscriber-ID", "sub-123")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	errObj, _ := resp["error"].(map[string]interface{})
	if errObj == nil || errObj["code"] != "missing_team" {
		t.Errorf("expected error.code=missing_team, got %v", resp["error"])
	}
}

func TestMyGames_MethodNotAllowed(t *testing.T) {
	srv := newSportsTestServer()
	mux := makeSportsMux(srv)

	req := httptest.NewRequest(http.MethodPost, "/sports/my-games", nil)
	req.Header.Set("X-Subscriber-ID", "sub-123")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

func TestSportsPreferencesGET_ReturnsPrefsOrDies(t *testing.T) {
	// With no DB, calling with a subscriber ID will panic or error at DB query.
	// This test verifies that the handler at least passes auth validation
	// (401 check is on subscriber ID presence, not DB call).
	// Since we have no DB, we just verify the response is not 401/400.
	// A nil DB produces an error response at the DB call — that's expected here.
	srv := newSportsTestServer()
	mux := makeSportsMux(srv)

	req := httptest.NewRequest(http.MethodGet, "/sports/preferences", nil)
	req.Header.Set("X-Subscriber-ID", "sub-with-valid-id")
	w := httptest.NewRecorder()

	// Should not panic — must handle nil DB gracefully (returns 500 or similar)
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("handler panicked with nil DB: %v", r)
		}
	}()

	mux.ServeHTTP(w, req)

	// Not 401 or 400 — auth passed; DB error expected
	if w.Code == http.StatusUnauthorized || w.Code == http.StatusBadRequest {
		t.Errorf("unexpected auth/validation error with valid subscriber ID: %d", w.Code)
	}
}
