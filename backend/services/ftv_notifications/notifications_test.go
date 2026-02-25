// notifications_test.go — Tests for the FTV notification service.
package ftv_notifications

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHealth(t *testing.T) {
	svc := NewNotificationService(nil, newTestLogger())
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	svc.Routes().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestContentReady_MissingCanonicalID(t *testing.T) {
	svc := NewNotificationService(nil, newTestLogger())
	body := `{"content_type": "movie"}`
	req := httptest.NewRequest(http.MethodPost, "/internal/content-ready", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	svc.Routes().ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestContentReady_NoDB_OK(t *testing.T) {
	svc := NewNotificationService(nil, newTestLogger())
	body := `{"canonical_id":"imdb:tt0111161","content_type":"movie"}`
	req := httptest.NewRequest(http.MethodPost, "/internal/content-ready", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	svc.Routes().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "families_notified") {
		t.Errorf("expected families_notified in response, got: %s", w.Body.String())
	}
}

func TestNotificationStream_MissingFamilyID(t *testing.T) {
	svc := NewNotificationService(nil, newTestLogger())
	req := httptest.NewRequest(http.MethodGet, "/ftv/notifications/stream", nil)
	w := httptest.NewRecorder()
	svc.Routes().ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestNotificationStream_NoDB_EmptyList(t *testing.T) {
	svc := NewNotificationService(nil, newTestLogger())
	req := httptest.NewRequest(http.MethodGet, "/ftv/notifications/stream?family_id=fam-123", nil)
	w := httptest.NewRecorder()
	svc.Routes().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "notifications") {
		t.Errorf("expected notifications key in response")
	}
}
