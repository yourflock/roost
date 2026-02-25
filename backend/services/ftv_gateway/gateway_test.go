// gateway_test.go — Unit tests for the FTV Gateway service.
package ftv_gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHealth(t *testing.T) {
	srv := NewServer(nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestCheckAndQueue_MissingCanonicalID(t *testing.T) {
	srv := NewServer(nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/ftv/acquire", strings.NewReader(`{"content_type":"movie"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestCheckAndQueue_NoDB_ReturnsQueued(t *testing.T) {
	srv := NewServer(nil, nil)
	body := `{"canonical_id":"imdb:tt0111161","content_type":"movie","family_id":"fam-123"}`
	req := httptest.NewRequest(http.MethodPost, "/ftv/acquire", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), `"queued"`) {
		t.Errorf("expected queued status, got: %s", w.Body.String())
	}
}

func TestGenerateStreamSignedURL_Structure(t *testing.T) {
	url := generateStreamSignedURL(
		"https://stream.yourflock.org",
		"test-secret-key",
		"movie/imdb:tt0111161/1080p/manifest.m3u8",
		15*time.Minute,
	)
	if !strings.Contains(url, "token=") {
		t.Errorf("signed URL must contain token= param: %s", url)
	}
	if !strings.Contains(url, "expires=") {
		t.Errorf("signed URL must contain expires= param: %s", url)
	}
}

func TestGenerateStreamSignedURL_DifferentKeys(t *testing.T) {
	path := "movie/imdb:tt0111161/1080p/manifest.m3u8"
	url1 := generateStreamSignedURL("https://stream.yourflock.org", "key-A", path, 15*time.Minute)
	url2 := generateStreamSignedURL("https://stream.yourflock.org", "key-B", path, 15*time.Minute)
	if url1 == url2 {
		t.Error("different signing keys must produce different URLs")
	}
}

func TestDedup_NoDB_ReturnsQueued(t *testing.T) {
	result, err := CheckAndQueueAcquisition(nil, nil, nil, "fam-1", "imdb:tt0111161", "movie")
	// context.Background() is not passed but function handles nil db.
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Queued {
		t.Error("expected Queued=true with nil DB")
	}
}

func TestFamilySanitizeID(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"a1b2c3d4-e5f6-7890-abcd-ef1234567890", "a1b2c3d4e5f6"},
		{"shortid", "shortid"},
	}
	for _, tc := range tests {
		got := sanitizeFamilyID(tc.input)
		if got != tc.expected {
			t.Errorf("sanitizeFamilyID(%q) = %q, want %q", tc.input, got, tc.expected)
		}
	}
}
