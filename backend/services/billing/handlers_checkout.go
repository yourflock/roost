// handlers_checkout.go — Stripe Checkout session creation.
// P3-T02: Stripe Checkout Flow
//
// POST /billing/checkout
//   Creates a Stripe Checkout session for a subscriber choosing a plan.
//   Requires a valid JWT (subscriber must be authenticated).
//   Returns: { checkout_url: "https://checkout.stripe.com/..." }
//
// The subscriber is redirected to Stripe-hosted checkout. On success,
// Stripe fires a checkout.session.completed webhook (P3-T03) which
// activates the subscription and API token.
//
// TODO (P3-T01): Requires STRIPE_SECRET_KEY in environment.
package billing

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/stripe/stripe-go/v76"
	"github.com/stripe/stripe-go/v76/checkout/session"
	"github.com/yourflock/roost/internal/auth"
)

// checkoutRequest is the JSON body for POST /billing/checkout.
type checkoutRequest struct {
	PlanSlug      string `json:"plan_slug"`      // "basic", "premium", "family"
	BillingPeriod string `json:"billing_period"` // "monthly" or "annual"
	PromoCode     string `json:"promo_code"`     // optional
}

// checkoutResponse is returned on successful session creation.
type checkoutResponse struct {
	CheckoutURL string `json:"checkout_url"`
	SessionID   string `json:"session_id"`
}

// handleCheckout creates a Stripe Checkout session.
// POST /billing/checkout
func (s *Server) handleCheckout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		auth.WriteError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST required")
		return
	}
	if s.stripeRequired(w) {
		return
	}

	// Authenticate subscriber via JWT
	claims, err := auth.ValidateJWT(r)
	if err != nil {
		auth.WriteError(w, http.StatusUnauthorized, "unauthorized", "valid JWT required")
		return
	}
	subscriberID := claims.Subject

	var req checkoutRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		auth.WriteError(w, http.StatusBadRequest, "invalid_json", "request body must be valid JSON")
		return
	}

	// Validate plan_slug
	req.PlanSlug = strings.ToLower(strings.TrimSpace(req.PlanSlug))
	validPlans := map[string]bool{"basic": true, "premium": true, "family": true}
	if !validPlans[req.PlanSlug] {
		auth.WriteError(w, http.StatusBadRequest, "invalid_plan", "plan_slug must be basic, premium, or family")
		return
	}

	// Validate billing_period
	req.BillingPeriod = strings.ToLower(strings.TrimSpace(req.BillingPeriod))
	if req.BillingPeriod != "monthly" && req.BillingPeriod != "annual" {
		auth.WriteError(w, http.StatusBadRequest, "invalid_billing_period", "billing_period must be monthly or annual")
		return
	}

	// Look up the Stripe price ID for this plan from subscription_plans table
	priceID, err := s.getPriceID(req.PlanSlug, req.BillingPeriod)
	if err != nil {
		auth.WriteError(w, http.StatusBadRequest, "plan_not_configured",
			"Stripe prices not yet configured for this plan — run /billing/admin/setup-stripe first")
		return
	}

	// Look up subscriber's Stripe customer ID (or create one)
	customerID, err := s.getOrCreateStripeCustomer(subscriberID)
	if err != nil {
		auth.WriteError(w, http.StatusInternalServerError, "stripe_error", "failed to resolve Stripe customer")
		return
	}

	// Build Checkout session params
	baseURL := getEnv("ROOST_BASE_URL", "https://roost.yourflock.com")
	params := &stripe.CheckoutSessionParams{
		Customer: stripe.String(customerID),
		LineItems: []*stripe.CheckoutSessionLineItemParams{
			{
				Price:    stripe.String(priceID),
				Quantity: stripe.Int64(1),
			},
		},
		Mode:              stripe.String(string(stripe.CheckoutSessionModeSubscription)),
		SuccessURL:        stripe.String(baseURL + "/subscribe/success?session_id={CHECKOUT_SESSION_ID}"),
		CancelURL:         stripe.String(baseURL + "/subscribe/cancel"),
		ClientReferenceID: stripe.String(subscriberID),
		SubscriptionData: &stripe.CheckoutSessionSubscriptionDataParams{
			Metadata: map[string]string{
				"roost_subscriber_id": subscriberID,
				"roost_plan":          req.PlanSlug,
				"billing_period":      req.BillingPeriod,
			},
			TrialPeriodDays: stripe.Int64(7), // P3-T06: 7-day free trial
		},
	}

	// Apply promo code if provided (P3-T07)
	if req.PromoCode != "" {
		params.AllowPromotionCodes = stripe.Bool(false)
		params.Discounts = []*stripe.CheckoutSessionDiscountParams{
			{PromotionCode: stripe.String(req.PromoCode)},
		}
	} else {
		params.AllowPromotionCodes = stripe.Bool(true)
	}

	// TODO (P3-T01): This call requires STRIPE_SECRET_KEY in environment.
	sess, err := session.New(params)
	if err != nil {
		auth.WriteError(w, http.StatusInternalServerError, "stripe_checkout_failed",
			"Failed to create checkout session: "+err.Error())
		return
	}

	// Record pending checkout in DB
	if err := s.recordPendingCheckout(subscriberID, sess.ID, req.PlanSlug, req.BillingPeriod); err != nil {
		// Non-fatal: log and continue (webhook will sync state regardless)
		_ = err
	}

	writeJSON(w, http.StatusOK, checkoutResponse{
		CheckoutURL: sess.URL,
		SessionID:   sess.ID,
	})
}

// getPriceID retrieves the Stripe price ID for a plan/period from the DB.
func (s *Server) getPriceID(planSlug, billingPeriod string) (string, error) {
	var priceID string
	var query string
	if billingPeriod == "monthly" {
		query = `SELECT stripe_price_id_monthly FROM subscription_plans WHERE slug = $1 AND stripe_price_id_monthly IS NOT NULL`
	} else {
		query = `SELECT stripe_price_id_annual FROM subscription_plans WHERE slug = $1 AND stripe_price_id_annual IS NOT NULL`
	}
	err := s.db.QueryRow(query, planSlug).Scan(&priceID)
	return priceID, err
}

// getOrCreateStripeCustomer returns existing Stripe customer ID for the subscriber,
// or creates a new one and stores it.
func (s *Server) getOrCreateStripeCustomer(subscriberID string) (string, error) {
	var customerID string
	err := s.db.QueryRow(
		`SELECT stripe_customer_id FROM subscribers WHERE id = $1`,
		subscriberID,
	).Scan(&customerID)
	if err == nil && customerID != "" {
		return customerID, nil
	}

	// Get subscriber email for customer creation
	var email, displayName string
	if err := s.db.QueryRow(
		`SELECT email, display_name FROM subscribers WHERE id = $1`, subscriberID,
	).Scan(&email, &displayName); err != nil {
		return "", err
	}

	// TODO (P3-T01): Requires STRIPE_SECRET_KEY
	cust, err := s.stripe.CreateCustomer(email, displayName, subscriberID)
	if err != nil {
		return "", err
	}

	// Store customer ID
	_, err = s.db.Exec(
		`UPDATE subscribers SET stripe_customer_id = $1 WHERE id = $2`,
		cust, subscriberID,
	)
	return cust, err
}

// recordPendingCheckout inserts a pending_checkouts row so we can correlate webhook events.
func (s *Server) recordPendingCheckout(subscriberID, sessionID, planSlug, billingPeriod string) error {
	_, err := s.db.Exec(`
		INSERT INTO pending_checkouts (subscriber_id, stripe_session_id, plan_slug, billing_period)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (stripe_session_id) DO NOTHING
	`, subscriberID, sessionID, planSlug, billingPeriod)
	return err
}

// writeJSON writes a JSON response with the given status code.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
