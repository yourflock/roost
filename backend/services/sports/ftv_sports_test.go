// ftv_sports_test.go — Tests for Flock TV sports handlers (FTV.4).
package sports

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMultiGameTicker_NoDB(t *testing.T) {
	srv := NewServer(nil)
	req := httptest.NewRequest(http.MethodGet, "/ftv/sports/scores", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"games"`) {
		t.Errorf("expected games key in response: %s", w.Body.String())
	}
}

func TestFamilyPicks_MissingFamilyID(t *testing.T) {
	srv := NewServer(nil)
	req := httptest.NewRequest(http.MethodGet, "/ftv/sports/picks", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestFamilyPicks_NoDB_EmptyList(t *testing.T) {
	srv := NewServer(nil)
	req := httptest.NewRequest(http.MethodGet, "/ftv/sports/picks?family_id=fam-123", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), `"picks"`) {
		t.Errorf("expected picks key: %s", w.Body.String())
	}
}

func TestAddPick_MissingFields(t *testing.T) {
	srv := NewServer(nil)
	body := `{"family_id":"fam-123"}`
	req := httptest.NewRequest(http.MethodPost, "/ftv/sports/picks", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestAddPick_NoDB_OK(t *testing.T) {
	srv := NewServer(nil)
	body := `{"family_id":"fam-123","team_id":"550e8400-e29b-41d4-a716-446655440000"}`
	req := httptest.NewRequest(http.MethodPost, "/ftv/sports/picks", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
}

func TestSportsLeaderboard_NoDB(t *testing.T) {
	srv := NewServer(nil)
	req := httptest.NewRequest(http.MethodGet, "/ftv/sports/leaderboard", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), `"leaderboard"`) {
		t.Errorf("expected leaderboard key: %s", w.Body.String())
	}
}

func TestScoreNotification_MissingEventID(t *testing.T) {
	srv := NewServer(nil)
	body := `{"home_score":3,"away_score":1}`
	req := httptest.NewRequest(http.MethodPost, "/internal/sports/score-update", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestScoreNotification_NoDB_OK(t *testing.T) {
	srv := NewServer(nil)
	body := `{"event_id":"550e8400-e29b-41d4-a716-446655440000","home_score":2,"away_score":1,"status":"in_progress"}`
	req := httptest.NewRequest(http.MethodPost, "/internal/sports/score-update", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}
