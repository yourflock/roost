package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ---- health endpoint --------------------------------------------------------

func TestHandleHealth(t *testing.T) {
	srv := &server{db: nil}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", srv.handleHealth)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("health: expected 200, got %d", rec.Code)
	}
	var resp map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("health: invalid JSON: %v", err)
	}
	if resp["status"] != "ok" {
		t.Errorf("health: expected status=ok, got %q", resp["status"])
	}
	if resp["service"] != "roost-recommendations" {
		t.Errorf("health: expected service=roost-recommendations, got %q", resp["service"])
	}
}

// ---- recommendations endpoint: missing subscriber_id ----------------------

func TestRecommendationsMissingSubscriberID(t *testing.T) {
	srv := &server{db: nil}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /recommendations/", srv.handleRecommendations)

	req := httptest.NewRequest(http.MethodGet, "/recommendations/", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for missing subscriber_id, got %d", rec.Code)
	}
}

// ---- trending endpoint (no DB): method validation -------------------------

func TestTrendingMethodNotAllowed(t *testing.T) {
	srv := &server{db: nil}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /recommendations/trending", srv.handleTrending)

	req := httptest.NewRequest(http.MethodPost, "/recommendations/trending", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	// POST to a GET-only endpoint should return 405
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405 for POST to trending, got %d", rec.Code)
	}
}

// ---- recItem JSON serialization --------------------------------------------

func TestRecItemJSON(t *testing.T) {
	genre := "Action"
	poster := "https://example.com/poster.jpg"
	item := recItem{
		ID:        "abc-123",
		Title:     "Test Movie",
		Type:      "movie",
		Genre:     &genre,
		PosterURL: &poster,
		Score:     0.85,
	}
	b, err := json.Marshal(item)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(b)
	if !strings.Contains(s, "abc-123") {
		t.Error("JSON should contain ID")
	}
	if !strings.Contains(s, "Action") {
		t.Error("JSON should contain genre")
	}
	if !strings.Contains(s, "0.85") {
		t.Error("JSON should contain score")
	}
}

func TestRecItemJSONOmitsNilOptional(t *testing.T) {
	item := recItem{
		ID:    "xyz",
		Title: "No Genre Movie",
		Type:  "movie",
		// Genre and PosterURL are nil
	}
	b, err := json.Marshal(item)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(b)
	if strings.Contains(s, "genre") {
		t.Error("JSON should omit nil genre field")
	}
	if strings.Contains(s, "poster_url") {
		t.Error("JSON should omit nil poster_url field")
	}
}

// ---- refresh endpoint ------------------------------------------------------

func TestHandleRefresh(t *testing.T) {
	srv := &server{db: nil}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /internal/reco/refresh", srv.handleRefresh)

	req := httptest.NewRequest(http.MethodPost, "/internal/reco/refresh", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("refresh: expected 200, got %d", rec.Code)
	}
	var resp map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("refresh: invalid JSON: %v", err)
	}
	if resp["status"] != "ok" {
		t.Errorf("refresh: expected status=ok, got %s", rec.Body.String())
	}
}
