// handlers_promo.go — Promo code and discount coupon management.
// P3-T07: Promo Codes & Discount Coupons
//
// POST /billing/promo/validate   — validate a promo code (public endpoint)
// POST /billing/admin/promo      — admin: create a new promo code
package billing

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/yourflock/roost/internal/auth"
)

// promoValidateRequest is the body for POST /billing/promo/validate.
type promoValidateRequest struct {
	Code string `json:"code"`
}

// promoValidateResponse is returned when a promo code is valid.
type promoValidateResponse struct {
	Valid            bool   `json:"valid"`
	Code             string `json:"code"`
	DiscountPercent  int    `json:"discount_percent,omitempty"`
	DiscountAmtCents int    `json:"discount_amount_cents,omitempty"`
	Message          string `json:"message"`
}

// handlePromoValidate validates a promo code and returns the discount.
// POST /billing/promo/validate (public — no auth required)
func (s *Server) handlePromoValidate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		auth.WriteError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST required")
		return
	}

	var req promoValidateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		auth.WriteError(w, http.StatusBadRequest, "invalid_json", "request body must be valid JSON")
		return
	}

	code := strings.ToUpper(strings.TrimSpace(req.Code))
	if code == "" {
		auth.WriteError(w, http.StatusBadRequest, "missing_code", "promo code is required")
		return
	}

	var discountPercent, discountAmtCents int
	var maxRedemptions, timesRedeemed int
	var validUntil *time.Time
	var stripeCouponID *string

	err := s.db.QueryRow(`
		SELECT
			COALESCE(discount_percent, 0),
			COALESCE(discount_amount_cents, 0),
			max_redemptions,
			times_redeemed,
			valid_until,
			stripe_coupon_id
		FROM promo_codes
		WHERE code = $1 AND is_active = true AND valid_from <= NOW()
	`, code).Scan(&discountPercent, &discountAmtCents, &maxRedemptions, &timesRedeemed, &validUntil, &stripeCouponID)
	if err != nil {
		writeJSON(w, http.StatusOK, promoValidateResponse{
			Valid:   false,
			Code:    code,
			Message: "promo code not found or expired",
		})
		return
	}

	// Check expiry
	if validUntil != nil && time.Now().After(*validUntil) {
		writeJSON(w, http.StatusOK, promoValidateResponse{
			Valid:   false,
			Code:    code,
			Message: "promo code has expired",
		})
		return
	}

	// Check redemption limit
	if maxRedemptions > 0 && timesRedeemed >= maxRedemptions {
		writeJSON(w, http.StatusOK, promoValidateResponse{
			Valid:   false,
			Code:    code,
			Message: "promo code has reached its redemption limit",
		})
		return
	}

	writeJSON(w, http.StatusOK, promoValidateResponse{
		Valid:            true,
		Code:             code,
		DiscountPercent:  discountPercent,
		DiscountAmtCents: discountAmtCents,
		Message:          "promo code is valid",
	})
}

// handleAdminPromo creates a new promo code (admin-only).
// POST /billing/admin/promo
func (s *Server) handleAdminPromo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		auth.WriteError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST required")
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

	var body struct {
		Code               string     `json:"code"`
		DiscountPercent    int        `json:"discount_percent"`
		DiscountAmtCents   int        `json:"discount_amount_cents"`
		MaxRedemptions     int        `json:"max_redemptions"`
		ValidUntil         *time.Time `json:"valid_until"`
		StripeCouponID     string     `json:"stripe_coupon_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		auth.WriteError(w, http.StatusBadRequest, "invalid_json", "invalid request body")
		return
	}

	code := strings.ToUpper(strings.TrimSpace(body.Code))
	if code == "" {
		auth.WriteError(w, http.StatusBadRequest, "missing_code", "code is required")
		return
	}

	id := uuid.New().String()
	_, err = s.db.Exec(`
		INSERT INTO promo_codes (id, code, discount_percent, discount_amount_cents,
			max_redemptions, valid_until, stripe_coupon_id)
		VALUES ($1, $2, $3, $4, $5, $6, NULLIF($7, ''))
	`, id, code, body.DiscountPercent, body.DiscountAmtCents,
		func() *int { if body.MaxRedemptions > 0 { v := body.MaxRedemptions; return &v }; return nil }(),
		body.ValidUntil, body.StripeCouponID)
	if err != nil {
		auth.WriteError(w, http.StatusConflict, "code_exists", "promo code already exists")
		return
	}

	// TODO (P3-T01): If stripe_coupon_id is empty, create a Stripe coupon automatically
	// This requires STRIPE_SECRET_KEY. For now, admin can manually pass the Stripe coupon ID.

	writeJSON(w, http.StatusCreated, map[string]any{
		"id":   id,
		"code": code,
		"status": "created",
	})
}
