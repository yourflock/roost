// handlers_promo_extended.go — Extended promotional code management for P17-T02.
//
// Extends the basic promo validation in handlers_promo.go with:
//   - Full promotional_codes table support (typed discounts)
//   - GET  /admin/promo         — list all promo codes (admin only)
//   - PATCH /admin/promo/:id    — update max_uses, expires_at, is_active (admin only)
//
// POST /admin/promo is handled in handlers_promo.go (handleAdminPromo).
// POST /billing/promo/validate is also handled there and validates both
// the legacy promo_codes table and the new promotional_codes table.
package billing

import (
	"fmt"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/yourflock/roost/internal/auth"
)

// handleAdminPromoList lists all promotional codes (admin only).
// GET /admin/promo
func (s *Server) handleAdminPromoList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		auth.WriteError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET required")
		return
	}
	claims, err := auth.ValidateJWT(r)
	if err != nil {
		auth.WriteError(w, http.StatusUnauthorized, "unauthorized", "valid JWT required")
		return
	}
	var isSuperowner bool
	_ = s.db.QueryRow(`SELECT is_superowner FROM subscribers WHERE id = $1`, claims.Subject).Scan(&isSuperowner)
	if !isSuperowner {
		auth.WriteError(w, http.StatusForbidden, "forbidden", "superowner access required")
		return
	}

	rows, err := s.db.Query(`
		SELECT
			id, code, type, value, currency, COALESCE(stripe_coupon_id,''),
			COALESCE(max_uses, 0), current_uses,
			expires_at, is_active, created_at
		FROM promotional_codes
		ORDER BY created_at DESC
		LIMIT 200
	`)
	if err != nil {
		auth.WriteError(w, http.StatusInternalServerError, "db_error", "failed to query promo codes")
		return
	}
	defer rows.Close()

	type promoRow struct {
		ID             string     `json:"id"`
		Code           string     `json:"code"`
		Type           string     `json:"type"`
		Value          int        `json:"value"`
		Currency       string     `json:"currency"`
		StripeCouponID string     `json:"stripe_coupon_id,omitempty"`
		MaxUses        int        `json:"max_uses"`
		CurrentUses    int        `json:"current_uses"`
		ExpiresAt      *time.Time `json:"expires_at,omitempty"`
		IsActive       bool       `json:"is_active"`
		CreatedAt      time.Time  `json:"created_at"`
	}

	var codes []promoRow
	for rows.Next() {
		var p promoRow
		if err := rows.Scan(
			&p.ID, &p.Code, &p.Type, &p.Value, &p.Currency, &p.StripeCouponID,
			&p.MaxUses, &p.CurrentUses, &p.ExpiresAt, &p.IsActive, &p.CreatedAt,
		); err != nil {
			continue
		}
		codes = append(codes, p)
	}
	if codes == nil {
		codes = []promoRow{}
	}

	writeJSON(w, http.StatusOK, map[string]any{"codes": codes, "count": len(codes)})
}

// handleAdminPromoUpdate updates a promotional code (admin only).
// PATCH /admin/promo/:id
func (s *Server) handleAdminPromoUpdate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPatch {
		auth.WriteError(w, http.StatusMethodNotAllowed, "method_not_allowed", "PATCH required")
		return
	}
	claims, err := auth.ValidateJWT(r)
	if err != nil {
		auth.WriteError(w, http.StatusUnauthorized, "unauthorized", "valid JWT required")
		return
	}
	var isSuperowner bool
	_ = s.db.QueryRow(`SELECT is_superowner FROM subscribers WHERE id = $1`, claims.Subject).Scan(&isSuperowner)
	if !isSuperowner {
		auth.WriteError(w, http.StatusForbidden, "forbidden", "superowner access required")
		return
	}

	// Extract ID from path: /admin/promo/{id}
	path := strings.TrimPrefix(r.URL.Path, "/admin/promo/")
	promoID := strings.TrimSuffix(path, "/")
	if promoID == "" {
		auth.WriteError(w, http.StatusBadRequest, "missing_id", "promo code ID required in path")
		return
	}

	var body struct {
		MaxUses   *int       `json:"max_uses"`
		ExpiresAt *time.Time `json:"expires_at"`
		IsActive  *bool      `json:"is_active"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		auth.WriteError(w, http.StatusBadRequest, "invalid_json", "invalid request body")
		return
	}

	// Build update query dynamically for fields provided.
	var setClauses []string
	var args []any
	argN := 1

	if body.MaxUses != nil {
		setClauses = append(setClauses, "max_uses = $"+itoa(argN))
		if *body.MaxUses == 0 {
			args = append(args, nil) // NULL = unlimited
		} else {
			args = append(args, *body.MaxUses)
		}
		argN++
	}
	if body.ExpiresAt != nil {
		setClauses = append(setClauses, "expires_at = $"+itoa(argN))
		args = append(args, *body.ExpiresAt)
		argN++
	}
	if body.IsActive != nil {
		setClauses = append(setClauses, "is_active = $"+itoa(argN))
		args = append(args, *body.IsActive)
		argN++
	}

	if len(setClauses) == 0 {
		auth.WriteError(w, http.StatusBadRequest, "nothing_to_update",
			"provide at least one of: max_uses, expires_at, is_active")
		return
	}

	query := "UPDATE promotional_codes SET " + strings.Join(setClauses, ", ") +
		" WHERE id = $" + itoa(argN)
	args = append(args, promoID)

	res, err := s.db.Exec(query, args...)
	if err != nil {
		auth.WriteError(w, http.StatusInternalServerError, "db_error", "update failed")
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		auth.WriteError(w, http.StatusNotFound, "not_found", "promo code not found")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "updated", "id": promoID})
}

// validateAndRedeemPromo validates a promotional code and records a redemption.
// Returns the code row data. Used internally by the checkout flow.
func (s *Server) validateAndRedeemPromo(code, subscriberID string) (promoType string, value int, err error) {
	code = strings.ToUpper(strings.TrimSpace(code))

	var codeID string
	var maxUses, currentUses int
	var expiresAt *time.Time

	err = s.db.QueryRow(`
		SELECT id, type, value, COALESCE(max_uses, 0), current_uses, expires_at
		FROM promotional_codes
		WHERE code = $1 AND is_active = TRUE
	`, code).Scan(&codeID, &promoType, &value, &maxUses, &currentUses, &expiresAt)
	if err != nil {
		return "", 0, fmt.Errorf("invalid promo code")
	}

	// Check expiry.
	if expiresAt != nil && time.Now().After(*expiresAt) {
		return "", 0, fmt.Errorf("promo code has expired")
	}

	// Check redemption limit.
	if maxUses > 0 && currentUses >= maxUses {
		return "", 0, fmt.Errorf("promo code redemption limit reached")
	}

	// Check subscriber hasn't already redeemed.
	var alreadyRedeemed int
	_ = s.db.QueryRow(
		`SELECT COUNT(*) FROM promo_code_redemptions WHERE code_id = $1 AND subscriber_id = $2`,
		codeID, subscriberID,
	).Scan(&alreadyRedeemed)
	if alreadyRedeemed > 0 {
		return "", 0, fmt.Errorf("promo code already redeemed")
	}

	// Record redemption and increment usage atomically.
	tx, err := s.db.Begin()
	if err != nil {
		return "", 0, err
	}
	defer tx.Rollback() //nolint:errcheck

	if _, err = tx.Exec(
		`INSERT INTO promo_code_redemptions (code_id, subscriber_id) VALUES ($1, $2)`,
		codeID, subscriberID,
	); err != nil {
		return "", 0, fmt.Errorf("redemption record failed: %w", err)
	}

	if _, err = tx.Exec(
		`UPDATE promotional_codes SET current_uses = current_uses + 1 WHERE id = $1`,
		codeID,
	); err != nil {
		return "", 0, fmt.Errorf("usage increment failed: %w", err)
	}

	return promoType, value, tx.Commit()
}

// itoa converts an int to a string (avoids importing strconv in small helpers).
func itoa(n int) string {
	return fmt.Sprintf("%d", n)
}
