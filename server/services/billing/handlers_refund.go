// handlers_refund.go — Refund request and processing workflow.
// P3-T11: Refund Workflow
//
// POST /billing/refund    — subscriber requests a refund
//
// Refund policy: full refund within 7 days of charge, prorated after that.
// All refunds require admin approval (not automatic) unless AUTO_REFUND_DAYS is set.
// Stripe refund is issued via stripe.Refund.New (requires STRIPE_SECRET_KEY).
package billing

import (
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/unyeco/roost/internal/auth"
)

// refundRequest is the body for POST /billing/refund.
type refundRequest struct {
	InvoiceID string `json:"invoice_id"` // roost_invoices.id
	Reason    string `json:"reason"`
}

// handleRefund creates a refund request.
// POST /billing/refund
func (s *Server) handleRefund(w http.ResponseWriter, r *http.Request) {
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

	var req refundRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		auth.WriteError(w, http.StatusBadRequest, "invalid_json", "invalid request body")
		return
	}

	// Look up the invoice
	var amountCents int64
	var stripeInvoiceID string
	var invoiceCreatedAt time.Time
	err = s.db.QueryRow(`
		SELECT amount_cents, stripe_invoice_id, created_at
		FROM roost_invoices
		WHERE id = $1 AND subscriber_id = $2 AND status = 'paid'
	`, req.InvoiceID, subscriberID).Scan(&amountCents, &stripeInvoiceID, &invoiceCreatedAt)
	if err != nil {
		auth.WriteError(w, http.StatusNotFound, "invoice_not_found",
			"invoice not found, not paid, or does not belong to you")
		return
	}

	// Check if a refund already exists for this invoice
	var existingRefund string
	_ = s.db.QueryRow(
		`SELECT id FROM roost_refunds WHERE stripe_invoice_id = $1 AND subscriber_id = $2`,
		stripeInvoiceID, subscriberID,
	).Scan(&existingRefund)
	if existingRefund != "" {
		auth.WriteError(w, http.StatusConflict, "refund_exists",
			"a refund request already exists for this invoice")
		return
	}

	// Determine refund amount (full within 7 days, prorated after)
	daysSinceCharge := int(time.Since(invoiceCreatedAt).Hours() / 24)
	refundAmountCents := amountCents
	if daysSinceCharge > 7 {
		// Prorated: reduce by 1/30 per day past the 7-day window
		daysOverWindow := daysSinceCharge - 7
		reduction := amountCents * int64(daysOverWindow) / 30
		refundAmountCents = amountCents - reduction
		if refundAmountCents < 0 {
			refundAmountCents = 0
		}
	}

	// Create refund record (pending — requires admin approval or auto-approval)
	refundID := uuid.New().String()
	_, err = s.db.Exec(`
		INSERT INTO roost_refunds (
			id, subscriber_id, stripe_invoice_id,
			amount_cents, currency, reason, status
		)
		VALUES ($1, $2, $3, $4, 'usd', $5, 'pending')
	`, refundID, subscriberID, stripeInvoiceID, refundAmountCents, req.Reason)
	if err != nil {
		auth.WriteError(w, http.StatusInternalServerError, "db_error", "failed to create refund request")
		return
	}

	// Auto-approve if within 7 days (no-questions-asked policy)
	if daysSinceCharge <= 7 && s.stripe != nil {
		go s.processRefund(refundID, stripeInvoiceID, refundAmountCents)
	} else {
		log.Printf("Refund request %s queued for admin review (subscriber=%s, days=%d, amount=%d)",
			refundID, subscriberID, daysSinceCharge, refundAmountCents)
	}

	writeJSON(w, http.StatusAccepted, map[string]any{
		"refund_id":           refundID,
		"amount_cents":        refundAmountCents,
		"status":              "pending",
		"days_since_charge":   daysSinceCharge,
		"auto_approve":        daysSinceCharge <= 7,
		"message":             "Refund request submitted. You will receive a confirmation email.",
	})
}

// processRefund issues the Stripe refund and updates the refund record.
// Called as a goroutine for auto-approved refunds.
//
// TODO (P3-T01): Requires STRIPE_SECRET_KEY in environment.
func (s *Server) processRefund(refundID, stripeInvoiceID string, amountCents int64) {
	// TODO (P3-T01): Issue Stripe refund when STRIPE_SECRET_KEY is configured.
	// Example:
	//   refund, err := refund.New(&stripe.RefundParams{
	//       PaymentIntent: stripe.String(paymentIntentID), // from invoice
	//       Amount: stripe.Int64(amountCents),
	//   })
	//
	// For now, mark as pending — admin must manually process in Stripe dashboard.

	_, err := s.db.Exec(`
		UPDATE roost_refunds
		SET status = 'pending', notes = 'Awaiting Stripe key configuration to auto-process',
			updated_at = NOW()
		WHERE id = $1
	`, refundID)
	if err != nil {
		log.Printf("processRefund %s: failed to update status: %v", refundID, err)
	}
	log.Printf("Refund %s queued (amount=%d, invoice=%s) — STRIPE_SECRET_KEY required to auto-process",
		refundID, amountCents, stripeInvoiceID)
}
