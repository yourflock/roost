// response.go — shared JSON response helpers for auth handlers.
package auth

import (
	"encoding/json"
	"net/http"
)

// ErrorResponse is the standard error envelope for all auth endpoints.
type ErrorResponse struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

// writeError writes a JSON error response with the given status code.
func writeError(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(ErrorResponse{Error: code, Message: msg})
}

// writeJSON writes a JSON success response with status 200.
func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
