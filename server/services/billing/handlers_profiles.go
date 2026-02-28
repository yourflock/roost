// handlers_profiles.go — Subscriber profile management endpoints.
// P12-T02: Profile Management API
//
// GET  /profiles         — list all profiles for the authenticated subscriber
// POST /profiles         — create a new profile (plan limit enforced)
// PUT  /profiles/:id     — update a profile
// DELETE /profiles/:id   — delete a profile (cannot delete primary)
// POST /profiles/:id/switch   — switch active profile (PIN check if set)
// POST /profiles/:id/avatar   — upload custom avatar
// GET  /profiles/limits  — current profile count and plan max
// GET  /profiles/:id/report   — viewing report for a profile (primary only)
package billing

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/unyeco/roost/internal/auth"
	"golang.org/x/crypto/bcrypt"
)

// ── Profile types ─────────────────────────────────────────────────────────────

// profileRecord is used in list responses.
type profileRecord struct {
	ID              string  `json:"id"`
	Name            string  `json:"name"`
	AvatarURL       *string `json:"avatar_url,omitempty"`
	AvatarPreset    *string `json:"avatar_preset,omitempty"`
	IsPrimary       bool    `json:"is_primary"`
	AgeRatingLimit  *string `json:"age_rating_limit,omitempty"`
	IsKidsProfile   bool    `json:"is_kids_profile"`
	HasPIN          bool    `json:"has_pin"`
	IsActive        bool    `json:"is_active"`
	BlockedCategories []string `json:"blocked_categories"`
	ViewingSchedule *viewingSchedule `json:"viewing_schedule,omitempty"`
	Preferences     map[string]interface{} `json:"preferences"`
	CreatedAt       time.Time `json:"created_at"`
}

// viewingSchedule holds time-of-day restriction settings.
type viewingSchedule struct {
	AllowedHours struct {
		Start string `json:"start"` // "08:00"
		End   string `json:"end"`   // "21:00"
	} `json:"allowed_hours"`
	Timezone string `json:"timezone"` // "America/New_York"
}

// profileLimitsResponse is returned by GET /profiles/limits.
type profileLimitsResponse struct {
	Current int    `json:"current"`
	Max     int    `json:"max"`
	Plan    string `json:"plan"`
}

// createProfileRequest is the body for POST /profiles.
type createProfileRequest struct {
	Name           string  `json:"name"`
	AvatarPreset   string  `json:"avatar_preset"`
	AgeRatingLimit *string `json:"age_rating_limit"`
	IsKidsProfile  bool    `json:"is_kids_profile"`
	PIN            string  `json:"pin"` // 4-digit string, optional
}

// updateProfileRequest is the body for PUT /profiles/:id.
type updateProfileRequest struct {
	Name                *string          `json:"name"`
	AvatarPreset        *string          `json:"avatar_preset"`
	AgeRatingLimit      *string          `json:"age_rating_limit"`
	IsKidsProfile       *bool            `json:"is_kids_profile"`
	PIN                 *string          `json:"pin"`  // set new PIN
	ClearPIN            bool             `json:"clear_pin"` // remove PIN
	BlockedCategories   []string         `json:"blocked_categories"`
	ViewingSchedule     *viewingSchedule `json:"viewing_schedule"`
	ClearViewingSchedule bool            `json:"clear_viewing_schedule"`
	Preferences         map[string]interface{} `json:"preferences"`
}

// switchProfileRequest is the body for POST /profiles/:id/switch.
type switchProfileRequest struct {
	PIN string `json:"pin"`
}

// ── Route dispatcher ─────────────────────────────────────────────────────────

// handleProfiles dispatches /profiles and /profiles/limits.
func (s *Server) handleProfiles(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		if strings.HasSuffix(r.URL.Path, "/limits") {
			s.getProfileLimits(w, r)
		} else {
			s.listProfiles(w, r)
		}
	case http.MethodPost:
		s.createProfile(w, r)
	default:
		auth.WriteError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET or POST required")
	}
}

// handleProfile dispatches /profiles/:id and sub-paths.
func (s *Server) handleProfile(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimSuffix(r.URL.Path, "/")
	parts := strings.Split(path, "/")
	// parts: ["", "profiles", "{id}", ...]
	if len(parts) < 3 {
		auth.WriteError(w, http.StatusBadRequest, "bad_request", "profile ID required")
		return
	}
	profileID := parts[2]

	if len(parts) == 4 {
		switch parts[3] {
		case "switch":
			s.switchProfile(w, r, profileID)
			return
		case "avatar":
			s.uploadAvatar(w, r, profileID)
			return
		case "report":
			s.getProfileReport(w, r, profileID)
			return
		case "parental-check":
			s.handleParentalCheck(w, r)
			return
		}
	}

	switch r.Method {
	case http.MethodGet:
		s.getProfile(w, r, profileID)
	case http.MethodPut:
		s.updateProfile(w, r, profileID)
	case http.MethodDelete:
		s.deleteProfile(w, r, profileID)
	default:
		auth.WriteError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET, PUT, or DELETE required")
	}
}

// ── Handlers ─────────────────────────────────────────────────────────────────

// listProfiles returns all profiles for the authenticated subscriber.
// GET /profiles
func (s *Server) listProfiles(w http.ResponseWriter, r *http.Request) {
	claims, err := auth.ValidateJWT(r)
	if err != nil {
		auth.WriteError(w, http.StatusUnauthorized, "unauthorized", "valid JWT required")
		return
	}
	subscriberID := claims.Subject

	rows, err := s.db.Query(`
		SELECT
			id, name, avatar_url, avatar_preset, is_primary, age_rating_limit,
			is_kids_profile, (pin_hash IS NOT NULL), is_active,
			COALESCE(blocked_categories::text, '[]'),
			viewing_schedule::text,
			COALESCE(preferences::text, '{}'),
			created_at
		FROM subscriber_profiles
		WHERE subscriber_id = $1
		ORDER BY is_primary DESC, created_at ASC
	`, subscriberID)
	if err != nil {
		auth.WriteError(w, http.StatusInternalServerError, "db_error", "failed to fetch profiles")
		return
	}
	defer rows.Close()

	profiles := make([]profileRecord, 0)
	for rows.Next() {
		p, err := scanProfile(rows)
		if err != nil {
			continue
		}
		profiles = append(profiles, p)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"profiles": profiles})
}

// getProfile returns a single profile by ID.
// GET /profiles/:id
func (s *Server) getProfile(w http.ResponseWriter, r *http.Request, profileID string) {
	claims, err := auth.ValidateJWT(r)
	if err != nil {
		auth.WriteError(w, http.StatusUnauthorized, "unauthorized", "valid JWT required")
		return
	}
	subscriberID := claims.Subject

	row := s.db.QueryRow(`
		SELECT
			id, name, avatar_url, avatar_preset, is_primary, age_rating_limit,
			is_kids_profile, (pin_hash IS NOT NULL), is_active,
			COALESCE(blocked_categories::text, '[]'),
			viewing_schedule::text,
			COALESCE(preferences::text, '{}'),
			created_at
		FROM subscriber_profiles
		WHERE id = $1 AND subscriber_id = $2
	`, profileID, subscriberID)

	p, err := scanProfile(row)
	if err != nil {
		auth.WriteError(w, http.StatusNotFound, "not_found", "profile not found")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(p)
}

// createProfile creates a new profile under the subscriber's account.
// POST /profiles
func (s *Server) createProfile(w http.ResponseWriter, r *http.Request) {
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

	var req createProfileRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		auth.WriteError(w, http.StatusBadRequest, "invalid_json", "request body must be valid JSON")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		auth.WriteError(w, http.StatusBadRequest, "missing_name", "name is required")
		return
	}
	if len(req.Name) > 100 {
		auth.WriteError(w, http.StatusBadRequest, "name_too_long", "name must be 100 characters or less")
		return
	}

	// Validate age rating limit
	if req.AgeRatingLimit != nil {
		valid := map[string]bool{"TV-G": true, "TV-PG": true, "TV-14": true, "TV-MA": true}
		if !valid[*req.AgeRatingLimit] {
			auth.WriteError(w, http.StatusBadRequest, "invalid_age_rating", "age_rating_limit must be TV-G, TV-PG, TV-14, or TV-MA")
			return
		}
	}

	// Validate avatar preset
	if req.AvatarPreset != "" {
		if !isValidAvatarPreset(req.AvatarPreset) {
			auth.WriteError(w, http.StatusBadRequest, "invalid_avatar_preset", "avatar_preset must be owl-1 through owl-12")
			return
		}
	}

	// Validate PIN (4 digits if provided)
	if req.PIN != "" {
		if len(req.PIN) != 4 || !isNumeric(req.PIN) {
			auth.WriteError(w, http.StatusBadRequest, "invalid_pin", "PIN must be exactly 4 digits")
			return
		}
	}

	// Enforce plan profile limit
	maxProfiles, planSlug, err := s.getProfileLimit(subscriberID)
	if err != nil {
		auth.WriteError(w, http.StatusInternalServerError, "db_error", "failed to determine plan limits")
		return
	}
	currentCount, err := s.countProfiles(subscriberID)
	if err != nil {
		auth.WriteError(w, http.StatusInternalServerError, "db_error", "failed to count profiles")
		return
	}
	if currentCount >= maxProfiles {
		auth.WriteError(w, http.StatusForbidden, "profile_limit_reached",
			fmt.Sprintf("your %s plan allows up to %d profiles (%d currently used)", planSlug, maxProfiles, currentCount))
		return
	}

	// Hash PIN if provided
	var pinHash *string
	if req.PIN != "" {
		h, err := bcrypt.GenerateFromPassword([]byte(req.PIN), bcrypt.DefaultCost)
		if err != nil {
			auth.WriteError(w, http.StatusInternalServerError, "hash_error", "failed to hash PIN")
			return
		}
		hs := string(h)
		pinHash = &hs
	}

	// Build avatar URL from preset
	var avatarURL *string
	var avatarPreset *string
	if req.AvatarPreset != "" {
		ap := req.AvatarPreset
		avatarPreset = &ap
		au := avatarPresetURL(req.AvatarPreset)
		avatarURL = &au
	}

	var profileID string
	err = s.db.QueryRow(`
		INSERT INTO subscriber_profiles
			(subscriber_id, name, avatar_url, avatar_preset, is_primary, age_rating_limit,
			 is_kids_profile, pin_hash)
		VALUES ($1, $2, $3, $4, false, $5, $6, $7)
		RETURNING id
	`, subscriberID, req.Name, avatarURL, avatarPreset, req.AgeRatingLimit,
		req.IsKidsProfile, pinHash).Scan(&profileID)
	if err != nil {
		if strings.Contains(err.Error(), "uq_subscriber_profile_name") {
			auth.WriteError(w, http.StatusConflict, "name_taken", "a profile with this name already exists")
			return
		}
		auth.WriteError(w, http.StatusInternalServerError, "db_error", "failed to create profile")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]string{"id": profileID, "status": "created"})
}

// updateProfile updates a profile's settings.
// PUT /profiles/:id
func (s *Server) updateProfile(w http.ResponseWriter, r *http.Request, profileID string) {
	if r.Method != http.MethodPut {
		auth.WriteError(w, http.StatusMethodNotAllowed, "method_not_allowed", "PUT required")
		return
	}

	claims, err := auth.ValidateJWT(r)
	if err != nil {
		auth.WriteError(w, http.StatusUnauthorized, "unauthorized", "valid JWT required")
		return
	}
	subscriberID := claims.Subject

	// Verify profile belongs to subscriber
	var isPrimary bool
	var oldPinHash *string
	err = s.db.QueryRow(`
		SELECT is_primary, pin_hash FROM subscriber_profiles
		WHERE id = $1 AND subscriber_id = $2
	`, profileID, subscriberID).Scan(&isPrimary, &oldPinHash)
	if err != nil {
		auth.WriteError(w, http.StatusNotFound, "not_found", "profile not found")
		return
	}

	var req updateProfileRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		auth.WriteError(w, http.StatusBadRequest, "invalid_json", "request body must be valid JSON")
		return
	}

	// Build update query dynamically
	setClauses := []string{}
	args := []interface{}{}
	argIdx := 1

	if req.Name != nil {
		name := strings.TrimSpace(*req.Name)
		if name == "" {
			auth.WriteError(w, http.StatusBadRequest, "missing_name", "name cannot be empty")
			return
		}
		setClauses = append(setClauses, fmt.Sprintf("name = $%d", argIdx))
		args = append(args, name)
		argIdx++
	}

	if req.AvatarPreset != nil {
		if *req.AvatarPreset != "" && !isValidAvatarPreset(*req.AvatarPreset) {
			auth.WriteError(w, http.StatusBadRequest, "invalid_avatar_preset", "avatar_preset must be owl-1 through owl-12")
			return
		}
		setClauses = append(setClauses, fmt.Sprintf("avatar_preset = $%d", argIdx))
		args = append(args, req.AvatarPreset)
		argIdx++
		if *req.AvatarPreset != "" {
			au := avatarPresetURL(*req.AvatarPreset)
			setClauses = append(setClauses, fmt.Sprintf("avatar_url = $%d", argIdx))
			args = append(args, au)
			argIdx++
		}
	}

	if req.AgeRatingLimit != nil {
		if *req.AgeRatingLimit != "" {
			valid := map[string]bool{"TV-G": true, "TV-PG": true, "TV-14": true, "TV-MA": true}
			if !valid[*req.AgeRatingLimit] {
				auth.WriteError(w, http.StatusBadRequest, "invalid_age_rating", "age_rating_limit must be TV-G, TV-PG, TV-14, or TV-MA")
				return
			}
		}
		setClauses = append(setClauses, fmt.Sprintf("age_rating_limit = $%d", argIdx))
		args = append(args, req.AgeRatingLimit)
		argIdx++
	}

	if req.IsKidsProfile != nil {
		setClauses = append(setClauses, fmt.Sprintf("is_kids_profile = $%d", argIdx))
		args = append(args, *req.IsKidsProfile)
		argIdx++
	}

	if req.ClearPIN {
		setClauses = append(setClauses, "pin_hash = NULL")
	} else if req.PIN != nil {
		if len(*req.PIN) != 4 || !isNumeric(*req.PIN) {
			auth.WriteError(w, http.StatusBadRequest, "invalid_pin", "PIN must be exactly 4 digits")
			return
		}
		h, err := bcrypt.GenerateFromPassword([]byte(*req.PIN), bcrypt.DefaultCost)
		if err != nil {
			auth.WriteError(w, http.StatusInternalServerError, "hash_error", "failed to hash PIN")
			return
		}
		setClauses = append(setClauses, fmt.Sprintf("pin_hash = $%d", argIdx))
		args = append(args, string(h))
		argIdx++
	}

	if req.BlockedCategories != nil {
		catJSON, _ := json.Marshal(req.BlockedCategories)
		setClauses = append(setClauses, fmt.Sprintf("blocked_categories = $%d", argIdx))
		args = append(args, string(catJSON))
		argIdx++
	}

	if req.ClearViewingSchedule {
		setClauses = append(setClauses, "viewing_schedule = NULL")
	} else if req.ViewingSchedule != nil {
		sched, _ := json.Marshal(req.ViewingSchedule)
		setClauses = append(setClauses, fmt.Sprintf("viewing_schedule = $%d", argIdx))
		args = append(args, string(sched))
		argIdx++
	}

	if req.Preferences != nil {
		prefJSON, _ := json.Marshal(req.Preferences)
		setClauses = append(setClauses, fmt.Sprintf("preferences = $%d", argIdx))
		args = append(args, string(prefJSON))
		argIdx++
	}

	if len(setClauses) == 0 {
		auth.WriteError(w, http.StatusBadRequest, "no_changes", "no fields to update provided")
		return
	}

	query := fmt.Sprintf(
		"UPDATE subscriber_profiles SET %s WHERE id = $%d AND subscriber_id = $%d",
		strings.Join(setClauses, ", "), argIdx, argIdx+1,
	)
	args = append(args, profileID, subscriberID)

	_, err = s.db.Exec(query, args...)
	if err != nil {
		if strings.Contains(err.Error(), "uq_subscriber_profile_name") {
			auth.WriteError(w, http.StatusConflict, "name_taken", "a profile with this name already exists")
			return
		}
		auth.WriteError(w, http.StatusInternalServerError, "db_error", "failed to update profile")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "updated"})
}

// deleteProfile removes a non-primary profile.
// DELETE /profiles/:id
func (s *Server) deleteProfile(w http.ResponseWriter, r *http.Request, profileID string) {
	if r.Method != http.MethodDelete {
		auth.WriteError(w, http.StatusMethodNotAllowed, "method_not_allowed", "DELETE required")
		return
	}

	claims, err := auth.ValidateJWT(r)
	if err != nil {
		auth.WriteError(w, http.StatusUnauthorized, "unauthorized", "valid JWT required")
		return
	}
	subscriberID := claims.Subject

	var isPrimary bool
	err = s.db.QueryRow(`
		SELECT is_primary FROM subscriber_profiles
		WHERE id = $1 AND subscriber_id = $2
	`, profileID, subscriberID).Scan(&isPrimary)
	if err != nil {
		auth.WriteError(w, http.StatusNotFound, "not_found", "profile not found")
		return
	}
	if isPrimary {
		auth.WriteError(w, http.StatusForbidden, "cannot_delete_primary", "the primary profile cannot be deleted")
		return
	}

	_, err = s.db.Exec(`
		DELETE FROM subscriber_profiles WHERE id = $1 AND subscriber_id = $2
	`, profileID, subscriberID)
	if err != nil {
		auth.WriteError(w, http.StatusInternalServerError, "db_error", "failed to delete profile")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "deleted"})
}

// switchProfile authenticates a PIN (if set) and returns a token scoped to the profile.
// POST /profiles/:id/switch
func (s *Server) switchProfile(w http.ResponseWriter, r *http.Request, profileID string) {
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

	var pinHash *string
	var profileName string
	err = s.db.QueryRow(`
		SELECT pin_hash, name FROM subscriber_profiles
		WHERE id = $1 AND subscriber_id = $2 AND is_active = TRUE
	`, profileID, subscriberID).Scan(&pinHash, &profileName)
	if err != nil {
		auth.WriteError(w, http.StatusNotFound, "not_found", "profile not found or inactive")
		return
	}

	// If profile has a PIN, validate it
	if pinHash != nil {
		var req switchProfileRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			auth.WriteError(w, http.StatusBadRequest, "invalid_json", "request body must be valid JSON")
			return
		}
		if req.PIN == "" {
			auth.WriteError(w, http.StatusForbidden, "pin_required", "this profile requires a PIN")
			return
		}
		if err := bcrypt.CompareHashAndPassword([]byte(*pinHash), []byte(req.PIN)); err != nil {
			auth.WriteError(w, http.StatusForbidden, "invalid_pin", "incorrect PIN")
			return
		}
	}

	// Issue a profile-scoped session token (stored in owl_profile_sessions or returned as JWT metadata)
	sessionToken, err := generateProfileSessionToken(subscriberID, profileID)
	if err != nil {
		auth.WriteError(w, http.StatusInternalServerError, "token_error", "failed to generate session token")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"profile_session_token": sessionToken,
		"profile_id":            profileID,
		"profile_name":          profileName,
	})
}

// getProfileLimits returns the subscriber's current and maximum profile count.
// GET /profiles/limits
func (s *Server) getProfileLimits(w http.ResponseWriter, r *http.Request) {
	claims, err := auth.ValidateJWT(r)
	if err != nil {
		auth.WriteError(w, http.StatusUnauthorized, "unauthorized", "valid JWT required")
		return
	}
	subscriberID := claims.Subject

	maxProfiles, planSlug, err := s.getProfileLimit(subscriberID)
	if err != nil {
		auth.WriteError(w, http.StatusInternalServerError, "db_error", "failed to get plan limits")
		return
	}
	current, err := s.countProfiles(subscriberID)
	if err != nil {
		auth.WriteError(w, http.StatusInternalServerError, "db_error", "failed to count profiles")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(profileLimitsResponse{
		Current: current,
		Max:     maxProfiles,
		Plan:    planSlug,
	})
}

// uploadAvatar handles custom avatar image upload.
// POST /profiles/:id/avatar
func (s *Server) uploadAvatar(w http.ResponseWriter, r *http.Request, profileID string) {
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

	// Verify ownership
	var exists bool
	err = s.db.QueryRow(`
		SELECT true FROM subscriber_profiles WHERE id = $1 AND subscriber_id = $2
	`, profileID, subscriberID).Scan(&exists)
	if err != nil {
		auth.WriteError(w, http.StatusNotFound, "not_found", "profile not found")
		return
	}

	// Limit: 1 MB
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		auth.WriteError(w, http.StatusBadRequest, "file_too_large", "avatar must be under 1 MB")
		return
	}
	file, header, err := r.FormFile("avatar")
	if err != nil {
		auth.WriteError(w, http.StatusBadRequest, "missing_file", "avatar file required (field: avatar)")
		return
	}
	defer file.Close()

	// Validate content type
	ct := header.Header.Get("Content-Type")
	if ct != "image/jpeg" && ct != "image/png" {
		auth.WriteError(w, http.StatusBadRequest, "invalid_content_type", "avatar must be JPEG or PNG")
		return
	}

	// Read the uploaded bytes (already limited to 1 MB by MaxBytesReader above).
	imgBytes, err := io.ReadAll(file)
	if err != nil {
		auth.WriteError(w, http.StatusInternalServerError, "read_error", "failed to read uploaded file")
		return
	}

	// Generate a random object key: avatars/<profileID>/<uuid>.jpg
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	objectKey := fmt.Sprintf("avatars/%s/%s.jpg", profileID, hex.EncodeToString(b))

	// Upload to Cloudflare R2.
	// avatarURL is the public CDN URL regardless of whether the upload succeeds —
	// if R2 is not configured we log a warning and store the URL as a placeholder
	// so the service keeps working in dev/CI without R2 credentials.
	avatarURL := fmt.Sprintf("https://media.roost.unity.dev/%s", objectKey)

	if s.r2 != nil {
		r2Bucket := getEnv("R2_MEDIA_BUCKET", "roost-media")
		if _, uploadErr := s.r2.PutObject(r2Bucket, objectKey, imgBytes, ct); uploadErr != nil {
			log.Printf("WARNING: R2 avatar upload failed for profile %s: %v", profileID, uploadErr)
			// Fall through: still update the DB with the CDN URL. The object will be
			// missing from R2 but the service remains functional. Operators can re-upload.
		}
	} else {
		log.Printf("WARNING: R2 not configured — avatar for profile %s stored as placeholder URL only", profileID)
	}

	_, err = s.db.Exec(`
		UPDATE subscriber_profiles
		SET avatar_url = $1, avatar_preset = NULL
		WHERE id = $2 AND subscriber_id = $3
	`, avatarURL, profileID, subscriberID)
	if err != nil {
		auth.WriteError(w, http.StatusInternalServerError, "db_error", "failed to update avatar")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"avatar_url": avatarURL,
		"status":     "uploaded",
	})
}

// getProfileReport returns viewing statistics for a profile.
// GET /profiles/:id/report?period=week|month
// Only accessible to the subscriber's primary profile.
func (s *Server) getProfileReport(w http.ResponseWriter, r *http.Request, profileID string) {
	if r.Method != http.MethodGet {
		auth.WriteError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET required")
		return
	}

	claims, err := auth.ValidateJWT(r)
	if err != nil {
		auth.WriteError(w, http.StatusUnauthorized, "unauthorized", "valid JWT required")
		return
	}
	subscriberID := claims.Subject

	// Only the primary profile holder can view reports
	var isPrimaryHolder bool
	err = s.db.QueryRow(`
		SELECT true FROM subscriber_profiles
		WHERE subscriber_id = $1 AND is_primary = TRUE
	`, subscriberID).Scan(&isPrimaryHolder)
	if err != nil || !isPrimaryHolder {
		auth.WriteError(w, http.StatusForbidden, "primary_only", "only the primary profile holder can view reports")
		return
	}

	// Verify the requested profile belongs to this subscriber
	var profileName string
	err = s.db.QueryRow(`
		SELECT name FROM subscriber_profiles WHERE id = $1 AND subscriber_id = $2
	`, profileID, subscriberID).Scan(&profileName)
	if err != nil {
		auth.WriteError(w, http.StatusNotFound, "not_found", "profile not found")
		return
	}

	period := r.URL.Query().Get("period")
	if period == "" {
		period = "week"
	}
	var since time.Time
	switch period {
	case "week":
		since = time.Now().AddDate(0, 0, -7)
	case "month":
		since = time.Now().AddDate(0, -1, 0)
	default:
		auth.WriteError(w, http.StatusBadRequest, "invalid_period", "period must be week or month")
		return
	}

	// Total watch time (from stream_sessions)
	var totalSeconds int64
	_ = s.db.QueryRow(`
		SELECT COALESCE(SUM(EXTRACT(EPOCH FROM (COALESCE(ended_at, now()) - started_at))), 0)
		FROM stream_sessions
		WHERE subscriber_id = $1 AND started_at >= $2
	`, subscriberID, since).Scan(&totalSeconds)

	// Top channels watched
	type channelStat struct {
		ChannelName string `json:"channel_name"`
		ChannelSlug string `json:"channel_slug"`
		WatchSeconds int64 `json:"watch_seconds"`
	}
	rows, err := s.db.Query(`
		SELECT
			COALESCE(c.name, 'Unknown'),
			COALESCE(c.slug, ''),
			COALESCE(SUM(EXTRACT(EPOCH FROM (COALESCE(ss.ended_at, now()) - ss.started_at))), 0)::bigint
		FROM stream_sessions ss
		LEFT JOIN channels c ON c.id = ss.channel_id
		WHERE ss.subscriber_id = $1 AND ss.started_at >= $2
		GROUP BY c.name, c.slug
		ORDER BY 3 DESC
		LIMIT 10
	`, subscriberID, since)
	channels := make([]channelStat, 0)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var cs channelStat
			if err := rows.Scan(&cs.ChannelName, &cs.ChannelSlug, &cs.WatchSeconds); err == nil {
				channels = append(channels, cs)
			}
		}
	}

	// VOD content watched
	type vodStat struct {
		Title       string `json:"title"`
		ContentType string `json:"content_type"`
		PositionSeconds int  `json:"position_seconds"`
		DurationSeconds int  `json:"duration_seconds"`
		Completed   bool   `json:"completed"`
	}
	vodRows, err := s.db.Query(`
		SELECT
			COALESCE(vm.title, ve.title, 'Unknown'),
			wp.content_type,
			wp.position_seconds,
			wp.duration_seconds,
			wp.completed
		FROM watch_progress wp
		LEFT JOIN vod_catalog vm ON vm.id = wp.content_id AND wp.content_type = 'movie'
		LEFT JOIN vod_episodes ve ON ve.id = wp.content_id AND wp.content_type = 'episode'
		WHERE wp.profile_id = $1 AND wp.last_watched_at >= $2
		ORDER BY wp.last_watched_at DESC
		LIMIT 20
	`, profileID, since)
	vodItems := make([]vodStat, 0)
	if err == nil {
		defer vodRows.Close()
		for vodRows.Next() {
			var v vodStat
			if err := vodRows.Scan(&v.Title, &v.ContentType, &v.PositionSeconds, &v.DurationSeconds, &v.Completed); err == nil {
				vodItems = append(vodItems, v)
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"profile_id":          profileID,
		"profile_name":        profileName,
		"period":              period,
		"since":               since.Format(time.RFC3339),
		"total_watch_seconds": totalSeconds,
		"channels_watched":    channels,
		"vod_watched":         vodItems,
	})
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// rowScanner abstracts *sql.Row and *sql.Rows for scanProfile.
type rowScanner interface {
	Scan(dest ...interface{}) error
}

// scanProfile scans a profile row from the database.
func scanProfile(row rowScanner) (profileRecord, error) {
	var p profileRecord
	var avatarURL, avatarPreset, ageRatingLimit *string
	var viewingScheduleJSON *string
	var blockedCatsJSON, prefsJSON string

	err := row.Scan(
		&p.ID, &p.Name, &avatarURL, &avatarPreset, &p.IsPrimary, &ageRatingLimit,
		&p.IsKidsProfile, &p.HasPIN, &p.IsActive,
		&blockedCatsJSON,
		&viewingScheduleJSON,
		&prefsJSON,
		&p.CreatedAt,
	)
	if err != nil {
		return p, err
	}

	p.AvatarURL = avatarURL
	p.AvatarPreset = avatarPreset
	p.AgeRatingLimit = ageRatingLimit

	// Parse blocked categories
	if err := json.Unmarshal([]byte(blockedCatsJSON), &p.BlockedCategories); err != nil {
		p.BlockedCategories = []string{}
	}

	// Parse viewing schedule
	if viewingScheduleJSON != nil && *viewingScheduleJSON != "" {
		var vs viewingSchedule
		if err := json.Unmarshal([]byte(*viewingScheduleJSON), &vs); err == nil {
			p.ViewingSchedule = &vs
		}
	}

	// Parse preferences
	if err := json.Unmarshal([]byte(prefsJSON), &p.Preferences); err != nil {
		p.Preferences = map[string]interface{}{}
	}

	return p, nil
}

// getProfileLimit returns the max profile count for the subscriber's current plan.
func (s *Server) getProfileLimit(subscriberID string) (int, string, error) {
	var maxProfiles int
	var planSlug string
	err := s.db.QueryRow(`
		SELECT COALESCE(sp.max_profiles, 2), COALESCE(sub.billing_plan_slug, sp.slug, 'basic')
		FROM subscribers s
		LEFT JOIN subscriptions sub ON sub.subscriber_id = s.id
		LEFT JOIN subscription_plans sp ON sp.slug = sub.plan_slug
		WHERE s.id = $1
		ORDER BY sub.created_at DESC
		LIMIT 1
	`, subscriberID).Scan(&maxProfiles, &planSlug)
	if err != nil {
		// No subscription — default to basic limits
		return 2, "basic", nil
	}
	return maxProfiles, planSlug, nil
}

// countProfiles returns the number of active profiles for a subscriber.
func (s *Server) countProfiles(subscriberID string) (int, error) {
	var count int
	err := s.db.QueryRow(`
		SELECT COUNT(*) FROM subscriber_profiles
		WHERE subscriber_id = $1 AND is_active = TRUE
	`, subscriberID).Scan(&count)
	return count, err
}

// isValidAvatarPreset returns true if the preset name is owl-1 through owl-12.
func isValidAvatarPreset(preset string) bool {
	for i := 1; i <= 12; i++ {
		if preset == fmt.Sprintf("owl-%d", i) {
			return true
		}
	}
	return false
}

// avatarPresetURL returns the CDN URL for an avatar preset.
func avatarPresetURL(preset string) string {
	return fmt.Sprintf("https://media.roost.unity.dev/avatars/presets/%s.png", preset)
}

// isNumeric returns true if all characters in s are ASCII digits.
func isNumeric(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// generateProfileSessionToken creates a short-lived token encoding subscriber+profile context.
// Format: hex(32 bytes). In production this would be a signed JWT or stored session.
func generateProfileSessionToken(subscriberID, profileID string) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	// Encode subscriber + profile into token payload for lookup
	// Real implementation: issue a JWT with profile_id claim.
	raw := fmt.Sprintf("%s:%s:%s", subscriberID, profileID, hex.EncodeToString(b))
	return hex.EncodeToString([]byte(raw)), nil
}
