// handlers_regional_pricing.go — Region-based subscription pricing endpoints (P14-T03).
//
// Endpoints:
//   GET  /billing/plans                    — list plans with regional pricing if subscriber has region set
//   GET  /billing/plans/:id/price          — get localized price for subscriber's region
//   POST /admin/billing/regional-prices    — create or update a regional price (admin)
//   GET  /admin/billing/regional-prices    — list all regional prices (admin)
package billing

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/yourflock/roost/internal/auth"
)

// ── Types ─────────────────────────────────────────────────────────────────────

// planWithPricing represents a subscription plan with optional regional pricing.
type planWithPricing struct {
	ID                   string         `json:"id"`
	Name                 string         `json:"name"`
	MaxConcurrentStreams  int            `json:"max_concurrent_streams"`
	Features             json.RawMessage `json:"features"`
	IsActive             bool           `json:"is_active"`
	CreatedAt            time.Time      `json:"created_at"`
	RegionalPrice        *regionalPrice `json:"regional_price,omitempty"`
	DefaultMonthlyCents  int            `json:"default_monthly_cents"`
	DefaultAnnualCents   int            `json:"default_annual_cents"`
}

// regionalPrice holds localized pricing for a plan+region combination.
type regionalPrice struct {
	PlanID               string  `json:"plan_id"`
	RegionID             string  `json:"region_id"`
	RegionCode           string  `json:"region_code"`
	RegionName           string  `json:"region_name"`
	Currency             string  `json:"currency"`
	MonthlyCents         int     `json:"monthly_price_cents"`
	AnnualCents          int     `json:"annual_price_cents"`
	StripePriceIDMonthly *string `json:"stripe_price_id_monthly,omitempty"`
	StripePriceIDAnnual  *string `json:"stripe_price_id_annual,omitempty"`
}

// regionalPriceAdmin is the admin view including IDs.
type regionalPriceAdmin struct {
	ID                   string  `json:"id"`
	PlanID               string  `json:"plan_id"`
	PlanName             string  `json:"plan_name"`
	RegionID             string  `json:"region_id"`
	RegionCode           string  `json:"region_code"`
	RegionName           string  `json:"region_name"`
	Currency             string  `json:"currency"`
	MonthlyCents         int     `json:"monthly_price_cents"`
	AnnualCents          int     `json:"annual_price_cents"`
	StripePriceIDMonthly *string `json:"stripe_price_id_monthly,omitempty"`
	StripePriceIDAnnual  *string `json:"stripe_price_id_annual,omitempty"`
}

// ── Handlers ──────────────────────────────────────────────────────────────────

// handleBillingPlans handles GET /billing/plans.
// Returns all active plans. If the authenticated subscriber has a region set,
// each plan is annotated with regional pricing. Falls back to plan-level prices
// (0 cents) if no regional price exists for the subscriber's region.
func (s *Server) handleBillingPlans(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		auth.WriteError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET required")
		return
	}

	// Region is optional — anonymous callers see plans without regional pricing.
	var subscriberRegionID *string
	claims, err := auth.ValidateJWT(r)
	if err == nil {
		var rid sql.NullString
		_ = s.db.QueryRow(
			`SELECT region_id FROM subscribers WHERE id = $1`,
			claims.Subject,
		).Scan(&rid)
		if rid.Valid {
			v := rid.String
			subscriberRegionID = &v
		}
	}

	rows, err := s.db.Query(`
		SELECT id, name, max_concurrent_streams, features, is_active, created_at
		FROM subscription_plans
		WHERE is_active = TRUE
		ORDER BY max_concurrent_streams ASC
	`)
	if err != nil {
		auth.WriteError(w, http.StatusInternalServerError, "db_error", "failed to query plans")
		return
	}
	defer rows.Close()

	var plans []planWithPricing
	for rows.Next() {
		var p planWithPricing
		if err := rows.Scan(&p.ID, &p.Name, &p.MaxConcurrentStreams, &p.Features, &p.IsActive, &p.CreatedAt); err != nil {
			continue
		}

		// Attach regional pricing if subscriber has a region.
		if subscriberRegionID != nil {
			rp, err := s.lookupRegionalPrice(p.ID, *subscriberRegionID)
			if err == nil {
				p.RegionalPrice = rp
			}
		}

		plans = append(plans, p)
	}
	if plans == nil {
		plans = []planWithPricing{}
	}

	auth.WriteJSON(w, http.StatusOK, map[string]interface{}{"plans": plans})
}

// handleBillingPlanPrice handles GET /billing/plans/{id}/price.
// Returns the localized price for the subscriber's region.
func (s *Server) handleBillingPlanPrice(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		auth.WriteError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET required")
		return
	}

	// Extract plan ID from path: /billing/plans/{id}/price
	path := strings.TrimPrefix(r.URL.Path, "/billing/plans/")
	path = strings.TrimSuffix(path, "/price")
	planID := strings.TrimSpace(path)
	if planID == "" || strings.Contains(planID, "/") {
		auth.WriteError(w, http.StatusBadRequest, "invalid_plan_id", "plan ID required")
		return
	}

	claims, err := auth.ValidateJWT(r)
	if err != nil {
		auth.WriteError(w, http.StatusUnauthorized, "unauthorized", "valid JWT required")
		return
	}

	var regionID sql.NullString
	_ = s.db.QueryRow(
		`SELECT region_id FROM subscribers WHERE id = $1`,
		claims.Subject,
	).Scan(&regionID)

	if !regionID.Valid {
		auth.WriteError(w, http.StatusNotFound, "no_region", "subscriber has no region set")
		return
	}

	rp, err := s.lookupRegionalPrice(planID, regionID.String)
	if err == sql.ErrNoRows {
		auth.WriteError(w, http.StatusNotFound, "price_not_found", "no regional price for this plan and region")
		return
	}
	if err != nil {
		auth.WriteError(w, http.StatusInternalServerError, "db_error", "failed to query regional price")
		return
	}

	auth.WriteJSON(w, http.StatusOK, rp)
}

// handleAdminRegionalPrices handles POST and GET /admin/billing/regional-prices.
func (s *Server) handleAdminRegionalPrices(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.listRegionalPrices(w, r)
	case http.MethodPost:
		s.upsertRegionalPrice(w, r)
	default:
		auth.WriteError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET or POST required")
	}
}

// listRegionalPrices handles GET /admin/billing/regional-prices.
func (s *Server) listRegionalPrices(w http.ResponseWriter, r *http.Request) {
	if !s.requireSuperowner(w, r) {
		return
	}

	rows, err := s.db.Query(`
		SELECT
			prp.id,
			prp.plan_id,
			sp.name        AS plan_name,
			prp.region_id,
			reg.code       AS region_code,
			reg.name       AS region_name,
			prp.currency,
			prp.monthly_price_cents,
			prp.annual_price_cents,
			prp.stripe_price_id_monthly,
			prp.stripe_price_id_annual
		FROM plan_regional_prices prp
		JOIN subscription_plans sp ON sp.id = prp.plan_id
		JOIN regions reg           ON reg.id = prp.region_id
		ORDER BY sp.name, reg.code
	`)
	if err != nil {
		auth.WriteError(w, http.StatusInternalServerError, "db_error", "failed to query regional prices")
		return
	}
	defer rows.Close()

	var prices []regionalPriceAdmin
	for rows.Next() {
		var p regionalPriceAdmin
		if err := rows.Scan(
			&p.ID, &p.PlanID, &p.PlanName, &p.RegionID, &p.RegionCode, &p.RegionName,
			&p.Currency, &p.MonthlyCents, &p.AnnualCents,
			&p.StripePriceIDMonthly, &p.StripePriceIDAnnual,
		); err != nil {
			continue
		}
		prices = append(prices, p)
	}
	if prices == nil {
		prices = []regionalPriceAdmin{}
	}

	auth.WriteJSON(w, http.StatusOK, map[string]interface{}{"regional_prices": prices})
}

// upsertRegionalPrice handles POST /admin/billing/regional-prices.
func (s *Server) upsertRegionalPrice(w http.ResponseWriter, r *http.Request) {
	if !s.requireSuperowner(w, r) {
		return
	}

	var req struct {
		PlanID               string  `json:"plan_id"`
		RegionID             string  `json:"region_id"`
		Currency             string  `json:"currency"`
		MonthlyCents         int     `json:"monthly_price_cents"`
		AnnualCents          int     `json:"annual_price_cents"`
		StripePriceIDMonthly *string `json:"stripe_price_id_monthly"`
		StripePriceIDAnnual  *string `json:"stripe_price_id_annual"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		auth.WriteError(w, http.StatusBadRequest, "invalid_json", "request body must be valid JSON")
		return
	}

	if req.PlanID == "" || req.RegionID == "" || req.Currency == "" {
		auth.WriteError(w, http.StatusBadRequest, "missing_fields", "plan_id, region_id, and currency are required")
		return
	}
	if req.MonthlyCents < 0 || req.AnnualCents < 0 {
		auth.WriteError(w, http.StatusBadRequest, "invalid_price", "prices must be non-negative")
		return
	}
	if len(req.Currency) != 3 {
		auth.WriteError(w, http.StatusBadRequest, "invalid_currency", "currency must be 3-character ISO code (e.g. usd)")
		return
	}

	var id string
	err := s.db.QueryRow(`
		INSERT INTO plan_regional_prices
			(plan_id, region_id, currency, monthly_price_cents, annual_price_cents, stripe_price_id_monthly, stripe_price_id_annual)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (plan_id, region_id)
		DO UPDATE SET
			currency             = EXCLUDED.currency,
			monthly_price_cents  = EXCLUDED.monthly_price_cents,
			annual_price_cents   = EXCLUDED.annual_price_cents,
			stripe_price_id_monthly = EXCLUDED.stripe_price_id_monthly,
			stripe_price_id_annual  = EXCLUDED.stripe_price_id_annual
		RETURNING id
	`, req.PlanID, req.RegionID, req.Currency,
		req.MonthlyCents, req.AnnualCents,
		req.StripePriceIDMonthly, req.StripePriceIDAnnual,
	).Scan(&id)

	if err != nil {
		auth.WriteError(w, http.StatusInternalServerError, "db_error", fmt.Sprintf("failed to upsert regional price: %v", err))
		return
	}

	auth.WriteJSON(w, http.StatusOK, map[string]interface{}{
		"id":      id,
		"message": "regional price saved",
	})
}

// ── Internal helpers ──────────────────────────────────────────────────────────

// lookupRegionalPrice fetches regional pricing for a given plan + region combination.
func (s *Server) lookupRegionalPrice(planID, regionID string) (*regionalPrice, error) {
	var rp regionalPrice
	err := s.db.QueryRow(`
		SELECT
			prp.plan_id,
			prp.region_id,
			reg.code,
			reg.name,
			prp.currency,
			prp.monthly_price_cents,
			prp.annual_price_cents,
			prp.stripe_price_id_monthly,
			prp.stripe_price_id_annual
		FROM plan_regional_prices prp
		JOIN regions reg ON reg.id = prp.region_id
		WHERE prp.plan_id = $1 AND prp.region_id = $2
	`, planID, regionID).Scan(
		&rp.PlanID, &rp.RegionID, &rp.RegionCode, &rp.RegionName,
		&rp.Currency, &rp.MonthlyCents, &rp.AnnualCents,
		&rp.StripePriceIDMonthly, &rp.StripePriceIDAnnual,
	)
	if err != nil {
		return nil, err
	}
	return &rp, nil
}
