// helpers.go — shared utilities for the auth service.
package auth

import (
	"os"

	"github.com/google/uuid"
)

// parseUUID is a convenience wrapper around uuid.Parse that discards the error
// (used when the UUID was already validated as a JWT claim).
func parseUUID(s string) (uuid.UUID, error) {
	return uuid.Parse(s)
}

// getEnv returns an environment variable value with fallback.
func getEnv(key string) string {
	return os.Getenv(key)
}
