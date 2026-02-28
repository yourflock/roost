// Package handlers provides shared HTTP handler functions used across Roost services.
// P20.1.001-S03: GET /system/info endpoint
package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/unyeco/roost/internal/config"
)

// SystemInfo is the response body for GET /system/info.
type SystemInfo struct {
	Mode     string            `json:"mode"`
	Version  string            `json:"version"`
	Features map[string]bool   `json:"features"`
}

// HandleSystemInfo returns a handler that reports mode and feature availability.
// GET /system/info
//
// Private mode response:
//
//	{"mode":"private","version":"0.2.0","features":{"subscriber_management":false,"billing":false,"cdn_relay":false}}
//
// Public mode response:
//
//	{"mode":"public","version":"0.2.0","features":{"subscriber_management":true,"billing":true,"cdn_relay":true}}
func HandleSystemInfo(cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		info := SystemInfo{
			Mode:    string(cfg.Mode),
			Version: "0.2.0",
			Features: map[string]bool{
				"subscriber_management": cfg.IsPublicMode(),
				"billing":               cfg.IsPublicMode(),
				"cdn_relay":             cfg.IsPublicMode(),
			},
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(info)
	}
}
