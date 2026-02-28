// Package health provides the AntBox HTTP health check endpoint (T-7H.1.004).
package health

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"runtime"
	"time"

	"antbox/daemon"
)

// Status is the JSON payload returned by the health endpoint.
type Status struct {
	Status    string       `json:"status"`
	Version   string       `json:"version"`
	Uptime    string       `json:"uptime"`
	GoVersion string       `json:"go_version"`
	Devices   []DeviceInfo `json:"devices"`
}

// DeviceInfo describes one DVB tuner device.
type DeviceInfo struct {
	Path    string `json:"path"`
	Online  bool   `json:"online"`
	Streams int    `json:"active_streams"`
}

var startTime = time.Now()

// Handler returns the health check HTTP handler.
func Handler(getDevices func() []DeviceInfo) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		devices := getDevices()
		allOnline := true
		for _, d := range devices {
			if !d.Online {
				allOnline = false
				break
			}
		}

		statusStr := "ok"
		if len(devices) > 0 && !allOnline {
			statusStr = "degraded"
		}

		resp := Status{
			Status:    statusStr,
			Version:   daemon.Version,
			Uptime:    fmt.Sprintf("%.0fs", time.Since(startTime).Seconds()),
			GoVersion: runtime.Version(),
			Devices:   devices,
		}

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(resp) //nolint:errcheck
	}
}

// StartServer starts the health HTTP server on addr (e.g. ":8087").
// It returns the server so the caller can shut it down gracefully.
func StartServer(addr string, getDevices func() []DeviceInfo) *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", Handler(getDevices))
	mux.HandleFunc("/healthz", Handler(getDevices)) // Kubernetes liveness alias

	srv := &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 5 * time.Second,
	}

	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			fmt.Fprintf(os.Stderr, "health server error: %v\n", err)
		}
	}()

	return srv
}
