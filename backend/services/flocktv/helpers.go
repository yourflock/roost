// helpers.go — Shared response helpers and SQL wrappers for the Flock TV service.
package flocktv

import (
	"database/sql"
	"encoding/json"
	"net/http"
)

// writeJSON encodes v as JSON and writes it with the given status code.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError writes a standard {error: code, message: msg} JSON error response.
func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]string{"error": code, "message": msg})
}

// decodeJSON decodes the request body into v.
// Returns false and writes a 400 if decoding fails.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "request body is not valid JSON")
		return false
	}
	return true
}

// sqlRows is an alias to allow wrapping *sql.Rows in a helper.
type sqlRows = sql.Rows

// sqlQueryRows is a thin wrapper that returns (rows, err) as a (*sqlRows, error) pair
// suitable for use in query helpers.
func sqlQueryRows(rows *sql.Rows, err error) (*sqlRows, error) {
	return rows, err
}
