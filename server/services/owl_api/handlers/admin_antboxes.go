package handlers

import (
	"net/http"
	"time"

	"github.com/yourflock/roost/services/owl_api/audit"
	"github.com/yourflock/roost/services/owl_api/middleware"
)

// AntBoxStatus thresholds
const (
	antboxOnlineThreshold = 2 * time.Minute
	antboxStaleThreshold  = 10 * time.Minute
)

// AntBoxRow is one antbox returned by GET /admin/antboxes.
type AntBoxRow struct {
	ID              string     `json:"id"`
	DisplayName     string     `json:"display_name"`
	Location        *string    `json:"location,omitempty"`
	TunerCount      int        `json:"tuner_count"`
	LastSeenAt      *time.Time `json:"last_seen_at,omitempty"`
	FirmwareVersion *string    `json:"firmware_version,omitempty"`
	IsActive        bool       `json:"is_active"`
	Status          string     `json:"status"` // "online" | "stale" | "offline"
}

// computeAntBoxStatus returns "online", "stale", or "offline" based on last_seen_at.
func computeAntBoxStatus(lastSeenAt *time.Time) string {
	if lastSeenAt == nil {
		return "offline"
	}
	age := time.Since(*lastSeenAt)
	if age <= antboxOnlineThreshold {
		return "online"
	}
	if age <= antboxStaleThreshold {
		return "stale"
	}
	return "offline"
}

// ListAntBoxes handles GET /admin/antboxes.
func (h *AdminHandlers) ListAntBoxes(w http.ResponseWriter, r *http.Request) {
	claims := middleware.AdminClaimsFromCtx(r.Context())

	rows, err := h.DB.QueryContext(r.Context(),
		`SELECT id, display_name, location, tuner_count, last_seen_at, firmware_version, is_active
		   FROM antboxes
		  WHERE roost_id = $1
		  ORDER BY created_at ASC`,
		claims.RoostID,
	)
	if err != nil {
		http.Error(w, `{"error":"db_error"}`, http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var boxes []AntBoxRow
	for rows.Next() {
		var b AntBoxRow
		if err := rows.Scan(
			&b.ID, &b.DisplayName, &b.Location, &b.TunerCount,
			&b.LastSeenAt, &b.FirmwareVersion, &b.IsActive,
		); err != nil {
			continue
		}
		b.Status = computeAntBoxStatus(b.LastSeenAt)
		boxes = append(boxes, b)
	}
	if boxes == nil {
		boxes = []AntBoxRow{}
	}
	writeAdminJSON(w, http.StatusOK, boxes)
}

// PatchAntBoxRequest is the PATCH /admin/antboxes/:id body.
type PatchAntBoxRequest struct {
	DisplayName *string `json:"display_name,omitempty"`
	Location    *string `json:"location,omitempty"`
}

// PatchAntBox handles PATCH /admin/antboxes/:id.
func (h *AdminHandlers) PatchAntBox(w http.ResponseWriter, r *http.Request, al *audit.Logger) {
	claims := middleware.AdminClaimsFromCtx(r.Context())
	boxID := extractPathID(r.URL.Path, "/admin/antboxes/", "")
	if !isValidUUID(boxID) {
		http.Error(w, `{"error":"invalid antbox id"}`, http.StatusBadRequest)
		return
	}

	var req PatchAntBoxRequest
	if err := decodeJSON(r, &req); err != nil {
		http.Error(w, `{"error":"invalid_body"}`, http.StatusBadRequest)
		return
	}

	_, err := h.DB.ExecContext(r.Context(),
		`UPDATE antboxes
		    SET display_name = COALESCE($1, display_name),
		        location     = COALESCE($2, location)
		  WHERE id = $3 AND roost_id = $4`,
		req.DisplayName, req.Location, boxID, claims.RoostID,
	)
	if err != nil {
		http.Error(w, `{"error":"db_error"}`, http.StatusInternalServerError)
		return
	}

	al.Log(r, claims.RoostID, claims.FlockUserID, "antbox.update", boxID, nil)
	writeAdminJSON(w, http.StatusOK, map[string]string{"id": boxID, "status": "updated"})
}

// DeleteAntBox handles DELETE /admin/antboxes/:id.
func (h *AdminHandlers) DeleteAntBox(w http.ResponseWriter, r *http.Request, al *audit.Logger) {
	claims := middleware.AdminClaimsFromCtx(r.Context())
	boxID := extractPathID(r.URL.Path, "/admin/antboxes/", "")
	if !isValidUUID(boxID) {
		http.Error(w, `{"error":"invalid antbox id"}`, http.StatusBadRequest)
		return
	}

	result, err := h.DB.ExecContext(r.Context(),
		`UPDATE antboxes SET is_active = FALSE WHERE id = $1 AND roost_id = $2`,
		boxID, claims.RoostID,
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

	al.Log(r, claims.RoostID, claims.FlockUserID, "antbox.remove", boxID, nil)
	w.WriteHeader(http.StatusNoContent)
}

// TriggerAntBoxChannelScan handles POST /admin/antboxes/scan-channels.
func (h *AdminHandlers) TriggerAntBoxChannelScan(w http.ResponseWriter, r *http.Request, al *audit.Logger) {
	claims := middleware.AdminClaimsFromCtx(r.Context())

	var body struct {
		AntBoxID *string `json:"antbox_id,omitempty"`
	}
	_ = decodeJSON(r, &body)

	jobID := newUUID()
	targetID := "all"
	if body.AntBoxID != nil {
		targetID = *body.AntBoxID
	}

	// Enqueue scan command via Redis: roost:antbox_scan:{roost_id}
	// When Redis is wired, publish: h.Redis.LPush(ctx, key, jobPayload)
	_ = targetID // used when Redis is wired

	al.Log(r, claims.RoostID, claims.FlockUserID, "antbox.scan_channels_triggered", targetID,
		map[string]any{"job_id": jobID},
	)

	writeAdminJSON(w, http.StatusAccepted, map[string]string{"job_id": jobID})
}

// AntBoxSignal handles GET /admin/antboxes/:id/signal.
func (h *AdminHandlers) AntBoxSignal(w http.ResponseWriter, r *http.Request) {
	_ = middleware.AdminClaimsFromCtx(r.Context())
	boxID := extractPathID(r.URL.Path, "/admin/antboxes/", "/signal")
	if !isValidUUID(boxID) {
		http.Error(w, `{"error":"invalid antbox id"}`, http.StatusBadRequest)
		return
	}

	// Read from Redis key antbox:signal:{antbox_id}
	// When Redis is wired: val, err := h.Redis.Get(ctx, "antbox:signal:"+boxID).Result()
	// For now: return antbox_offline (device not connected)
	writeAdminJSON(w, http.StatusAccepted, map[string]string{
		"error": "antbox_offline",
	})
}
