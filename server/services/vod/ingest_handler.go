// ingest_handler.go — HTTP handlers for the VOD ingest admin API.
//
// These endpoints are admin-only (require superowner JWT).
// They complement the existing movie/series CRUD in cmd/vod/main.go by
// adding a pipeline for ingesting external content via URL.
//
// Routes:
//
//	POST   /admin/catalog/ingest                   — start ingest job
//	GET    /admin/catalog/ingest/{job_id}/status   — poll job status
//	DELETE /admin/catalog/ingest/{job_id}          — cancel job
package vod

import (
	"encoding/json"
	"net/http"
	"strings"
)

// IngestHandler provides HTTP handlers for the VOD ingest API.
type IngestHandler struct {
	Store *JobStore
}

// NewIngestHandler creates an IngestHandler backed by store.
// Pass nil store if DB persistence is not yet available (dev mode).
func NewIngestHandler(store *JobStore) *IngestHandler {
	return &IngestHandler{Store: store}
}

// ServeHTTP dispatches ingest API routes.
// Register with: mux.Handle("/admin/catalog/ingest", h)
func (h *IngestHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Strip prefix to get the tail of the path.
	tail := strings.TrimPrefix(r.URL.Path, "/admin/catalog/ingest")
	tail = strings.TrimSuffix(tail, "/")

	switch {
	case tail == "" && r.Method == http.MethodPost:
		h.handleStart(w, r)
	case strings.HasPrefix(tail, "/") && strings.HasSuffix(tail, "/status") && r.Method == http.MethodGet:
		// /admin/catalog/ingest/{id}/status
		parts := strings.Split(strings.Trim(tail, "/"), "/")
		if len(parts) == 2 && parts[1] == "status" {
			h.handleStatus(w, r, parts[0])
		} else {
			writeIngestError(w, http.StatusNotFound, "not_found", "endpoint not found")
		}
	case strings.HasPrefix(tail, "/") && !strings.Contains(tail[1:], "/") && r.Method == http.MethodDelete:
		h.handleCancel(w, r, tail[1:])
	default:
		writeIngestError(w, http.StatusNotFound, "not_found", "endpoint not found")
	}
}

// handleStart handles POST /admin/catalog/ingest.
// Body: { "url": "...", "type": "movie|show|music|podcast|game", "title": "..." }
func (h *IngestHandler) handleStart(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL   string `json:"url"`
		Type  string `json:"type"`
		Title string `json:"title"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeIngestError(w, http.StatusBadRequest, "invalid_json", "valid JSON body required")
		return
	}
	if req.URL == "" {
		writeIngestError(w, http.StatusBadRequest, "missing_field", "url is required")
		return
	}

	job, err := StartIngest(r.Context(), h.Store, req.URL, req.Type, req.Title)
	if err != nil {
		writeIngestError(w, http.StatusBadRequest, "ingest_error", err.Error())
		return
	}

	writeIngestJSON(w, http.StatusAccepted, map[string]string{
		"job_id": job.ID,
		"status": job.Status,
	})
}

// handleStatus handles GET /admin/catalog/ingest/{job_id}/status.
func (h *IngestHandler) handleStatus(w http.ResponseWriter, r *http.Request, jobID string) {
	job := GetJob(jobID)
	if job == nil {
		// Try DB fallback if store is available.
		if h.Store != nil && h.Store.DB != nil {
			dbJob, err := h.Store.getJob(r.Context(), jobID)
			if err == nil {
				writeIngestJSON(w, http.StatusOK, dbJob)
				return
			}
		}
		writeIngestError(w, http.StatusNotFound, "not_found", "job not found")
		return
	}
	writeIngestJSON(w, http.StatusOK, job)
}

// handleCancel handles DELETE /admin/catalog/ingest/{job_id}.
func (h *IngestHandler) handleCancel(w http.ResponseWriter, r *http.Request, jobID string) {
	if !CancelJob(jobID) {
		writeIngestError(w, http.StatusNotFound, "not_found", "job not found or already complete")
		return
	}
	writeIngestJSON(w, http.StatusOK, map[string]string{"status": "cancelled"})
}

// ── DB read ───────────────────────────────────────────────────────────────────

// getJob fetches a job from the DB by ID.
func (s *JobStore) getJob(ctx interface{ Done() <-chan struct{} }, jobID string) (*IngestJob, error) {
	// Implementation note: ctx here is context.Context via the interface pattern.
	// Full pgx/sql implementation would scan from ingest_jobs.
	// This stub returns a placeholder and will be completed when the ingest_jobs
	// migration is run.
	return &IngestJob{
		ID:     jobID,
		Status: "unknown",
	}, nil
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func writeIngestJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeIngestError(w http.ResponseWriter, status int, code, msg string) {
	writeIngestJSON(w, status, map[string]string{"error": code, "message": msg})
}
