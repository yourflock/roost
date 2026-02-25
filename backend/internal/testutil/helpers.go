// helpers.go — HTTP test helpers for calling service endpoints.
package testutil

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// PostJSON makes a POST request with a JSON body to the given handler.
// Returns the response recorder for assertion.
func PostJSON(t *testing.T, handler http.Handler, path string, body interface{}) *httptest.ResponseRecorder {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal request body: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	return rr
}

// GetJSON makes a GET request to the given handler.
func GetJSON(t *testing.T, handler http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	return rr
}

// GetJSONWithAuth makes a GET request with a Bearer token.
func GetJSONWithAuth(t *testing.T, handler http.Handler, path, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token))
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	return rr
}

// DecodeJSON decodes the response body into v.
func DecodeJSON(t *testing.T, rr *httptest.ResponseRecorder, v interface{}) {
	t.Helper()
	body, err := io.ReadAll(rr.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}
	if err := json.Unmarshal(body, v); err != nil {
		t.Fatalf("decode JSON response (status %d, body: %s): %v", rr.Code, string(body), err)
	}
}

// AssertStatus fails the test if the response code does not match expected.
func AssertStatus(t *testing.T, rr *httptest.ResponseRecorder, expected int) {
	t.Helper()
	if rr.Code != expected {
		body, _ := io.ReadAll(rr.Body)
		t.Errorf("expected status %d, got %d (body: %s)", expected, rr.Code, string(body))
	}
}
