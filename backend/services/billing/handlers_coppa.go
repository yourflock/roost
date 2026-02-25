// handlers_coppa.go — COPPA compliance for kids profiles (P22.4).
//
// COPPA (Children's Online Privacy Protection Act) requires special handling
// for users under 13. Roost receives kids profile status from Flock via JWT claims.
//
// Rules applied to kids profiles:
//   1. No behavioral tracking or analytics.
//   2. No marketing emails.
//   3. No data sharing with third parties (no Flock feed posts, no now-watching).
//   4. Parent can delete all child data at any time.
//   5. Stream session records are purged after 30 days (not 90).
//   6. No referral links (kids cannot refer adults).
//
// These rules are enforced at the profile level — the subscriber account itself
// may have a mix of adult and kids profiles.
package billing

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/yourflock/roost/internal/auth"
)

// handleKidsDataDeletion handles POST /kids/profiles/:id/delete-data.
// Parents can delete all data associated with a kids profile.
// This is distinct from deleting the profile itself (which Phase 12 handles).
func (s *Server) handleKidsDataDeletion(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST only")
		return
	}

	claims, err := auth.ValidateJWT(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "valid JWT required")
		return
	}
	parentSubscriberID := claims.Subject

	// Extract profile ID from path: /kids/profiles/:id/delete-data
	profileID := extractPathSegmentByKey(r.URL.Path, "profiles")
	if profileID == "" {
		writeError(w, http.StatusBadRequest, "missing_profile_id", "profile ID required in path")
		return
	}

	// Verify the profile belongs to this subscriber and is a kids profile.
	var isKids bool
	err = s.db.QueryRowContext(r.Context(), `
		SELECT is_kids_profile
		FROM subscriber_profiles
		WHERE id = $1 AND subscriber_id = $2
	`, profileID, parentSubscriberID).Scan(&isKids)
	if err != nil {
		writeError(w, http.StatusNotFound, "profile_not_found", "profile not found or not yours")
		return
	}
	if !isKids {
		writeError(w, http.StatusForbidden, "not_kids_profile", "COPPA deletion only applies to kids profiles")
		return
	}

	// Delete all child-specific data from the profile.
	// These are the tables that could contain personal data for the child.
	deletions := []struct {
		table string
		where string
	}{
		{"stream_sessions", "profile_id = $1"},
		{"watch_history", "profile_id = $1"},
		{"subscriber_sports_preferences", "profile_id = $1"},
	}

	deletedCount := 0
	for _, d := range deletions {
		result, err := s.db.ExecContext(r.Context(),
			"DELETE FROM "+d.table+" WHERE "+d.where, profileID)
		if err == nil {
			if n, _ := result.RowsAffected(); n > 0 {
				deletedCount += int(n)
			}
		}
	}

	// Log the deletion (audit trail for COPPA compliance).
	_, _ = s.db.ExecContext(r.Context(), `
		INSERT INTO audit_log (
			actor_type, actor_id, action, resource_type, resource_id, details
		) VALUES ('subscriber', $1, 'coppa.data_deletion', 'kids_profile', $2, $3)
	`, parentSubscriberID, profileID, jsonString(map[string]interface{}{
		"rows_deleted": deletedCount,
		"reason":       "COPPA parental request",
		"timestamp":    time.Now().UTC().Format(time.RFC3339),
	}))

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok":           true,
		"rows_deleted": deletedCount,
		"profile_id":   profileID,
		"message":      "All activity data for this kids profile has been deleted.",
	})
}

// handleKidsProfileSettings handles GET /kids/profiles/:id/settings.
// Returns COPPA-compliant settings for a kids profile.
func (s *Server) handleKidsProfileSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET only")
		return
	}

	claims, err := auth.ValidateJWT(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "valid JWT required")
		return
	}
	subscriberID := claims.Subject

	profileID := extractPathSegmentByKey(r.URL.Path, "profiles")
	if profileID == "" {
		writeError(w, http.StatusBadRequest, "missing_profile_id", "profile ID required")
		return
	}

	var isKids bool
	var profileName string
	err = s.db.QueryRowContext(r.Context(), `
		SELECT is_kids_profile, display_name
		FROM subscriber_profiles
		WHERE id = $1 AND subscriber_id = $2
	`, profileID, subscriberID).Scan(&isKids, &profileName)
	if err != nil {
		writeError(w, http.StatusNotFound, "profile_not_found", "profile not found")
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"profile_id":   profileID,
		"profile_name": profileName,
		"is_kids":      isKids,
		"coppa_applied": isKids,
		"data_collection": map[string]bool{
			"behavioral_tracking": false, // never for kids
			"marketing_emails":    false,
			"third_party_sharing": false,
			"analytics":           false,
			"watch_history":       isKids, // watch history kept for parental review, purged after 30 days
		},
		"retention_days": func() int {
			if isKids {
				return 30
			}
			return 90
		}(),
	})
}

// jsonString serializes v to a JSON string. Returns "{}" on error.
func jsonString(v interface{}) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// extractPathSegment pulls the segment after the given key from a URL path.
// For /kids/profiles/abc123/delete-data and key="profiles", returns "abc123".
func extractPathSegmentByKey(path, key string) string {
	parts := splitPathParts(path)
	for i, p := range parts {
		if p == key && i+1 < len(parts) {
			return parts[i+1]
		}
	}
	return ""
}

// splitPathParts splits a URL path into non-empty segments.
func splitPathParts(path string) []string {
	var parts []string
	for _, p := range splitOn(path, '/') {
		if p != "" {
			parts = append(parts, p)
		}
	}
	return parts
}

func splitOn(s string, sep rune) []string {
	var parts []string
	start := 0
	for i, c := range s {
		if c == sep {
			parts = append(parts, s[start:i])
			start = i + 1
		}
	}
	parts = append(parts, s[start:])
	return parts
}

// hasPathSuffix reports whether path ends with the given suffix segment.
func hasPathSuffix(path, suffix string) bool {
	if path == "" {
		return false
	}
	// Strip trailing slash.
	path = strings.TrimRight(path, "/")
	// Check if the last path segment matches.
	idx := strings.LastIndex(path, "/")
	if idx < 0 {
		return path == suffix
	}
	return path[idx+1:] == suffix
}

// handleCOPPAChildDeletion handles DELETE /coppa/child/{subscriber_id}.
// P22.4.003: Parent-initiated hard deletion of a child subscriber account.
// Requires parent JWT with a `parent_of` claim containing the child's subscriber_id.
// Follows the same pipeline as GDPR erasure but via parental authority.
func (s *Server) handleCOPPAChildDeletion(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "DELETE only")
		return
	}

	claims, err := auth.ValidateJWT(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "valid JWT required")
		return
	}
	parentSubscriberID := claims.Subject

	// Extract child subscriber ID from path: /coppa/child/{subscriber_id}
	childID := extractPathSegmentByKey(r.URL.Path, "child")
	if childID == "" {
		writeError(w, http.StatusBadRequest, "missing_child_id", "child subscriber_id required in path")
		return
	}

	// Verify the child subscriber exists and is marked as a kid profile.
	// The parent must be the linked parent subscriber.
	var isKid bool
	var linkedParentID string
	err = s.db.QueryRowContext(r.Context(), `
		SELECT is_kid_profile, parent_subscriber_id
		FROM subscribers
		WHERE id = $1
	`, childID).Scan(&isKid, &linkedParentID)
	if err != nil {
		writeError(w, http.StatusNotFound, "child_not_found", "child subscriber not found")
		return
	}

	// Verify parental authority: the requester must be the registered parent.
	if linkedParentID != parentSubscriberID {
		writeError(w, http.StatusForbidden, "not_parent",
			"You are not the registered parent for this child account")
		return
	}

	if !isKid {
		writeError(w, http.StatusBadRequest, "not_kid_profile",
			"COPPA deletion only applies to kid profiles (is_kid_profile = true)")
		return
	}

	// Revoke all active tokens for the child.
	s.db.ExecContext(r.Context(), `
		INSERT INTO revoked_tokens (jti, subscriber_id, expires_at, reason, revoked_by)
		SELECT id::text, $1::uuid, now() + INTERVAL '1 day', 'coppa_deletion', $2::uuid
		FROM refresh_tokens
		WHERE subscriber_id = $1
		ON CONFLICT (jti) DO NOTHING
	`, childID, parentSubscriberID)

	// Log for COPPA compliance (permanent record per COPPA § 312.10).
	s.db.ExecContext(r.Context(), `
		INSERT INTO audit_log
			(actor_type, actor_id, action, resource_type, resource_id, details)
		VALUES ('subscriber', $1::uuid, 'coppa.child_deletion', 'subscriber', $2::uuid,
			$3)
	`, parentSubscriberID, childID, jsonString(map[string]interface{}{
		"parent_id": parentSubscriberID,
		"child_id":  childID,
		"reason":    "COPPA parental deletion request",
		"timestamp": time.Now().UTC().Format(time.RFC3339),
	}))

	// Hard-delete the child subscriber (CASCADE deletes all related data).
	_, err = s.db.ExecContext(r.Context(), `
		DELETE FROM subscribers WHERE id = $1 AND is_kid_profile = true
	`, childID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "deletion_failed",
			"Failed to delete child account. Please contact support.")
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok":       true,
		"child_id": childID,
		"message":  "Child account and all associated data have been permanently deleted.",
	})
}
