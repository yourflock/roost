// exports.go — re-exports for use by service handlers.
// Keeps handler imports clean by providing WriteError/WriteJSON from the auth package.
package auth

import "net/http"

// WriteError is the exported version of the internal writeError.
func WriteError(w http.ResponseWriter, status int, code, msg string) {
	writeError(w, status, code, msg)
}

// WriteJSON is the exported version of the internal writeJSON.
func WriteJSON(w http.ResponseWriter, status int, v interface{}) {
	writeJSON(w, status, v)
}

// RequireAuth wraps the auth middleware for external use.
// This is the same as the unexported version — exported for service handlers.
// (RequireAuth and RequireVerifiedEmail are already exported in middleware.go)
