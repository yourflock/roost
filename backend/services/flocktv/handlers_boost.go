// handlers_boost.go — Roost Boost IPTV contribution handlers (DB-wired).
// Phase FLOCKTV FTV.2.T01/T02/T06: subscribers contribute M3U/Xtream IPTV accounts
// to unlock live TV. Credentials are encrypted with AES-256-GCM before any DB write.
// NEVER store plaintext IPTV credentials — the AES key lives in ROOST_CREDS_KEY env var.
package flocktv

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// contributeRequest is the POST /flocktv/boost/contribute body.
type contributeRequest struct {
	SourceType  string `json:"source_type"` // "m3u_url" or "xtream"
	Credentials string `json:"credentials"` // M3U URL or Xtream credentials JSON
	Label       string `json:"label"`       // user-given label, e.g., "My IPTV Provider"
}

// ContributionStatus is the API representation of an IPTV contribution.
type ContributionStatus struct {
	ID           string    `json:"id"`
	SourceType   string    `json:"source_type"`
	Label        string    `json:"label,omitempty"`
	ChannelCount int       `json:"channel_count"`
	Active       bool      `json:"active"`
	LastVerified *time.Time `json:"last_verified,omitempty"`
}

// encryptCredentials encrypts plaintext with AES-256-GCM.
// The key is read from the ROOST_CREDS_KEY environment variable (must be 32 bytes).
// Returns (ciphertext, nonce, error). Both ciphertext and nonce must be stored.
func encryptCredentials(plaintext []byte) (ciphertext, nonce []byte, err error) {
	rawKey := []byte(getEnv("ROOST_CREDS_KEY", ""))
	if len(rawKey) == 0 {
		return nil, nil, errors.New("ROOST_CREDS_KEY is not set")
	}
	// Pad or truncate to exactly 32 bytes for AES-256.
	key := make([]byte, 32)
	copy(key, rawKey)

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, nil, err
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, nil, err
	}

	nonce = make([]byte, gcm.NonceSize())
	if _, err = io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, nil, err
	}

	ciphertext = gcm.Seal(nil, nonce, plaintext, nil)
	return ciphertext, nonce, nil
}

// decryptCredentials decrypts AES-256-GCM ciphertext using ROOST_CREDS_KEY.
func decryptCredentials(ciphertext, nonce []byte) ([]byte, error) {
	rawKey := []byte(getEnv("ROOST_CREDS_KEY", ""))
	if len(rawKey) == 0 {
		return nil, errors.New("ROOST_CREDS_KEY is not set")
	}
	key := make([]byte, 32)
	copy(key, rawKey)

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	return gcm.Open(nil, nonce, ciphertext, nil)
}

// validateIPTVCredentials performs a basic syntax validation of IPTV credentials
// before accepting them. For M3U URLs: must be http/https. For Xtream: JSON with
// required fields. This does NOT verify the credentials are live — that happens
// asynchronously via the channel discovery job.
func validateIPTVCredentials(sourceType, credentials string) error {
	if credentials == "" {
		return errors.New("credentials are empty")
	}

	switch sourceType {
	case "m3u_url":
		creds := strings.TrimSpace(credentials)
		if !strings.HasPrefix(creds, "http://") && !strings.HasPrefix(creds, "https://") {
			return errors.New("M3U URL must start with http:// or https://")
		}
		// Basic sanity: no spaces in URL.
		for _, c := range creds {
			if unicode.IsSpace(c) {
				return errors.New("M3U URL must not contain spaces")
			}
		}
	case "xtream":
		// Xtream credentials must be non-empty JSON-like string.
		// Full validation happens in the async channel discovery job.
		if len(credentials) < 10 {
			return errors.New("Xtream credentials too short")
		}
	default:
		return errors.New("unknown source type")
	}

	return nil
}

// handleContribute registers a subscriber's IPTV account as a Roost Boost contribution.
// POST /flocktv/boost/contribute
// Credentials are encrypted before storage — plaintext never reaches the DB.
func (s *Server) handleContribute(w http.ResponseWriter, r *http.Request) {
	var req contributeRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	if req.SourceType != "m3u_url" && req.SourceType != "xtream" {
		writeError(w, http.StatusBadRequest, "invalid_source_type",
			"source_type must be m3u_url or xtream")
		return
	}

	if req.Credentials == "" {
		writeError(w, http.StatusBadRequest, "missing_field", "credentials are required")
		return
	}

	// Validate credentials format before encryption.
	if err := validateIPTVCredentials(req.SourceType, req.Credentials); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_credentials", err.Error())
		return
	}

	// Encrypt before any DB write.
	ciphertext, nonce, err := encryptCredentials([]byte(req.Credentials))
	if err != nil {
		s.logger.Error("credential encryption failed", "error", err.Error())
		writeError(w, http.StatusInternalServerError, "encryption_error",
			"failed to secure credentials")
		return
	}

	// Get subscriber ID from JWT context or header.
	subscriberID := r.Header.Get("X-Subscriber-ID")
	if sid, ok := r.Context().Value(contextKeySubscriberID).(string); ok && sid != "" {
		subscriberID = sid
	}

	var contributionID string
	boostNowActive := false

	if s.db != nil && subscriberID != "" {
		err = s.db.QueryRowContext(r.Context(), `
			INSERT INTO iptv_contributions
			  (subscriber_id, source_type, encrypted_creds, creds_nonce, label, active, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, true, NOW(), NOW())
			RETURNING id`,
			subscriberID, req.SourceType, ciphertext, nonce, req.Label,
		).Scan(&contributionID)

		if err != nil {
			s.logger.Error("contribution insert failed", "error", err.Error())
			writeError(w, http.StatusInternalServerError, "db_error",
				"failed to store contribution")
			return
		}

		// Increment contribution count and activate boost.
		_, _ = s.db.ExecContext(r.Context(), `
			UPDATE subscribers
			SET iptv_contribution_count = iptv_contribution_count + 1,
			    roost_boost_active = true,
			    updated_at = NOW()
			WHERE id = $1`,
			subscriberID,
		)
		boostNowActive = true
	} else {
		// No DB available (dev/test mode) — generate a transient UUID so the response
		// is structurally valid. The contribution is not persisted.
		contributionID = uuid.New().String()
	}

	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"id":           contributionID,
		"source_type":  req.SourceType,
		"label":        req.Label,
		"status":       "verifying",
		"boost_active": boostNowActive,
	})
}

// handleBoostStatus returns the subscriber's Roost Boost contribution summary.
// GET /flocktv/boost/status
func (s *Server) handleBoostStatus(w http.ResponseWriter, r *http.Request) {
	subscriberID := r.Header.Get("X-Subscriber-ID")
	if sid, ok := r.Context().Value(contextKeySubscriberID).(string); ok && sid != "" {
		subscriberID = sid
	}

	if s.db == nil || subscriberID == "" {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"boost_active":       false,
			"contribution_count": 0,
			"channel_count":      0,
			"unlock_requirement": "contribute at least 1 active IPTV account to unlock live TV",
		})
		return
	}

	var boostActive bool
	var contributionCount int
	_ = s.db.QueryRowContext(r.Context(),
		`SELECT roost_boost_active, iptv_contribution_count FROM subscribers WHERE id = $1`,
		subscriberID,
	).Scan(&boostActive, &contributionCount)

	// Count total channels from active contributions.
	var channelCount int
	_ = s.db.QueryRowContext(r.Context(), `
		SELECT COALESCE(SUM(channel_count), 0)
		FROM iptv_contributions
		WHERE subscriber_id = $1 AND active = true`,
		subscriberID,
	).Scan(&channelCount)

	// Load contribution list.
	contribRows, err := s.db.QueryContext(r.Context(), `
		SELECT id, source_type, label, channel_count, active, last_verified
		FROM iptv_contributions
		WHERE subscriber_id = $1
		ORDER BY created_at DESC`,
		subscriberID,
	)

	contributions := []ContributionStatus{}
	if err == nil {
		defer contribRows.Close()
		for contribRows.Next() {
			var c ContributionStatus
			var label *string
			_ = contribRows.Scan(&c.ID, &c.SourceType, &label, &c.ChannelCount, &c.Active, &c.LastVerified)
			if label != nil {
				c.Label = *label
			}
			contributions = append(contributions, c)
		}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"boost_active":       boostActive,
		"contribution_count": contributionCount,
		"channel_count":      channelCount,
		"contributions":      contributions,
		"unlock_requirement": "contribute at least 1 active IPTV account to unlock live TV",
	})
}

// handleRemoveContribution removes a subscriber's IPTV contribution.
// DELETE /flocktv/boost/contribute/{id}
func (s *Server) handleRemoveContribution(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "missing_param", "contribution id is required")
		return
	}

	subscriberID := r.Header.Get("X-Subscriber-ID")
	if sid, ok := r.Context().Value(contextKeySubscriberID).(string); ok && sid != "" {
		subscriberID = sid
	}

	if s.db == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	// Soft-delete: set active=false.
	result, err := s.db.ExecContext(r.Context(), `
		UPDATE iptv_contributions
		SET active = false, updated_at = NOW()
		WHERE id = $1 AND subscriber_id = $2`,
		id, subscriberID,
	)
	if err != nil {
		s.logger.Error("remove contribution failed", "error", err.Error())
		writeError(w, http.StatusInternalServerError, "db_error", "failed to remove contribution")
		return
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		writeError(w, http.StatusNotFound, "not_found", "contribution not found")
		return
	}

	// Decrement contribution count; if it hits 0, disable boost.
	_, _ = s.db.ExecContext(r.Context(), `
		UPDATE subscribers
		SET iptv_contribution_count = GREATEST(0, iptv_contribution_count - 1),
		    roost_boost_active = (
		      SELECT COUNT(*) > 0 FROM iptv_contributions
		      WHERE subscriber_id = $1 AND active = true
		    ),
		    updated_at = NOW()
		WHERE id = $1`,
		subscriberID,
	)

	// Re-evaluate channel_sources priorities for canonical channels that used this contribution.
	if s.db != nil {
		_, _ = s.db.ExecContext(r.Context(), `
			UPDATE channel_sources cs
			SET priority = (
			  SELECT ROW_NUMBER() OVER (PARTITION BY canonical_channel_id ORDER BY health_score DESC) - 1
			  FROM channel_sources
			  WHERE canonical_channel_id = cs.canonical_channel_id AND contribution_id != $1
			)
			WHERE contribution_id = $1`,
			id,
		)
		_, _ = s.db.ExecContext(r.Context(), `
			DELETE FROM channel_sources WHERE contribution_id = $1`, id)
		// Update active_source_count for affected canonical channels.
		_, _ = s.db.ExecContext(r.Context(), `
			UPDATE canonical_channels cc
			SET active_source_count = (
			  SELECT COUNT(*) FROM channel_sources WHERE canonical_channel_id = cc.id
			)
			WHERE id IN (
			  SELECT canonical_channel_id FROM channel_sources WHERE contribution_id = $1
			)`,
			id,
		)
	}

	w.WriteHeader(http.StatusNoContent)
}

// handleBoostChannels returns the list of canonical channels available via Roost Boost.
// GET /flocktv/boost/channels
func (s *Server) handleBoostChannels(w http.ResponseWriter, r *http.Request) {
	if !boostActiveFromCtx(r.Context()) {
		// Check the header fallback for dev mode.
		if r.Header.Get("X-Roost-Boost") != "true" {
			writeError(w, http.StatusForbidden, "boost_required",
				"Roost Boost subscription required — contribute an IPTV account to unlock live TV")
			return
		}
	}

	category := r.URL.Query().Get("category")
	country := r.URL.Query().Get("country")

	if s.db == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"channels": []interface{}{},
			"count":    0,
		})
		return
	}

	query := `SELECT id, canonical_name, category, country, logo_url, active_source_count, ingest_active
		FROM canonical_channels WHERE ingest_active = true`
	args := []interface{}{}
	argN := 1

	if category != "" {
		query += " AND category = $" + string(rune('0'+argN))
		args = append(args, category)
		argN++
	}
	if country != "" {
		query += " AND country = $" + string(rune('0'+argN))
		args = append(args, country)
		argN++
	}
	query += " ORDER BY canonical_name ASC LIMIT 200"

	rows, err := s.db.QueryContext(r.Context(), query, args...)
	if err != nil {
		s.logger.Error("boost channels query failed", "error", err.Error())
		writeError(w, http.StatusInternalServerError, "db_error", "failed to retrieve channels")
		return
	}
	defer rows.Close()

	type Channel struct {
		ID               string  `json:"id"`
		Name             string  `json:"name"`
		Category         *string `json:"category,omitempty"`
		Country          *string `json:"country,omitempty"`
		LogoURL          *string `json:"logo_url,omitempty"`
		ActiveSources    int     `json:"active_sources"`
		IngestActive     bool    `json:"ingest_active"`
	}

	channels := []Channel{}
	for rows.Next() {
		var ch Channel
		if scanErr := rows.Scan(
			&ch.ID, &ch.Name, &ch.Category, &ch.Country,
			&ch.LogoURL, &ch.ActiveSources, &ch.IngestActive,
		); scanErr == nil {
			channels = append(channels, ch)
		}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"channels": channels,
		"count":    len(channels),
	})
}
