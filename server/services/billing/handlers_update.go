// handlers_update.go — Version check endpoint for Roost self-update awareness.
//
// GET /update/check
//
//	Returns the latest Roost version from GitHub releases.
//	Compares against the running version (from X-Roost-Version header or VERSION env var).
//	Results are cached for 1 hour to avoid hitting GitHub rate limits.
//
//	Response:
//	  {
//	    "current_version": "1.0.0",
//	    "latest_version":  "1.1.0",
//	    "update_available": true,
//	    "release_url":  "https://github.com/unyeco/roost/releases/tag/v1.1.0",
//	    "published_at": "2026-03-01T00:00:00Z",
//	    "checked_at":   "2026-03-15T12:00:00Z"
//	  }
//
//	No authentication required — version check is safe to expose publicly.
//	Self-hosters can poll this to know when to update.
package billing

import (
	"encoding/json"
	"net/http"
	"os"

	"github.com/unyeco/roost/pkg/updater"
)

// currentVersion is set at build time via -ldflags="-X billing.currentVersion=...".
// Falls back to the VERSION environment variable, then "dev".
var currentVersion = ""

// resolveCurrentVersion returns the running version string.
// Priority: build-time injection → VERSION env var → "dev".
func resolveCurrentVersion(r *http.Request) string {
	// 1. Build-time injected version (set by CI/CD via ldflags)
	if currentVersion != "" {
		return currentVersion
	}
	// 2. VERSION environment variable (set in Docker image / systemd unit)
	if v := os.Getenv("VERSION"); v != "" {
		return v
	}
	// 3. X-Roost-Version header (useful for proxied health checks)
	if v := r.Header.Get("X-Roost-Version"); v != "" {
		return v
	}
	return "dev"
}

// handleUpdateCheck handles GET /update/check.
// Returns version comparison info for the running Roost instance.
// Cached for 1 hour per the updater package's cache policy.
func (s *Server) handleUpdateCheck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET only")
		return
	}

	version := resolveCurrentVersion(r)

	info, err := updater.CheckLatestVersion(r.Context(), version)
	if err != nil {
		// Treat network errors as a non-fatal degradation.
		// Still return a response — just mark the check as failed.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"current_version":  version,
			"latest_version":   version,
			"update_available": false,
			"error":            "version check temporarily unavailable",
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "public, max-age=3600") // Cache at HTTP layer too
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(info)
}
