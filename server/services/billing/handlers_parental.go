// handlers_parental.go — Parental controls and content filtering for subscriber profiles.
// P12-T05: Per-Profile Parental Controls
//
// These handlers enforce age-rating limits, category blocking, and time-of-day
// viewing restrictions. Checking logic is called from the owl_api service
// (content filtering per profile context) and from this billing service for
// parental control updates.
//
// Note: The actual enforcement of content filtering happens in owl_api when
// serving /owl/live, /owl/vod, and /owl/epg — the profile's age_rating_limit
// and blocked_categories are included in the session context fetched from the DB.
package billing

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/unyeco/roost/internal/auth"
)

// ── Time restriction checker ──────────────────────────────────────────────────

// IsViewingAllowed checks whether the current time falls within the profile's
// allowed viewing hours. Returns true if no schedule is set (unrestricted).
// timeNow is injected for testability.
func IsViewingAllowed(scheduleJSON *string, timeNow time.Time) bool {
	if scheduleJSON == nil || *scheduleJSON == "" {
		return true // no restriction
	}

	var sched viewingSchedule
	if err := json.Unmarshal([]byte(*scheduleJSON), &sched); err != nil {
		return true // parse failure → allow
	}

	loc, err := time.LoadLocation(sched.Timezone)
	if err != nil {
		loc = time.UTC
	}
	local := timeNow.In(loc)

	startParts := parseHHMM(sched.AllowedHours.Start)
	endParts := parseHHMM(sched.AllowedHours.End)
	if startParts == nil || endParts == nil {
		return true
	}

	start := time.Date(local.Year(), local.Month(), local.Day(),
		startParts[0], startParts[1], 0, 0, loc)
	end := time.Date(local.Year(), local.Month(), local.Day(),
		endParts[0], endParts[1], 0, 0, loc)

	return !local.Before(start) && local.Before(end)
}

// parseHHMM parses "HH:MM" into [hour, minute]. Returns nil on error.
func parseHHMM(s string) []int {
	if len(s) != 5 || s[2] != ':' {
		return nil
	}
	h := int(s[0]-'0')*10 + int(s[1]-'0')
	m := int(s[3]-'0')*10 + int(s[4]-'0')
	if h < 0 || h > 23 || m < 0 || m > 59 {
		return nil
	}
	return []int{h, m}
}

// ── Rating enforcement ────────────────────────────────────────────────────────

// ratingOrder maps TV rating strings to ordinal values (lower = more restrictive).
var ratingOrder = map[string]int{
	"TV-Y":  0,
	"TV-Y7": 1,
	"TV-G":  2,
	"TV-PG": 3,
	"TV-14": 4,
	"TV-MA": 5,
}

// IsContentAllowedByRating returns true if contentRating is within the profile limit.
// If profileLimit is empty/null, all content is allowed.
// If content has no rating, it is blocked for kids profiles, allowed otherwise.
func IsContentAllowedByRating(profileLimit, contentRating string, isKids bool) bool {
	if isKids {
		// Kids profiles: only TV-Y and TV-G are allowed
		allowed := map[string]bool{"TV-Y": true, "TV-Y7": true, "TV-G": true}
		return allowed[contentRating]
	}
	if profileLimit == "" {
		return true // unrestricted
	}
	limitOrd, ok := ratingOrder[profileLimit]
	if !ok {
		return true
	}
	contentOrd, ok := ratingOrder[contentRating]
	if !ok {
		return true // unknown rating → allow (caller may choose to block)
	}
	return contentOrd <= limitOrd
}

// ── Parental check endpoint (internal utility) ────────────────────────────────

// parentalCheckRequest is the body for POST /profiles/:id/parental-check.
type parentalCheckRequest struct {
	ContentRating string   `json:"content_rating"` // "TV-G", "TV-14", etc.
	CategoryIDs   []string `json:"category_ids"`   // categories this content belongs to
}

// parentalCheckResponse is returned by POST /profiles/:id/parental-check.
type parentalCheckResponse struct {
	Allowed       bool   `json:"allowed"`
	BlockedReason string `json:"blocked_reason,omitempty"` // "rating", "category", "time", ""
}

// handleParentalCheck validates whether content is accessible for a profile.
// POST /profiles/:id/parental-check
// Used by the owl_api service before issuing a stream URL.
func (s *Server) handleParentalCheck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		auth.WriteError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST required")
		return
	}

	claims, err := auth.ValidateJWT(r)
	if err != nil {
		auth.WriteError(w, http.StatusUnauthorized, "unauthorized", "valid JWT required")
		return
	}
	subscriberID := claims.Subject

	// Extract profile ID from URL path
	profileID := extractPathSegment(r.URL.Path, 2)
	if profileID == "" {
		auth.WriteError(w, http.StatusBadRequest, "bad_request", "profile ID required in path")
		return
	}

	// Load profile settings from DB
	var ageRatingLimit *string
	var isKids bool
	var blockedCatsJSON, viewingSchedJSON *string
	err = s.db.QueryRow(`
		SELECT age_rating_limit, is_kids_profile, blocked_categories::text, viewing_schedule::text
		FROM subscriber_profiles
		WHERE id = $1 AND subscriber_id = $2 AND is_active = TRUE
	`, profileID, subscriberID).Scan(&ageRatingLimit, &isKids, &blockedCatsJSON, &viewingSchedJSON)
	if err != nil {
		auth.WriteError(w, http.StatusNotFound, "not_found", "profile not found")
		return
	}

	var req parentalCheckRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		auth.WriteError(w, http.StatusBadRequest, "invalid_json", "request body must be valid JSON")
		return
	}

	resp := parentalCheckResponse{Allowed: true}

	// Check time restriction
	if !IsViewingAllowed(viewingSchedJSON, time.Now()) {
		resp.Allowed = false
		resp.BlockedReason = "time"
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
		return
	}

	// Check age rating
	limit := ""
	if ageRatingLimit != nil {
		limit = *ageRatingLimit
	}
	if !IsContentAllowedByRating(limit, req.ContentRating, isKids) {
		resp.Allowed = false
		resp.BlockedReason = "rating"
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
		return
	}

	// Check blocked categories
	if blockedCatsJSON != nil && len(req.CategoryIDs) > 0 {
		var blocked []string
		if err := json.Unmarshal([]byte(*blockedCatsJSON), &blocked); err == nil {
			blockedSet := make(map[string]bool)
			for _, c := range blocked {
				blockedSet[c] = true
			}
			for _, cat := range req.CategoryIDs {
				if blockedSet[cat] {
					resp.Allowed = false
					resp.BlockedReason = "category"
					w.Header().Set("Content-Type", "application/json")
					json.NewEncoder(w).Encode(resp)
					return
				}
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// extractPathSegment returns the Nth segment (0-indexed) from a URL path like /profiles/abc/report.
// Segment 0 = "profiles", 1 = "abc", 2 = "report" for path "/profiles/abc/report".
func extractPathSegment(path string, n int) string {
	// Strip leading slash
	if len(path) > 0 && path[0] == '/' {
		path = path[1:]
	}
	parts := make([]string, 0)
	start := 0
	for i := 0; i <= len(path); i++ {
		if i == len(path) || path[i] == '/' {
			if i > start {
				parts = append(parts, path[start:i])
			}
			start = i + 1
		}
	}
	if n < len(parts) {
		return parts[n]
	}
	return ""
}
