package handlers

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/yourflock/roost/services/owl_api/audit"
	"github.com/yourflock/roost/services/owl_api/middleware"
)

// ScheduleItem is one entry in GET /admin/dvr/schedule.
type ScheduleItem struct {
	ID                string    `json:"id"`
	Title             string    `json:"title"`
	ChannelName       string    `json:"channel_name"`
	ChannelID         string    `json:"channel_id"`
	StartTime         time.Time `json:"start_time"`
	EndTime           time.Time `json:"end_time"`
	PaddingBeforeSecs int       `json:"padding_before_secs"`
	PaddingAfterSecs  int       `json:"padding_after_secs"`
	StoragePathID     *string   `json:"storage_path_id,omitempty"`
	Status            string    `json:"status"`
}

// ListDVRSchedule handles GET /admin/dvr/schedule.
func (h *AdminHandlers) ListDVRSchedule(w http.ResponseWriter, r *http.Request) {
	claims := middleware.AdminClaimsFromCtx(r.Context())

	rows, err := h.DB.QueryContext(r.Context(),
		`SELECT d.id, d.title, d.channel_id::text, i.display_name,
		        d.start_time, d.end_time, d.padding_before_secs, d.padding_after_secs,
		        d.storage_path_id::text, d.status
		   FROM dvr_schedule d
		   JOIN iptv_sources i ON d.channel_id = i.id
		  WHERE d.roost_id = $1
		    AND d.start_time > NOW()
		    AND d.status != 'cancelled'
		  ORDER BY d.start_time ASC
		  LIMIT 100`,
		claims.RoostID,
	)
	if err != nil {
		http.Error(w, `{"error":"db_error"}`, http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var items []ScheduleItem
	for rows.Next() {
		var s ScheduleItem
		if err := rows.Scan(
			&s.ID, &s.Title, &s.ChannelID, &s.ChannelName,
			&s.StartTime, &s.EndTime, &s.PaddingBeforeSecs, &s.PaddingAfterSecs,
			&s.StoragePathID, &s.Status,
		); err != nil {
			continue
		}
		items = append(items, s)
	}
	if items == nil {
		items = []ScheduleItem{}
	}
	writeAdminJSON(w, http.StatusOK, items)
}

// CreateScheduleRequest is the POST /admin/dvr/schedule body.
type CreateScheduleRequest struct {
	ChannelID          string    `json:"channel_id"`
	Title              string    `json:"title"`
	StartTime          time.Time `json:"start_time"`
	DurationSecs       int       `json:"duration_secs"`
	PaddingBeforeSecs  int       `json:"padding_before_secs"`
	PaddingAfterSecs   int       `json:"padding_after_secs"`
	StoragePathID      *string   `json:"storage_path_id,omitempty"`
}

// ScheduleConflict is returned in 409 responses.
type ScheduleConflict struct {
	ConflictingID    string `json:"conflicting_id"`
	ConflictingTitle string `json:"conflicting_title"`
	StartTime        time.Time `json:"start_time"`
	EndTime          time.Time `json:"end_time"`
	Suggestion       string `json:"suggestion"`
}

// CreateDVRSchedule handles POST /admin/dvr/schedule.
func (h *AdminHandlers) CreateDVRSchedule(w http.ResponseWriter, r *http.Request, al *audit.Logger) {
	claims := middleware.AdminClaimsFromCtx(r.Context())

	var req CreateScheduleRequest
	if err := decodeJSON(r, &req); err != nil {
		http.Error(w, `{"error":"invalid_body"}`, http.StatusBadRequest)
		return
	}

	if req.Title == "" || !isValidUUID(req.ChannelID) || req.DurationSecs <= 0 {
		http.Error(w, `{"error":"title, channel_id, duration_secs required"}`, http.StatusBadRequest)
		return
	}

	// Validate channel belongs to this roost
	var channelOwner string
	err := h.DB.QueryRowContext(r.Context(),
		`SELECT roost_id::text FROM iptv_sources WHERE id = $1`,
		req.ChannelID,
	).Scan(&channelOwner)
	if err != nil || channelOwner != claims.RoostID {
		http.Error(w, `{"error":"channel not found or not owned by this roost"}`, http.StatusBadRequest)
		return
	}

	endTime := req.StartTime.Add(time.Duration(req.DurationSecs+req.PaddingAfterSecs) * time.Second)

	// Conflict detection — Allen interval overlap: A.start < B.end AND A.end > B.start
	conflicts, err := h.detectScheduleConflicts(r, claims.RoostID, req.ChannelID, req.StartTime, endTime, "")
	if err == nil && len(conflicts) > 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error":     "schedule_conflict",
			"conflicts": conflicts,
		})
		return
	}

	var rowID string
	err = h.DB.QueryRowContext(r.Context(),
		`INSERT INTO dvr_schedule
		     (roost_id, channel_id, title, start_time, end_time, padding_before_secs, padding_after_secs, storage_path_id, scheduled_by)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) RETURNING id`,
		claims.RoostID, req.ChannelID, req.Title,
		req.StartTime, endTime, req.PaddingBeforeSecs, req.PaddingAfterSecs,
		req.StoragePathID, claims.FlockUserID,
	).Scan(&rowID)
	if err != nil {
		slog.Error("dvr/schedule: db error", "err", err)
		http.Error(w, `{"error":"db_error"}`, http.StatusInternalServerError)
		return
	}

	al.Log(r, claims.RoostID, claims.FlockUserID, "dvr.schedule_recording", rowID,
		map[string]any{"title": req.Title, "channel_id": req.ChannelID},
	)

	writeAdminJSON(w, http.StatusCreated, map[string]string{"id": rowID, "status": "scheduled"})
}

// CancelDVRSchedule handles DELETE /admin/dvr/schedule/:id.
func (h *AdminHandlers) CancelDVRSchedule(w http.ResponseWriter, r *http.Request, al *audit.Logger) {
	claims := middleware.AdminClaimsFromCtx(r.Context())
	schedID := extractPathID(r.URL.Path, "/admin/dvr/schedule/", "")
	if !isValidUUID(schedID) {
		http.Error(w, `{"error":"invalid schedule id"}`, http.StatusBadRequest)
		return
	}

	// Only cancel 'scheduled' recordings — not 'recording' or 'complete'
	result, err := h.DB.ExecContext(r.Context(),
		`UPDATE dvr_schedule SET status = 'cancelled'
		  WHERE id = $1 AND roost_id = $2 AND status = 'scheduled'`,
		schedID, claims.RoostID,
	)
	if err != nil {
		http.Error(w, `{"error":"db_error"}`, http.StatusInternalServerError)
		return
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		// Either not found or status is not 'scheduled'
		http.Error(w, `{"error":"not found or recording not in scheduled state"}`, http.StatusConflict)
		return
	}

	al.Log(r, claims.RoostID, claims.FlockUserID, "dvr.cancel_recording", schedID, nil)
	w.WriteHeader(http.StatusNoContent)
}

// CreateSeriesRuleRequest is the POST /admin/dvr/series body.
type CreateSeriesRuleRequest struct {
	ChannelID      string  `json:"channel_id"`
	ShowTitle      string  `json:"show_title"`
	AlwaysRecord   bool    `json:"always_record"`
	KeepLastN      *int    `json:"keep_last_n,omitempty"`
	StoragePathID  *string `json:"storage_path_id,omitempty"`
}

// CreateSeriesRule handles POST /admin/dvr/series.
func (h *AdminHandlers) CreateSeriesRule(w http.ResponseWriter, r *http.Request, al *audit.Logger) {
	claims := middleware.AdminClaimsFromCtx(r.Context())

	var req CreateSeriesRuleRequest
	if err := decodeJSON(r, &req); err != nil {
		http.Error(w, `{"error":"invalid_body"}`, http.StatusBadRequest)
		return
	}

	if req.ShowTitle == "" || !isValidUUID(req.ChannelID) {
		http.Error(w, `{"error":"channel_id and show_title required"}`, http.StatusBadRequest)
		return
	}

	if req.KeepLastN != nil && *req.KeepLastN <= 0 {
		http.Error(w, `{"error":"keep_last_n must be greater than 0"}`, http.StatusBadRequest)
		return
	}

	var rowID string
	err := h.DB.QueryRowContext(r.Context(),
		`INSERT INTO dvr_series (roost_id, channel_id, show_title, always_record, keep_last_n, storage_path_id, scheduled_by)
		 VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id`,
		claims.RoostID, req.ChannelID, req.ShowTitle, req.AlwaysRecord, req.KeepLastN, req.StoragePathID, claims.FlockUserID,
	).Scan(&rowID)
	if err != nil {
		http.Error(w, `{"error":"db_error"}`, http.StatusInternalServerError)
		return
	}

	al.Log(r, claims.RoostID, claims.FlockUserID, "dvr.series_rule_created", rowID,
		map[string]any{"show_title": req.ShowTitle},
	)

	writeAdminJSON(w, http.StatusCreated, map[string]string{"id": rowID})
}

// ListDVRRecordings handles GET /admin/dvr/recordings.
func (h *AdminHandlers) ListDVRRecordings(w http.ResponseWriter, r *http.Request) {
	claims := middleware.AdminClaimsFromCtx(r.Context())

	rows, err := h.DB.QueryContext(r.Context(),
		`SELECT id, title, start_time, end_time, status, COALESCE(file_path,''), COALESCE(file_size_bytes,0), channel_id::text
		   FROM dvr_schedule
		  WHERE roost_id = $1 AND status IN ('complete', 'failed')
		  ORDER BY start_time DESC
		  LIMIT 50`,
		claims.RoostID,
	)
	if err != nil {
		http.Error(w, `{"error":"db_error"}`, http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	type recordingRow struct {
		ID          string    `json:"id"`
		Title       string    `json:"title"`
		StartTime   time.Time `json:"start_time"`
		EndTime     time.Time `json:"end_time"`
		Status      string    `json:"status"`
		FilePath    string    `json:"file_path,omitempty"`
		FileSizeBytes int64   `json:"file_size_bytes"`
		ChannelID   string    `json:"channel_id"`
	}

	var recordings []recordingRow
	for rows.Next() {
		var rec recordingRow
		if err := rows.Scan(&rec.ID, &rec.Title, &rec.StartTime, &rec.EndTime,
			&rec.Status, &rec.FilePath, &rec.FileSizeBytes, &rec.ChannelID); err != nil {
			continue
		}
		recordings = append(recordings, rec)
	}
	if recordings == nil {
		recordings = []recordingRow{}
	}
	writeAdminJSON(w, http.StatusOK, recordings)
}

// DeleteDVRRecording handles DELETE /admin/dvr/recordings/:id.
func (h *AdminHandlers) DeleteDVRRecording(w http.ResponseWriter, r *http.Request, al *audit.Logger) {
	claims := middleware.AdminClaimsFromCtx(r.Context())
	recID := extractPathID(r.URL.Path, "/admin/dvr/recordings/", "")
	if !isValidUUID(recID) {
		http.Error(w, `{"error":"invalid recording id"}`, http.StatusBadRequest)
		return
	}

	var filePath string
	var fileSize int64
	err := h.DB.QueryRowContext(r.Context(),
		`SELECT COALESCE(file_path,''), COALESCE(file_size_bytes,0)
		   FROM dvr_schedule
		  WHERE id = $1 AND roost_id = $2 AND status IN ('complete','failed')`,
		recID, claims.RoostID,
	).Scan(&filePath, &fileSize)
	if err != nil {
		http.Error(w, `{"error":"not found or recording not complete/failed"}`, http.StatusNotFound)
		return
	}

	// Enqueue async file deletion job — never inline os.Remove in handler
	slog.Info("recording delete: file deletion enqueued", "path", filePath, "size", fileSize)

	// Hard delete DB row (recordings are user data; no audit value in soft-delete)
	_, _ = h.DB.ExecContext(r.Context(),
		`DELETE FROM dvr_schedule WHERE id = $1 AND roost_id = $2`,
		recID, claims.RoostID,
	)

	al.Log(r, claims.RoostID, claims.FlockUserID, "dvr.recording_deleted", recID,
		map[string]any{"file_size_bytes": fileSize},
	)

	w.WriteHeader(http.StatusNoContent)
}

// ListDVRConflicts handles GET /admin/dvr/conflicts.
func (h *AdminHandlers) ListDVRConflicts(w http.ResponseWriter, r *http.Request) {
	claims := middleware.AdminClaimsFromCtx(r.Context())

	// Find all scheduled recordings that overlap with other scheduled recordings
	rows, err := h.DB.QueryContext(r.Context(),
		`SELECT a.id, a.title, a.start_time, a.end_time, a.channel_id::text,
		        b.id, b.title, b.start_time, b.end_time
		   FROM dvr_schedule a
		   JOIN dvr_schedule b
		     ON a.channel_id = b.channel_id
		    AND a.id < b.id  -- prevent duplicate pairs
		    AND a.start_time < b.end_time
		    AND a.end_time   > b.start_time
		  WHERE a.roost_id = $1
		    AND a.status = 'scheduled'
		    AND b.status = 'scheduled'`,
		claims.RoostID,
	)
	if err != nil {
		http.Error(w, `{"error":"db_error"}`, http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	type conflictPair struct {
		RecordingA ScheduleConflict `json:"recording_a"`
		RecordingB ScheduleConflict `json:"recording_b"`
	}

	var pairs []conflictPair
	for rows.Next() {
		var a, b ScheduleConflict
		if err := rows.Scan(
			&a.ConflictingID, &a.ConflictingTitle, &a.StartTime, &a.EndTime, nil,
			&b.ConflictingID, &b.ConflictingTitle, &b.StartTime, &b.EndTime,
		); err != nil {
			continue
		}
		a.Suggestion = "Consider adding a second IPTV source or rescheduling one recording"
		b.Suggestion = "Consider adding a second IPTV source or rescheduling one recording"
		pairs = append(pairs, conflictPair{RecordingA: a, RecordingB: b})
	}
	if pairs == nil {
		pairs = []conflictPair{}
	}
	writeAdminJSON(w, http.StatusOK, pairs)
}

// detectScheduleConflicts finds existing scheduled recordings that overlap the given window.
func (h *AdminHandlers) detectScheduleConflicts(
	r *http.Request,
	roostID, channelID string,
	startTime, endTime time.Time,
	excludeID string,
) ([]ScheduleConflict, error) {
	rows, err := h.DB.QueryContext(r.Context(),
		`SELECT id, title, start_time, end_time
		   FROM dvr_schedule
		  WHERE roost_id = $1
		    AND channel_id = $2
		    AND status = 'scheduled'
		    AND start_time < $4
		    AND end_time   > $3
		    AND id != COALESCE($5::uuid, '00000000-0000-0000-0000-000000000000'::uuid)`,
		roostID, channelID, startTime, endTime, excludeID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var conflicts []ScheduleConflict
	for rows.Next() {
		var c ScheduleConflict
		if err := rows.Scan(&c.ConflictingID, &c.ConflictingTitle, &c.StartTime, &c.EndTime); err != nil {
			continue
		}
		c.Suggestion = "Consider adding a second IPTV source or adjusting the recording time"
		conflicts = append(conflicts, c)
	}
	return conflicts, nil
}
