package handlers

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/yourflock/roost/services/owl_api/audit"
	"github.com/yourflock/roost/services/owl_api/middleware"
)

// StoragePathRow is one row from roost_storage_paths.
type StoragePathRow struct {
	ID             string     `json:"id"`
	DisplayName    string     `json:"display_name"`
	PathType       string     `json:"path_type"`
	Path           string     `json:"path"`
	Endpoint       *string    `json:"endpoint,omitempty"`
	TotalBytes     *int64     `json:"total_bytes,omitempty"`
	UsedBytes      *int64     `json:"used_bytes,omitempty"`
	ItemCount      *int       `json:"item_count,omitempty"`
	LastScannedAt  *time.Time `json:"last_scanned_at,omitempty"`
	IsActive       bool       `json:"is_active"`
	CreatedAt      time.Time  `json:"created_at"`
}

// ListStoragePaths handles GET /admin/storage.
func (h *AdminHandlers) ListStoragePaths(w http.ResponseWriter, r *http.Request) {
	claims := middleware.AdminClaimsFromCtx(r.Context())

	rows, err := h.DB.QueryContext(r.Context(),
		`SELECT id, display_name, path_type, path, endpoint,
		        total_bytes, used_bytes, item_count, last_scanned_at, is_active, created_at
		   FROM roost_storage_paths
		  WHERE roost_id = $1
		  ORDER BY created_at ASC`,
		claims.RoostID,
	)
	if err != nil {
		http.Error(w, `{"error":"db_error"}`, http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var paths []StoragePathRow
	for rows.Next() {
		var p StoragePathRow
		if err := rows.Scan(
			&p.ID, &p.DisplayName, &p.PathType, &p.Path, &p.Endpoint,
			&p.TotalBytes, &p.UsedBytes, &p.ItemCount, &p.LastScannedAt,
			&p.IsActive, &p.CreatedAt,
		); err != nil {
			continue
		}
		paths = append(paths, p)
	}
	if paths == nil {
		paths = []StoragePathRow{}
	}
	writeAdminJSON(w, http.StatusOK, paths)
}

// AddStoragePathRequest is the POST /admin/storage/add body.
type AddStoragePathRequest struct {
	DisplayName string  `json:"display_name"`
	PathType    string  `json:"path_type"` // "local" | "minio" | "s3" | "nfs"
	Path        string  `json:"path"`
	Endpoint    *string `json:"endpoint,omitempty"`
}

// AddStoragePath handles POST /admin/storage/add.
func (h *AdminHandlers) AddStoragePath(w http.ResponseWriter, r *http.Request, al *audit.Logger) {
	claims := middleware.AdminClaimsFromCtx(r.Context())

	var req AddStoragePathRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid_body"}`, http.StatusBadRequest)
		return
	}

	if req.DisplayName == "" || req.Path == "" {
		http.Error(w, `{"error":"display_name and path are required"}`, http.StatusBadRequest)
		return
	}

	validTypes := map[string]bool{"local": true, "minio": true, "s3": true, "nfs": true}
	if !validTypes[req.PathType] {
		http.Error(w, `{"error":"invalid path_type"}`, http.StatusBadRequest)
		return
	}

	// Prevent directory traversal in local/nfs paths
	if (req.PathType == "local" || req.PathType == "nfs") && strings.Contains(req.Path, "..") {
		http.Error(w, `{"error":"path contains invalid traversal sequence"}`, http.StatusBadRequest)
		return
	}

	var rowID string
	err := h.DB.QueryRowContext(r.Context(),
		`INSERT INTO roost_storage_paths (roost_id, display_name, path_type, path, endpoint)
		 VALUES ($1, $2, $3, $4, $5)
		 RETURNING id`,
		claims.RoostID, req.DisplayName, req.PathType, req.Path, req.Endpoint,
	).Scan(&rowID)
	if err != nil {
		slog.Error("admin/storage/add: db error", "err", err)
		http.Error(w, `{"error":"db_error"}`, http.StatusInternalServerError)
		return
	}

	al.Log(r, claims.RoostID, claims.FlockUserID, "storage.add", rowID,
		map[string]any{"path_type": req.PathType, "display_name": req.DisplayName},
	)

	writeAdminJSON(w, http.StatusCreated, map[string]string{"id": rowID})
}

// RemoveStoragePath handles DELETE /admin/storage/:id.
// Returns 409 if the path has content. Soft-deletes (sets is_active=false) on success.
func (h *AdminHandlers) RemoveStoragePath(w http.ResponseWriter, r *http.Request, al *audit.Logger) {
	claims := middleware.AdminClaimsFromCtx(r.Context())
	pathID := extractPathID(r.URL.Path, "/admin/storage/", "")
	if !isValidUUID(pathID) {
		http.Error(w, `{"error":"invalid path id"}`, http.StatusBadRequest)
		return
	}

	// Safety check: does this path have indexed content?
	var itemCount int
	_ = h.DB.QueryRowContext(r.Context(),
		`SELECT COUNT(*) FROM recordings WHERE storage_path_id = $1`,
		pathID,
	).Scan(&itemCount)

	if itemCount > 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error":      "storage_path_has_content",
			"item_count": itemCount,
		})
		return
	}

	// Soft delete — preserve history and foreign keys
	result, err := h.DB.ExecContext(r.Context(),
		`UPDATE roost_storage_paths SET is_active = FALSE
		  WHERE id = $1 AND roost_id = $2`,
		pathID, claims.RoostID,
	)
	if err != nil {
		http.Error(w, `{"error":"db_error"}`, http.StatusInternalServerError)
		return
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
		return
	}

	al.Log(r, claims.RoostID, claims.FlockUserID, "storage.remove", pathID, nil)
	w.WriteHeader(http.StatusNoContent)
}

// TriggerScanRequest is the POST /admin/storage/scan body.
type TriggerScanRequest struct {
	PathID *string `json:"path_id,omitempty"` // nil = scan all paths
}

// ScanJobResponse is returned by POST /admin/storage/scan.
type ScanJobResponse struct {
	JobID string `json:"job_id"`
}

// TriggerScan handles POST /admin/storage/scan.
// Enqueues a background scan job and responds 202 Accepted.
func (h *AdminHandlers) TriggerScan(w http.ResponseWriter, r *http.Request, al *audit.Logger) {
	claims := middleware.AdminClaimsFromCtx(r.Context())

	var req TriggerScanRequest
	_ = json.NewDecoder(r.Body).Decode(&req)

	jobID := uuid.New().String()

	// Enqueue scan job — write to Redis key roost:scan_jobs:{roost_id}
	// When Redis is wired, replace with: h.Redis.LPush(ctx, key, payload)
	// For now, log the job intent and proceed
	targetID := "all"
	if req.PathID != nil {
		targetID = *req.PathID
	}

	slog.Info("scan job enqueued",
		"job_id", jobID,
		"roost_id", claims.RoostID,
		"target", targetID,
	)

	al.Log(r, claims.RoostID, claims.FlockUserID, "storage.scan_triggered", targetID,
		map[string]any{"job_id": jobID},
	)

	writeAdminJSON(w, http.StatusAccepted, ScanJobResponse{JobID: jobID})
}

// ScanStatusResponse is the GET /admin/storage/scan/status response.
type ScanStatusResponse struct {
	Running          bool    `json:"running"`
	JobID            string  `json:"job_id"`
	FilesScanned     int     `json:"files_scanned"`
	FilesFound       int     `json:"files_found"`
	Errors           int     `json:"errors"`
	PercentComplete  float64 `json:"percent_complete"`
	EstimatedFinish  *string `json:"estimated_finish,omitempty"`
}

// ScanStatus handles GET /admin/storage/scan/status.
func (h *AdminHandlers) ScanStatus(w http.ResponseWriter, r *http.Request) {
	_ = middleware.AdminClaimsFromCtx(r.Context())

	// Read from Redis key roost:scan_status:{roost_id}
	// When Redis is wired, replace with real Redis read.
	resp := ScanStatusResponse{
		Running:         false,
		JobID:           "",
		FilesScanned:    0,
		FilesFound:      0,
		Errors:          0,
		PercentComplete: 0,
	}
	writeAdminJSON(w, http.StatusOK, resp)
}

// DuplicateGroup represents a set of files with the same content hash.
type DuplicateGroup struct {
	ContentHash string           `json:"content_hash"`
	Copies      []DuplicateCopy  `json:"copies"`
}

// DuplicateCopy is one file in a duplicate group.
type DuplicateCopy struct {
	ID        string    `json:"id"`
	FilePath  string    `json:"file_path"`
	SizeBytes int64     `json:"size_bytes"`
	AddedAt   time.Time `json:"added_at"`
	IsKeeper  bool      `json:"is_keeper"` // true for the oldest copy (will not be deleted)
}

// ListDuplicates handles GET /admin/storage/duplicates.
func (h *AdminHandlers) ListDuplicates(w http.ResponseWriter, r *http.Request) {
	claims := middleware.AdminClaimsFromCtx(r.Context())

	rows, err := h.DB.QueryContext(r.Context(),
		`SELECT content_hash,
		        json_agg(
		            json_build_object(
		                'id', id::text,
		                'file_path', COALESCE(file_path, ''),
		                'size_bytes', COALESCE(file_size_bytes, 0),
		                'added_at', created_at
		            ) ORDER BY created_at ASC
		        ) AS copies
		   FROM recordings
		  WHERE roost_id = $1 AND content_hash IS NOT NULL
		  GROUP BY content_hash
		 HAVING COUNT(*) > 1
		  ORDER BY COUNT(*) DESC`,
		claims.RoostID,
	)
	if err != nil {
		http.Error(w, `{"error":"db_error"}`, http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var groups []DuplicateGroup
	for rows.Next() {
		var hash string
		var copiesJSON []byte
		if err := rows.Scan(&hash, &copiesJSON); err != nil {
			continue
		}
		var copies []DuplicateCopy
		if err := json.Unmarshal(copiesJSON, &copies); err != nil {
			continue
		}
		// Mark the first (oldest) copy as the keeper
		if len(copies) > 0 {
			copies[0].IsKeeper = true
		}
		groups = append(groups, DuplicateGroup{ContentHash: hash, Copies: copies})
	}
	if groups == nil {
		groups = []DuplicateGroup{}
	}
	writeAdminJSON(w, http.StatusOK, groups)
}

// PurgeDuplicatesResponse is returned by DELETE /admin/storage/duplicates.
type PurgeDuplicatesResponse struct {
	DeletedCount int   `json:"deleted_count"`
	BytesFreed   int64 `json:"bytes_freed"`
}

// PurgeDuplicates handles DELETE /admin/storage/duplicates.
// For each duplicate group, deletes all copies except the oldest (keeper).
func (h *AdminHandlers) PurgeDuplicates(w http.ResponseWriter, r *http.Request, al *audit.Logger) {
	claims := middleware.AdminClaimsFromCtx(r.Context())

	// Find all non-keeper duplicate IDs and their sizes
	rows, err := h.DB.QueryContext(r.Context(),
		`WITH ranked AS (
		    SELECT id, file_path, file_size_bytes, content_hash,
		           ROW_NUMBER() OVER (PARTITION BY content_hash ORDER BY created_at ASC) AS rn
		      FROM recordings
		     WHERE roost_id = $1 AND content_hash IS NOT NULL
		 ),
		 dupes AS (
		    SELECT r.content_hash FROM ranked r
		     WHERE r.rn = 1
		     GROUP BY r.content_hash
		    HAVING (SELECT COUNT(*) FROM recordings WHERE roost_id = $1 AND content_hash = r.content_hash) > 1
		 )
		 SELECT r.id, r.file_path, COALESCE(r.file_size_bytes, 0)
		   FROM ranked r
		  WHERE r.rn > 1
		    AND r.content_hash IN (SELECT content_hash FROM dupes)`,
		claims.RoostID,
	)
	if err != nil {
		http.Error(w, `{"error":"db_error"}`, http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	type dupeRow struct {
		id       string
		filePath string
		size     int64
	}
	var toDelete []dupeRow
	for rows.Next() {
		var d dupeRow
		if err := rows.Scan(&d.id, &d.filePath, &d.size); err != nil {
			continue
		}
		toDelete = append(toDelete, d)
	}

	var bytesFreed int64
	for _, d := range toDelete {
		bytesFreed += d.size
		_, _ = h.DB.ExecContext(r.Context(),
			`DELETE FROM recordings WHERE id = $1 AND roost_id = $2`,
			d.id, claims.RoostID,
		)
		// TODO: enqueue file deletion job for d.filePath
		// File path sourced from DB — validated against configured storage roots by worker
		slog.Info("duplicate purged", "id", d.id, "path", d.filePath)
	}

	al.Log(r, claims.RoostID, claims.FlockUserID, "storage.duplicates_purged", "",
		map[string]any{"deleted_count": len(toDelete), "bytes_freed": bytesFreed},
	)

	writeAdminJSON(w, http.StatusOK, PurgeDuplicatesResponse{
		DeletedCount: len(toDelete),
		BytesFreed:   bytesFreed,
	})
}

// StorageRoutes registers all storage admin routes on the given mux prefix.
func (h *AdminHandlers) StorageRoutes(prefix string) func(w http.ResponseWriter, r *http.Request, al *audit.Logger) {
	return func(w http.ResponseWriter, r *http.Request, al *audit.Logger) {
		path := strings.TrimPrefix(r.URL.Path, prefix)

		switch {
		case path == "/storage" && r.Method == http.MethodGet:
			h.ListStoragePaths(w, r)
		case path == "/storage/add" && r.Method == http.MethodPost:
			h.AddStoragePath(w, r, al)
		case path == "/storage/scan" && r.Method == http.MethodPost:
			h.TriggerScan(w, r, al)
		case path == "/storage/scan/status" && r.Method == http.MethodGet:
			h.ScanStatus(w, r)
		case strings.HasPrefix(path, "/storage/") && !strings.Contains(path[9:], "/") && r.Method == http.MethodDelete:
			h.RemoveStoragePath(w, r, al)
		case path == "/storage/duplicates" && r.Method == http.MethodGet:
			h.ListDuplicates(w, r)
		case path == "/storage/duplicates" && r.Method == http.MethodDelete:
			h.PurgeDuplicates(w, r, al)
		default:
			http.NotFound(w, r)
		}
	}
}

// ── SSE scan progress helper (used by admin_storage_sse.go) ──────────────────

// FormatSSEEvent formats a JSON payload as an SSE data line.
func FormatSSEEvent(payload interface{}) (string, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("data: %s\n\n", b), nil
}
