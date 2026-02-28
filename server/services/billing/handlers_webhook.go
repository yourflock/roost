// handlers_webhook.go — Stripe webhook event handler.
// P3-T03: Stripe Webhook Handler
//
// POST /billing/webhook
//   Receives and verifies signed Stripe webhook events.
//   Handles all subscription lifecycle events:
//   - checkout.session.completed    → activate subscription + API token
//   - invoice.payment_succeeded     → renew subscription, reset dunning counter
//   - invoice.payment_failed        → increment dunning counter, send grace period email
//   - customer.subscription.deleted → deactivate subscription + API token
//   - customer.subscription.updated → update plan, handle paused/resumed state
//   - invoice.created               → store invoice record for PDF generation
//
// Stripe signature verified via STRIPE_WEBHOOK_SECRET (separate from API key).
package billing

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/stripe/stripe-go/v76"
	"github.com/stripe/stripe-go/v76/webhook"
	"github.com/unyeco/roost/internal/auth"
	"github.com/unyeco/roost/internal/email"
)

// handleWebhook processes incoming Stripe webhook events.
// POST /billing/webhook
func (s *Server) handleWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		auth.WriteError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST required")
		return
	}

	// Read body (max 65KB — Stripe events are always small)
	const maxBytes = 65536
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		auth.WriteError(w, http.StatusRequestEntityTooLarge, "body_too_large", "request body too large")
		return
	}

	// Verify Stripe signature.
	webhookSecret := os.Getenv("STRIPE_WEBHOOK_SECRET")
	if webhookSecret == "" {
		log.Printf("WARNING: STRIPE_WEBHOOK_SECRET not set — skipping signature verification (dev only)")
	}

	var event stripe.Event
	if webhookSecret != "" {
		sigHeader := r.Header.Get("Stripe-Signature")
		event, err = webhook.ConstructEvent(body, sigHeader, webhookSecret)
		if err != nil {
			log.Printf("Webhook signature verification failed: %v", err)
			auth.WriteError(w, http.StatusBadRequest, "invalid_signature", "webhook signature verification failed")
			return
		}
	} else {
		if err := json.Unmarshal(body, &event); err != nil {
			auth.WriteError(w, http.StatusBadRequest, "invalid_json", "failed to parse webhook body")
			return
		}
	}

	log.Printf("Stripe webhook received: type=%s id=%s", event.Type, event.ID)

	// Idempotency check — skip already-processed events
	if s.isEventProcessed(event.ID) {
		log.Printf("Webhook event %s already processed — skipping", event.ID)
		w.WriteHeader(http.StatusOK)
		return
	}

	// Dispatch to event-specific handler
	var handlerErr error
	switch event.Type {
	case "checkout.session.completed":
		handlerErr = s.onCheckoutComplete(event)
	case "invoice.payment_succeeded":
		handlerErr = s.onPaymentSucceeded(event)
	case "invoice.payment_failed":
		handlerErr = s.onPaymentFailed(event)
	case "customer.subscription.deleted":
		handlerErr = s.onSubscriptionDeleted(event)
	case "customer.subscription.updated":
		handlerErr = s.onSubscriptionUpdated(event)
	case "invoice.created":
		handlerErr = s.onInvoiceCreated(event)
	default:
		log.Printf("Unhandled Stripe event type: %s", event.Type)
	}

	if handlerErr != nil {
		log.Printf("Error processing webhook event %s (%s): %v", event.ID, event.Type, handlerErr)
		// Return 200 anyway so Stripe doesn't retry transient errors indefinitely.
		// For critical failures the dunning logic will self-heal on next payment attempt.
		w.WriteHeader(http.StatusOK)
		return
	}

	// Mark event as processed (idempotency)
	_ = s.markEventProcessed(event.ID)
	w.WriteHeader(http.StatusOK)
}

// onCheckoutComplete activates a subscription after successful Stripe Checkout.
// Fired by: checkout.session.completed
func (s *Server) onCheckoutComplete(event stripe.Event) error {
	var sess stripe.CheckoutSession
	if err := json.Unmarshal(event.Data.Raw, &sess); err != nil {
		return fmt.Errorf("unmarshal checkout.session: %w", err)
	}

	subscriberID := sess.ClientReferenceID
	if subscriberID == "" {
		// Fall back to metadata
		if sess.Metadata != nil {
			subscriberID = sess.Metadata["roost_subscriber_id"]
		}
	}
	if subscriberID == "" {
		return fmt.Errorf("checkout.session.completed: no roost_subscriber_id found")
	}

	planSlug := ""
	billingPeriod := "monthly"
	if sess.Metadata != nil {
		planSlug = sess.Metadata["roost_plan"]
		if sess.Metadata["billing_period"] != "" {
			billingPeriod = sess.Metadata["billing_period"]
		}
	}

	stripeSubscriptionID := ""
	if sess.Subscription != nil {
		stripeSubscriptionID = sess.Subscription.ID
	}

	// Calculate trial end / subscription start
	trialEnd := sql.NullTime{}
	if sess.Subscription != nil && sess.Subscription.TrialEnd > 0 {
		trialEnd = sql.NullTime{Time: time.Unix(sess.Subscription.TrialEnd, 0), Valid: true}
	}

	// Activate subscription in DB
	_, err := s.db.Exec(`
		INSERT INTO subscriptions (
			subscriber_id, plan_slug, billing_period, status,
			stripe_subscription_id, stripe_customer_id,
			trial_ends_at, current_period_start, current_period_end
		)
		SELECT
			$1, sp.id, $3, 'active',
			$4, $5,
			$6, NOW(), NOW() + INTERVAL '1 month'
		FROM subscription_plans sp WHERE sp.slug = $2
		ON CONFLICT (subscriber_id) DO UPDATE SET
			plan_slug = EXCLUDED.plan_slug,
			billing_period = EXCLUDED.billing_period,
			status = 'active',
			stripe_subscription_id = EXCLUDED.stripe_subscription_id,
			stripe_customer_id = EXCLUDED.stripe_customer_id,
			trial_ends_at = EXCLUDED.trial_ends_at,
			updated_at = NOW()
	`, subscriberID, planSlug, billingPeriod, stripeSubscriptionID,
		sess.Customer.ID, trialEnd.Time)
	if err != nil {
		return fmt.Errorf("activate subscription: %w", err)
	}

	// Activate or create API token for this subscriber (P3-T05)
	if err := s.activateAPIToken(subscriberID); err != nil {
		log.Printf("WARNING: failed to activate API token for %s: %v", subscriberID, err)
	}

	// Update subscriber status to 'active'
	_, _ = s.db.Exec(`UPDATE subscribers SET status = 'active' WHERE id = $1`, subscriberID)

	log.Printf("Subscription activated: subscriber=%s plan=%s period=%s", subscriberID, planSlug, billingPeriod)
	return nil
}

// onPaymentSucceeded handles successful recurring payment.
// Fired by: invoice.payment_succeeded
func (s *Server) onPaymentSucceeded(event stripe.Event) error {
	var inv stripe.Invoice
	if err := json.Unmarshal(event.Data.Raw, &inv); err != nil {
		return fmt.Errorf("unmarshal invoice: %w", err)
	}
	if inv.Subscription == nil {
		return nil // Not a subscription invoice
	}

	// Reset dunning counter, mark subscription active, update period
	_, err := s.db.Exec(`
		UPDATE subscriptions
		SET status = 'active',
			dunning_count = 0,
			dunning_next_retry_at = NULL,
			current_period_start = to_timestamp($2),
			current_period_end = to_timestamp($3),
			updated_at = NOW()
		WHERE stripe_subscription_id = $1
	`, inv.Subscription.ID,
		inv.PeriodStart, inv.PeriodEnd)
	if err != nil {
		return fmt.Errorf("reset dunning: %w", err)
	}

	// Re-activate API token if it was suspended during dunning
	if inv.Subscription.Metadata != nil {
		if subID := inv.Subscription.Metadata["roost_subscriber_id"]; subID != "" {
			_ = s.activateAPIToken(subID)
		}
	}

	// Store invoice record (P3-T09)
	_ = s.storeInvoice(inv)
	log.Printf("Payment succeeded: subscription=%s period=%d-%d", inv.Subscription.ID, inv.PeriodStart, inv.PeriodEnd)
	return nil
}

// onPaymentFailed handles a failed payment attempt — initiates dunning (P3-T08).
// Fired by: invoice.payment_failed
func (s *Server) onPaymentFailed(event stripe.Event) error {
	var inv stripe.Invoice
	if err := json.Unmarshal(event.Data.Raw, &inv); err != nil {
		return fmt.Errorf("unmarshal invoice: %w", err)
	}
	if inv.Subscription == nil {
		return nil
	}

	// Look up subscriber and dunning state
	var subscriberID string
	var dunningCount int
	err := s.db.QueryRow(`
		SELECT sub.id, COALESCE(s.dunning_count, 0)
		FROM subscriptions s
		JOIN subscribers sub ON sub.id = s.subscriber_id
		WHERE s.stripe_subscription_id = $1
	`, inv.Subscription.ID).Scan(&subscriberID, &dunningCount)
	if err != nil {
		return fmt.Errorf("lookup subscription for dunning: %w", err)
	}

	dunningCount++
	nextRetry := time.Now().AddDate(0, 0, dunningRetryDays(dunningCount))

	// After 3 failures, suspend API token but keep subscription record (P3-T08)
	status := "past_due"
	if dunningCount >= 3 {
		status = "suspended"
		if err := s.suspendAPIToken(subscriberID); err != nil {
			log.Printf("WARNING: failed to suspend API token for %s: %v", subscriberID, err)
		}
	}

	_, _ = s.db.Exec(`
		UPDATE subscriptions
		SET status = $2, dunning_count = $3, dunning_next_retry_at = $4, updated_at = NOW()
		WHERE stripe_subscription_id = $1
	`, inv.Subscription.ID, status, dunningCount, nextRetry)

	// Send dunning email (P3-T08)
	go s.sendDunningEmail(subscriberID, dunningCount, inv.HostedInvoiceURL)

	log.Printf("Payment failed: subscription=%s attempt=%d status=%s", inv.Subscription.ID, dunningCount, status)
	return nil
}

// onSubscriptionDeleted handles subscription cancellation/expiry.
// Fired by: customer.subscription.deleted
func (s *Server) onSubscriptionDeleted(event stripe.Event) error {
	var sub stripe.Subscription
	if err := json.Unmarshal(event.Data.Raw, &sub); err != nil {
		return fmt.Errorf("unmarshal subscription: %w", err)
	}

	var subscriberID string
	err := s.db.QueryRow(`
		SELECT subscriber_id FROM subscriptions WHERE stripe_subscription_id = $1
	`, sub.ID).Scan(&subscriberID)
	if err != nil {
		return fmt.Errorf("lookup subscription: %w", err)
	}

	// Mark subscription canceled
	_, err = s.db.Exec(`
		UPDATE subscriptions
		SET status = 'canceled', canceled_at = NOW(), updated_at = NOW()
		WHERE stripe_subscription_id = $1
	`, sub.ID)
	if err != nil {
		return err
	}

	// Revoke API token
	if err := s.revokeAPIToken(subscriberID); err != nil {
		log.Printf("WARNING: failed to revoke API token for %s: %v", subscriberID, err)
	}

	// Update subscriber status
	_, _ = s.db.Exec(`UPDATE subscribers SET status = 'inactive' WHERE id = $1`, subscriberID)
	log.Printf("Subscription deleted: %s subscriber=%s", sub.ID, subscriberID)
	return nil
}

// onSubscriptionUpdated handles plan changes and pause/resume state.
// Fired by: customer.subscription.updated
func (s *Server) onSubscriptionUpdated(event stripe.Event) error {
	var sub stripe.Subscription
	if err := json.Unmarshal(event.Data.Raw, &sub); err != nil {
		return fmt.Errorf("unmarshal subscription: %w", err)
	}

	// Map Stripe pause state (P3-T10)
	status := "active"
	switch sub.Status {
	case stripe.SubscriptionStatusPaused:
		status = "paused"
	case stripe.SubscriptionStatusCanceled:
		status = "canceled"
	case stripe.SubscriptionStatusPastDue:
		status = "past_due"
	case stripe.SubscriptionStatusUnpaid:
		status = "suspended"
	}

	_, err := s.db.Exec(`
		UPDATE subscriptions
		SET status = $2,
			current_period_start = to_timestamp($3),
			current_period_end = to_timestamp($4),
			updated_at = NOW()
		WHERE stripe_subscription_id = $1
	`, sub.ID, status, sub.CurrentPeriodStart, sub.CurrentPeriodEnd)
	return err
}

// onInvoiceCreated stores a new invoice record (P3-T09).
// Fired by: invoice.created
func (s *Server) onInvoiceCreated(event stripe.Event) error {
	var inv stripe.Invoice
	if err := json.Unmarshal(event.Data.Raw, &inv); err != nil {
		return fmt.Errorf("unmarshal invoice: %w", err)
	}
	return s.storeInvoice(inv)
}

// storeInvoice saves an invoice to the roost_invoices table.
func (s *Server) storeInvoice(inv stripe.Invoice) error {
	if inv.ID == "" {
		return nil
	}
	var subscriberID string
	if inv.Subscription != nil {
		_ = s.db.QueryRow(
			`SELECT subscriber_id FROM subscriptions WHERE stripe_subscription_id = $1`, inv.Subscription.ID,
		).Scan(&subscriberID)
	}
	_, err := s.db.Exec(`
		INSERT INTO roost_invoices (
			stripe_invoice_id, subscriber_id, stripe_subscription_id,
			amount_cents, currency, status,
			period_start, period_end, hosted_invoice_url, invoice_pdf_url
		) VALUES ($1, $2, $3, $4, $5, $6, to_timestamp($7), to_timestamp($8), $9, $10)
		ON CONFLICT (stripe_invoice_id) DO UPDATE SET
			status = EXCLUDED.status,
			hosted_invoice_url = EXCLUDED.hosted_invoice_url,
			invoice_pdf_url = EXCLUDED.invoice_pdf_url,
			updated_at = NOW()
	`, inv.ID, subscriberID, func() string {
		if inv.Subscription != nil {
			return inv.Subscription.ID
		}
		return ""
	}(),
		inv.AmountPaid, string(inv.Currency), string(inv.Status),
		inv.PeriodStart, inv.PeriodEnd,
		inv.HostedInvoiceURL, inv.InvoicePDF)
	return err
}

// dunningRetryDays returns the number of days to wait before the next retry.
// Attempt 1: retry after 3 days, Attempt 2: 7 days, Attempt 3+: 14 days.
func dunningRetryDays(attempt int) int {
	switch attempt {
	case 1:
		return 3
	case 2:
		return 7
	default:
		return 14
	}
}

// isEventProcessed checks if a Stripe event ID has already been handled (idempotency).
func (s *Server) isEventProcessed(eventID string) bool {
	var exists bool
	_ = s.db.QueryRow(
		`SELECT EXISTS(SELECT 1 FROM stripe_events WHERE stripe_event_id = $1)`, eventID,
	).Scan(&exists)
	return exists
}

// markEventProcessed records a processed Stripe event ID.
func (s *Server) markEventProcessed(eventID string) error {
	_, err := s.db.Exec(
		`INSERT INTO stripe_events (stripe_event_id) VALUES ($1) ON CONFLICT DO NOTHING`, eventID,
	)
	return err
}

// sendDunningEmail sends a payment failure notification to the subscriber (P3-T08).
func (s *Server) sendDunningEmail(subscriberID string, attempt int, invoiceURL string) {
	var emailAddr, displayName string
	if err := s.db.QueryRow(
		`SELECT email, display_name FROM subscribers WHERE id = $1`, subscriberID,
	).Scan(&emailAddr, &displayName); err != nil {
		log.Printf("sendDunningEmail: failed to look up subscriber %s: %v", subscriberID, err)
		return
	}

	if err := email.SendDunningEmail(emailAddr, displayName, attempt, invoiceURL); err != nil {
		log.Printf("sendDunningEmail: failed to send to %s: %v", emailAddr, err)
	}
}
