// handlers_referral.go — Referral program for P17-T03.
//
// GET  /billing/referral          — get subscriber's referral code and stats
// POST /billing/referral/claim    — claim a referral code on signup
// GET  /billing/referral/list     — list referrals made by the subscriber
//
// Reward flow: referee subscribes -> webhook marks qualified -> daily cron
// checks 7-day hold -> applies Stripe credit -> marks rewarded.
package billing

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"time"

	"github.com/yourflock/roost/internal/auth"
)

// referralCodeAlphabet is the character set for referral codes. No ambiguous chars.
const referralCodeAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

// handleReferral routes GET /billing/referral.
func (s *Server) handleReferral(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.getReferralCode(w, r)
	default:
		auth.WriteError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET required")
	}
}

// getReferralCode returns the subscriber's referral code and stats.
// GET /billing/referral
func (s *Server) getReferralCode(w http.ResponseWriter, r *http.Request) {
	claims, err := auth.ValidateJWT(r)
	if err != nil {
		auth.WriteError(w, http.StatusUnauthorized, "unauthorized", "valid JWT required")
		return
	}
	subscriberID := claims.Subject

	// Get or create referral code.
	code, err := s.getOrCreateReferralCode(subscriberID)
	if err != nil {
		auth.WriteError(w, http.StatusInternalServerError, "db_error", "failed to get referral code")
		return
	}

	baseURL := getEnv("ROOST_BASE_URL", "https://roost.yourflock.com")
	link := fmt.Sprintf("%s/ref/%s", baseURL, code)

	// Aggregate stats.
	var totalReferred, totalQualified, totalRewarded int
	var pendingCents, lifetimeCents int64
	_ = s.db.QueryRow(`
		SELECT
			COUNT(*),
			COUNT(*) FILTER (WHERE status IN ('qualified', 'rewarded')),
			COUNT(*) FILTER (WHERE status = 'rewarded'),
			COALESCE(SUM(reward_amount_cents) FILTER (WHERE status = 'qualified'), 0),
			COALESCE(SUM(reward_amount_cents) FILTER (WHERE status = 'rewarded'), 0)
		FROM referrals WHERE referrer_id = $1
	`, subscriberID).Scan(&totalReferred, &totalQualified, &totalRewarded, &pendingCents, &lifetimeCents)

	writeJSON(w, http.StatusOK, map[string]any{
		"code":                  code,
		"link":                  link,
		"total_referred":        totalReferred,
		"total_qualified":       totalQualified,
		"total_rewarded":        totalRewarded,
		"pending_reward_cents":  pendingCents,
		"lifetime_reward_cents": lifetimeCents,
	})
}

// handleReferralClaim registers a referral when a new subscriber uses a referral link.
// POST /billing/referral/claim
func (s *Server) handleReferralClaim(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		auth.WriteError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST required")
		return
	}
	claims, err := auth.ValidateJWT(r)
	if err != nil {
		auth.WriteError(w, http.StatusUnauthorized, "unauthorized", "valid JWT required")
		return
	}
	refereeID := claims.Subject

	var body struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		auth.WriteError(w, http.StatusBadRequest, "invalid_json", "code required")
		return
	}

	code := strings.ToUpper(strings.TrimSpace(body.Code))
	if code == "" {
		auth.WriteError(w, http.StatusBadRequest, "missing_code", "referral code is required")
		return
	}

	// Look up the referral code.
	var referralCodeID, referrerID string
	err = s.db.QueryRow(`
		SELECT id, subscriber_id FROM referral_codes WHERE code = $1
	`, code).Scan(&referralCodeID, &referrerID)
	if err != nil {
		auth.WriteError(w, http.StatusNotFound, "invalid_code", "referral code not found")
		return
	}

	// Referee cannot refer themselves.
	if referrerID == refereeID {
		auth.WriteError(w, http.StatusBadRequest, "self_referral", "you cannot use your own referral code")
		return
	}

	// Check referee doesn't already have a referral record.
	var existing int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM referrals WHERE referee_id = $1`, refereeID).Scan(&existing)
	if existing > 0 {
		// Already claimed — idempotent success.
		writeJSON(w, http.StatusOK, map[string]string{"status": "already_claimed"})
		return
	}

	// Create the referral record.
	var referralID string
	err = s.db.QueryRow(`
		INSERT INTO referrals (referrer_id, referee_id, referral_code_id, status)
		VALUES ($1, $2, $3, 'pending')
		RETURNING id
	`, referrerID, refereeID, referralCodeID).Scan(&referralID)
	if err != nil {
		auth.WriteError(w, http.StatusInternalServerError, "db_error", "failed to record referral")
		return
	}

	// Flag if referrer and referee share an IP (suspicious, not auto-rejected).
	clientIP := extractClientIP(r)
	if clientIP != "" {
		var ipMatchCount int
		_ = s.db.QueryRow(`
			SELECT COUNT(*) FROM trial_abuse_tracking
			WHERE ip_address::text = $1
			  AND email_hash IN (
				  SELECT encode(sha256(lower(email)::bytea), 'hex')
				  FROM subscribers WHERE id = $2
			  )
		`, clientIP, referrerID).Scan(&ipMatchCount)
		if ipMatchCount > 0 {
			_, _ = s.db.Exec(`
				INSERT INTO referral_flags (referral_id, flag_type, details)
				VALUES ($1, 'same_ip', $2::jsonb)
			`, referralID, fmt.Sprintf(`{"ip":%q}`, clientIP))
		}
	}

	writeJSON(w, http.StatusCreated, map[string]string{"status": "claimed", "code": code})
}

// handleReferralList returns paginated referrals made by the subscriber.
// GET /billing/referral/list
func (s *Server) handleReferralList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		auth.WriteError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET required")
		return
	}
	claims, err := auth.ValidateJWT(r)
	if err != nil {
		auth.WriteError(w, http.StatusUnauthorized, "unauthorized", "valid JWT required")
		return
	}

	rows, err := s.db.Query(`
		SELECT
			r.id,
			r.status,
			r.created_at,
			r.qualified_at,
			r.reward_applied_at,
			r.reward_amount_cents,
			regexp_replace(sub.email, '^(.).*(@.*)$', '\1***\2') AS masked_email
		FROM referrals r
		JOIN subscribers sub ON sub.id = r.referee_id
		WHERE r.referrer_id = $1
		ORDER BY r.created_at DESC
		LIMIT 50
	`, claims.Subject)
	if err != nil {
		auth.WriteError(w, http.StatusInternalServerError, "db_error", "failed to query referrals")
		return
	}
	defer rows.Close()

	type referralItem struct {
		ID                 string     `json:"id"`
		Status             string     `json:"status"`
		CreatedAt          time.Time  `json:"created_at"`
		QualifiedAt        *time.Time `json:"qualified_at,omitempty"`
		RewardAppliedAt    *time.Time `json:"reward_applied_at,omitempty"`
		RewardAmountCents  *int       `json:"reward_amount_cents,omitempty"`
		RefereeEmailMasked string     `json:"referee_email"`
	}

	var items []referralItem
	for rows.Next() {
		var item referralItem
		if err := rows.Scan(
			&item.ID, &item.Status, &item.CreatedAt,
			&item.QualifiedAt, &item.RewardAppliedAt, &item.RewardAmountCents,
			&item.RefereeEmailMasked,
		); err != nil {
			continue
		}
		items = append(items, item)
	}
	if items == nil {
		items = []referralItem{}
	}

	writeJSON(w, http.StatusOK, map[string]any{"referrals": items, "count": len(items)})
}

// getOrCreateReferralCode returns the subscriber's referral code, creating one if needed.
func (s *Server) getOrCreateReferralCode(subscriberID string) (string, error) {
	var code string
	err := s.db.QueryRow(
		`SELECT code FROM referral_codes WHERE subscriber_id = $1`, subscriberID,
	).Scan(&code)
	if err == nil {
		return code, nil
	}

	// Generate a unique short code: ROOST-XXXXX
	for attempts := 0; attempts < 10; attempts++ {
		suffix, err := randomString(5, referralCodeAlphabet)
		if err != nil {
			return "", err
		}
		candidate := "ROOST-" + suffix
		_, insertErr := s.db.Exec(
			`INSERT INTO referral_codes (subscriber_id, code) VALUES ($1, $2)`,
			subscriberID, candidate,
		)
		if insertErr == nil {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("failed to generate unique referral code after 10 attempts")
}

// randomString generates a cryptographically random string of length n from alphabet.
func randomString(n int, alphabet string) (string, error) {
	b := make([]byte, n)
	for i := range b {
		idx, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		if err != nil {
			return "", err
		}
		b[i] = alphabet[idx.Int64()]
	}
	return string(b), nil
}
