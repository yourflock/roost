// handlers_devices.go — subscriber device management endpoints.
// P2-T09: Device Management Backend Endpoints
package auth

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/yourflock/roost/internal/auth"
)

// ipPrefixRegex captures the first three octets of an IPv4 address for /24 truncation.
var ipPrefixRegex = regexp.MustCompile(`^(\d+\.\d+\.\d+)\.\d+$`)

// deviceListItem is the safe device record returned to subscribers.
// IP is truncated to /24 for privacy.
type deviceListItem struct {
	ID                  string     `json:"id"`
	DeviceName          *string    `json:"device_name"`
	IPAddressTruncated  string     `json:"ip_address_truncated"`
	UserAgentSummary    string     `json:"user_agent_summary"`
	LastActiveAt        time.Time  `json:"last_active_at"`
	IsCurrentlyStreaming bool      `json:"is_currently_streaming"`
}

// HandleListDevices processes GET /auth/devices.
// Returns all active devices for the authenticated subscriber.
func HandleListDevices(db *sql.DB) http.HandlerFunc {
	return auth.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			auth.WriteError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET required")
			return
		}

		subscriberID := auth.SubscriberIDFromContext(r.Context())

		rows, err := db.QueryContext(r.Context(), `
			SELECT id, device_name, COALESCE(ip_address::text,''), COALESCE(user_agent,''), last_active_at
			FROM subscriber_devices
			WHERE subscriber_id = $1 AND is_active = true
			ORDER BY last_active_at DESC
		`, subscriberID)
		if err != nil {
			auth.WriteError(w, http.StatusInternalServerError, "server_error", "Failed to list devices")
			return
		}
		defer rows.Close()

		var devices []deviceListItem
		for rows.Next() {
			var d deviceListItem
			var ipStr string
			var lastActive time.Time
			var deviceName sql.NullString
			var userAgent string

			rows.Scan(&d.ID, &deviceName, &ipStr, &userAgent, &lastActive)

			if deviceName.Valid {
				d.DeviceName = &deviceName.String
			}

			// Truncate IP to /24 for privacy: 192.168.1.123 → 192.168.1.x
			if m := ipPrefixRegex.FindStringSubmatch(ipStr); len(m) == 2 {
				d.IPAddressTruncated = m[1] + ".x"
			} else {
				d.IPAddressTruncated = "[unknown]"
			}

			// Summarize user agent (first 80 chars)
			if len(userAgent) > 80 {
				d.UserAgentSummary = userAgent[:80] + "..."
			} else {
				d.UserAgentSummary = userAgent
			}

			d.LastActiveAt = lastActive
			// is_currently_streaming would be checked via Redis in Phase 4
			// For now always false until relay is built
			d.IsCurrentlyStreaming = false

			devices = append(devices, d)
		}

		if devices == nil {
			devices = []deviceListItem{}
		}

		auth.WriteJSON(w, http.StatusOK, map[string]interface{}{
			"devices": devices,
			"count":   len(devices),
		})
	}))
}

// HandleRenameDevice processes PATCH /auth/devices/:id.
// Allows the subscriber to give a friendly name to a device.
func HandleRenameDevice(db *sql.DB) http.HandlerFunc {
	return auth.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			auth.WriteError(w, http.StatusMethodNotAllowed, "method_not_allowed", "PATCH required")
			return
		}

		subscriberID := auth.SubscriberIDFromContext(r.Context())
		deviceID := extractDeviceID(r.URL.Path, "/auth/devices/")

		if deviceID == "" {
			auth.WriteError(w, http.StatusBadRequest, "missing_id", "Device ID required in path")
			return
		}

		var req struct {
			DeviceName string `json:"device_name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			auth.WriteError(w, http.StatusBadRequest, "invalid_json", "Request body must be valid JSON")
			return
		}

		name := strings.TrimSpace(req.DeviceName)
		if len(name) > 100 {
			auth.WriteError(w, http.StatusBadRequest, "name_too_long",
				"Device name must be 100 characters or less")
			return
		}
		if htmlTagRegex.MatchString(name) {
			auth.WriteError(w, http.StatusBadRequest, "invalid_name",
				"Device name must not contain HTML")
			return
		}

		// Update only if device belongs to this subscriber
		result, err := db.ExecContext(r.Context(), `
			UPDATE subscriber_devices SET device_name = $1
			WHERE id = $2 AND subscriber_id = $3 AND is_active = true
		`, name, deviceID, subscriberID)
		if err != nil {
			auth.WriteError(w, http.StatusInternalServerError, "server_error", "Rename failed")
			return
		}

		rowsAffected, _ := result.RowsAffected()
		if rowsAffected == 0 {
			auth.WriteError(w, http.StatusNotFound, "not_found",
				"Device not found or you don't have permission to rename it")
			return
		}

		auth.WriteJSON(w, http.StatusOK, map[string]string{
			"message": "Device renamed successfully.",
		})
	}))
}

// HandleRevokeDevice processes DELETE /auth/devices/:id.
// Soft-deletes a device and terminates any active stream session.
func HandleRevokeDevice(db *sql.DB) http.HandlerFunc {
	return auth.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			auth.WriteError(w, http.StatusMethodNotAllowed, "method_not_allowed", "DELETE required")
			return
		}

		subscriberID := auth.SubscriberIDFromContext(r.Context())
		deviceID := extractDeviceID(r.URL.Path, "/auth/devices/")

		if deviceID == "" {
			auth.WriteError(w, http.StatusBadRequest, "missing_id", "Device ID required in path")
			return
		}

		tx, err := db.BeginTx(r.Context(), nil)
		if err != nil {
			auth.WriteError(w, http.StatusInternalServerError, "server_error", "Revocation failed")
			return
		}
		defer tx.Rollback()

		// Soft-delete device — ownership check in WHERE clause
		result, err := tx.ExecContext(r.Context(), `
			UPDATE subscriber_devices SET is_active = false
			WHERE id = $1 AND subscriber_id = $2
		`, deviceID, subscriberID)
		if err != nil {
			auth.WriteError(w, http.StatusInternalServerError, "server_error", "Revocation failed")
			return
		}

		rowsAffected, _ := result.RowsAffected()
		if rowsAffected == 0 {
			auth.WriteError(w, http.StatusForbidden, "forbidden",
				"Device not found or you don't have permission to revoke it")
			return
		}

		// Terminate any active stream session for this device
		// Phase 4 relay will populate stream_sessions; terminate by setting ended_at
		tx.ExecContext(r.Context(), `
			UPDATE stream_sessions SET ended_at = now()
			WHERE subscriber_id = $1 AND device_id = $2 AND ended_at IS NULL
		`, subscriberID, deviceID)

		// Audit log
		tx.ExecContext(r.Context(), `
			INSERT INTO audit_log (subscriber_id, action, metadata)
			VALUES ($1, 'device_revoked', $2)
		`, subscriberID, `{"device_id":"`+deviceID+`"}`)

		if err := tx.Commit(); err != nil {
			auth.WriteError(w, http.StatusInternalServerError, "server_error", "Revocation failed")
			return
		}

		auth.WriteJSON(w, http.StatusOK, map[string]string{
			"message": "Device revoked. Any active stream on that device has been terminated.",
		})
	}))
}

// HandleRevokeAllDevices processes DELETE /auth/devices (no :id).
// Revokes all devices except the current one.
func HandleRevokeAllDevices(db *sql.DB) http.HandlerFunc {
	return auth.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			auth.WriteError(w, http.StatusMethodNotAllowed, "method_not_allowed", "DELETE required")
			return
		}

		subscriberID := auth.SubscriberIDFromContext(r.Context())

		// Current device identified by X-Device-Id header or body param
		currentDeviceID := r.Header.Get("X-Device-Id")
		if currentDeviceID == "" {
			var body struct {
				CurrentDeviceID string `json:"current_device_id"`
			}
			json.NewDecoder(r.Body).Decode(&body)
			currentDeviceID = body.CurrentDeviceID
		}

		tx, err := db.BeginTx(r.Context(), nil)
		if err != nil {
			auth.WriteError(w, http.StatusInternalServerError, "server_error", "Revocation failed")
			return
		}
		defer tx.Rollback()

		// Revoke all EXCEPT current device
		var result sql.Result
		if currentDeviceID != "" {
			result, err = tx.ExecContext(r.Context(), `
				UPDATE subscriber_devices SET is_active = false
				WHERE subscriber_id = $1 AND id != $2 AND is_active = true
			`, subscriberID, currentDeviceID)
		} else {
			result, err = tx.ExecContext(r.Context(), `
				UPDATE subscriber_devices SET is_active = false
				WHERE subscriber_id = $1 AND is_active = true
			`, subscriberID)
		}

		if err != nil {
			auth.WriteError(w, http.StatusInternalServerError, "server_error", "Revocation failed")
			return
		}

		rowsAffected, _ := result.RowsAffected()

		// Terminate all non-current active streams
		if currentDeviceID != "" {
			tx.ExecContext(r.Context(), `
				UPDATE stream_sessions SET ended_at = now()
				WHERE subscriber_id = $1 AND device_id != $2 AND ended_at IS NULL
			`, subscriberID, currentDeviceID)
		} else {
			tx.ExecContext(r.Context(), `
				UPDATE stream_sessions SET ended_at = now()
				WHERE subscriber_id = $1 AND ended_at IS NULL
			`, subscriberID)
		}

		tx.ExecContext(r.Context(), `
			INSERT INTO audit_log (subscriber_id, action, metadata)
			VALUES ($1, 'all_devices_revoked', $2)
		`, subscriberID, `{"devices_revoked":`+string(rune(rowsAffected+'0'))+`}`)

		if err := tx.Commit(); err != nil {
			auth.WriteError(w, http.StatusInternalServerError, "server_error", "Revocation failed")
			return
		}

		auth.WriteJSON(w, http.StatusOK, map[string]interface{}{
			"message":          "All other devices signed out and streams terminated.",
			"devices_revoked": rowsAffected,
		})
	}))
}

// extractDeviceID extracts the device ID from a URL path like /auth/devices/{id}.
func extractDeviceID(path, prefix string) string {
	id := strings.TrimPrefix(path, prefix)
	// Remove any trailing slash
	id = strings.TrimSuffix(id, "/")
	// If there's a sub-path (e.g., /auth/devices), return empty
	if strings.Contains(id, "/") {
		return ""
	}
	return id
}
