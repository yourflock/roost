package handlers

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/unyeco/roost/services/owl_api/middleware"
)

// ScanProgressEvent is the SSE event payload for real-time scan progress.
type ScanProgressEvent struct {
	JobID           string  `json:"job_id"`
	Status          string  `json:"status"` // "running" | "complete" | "error"
	FilesScanned    int     `json:"files_scanned"`
	FilesFound      int     `json:"files_found"`
	Errors          int     `json:"errors"`
	PercentComplete float64 `json:"percent_complete"`
	CurrentPath     string  `json:"current_path,omitempty"`
}

// StorageScanStream handles GET /admin/storage/scan/stream.
// Streams Server-Sent Events with real-time scan progress.
// When Redis is configured (h.Redis != nil), events arrive via Redis pub/sub on
// channel "roost:scan_progress:{roostID}". Without Redis, the handler sends
// periodic heartbeats so clients stay connected without error.
// Maximum connection lifetime is 30 minutes to prevent resource leaks.
func (h *AdminHandlers) StorageScanStream(w http.ResponseWriter, r *http.Request) {
	claims := middleware.AdminClaimsFromCtx(r.Context())

	// Set SSE response headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // Disable Nginx buffering for streaming

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, `{"error":"streaming unsupported"}`, http.StatusInternalServerError)
		return
	}

	// 30-minute maximum lifetime — prevents stale connections accumulating
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Minute)
	defer cancel()

	// Send initial "connected" SSE comment to establish the stream
	fmt.Fprintf(w, ": connected to scan progress stream\n\n")
	flusher.Flush()

	// Redis path: subscribe to the per-roost scan progress channel.
	if h.Redis != nil {
		channel := fmt.Sprintf("roost:scan_progress:%s", claims.RoostID)
		sub := h.Redis.Subscribe(ctx, channel)
		defer sub.Close()

		msgCh := sub.Channel()
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				// Keep-alive heartbeat — prevents proxy timeouts between events.
				fmt.Fprintf(w, ": heartbeat\n\n")
				flusher.Flush()
			case msg, ok := <-msgCh:
				if !ok {
					// Subscription closed (Redis disconnect or context cancel).
					return
				}
				// Forward the raw Redis message payload as an SSE data line.
				fmt.Fprintf(w, "data: %s\n\n", msg.Payload)
				flusher.Flush()
				// Terminal events close the stream so the client can reconnect cleanly.
				if strings.Contains(msg.Payload, `"status":"complete"`) ||
					strings.Contains(msg.Payload, `"status":"error"`) {
					return
				}
			}
		}
	}

	// No-Redis fallback: heartbeat-only mode. Clients will not receive scan events
	// but the connection stays alive without error. Log at WARN so operators know
	// Redis is not configured.
	slog.Warn("StorageScanStream: Redis not configured — scan events unavailable, heartbeat only",
		"roost_id", claims.RoostID)

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			fmt.Fprintf(w, ": heartbeat\n\n")
			flusher.Flush()
		}
	}
}

// WriteSSEEvent writes a formatted SSE event to the response writer.
// payload must be JSON-serializable. Returns an error if serialization fails.
func WriteSSEEvent(w http.ResponseWriter, flusher http.Flusher, payload interface{}) error {
	line, err := FormatSSEEvent(payload)
	if err != nil {
		return err
	}
	_, err = fmt.Fprint(w, line)
	if err != nil {
		return err
	}
	flusher.Flush()
	// Check if this is a terminal event
	if strings.Contains(line, `"status":"complete"`) || strings.Contains(line, `"status":"error"`) {
		return fmt.Errorf("stream complete")
	}
	return nil
}
