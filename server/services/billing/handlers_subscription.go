// handlers_subscription.go — Subscription management endpoints.
// P3-T04: GET /billing/subscription, POST /billing/cancel
// P3-T10: POST /billing/pause, POST /billing/resume
package billing

import (
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/stripe/stripe-go/v76"
	stripeSub "github.com/stripe/stripe-go/v76/subscription"
	"github.com/unyeco/roost/internal/auth"
)

// subscriptionResponse is returned by GET /billing/subscription.
type subscriptionResponse struct {
	SubscriptionID     string     `json:"subscription_id"`
	PlanSlug           string     `json:"plan_slug"`
	PlanName           string     `json:"plan_name"`
	BillingPeriod      string     `json:"billing_period"`
	Status             string     `json:"status"`
	BillingExempt      bool       `json:"billing_exempt"`
	TrialEndsAt        *time.Time `json:"trial_ends_at,omitempty"`
	CurrentPeriodStart *time.Time `json:"current_period_start,omitempty"`
	CurrentPeriodEnd   *time.Time `json:"current_period_end,omitempty"`
	CancelAtPeriodEnd  bool       `json:"cancel_at_period_end"`
	PausedAt           *time.Time `json:"paused_at,omitempty"`
	PauseResumesAt     *time.Time `json:"pause_resumes_at,omitempty"`
}

// handleSubscription routes GET/DELETE for /billing/subscription.
func (s *Server) handleSubscription(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.getSubscription(w, r)
	default:
		auth.WriteError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET required")
	}
}

// getSubscription returns the current subscription for the authenticated subscriber.
// GET /billing/subscription
func (s *Server) getSubscription(w http.ResponseWriter, r *http.Request) {
	claims, err := auth.ValidateJWT(r)
	if err != nil {
		auth.WriteError(w, http.StatusUnauthorized, "unauthorized", "valid JWT required")
		return
	}
	subscriberID := claims.Subject

	var resp subscriptionResponse
	var billingExempt bool
	err = s.db.QueryRow(`
		SELECT
			s.id,
			COALESCE(sp.slug, ''),
			COALESCE(sp.name, ''),
			COALESCE(s.billing_period, 'monthly'),
			s.status,
			sub.billing_exempt,
			s.trial_ends_at,
			s.current_period_start,
			s.current_period_end,
			s.cancel_at_period_end,
			s.paused_at,
			s.pause_resumes_at
		FROM subscriptions s
		JOIN subscribers sub ON sub.id = s.subscriber_id
		JOIN subscription_plans sp ON sp.id = s.plan_id
		WHERE s.subscriber_id = $1
	`, subscriberID).Scan(
		&resp.SubscriptionID,
		&resp.PlanSlug,
		&resp.PlanName,
		&resp.BillingPeriod,
		&resp.Status,
		&billingExempt,
		&resp.TrialEndsAt,
		&resp.CurrentPeriodStart,
		&resp.CurrentPeriodEnd,
		&resp.CancelAtPeriodEnd,
		&resp.PausedAt,
		&resp.PauseResumesAt,
	)
	if err != nil {
		auth.WriteError(w, http.StatusNotFound, "no_subscription", "no active subscription found")
		return
	}
	resp.BillingExempt = billingExempt
	writeJSON(w, http.StatusOK, resp)
}

// handleCancel cancels the subscription at period end.
// POST /billing/cancel
func (s *Server) handleCancel(w http.ResponseWriter, r *http.Request) {
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

	var body struct {
		Reason    string `json:"reason"`
		Immediate bool   `json:"immediate"` // if true, cancel immediately; otherwise at period end
	}
	_ = json.NewDecoder(r.Body).Decode(&body)

	// Check billing_exempt — founding family cannot cancel
	var billingExempt bool
	_ = s.db.QueryRow(`SELECT billing_exempt FROM subscribers WHERE id = $1`, subscriberID).Scan(&billingExempt)
	if billingExempt {
		auth.WriteError(w, http.StatusForbidden, "founding_plan", "founding family plan cannot be canceled")
		return
	}

	var stripeSubID string
	err = s.db.QueryRow(
		`SELECT stripe_subscription_id FROM subscriptions WHERE subscriber_id = $1`, subscriberID,
	).Scan(&stripeSubID)
	if err != nil {
		auth.WriteError(w, http.StatusNotFound, "no_subscription", "no active subscription found")
		return
	}

	if body.Immediate {
		// Cancel immediately in DB
		_, err = s.db.Exec(`
			UPDATE subscriptions
			SET status = 'canceled', canceled_at = NOW(),
				cancellation_reason = $2, updated_at = NOW()
			WHERE subscriber_id = $1
		`, subscriberID, body.Reason)

		// Cancel in Stripe immediately (non-fatal if Stripe unavailable).
		if s.stripe != nil && stripeSubID != "" {
			if _, stripeErr := stripeSub.Cancel(stripeSubID, nil); stripeErr != nil {
				log.Printf("handleCancel: Stripe immediate cancel failed for %s: %v", subscriberID, stripeErr)
			}
		}
	} else {
		// Cancel at period end
		_, err = s.db.Exec(`
			UPDATE subscriptions
			SET cancel_at_period_end = true,
				cancellation_reason = $2, updated_at = NOW()
			WHERE subscriber_id = $1
		`, subscriberID, body.Reason)

		// Set cancel_at_period_end in Stripe (non-fatal if Stripe unavailable).
		if s.stripe != nil && stripeSubID != "" {
			params := &stripe.SubscriptionParams{CancelAtPeriodEnd: stripe.Bool(true)}
			if _, stripeErr := stripeSub.Update(stripeSubID, params); stripeErr != nil {
				log.Printf("handleCancel: Stripe period-end cancel failed for %s: %v", subscriberID, stripeErr)
			}
		}
	}

	if err != nil {
		auth.WriteError(w, http.StatusInternalServerError, "db_error", "failed to update subscription")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status":  "canceled",
		"message": "Subscription will be canceled at the end of the current billing period",
	})
}

// handlePause pauses the subscription (P3-T10).
// POST /billing/pause
func (s *Server) handlePause(w http.ResponseWriter, r *http.Request) {
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

	var body struct {
		ResumeDate string `json:"resume_date"` // optional ISO8601 date
	}
	_ = json.NewDecoder(r.Body).Decode(&body)

	var pauseResumesAt *time.Time
	if body.ResumeDate != "" {
		t, err := time.Parse("2006-01-02", body.ResumeDate)
		if err == nil {
			pauseResumesAt = &t
		}
	}

	// Fetch Stripe subscription ID before DB update (needed for Stripe API call).
	var pauseStripeSubID string
	_ = s.db.QueryRow(
		`SELECT stripe_subscription_id FROM subscriptions WHERE subscriber_id = $1 AND status = 'active'`,
		subscriberID,
	).Scan(&pauseStripeSubID)

	_, err = s.db.Exec(`
		UPDATE subscriptions
		SET status = 'paused',
			paused_at = NOW(),
			pause_resumes_at = $2,
			updated_at = NOW()
		WHERE subscriber_id = $1 AND status = 'active'
	`, subscriberID, pauseResumesAt)
	if err != nil {
		auth.WriteError(w, http.StatusInternalServerError, "db_error", "failed to pause subscription")
		return
	}

	// Suspend API token during pause
	if err := s.suspendAPIToken(subscriberID); err != nil {
		_ = err
	}

	// Pause collection in Stripe — mark invoices as uncollectible while paused.
	if s.stripe != nil && pauseStripeSubID != "" {
		params := &stripe.SubscriptionParams{
			PauseCollection: &stripe.SubscriptionPauseCollectionParams{
				Behavior: stripe.String("mark_uncollectible"),
			},
		}
		if _, stripeErr := stripeSub.Update(pauseStripeSubID, params); stripeErr != nil {
			log.Printf("handlePause: Stripe pause failed for %s: %v", subscriberID, stripeErr)
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status":  "paused",
		"message": "Subscription paused. API token suspended until resumed.",
	})
}

// handleResume resumes a paused subscription (P3-T10).
// POST /billing/resume
func (s *Server) handleResume(w http.ResponseWriter, r *http.Request) {
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

	result, err := s.db.Exec(`
		UPDATE subscriptions
		SET status = 'active',
			resumed_at = NOW(),
			paused_at = NULL,
			pause_resumes_at = NULL,
			updated_at = NOW()
		WHERE subscriber_id = $1 AND status = 'paused'
	`, subscriberID)
	if err != nil {
		auth.WriteError(w, http.StatusInternalServerError, "db_error", "failed to resume subscription")
		return
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		auth.WriteError(w, http.StatusBadRequest, "not_paused", "subscription is not currently paused")
		return
	}

	// Re-activate API token
	if err := s.activateAPIToken(subscriberID); err != nil {
		_ = err
	}

	// Remove pause_collection in Stripe to resume normal billing.
	var resumeStripeSubID string
	_ = s.db.QueryRow(
		`SELECT stripe_subscription_id FROM subscriptions WHERE subscriber_id = $1`,
		subscriberID,
	).Scan(&resumeStripeSubID)
	if s.stripe != nil && resumeStripeSubID != "" {
		params := &stripe.SubscriptionParams{}
		params.AddExtra("pause_collection", "")
		if _, stripeErr := stripeSub.Update(resumeStripeSubID, params); stripeErr != nil {
			log.Printf("handleResume: Stripe resume failed for %s: %v", subscriberID, stripeErr)
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status":  "active",
		"message": "Subscription resumed. API token reactivated.",
	})
}

// handleSetupStripe creates Stripe products and prices (admin-only, P3-T01).
// POST /billing/admin/setup-stripe
func (s *Server) handleSetupStripe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		auth.WriteError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST required")
		return
	}

	// Admin auth: require superowner JWT
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

	if s.stripeRequired(w) {
		return
	}

	// TODO (P3-T01): Requires STRIPE_SECRET_KEY in environment.
	// Plans, prices, and Stripe product IDs will be created here.
	plans, err := s.stripe.CreateProducts()
	if err != nil {
		auth.WriteError(w, http.StatusInternalServerError, "stripe_error", "failed to create Stripe products: "+err.Error())
		return
	}

	// Update subscription_plans table with Stripe IDs
	for slug, pp := range plans {
		_, _ = s.db.Exec(`
			UPDATE subscription_plans
			SET stripe_product_id = $1,
				stripe_price_id_monthly = $2,
				stripe_price_id_annual = $3
			WHERE slug = $4
		`, pp.ProductID, pp.PriceIDMonthly, pp.PriceIDAnnual, slug)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ok",
		"plans":  plans,
	})
}
