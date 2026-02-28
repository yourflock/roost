package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/unyeco/roost/services/owl_api/audit"
	"github.com/unyeco/roost/services/owl_api/middleware"
)

// AdminUserRow is one row from roost_users returned by GET /admin/users.
type AdminUserRow struct {
	ID           string    `json:"id"`
	UserID string `json:"user_id"``
	Role         string    `json:"role"`
	InvitedBy    *string   `json:"invited_by,omitempty"`
	AddedAt      time.Time `json:"added_at"`
}

// ListUsers handles GET /admin/users.
// Returns all users for the caller's Roost server, ordered by added_at ASC.
func (h *AdminHandlers) ListUsers(w http.ResponseWriter, r *http.Request) {
	claims := middleware.AdminClaimsFromCtx(r.Context())

	rows, err := h.DB.QueryContext(r.Context(),
		`SELECT id, user_id, role, invited_by, added_at
		   FROM roost_users
		  WHERE roost_id = $1
		  ORDER BY added_at ASC`,
		claims.RoostID,
	)
	if err != nil {
		http.Error(w, `{"error":"db_error"}`, http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var users []AdminUserRow
	for rows.Next() {
		var u AdminUserRow
		if err := rows.Scan(&u.ID, &u.UserID, &u.Role, &u.InvitedBy, &u.AddedAt); err != nil {
			continue
		}
		users = append(users, u)
	}
	if users == nil {
		users = []AdminUserRow{}
	}
	writeAdminJSON(w, http.StatusOK, users)
}

// InviteUserRequest is the POST /admin/users/invite request body.
type InviteUserRequest struct {
	UserID string `json:"user_id"``
	Role        string `json:"role"` // "admin" | "member" | "guest"
}

// InviteUser handles POST /admin/users/invite.
func (h *AdminHandlers) InviteUser(w http.ResponseWriter, r *http.Request, al *audit.Logger) {
	claims := middleware.AdminClaimsFromCtx(r.Context())

	var req InviteUserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid_body"}`, http.StatusBadRequest)
		return
	}

	if req.UserID == "" {
		http.Error(w, `{"error":"user_id required"}`, http.StatusBadRequest)
		return
	}

	// Role validation — owner is set only at install time, never via invite
	validRoles := map[string]bool{"admin": true, "member": true, "guest": true}
	if !validRoles[req.Role] {
		http.Error(w, `{"error":"invalid role; must be admin, member, or guest"}`, http.StatusBadRequest)
		return
	}

	// Upsert: insert or update role if user already exists
	var rowID string
	err := h.DB.QueryRowContext(r.Context(),
		`INSERT INTO roost_users (roost_id, user_id, role, invited_by)
		 VALUES ($1, $2, $3, $4)
		 ON CONFLICT (roost_id, user_id) DO UPDATE SET role = EXCLUDED.role
		 RETURNING id`,
		claims.RoostID, req.UserID, req.Role, claims.UserID,
	).Scan(&rowID)
	if err != nil {
		slog.Error("admin/users/invite: db error", "err", err)
		http.Error(w, `{"error":"db_error"}`, http.StatusInternalServerError)
		return
	}

	// Fire-and-forget invite notification (5s timeout, failure does not affect response)
	go sendInviteNotification(claims.RoostID, req.UserID, claims.UserID, "roost_invite")

	al.Log(r, claims.RoostID, claims.UserID, "user.invite", req.UserID,
		map[string]any{"role": req.Role, "row_id": rowID},
	)

	writeAdminJSON(w, http.StatusCreated, map[string]string{"id": rowID, "status": "invited"})
}

// PatchUserRoleRequest is the PATCH /admin/users/:id/role request body.
type PatchUserRoleRequest struct {
	Role string `json:"role"`
}

// PatchUserRole handles PATCH /admin/users/:id/role.
// Cannot change the role of the owner row.
func (h *AdminHandlers) PatchUserRole(w http.ResponseWriter, r *http.Request, al *audit.Logger) {
	claims := middleware.AdminClaimsFromCtx(r.Context())
	userID := extractPathID(r.URL.Path, "/admin/users/", "/role")
	if !isValidUUID(userID) {
		http.Error(w, `{"error":"invalid user id"}`, http.StatusBadRequest)
		return
	}

	var req PatchUserRoleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid_body"}`, http.StatusBadRequest)
		return
	}

	validRoles := map[string]bool{"admin": true, "member": true, "guest": true}
	if !validRoles[req.Role] {
		http.Error(w, `{"error":"invalid role"}`, http.StatusBadRequest)
		return
	}

	// Fetch old role for audit
	var oldRole string
	_ = h.DB.QueryRowContext(r.Context(),
		`SELECT role FROM roost_users WHERE id=$1 AND roost_id=$2`,
		userID, claims.RoostID,
	).Scan(&oldRole)

	// Update — WHERE role != 'owner' ensures owner is immutable at SQL level
	result, err := h.DB.ExecContext(r.Context(),
		`UPDATE roost_users SET role=$1
		  WHERE id=$2 AND roost_id=$3 AND role != 'owner'`,
		req.Role, userID, claims.RoostID,
	)
	if err != nil {
		http.Error(w, `{"error":"db_error"}`, http.StatusInternalServerError)
		return
	}

	n, _ := result.RowsAffected()
	if n == 0 {
		// Either user not found, or they are the owner
		http.Error(w, `{"error":"user not found or owner is immutable"}`, http.StatusForbidden)
		return
	}

	al.Log(r, claims.RoostID, claims.UserID, "user.role_change", userID,
		map[string]any{"old_role": oldRole, "new_role": req.Role},
	)

	writeAdminJSON(w, http.StatusOK, map[string]string{"id": userID, "role": req.Role})
}

// DeleteUser handles DELETE /admin/users/:id.
// Cannot delete the owner row.
func (h *AdminHandlers) DeleteUser(w http.ResponseWriter, r *http.Request, al *audit.Logger) {
	claims := middleware.AdminClaimsFromCtx(r.Context())
	userID := extractPathID(r.URL.Path, "/admin/users/", "")
	if !isValidUUID(userID) {
		http.Error(w, `{"error":"invalid user id"}`, http.StatusBadRequest)
		return
	}

	// DELETE — WHERE role != 'owner' ensures owner is immutable at SQL level
	result, err := h.DB.ExecContext(r.Context(),
		`DELETE FROM roost_users
		  WHERE id=$1 AND roost_id=$2 AND role != 'owner'`,
		userID, claims.RoostID,
	)
	if err != nil {
		http.Error(w, `{"error":"db_error"}`, http.StatusInternalServerError)
		return
	}

	n, _ := result.RowsAffected()
	if n == 0 {
		http.Error(w, `{"error":"user not found or owner cannot be deleted"}`, http.StatusForbidden)
		return
	}

	al.Log(r, claims.RoostID, claims.UserID, "user.revoke", userID, nil)

	w.WriteHeader(http.StatusNoContent)
}

// ── Internal helpers ──────────────────────────────────────────────────────────

// sendInviteNotification sends a fire-and-forget invite notification.
func sendInviteNotification(roostID, toUserID, fromUserID, notifType string) {
	body, _ := json.Marshal(map[string]string{
		"to":      toUserID,
		"type":    notifType,
		"roost_id": roostID,
		"from":    fromUserID,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "POST",
		"https://api.roost.unity.dev/internal/notify", bytes.NewReader(body))
	if err != nil {
		slog.Warn("notify: failed to build request", "err", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		slog.Warn("notify: request failed", "err", err)
		return
	}
	defer resp.Body.Close()
}

// extractPathID extracts the ID segment from a URL path.
// e.g. extractPathID("/admin/users/abc-123/role", "/admin/users/", "/role") → "abc-123"
func extractPathID(path, prefix, suffix string) string {
	id := strings.TrimPrefix(path, prefix)
	id = strings.TrimSuffix(id, suffix)
	return strings.TrimSuffix(id, "/")
}

// isValidUUID performs a basic UUID format check to prevent path traversal.
func isValidUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
				return false
			}
		}
	}
	return true
}
