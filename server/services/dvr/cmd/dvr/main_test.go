// main_test.go — DVR service unit tests.
package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestDVRHealthEndpoint verifies the /health endpoint responds correctly.
func TestDVRHealthEndpoint(t *testing.T) {
	h := &handler{}
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	h.handleHealth(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "ok") {
		t.Errorf("expected 'ok' in response, got: %s", body)
	}
	if !strings.Contains(body, "roost-dvr") {
		t.Errorf("expected service name in response, got: %s", body)
	}
}

// TestSubscriberIDRequired ensures endpoints reject requests without subscriber_id.
func TestSubscriberIDRequired(t *testing.T) {
	h := &handler{}
	endpoints := []struct {
		method string
		path   string
		fn     func(http.ResponseWriter, *http.Request)
	}{
		{http.MethodGet, "/dvr/recordings", h.handleList},
		{http.MethodGet, "/dvr/quota", h.handleQuota},
	}
	for _, ep := range endpoints {
		req := httptest.NewRequest(ep.method, ep.path, nil)
		rec := httptest.NewRecorder()
		ep.fn(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s: expected 401, got %d", ep.method, ep.path, rec.Code)
		}
	}
}

// TestPathSegment verifies path segment extraction.
func TestPathSegment(t *testing.T) {
	tests := []struct {
		path string
		n    int
		want string
	}{
		{"/dvr/recordings/abc-123", 2, "abc-123"},
		{"/dvr/recordings/abc-123/play", 2, "abc-123"},
		{"/dvr/quota", 1, "quota"},
		{"/dvr/recordings", 2, ""},
	}
	for _, tt := range tests {
		got := pathSegment(tt.path, tt.n)
		if got != tt.want {
			t.Errorf("pathSegment(%q, %d) = %q, want %q", tt.path, tt.n, got, tt.want)
		}
	}
}

// TestNullableString verifies nullable string conversion.
func TestNullableString(t *testing.T) {
	if nullableString("") != nil {
		t.Error("expected nil for empty string")
	}
	got := nullableString("abc")
	if got == nil {
		t.Error("expected non-nil for non-empty string")
	}
	if got.(string) != "abc" {
		t.Errorf("expected 'abc', got %v", got)
	}
}
