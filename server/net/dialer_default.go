//go:build !submarine

// dialer_default.go — Standard HTTP client for non-submarine builds.
//
// When built without the "submarine" tag, NewHTTPClient returns a plain
// *http.Client with default transport (no outbound restrictions).
package roostnет

import "net/http"

// NewHTTPClient returns a standard HTTP client with no outbound restrictions.
func NewHTTPClient() *http.Client {
	return &http.Client{}
}
