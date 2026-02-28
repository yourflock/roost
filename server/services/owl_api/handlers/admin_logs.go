package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/yourflock/roost/services/owl_api/middleware"
)

// logCredentialDenyList is the case-insensitive list of field keys to redact.
// Any field whose key contains one of these substrings has its value replaced with "[REDACTED]".
var logCredentialDenyList = []string{
	"password", "secret", "token", "key", "credential", "auth", "private",
}

// LogEntry is one structured log entry from the ring buffer.
type LogEntry struct {
	Timestamp string                 `json:"ts"`
	Level     string                 `json:"level"`
	Service   string                 `json:"service"`
	Message   string                 `json:"msg"`
	Fields    map[string]interface{} `json:"fields,omitempty"`
}

// GetLogs handles GET /admin/logs.
// Query params: ?limit=500&level=error|warn|info|debug&service=ingest|catalog|billing
func (h *AdminHandlers) GetLogs(w http.ResponseWriter, r *http.Request) {
	_ = middleware.AdminClaimsFromCtx(r.Context())

	q := r.URL.Query()
	limit := 100
	if l := q.Get("limit"); l != "" {
		fmt.Sscanf(l, "%d", &limit)
		if limit > 2000 {
			limit = 2000
		}
		if limit < 1 {
			limit = 1
		}
	}
	levelFilter := strings.ToLower(q.Get("level"))
	serviceFilter := strings.ToLower(q.Get("service"))

	// When Redis is wired:
	//   entries, _ := h.Redis.LRange(ctx, "roost:log_buffer:"+claims.RoostID, 0, int64(limit-1)).Result()
	// For now, return empty log list
	var entries []LogEntry

	// Filter and redact
	var filtered []LogEntry
	for _, entry := range entries {
		if levelFilter != "" && strings.ToLower(entry.Level) != levelFilter {
			continue
		}
		if serviceFilter != "" && strings.ToLower(entry.Service) != serviceFilter {
			continue
		}
		entry.Fields = redactLogFields(entry.Fields)
		filtered = append(filtered, entry)
	}
	if filtered == nil {
		filtered = []LogEntry{}
	}
	writeAdminJSON(w, http.StatusOK, filtered)
}

// LogStream handles GET /admin/logs/stream (SSE).
// Optional ?level= filter. Maximum connection: 30 minutes.
func (h *AdminHandlers) LogStream(w http.ResponseWriter, r *http.Request) {
	_ = middleware.AdminClaimsFromCtx(r.Context())

	levelFilter := strings.ToLower(r.URL.Query().Get("level"))

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, `{"error":"streaming unsupported"}`, http.StatusInternalServerError)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Minute)
	defer cancel()

	_ = levelFilter // used when Redis pub/sub is wired

	fmt.Fprintf(w, ": connected to log stream\n\n")
	flusher.Flush()

	ticker := time.NewTicker(10 * time.Second)
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

// redactLogFields replaces values of credential-like keys with "[REDACTED]".
func redactLogFields(fields map[string]interface{}) map[string]interface{} {
	if fields == nil {
		return nil
	}
	result := make(map[string]interface{}, len(fields))
	for k, v := range fields {
		if isCredentialKey(k) {
			result[k] = "[REDACTED]"
		} else {
			result[k] = v
		}
	}
	return result
}

// isCredentialKey returns true if the key (case-insensitive) contains a credential keyword.
func isCredentialKey(key string) bool {
	lower := strings.ToLower(key)
	for _, deny := range logCredentialDenyList {
		if strings.Contains(lower, deny) {
			return true
		}
	}
	return false
}

// WriteLogSSEEvent formats and writes one log entry as an SSE event.
func WriteLogSSEEvent(w http.ResponseWriter, flusher http.Flusher, entry LogEntry) error {
	entry.Fields = redactLogFields(entry.Fields)
	b, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "data: %s\n\n", b)
	if err != nil {
		return err
	}
	flusher.Flush()
	return nil
}
