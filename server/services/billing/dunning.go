// dunning.go — Payment grace period and dunning retry management.
// P3-T08: Payment Grace Period & Dunning
//
// Dunning schedule: Attempt 1 → retry in 3 days, Attempt 2 → 7 days, Attempt 3 → 14 days.
// After 3 failures: suspend API token, set status = 'suspended'.
// On success: reset dunning_count, set status = 'active', re-activate token.
//
// This file provides:
//   - RunDunningCheck: background job to retry dunning for past_due subscriptions
//   - Trial expiry check: downgrade trialing subscriptions whose trial has ended
//   - Both are called by a daily cron job (triggered via POST /billing/admin/dunning-check)
package billing

import (
	"database/sql"
	"log"
	"net/http"
	"time"

	stripeInvoice "github.com/stripe/stripe-go/v76/invoice"
	stripeSub "github.com/stripe/stripe-go/v76/subscription"
	"github.com/yourflock/roost/internal/auth"
)

// DunningResult summarizes the outcome of a dunning check run.
type DunningResult struct {
	Checked       int `json:"checked"`
	Retried       int `json:"retried"`
	Suspended     int `json:"suspended"`
	TrialsExpired int `json:"trials_expired"`
	Errors        int `json:"errors"`
}

// handleDunningCheck runs the dunning check (admin/cron only).
// POST /billing/admin/dunning-check
func (s *Server) handleDunningCheck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		auth.WriteError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST required")
		return
	}

	// Cron auth: shared secret or superowner JWT
	cronKey := r.Header.Get("X-Cron-Key")
	expectedKey := getEnv("BILLING_CRON_KEY", "")
	if cronKey != expectedKey || expectedKey == "" {
		claims, err := auth.ValidateJWT(r)
		if err != nil {
			auth.WriteError(w, http.StatusUnauthorized, "unauthorized", "X-Cron-Key or JWT required")
			return
		}
		var isSuperowner bool
		_ = s.db.QueryRow(`SELECT is_superowner FROM subscribers WHERE id = $1`, claims.Subject).Scan(&isSuperowner)
		if !isSuperowner {
			auth.WriteError(w, http.StatusForbidden, "forbidden", "superowner access required")
			return
		}
	}

	result := s.RunDunningCheck()
	writeJSON(w, http.StatusOK, result)
}

// RunDunningCheck processes all subscriptions that need dunning action.
// Called by the admin endpoint and (in production) by a daily cron.
func (s *Server) RunDunningCheck() DunningResult {
	result := DunningResult{}

	// 1. Find past_due subscriptions whose retry time has arrived
	rows, err := s.db.Query(`
		SELECT s.id, s.subscriber_id, s.dunning_count, s.stripe_subscription_id
		FROM subscriptions s
		WHERE s.status = 'past_due'
		  AND (s.dunning_next_retry_at IS NULL OR s.dunning_next_retry_at <= NOW())
	`)
	if err != nil {
		log.Printf("RunDunningCheck: query error: %v", err)
		result.Errors++
		return result
	}
	defer rows.Close()

	type dunningRow struct {
		ID             string
		SubscriberID   string
		DunningCount   int
		StripeSubID    sql.NullString
	}

	var pastDue []dunningRow
	for rows.Next() {
		var d dunningRow
		if err := rows.Scan(&d.ID, &d.SubscriberID, &d.DunningCount, &d.StripeSubID); err != nil {
			result.Errors++
			continue
		}
		pastDue = append(pastDue, d)
	}
	result.Checked = len(pastDue)

	for _, d := range pastDue {
		d.DunningCount++
		if d.DunningCount >= 3 {
			// Final suspension
			_, _ = s.db.Exec(`
				UPDATE subscriptions
				SET status = 'suspended', dunning_count = $2, updated_at = NOW()
				WHERE id = $1
			`, d.ID, d.DunningCount)
			if err := s.suspendAPIToken(d.SubscriberID); err != nil {
				log.Printf("RunDunningCheck: suspendAPIToken %s: %v", d.SubscriberID, err)
			}
			// Cancel Stripe subscription after final failure (non-fatal).
			if s.stripe != nil && d.StripeSubID.Valid && d.StripeSubID.String != "" {
				if _, stripeErr := stripeSub.Cancel(d.StripeSubID.String, nil); stripeErr != nil {
					log.Printf("RunDunningCheck: Stripe cancel failed for %s: %v", d.SubscriberID, stripeErr)
				}
			}
			result.Suspended++
			go s.sendDunningEmail(d.SubscriberID, d.DunningCount, "")
		} else {
			// Schedule next retry
			nextRetry := time.Now().AddDate(0, 0, dunningRetryDays(d.DunningCount))
			_, _ = s.db.Exec(`
				UPDATE subscriptions
				SET dunning_count = $2, dunning_next_retry_at = $3, updated_at = NOW()
				WHERE id = $1
			`, d.ID, d.DunningCount, nextRetry)
			// Trigger Stripe payment retry on the latest open invoice.
			if s.stripe != nil && d.StripeSubID.Valid && d.StripeSubID.String != "" {
				var invoiceID string
				_ = s.db.QueryRow(`
					SELECT stripe_invoice_id FROM roost_invoices
					WHERE subscriber_id = $1 AND status IN ('open','past_due')
					ORDER BY created_at DESC LIMIT 1
				`, d.SubscriberID).Scan(&invoiceID)
				if invoiceID != "" {
					if _, stripeErr := stripeInvoice.Pay(invoiceID, nil); stripeErr != nil {
						log.Printf("RunDunningCheck: Stripe invoice retry failed for %s: %v", d.SubscriberID, stripeErr)
					}
				}
			}
			result.Retried++
			go s.sendDunningEmail(d.SubscriberID, d.DunningCount, "")
		}
	}

	// 2. Expire trials that have ended without subscribing
	expiredRows, err := s.db.Query(`
		SELECT s.id, s.subscriber_id
		FROM subscriptions s
		WHERE s.status = 'trialing'
		  AND s.trial_ends_at IS NOT NULL
		  AND s.trial_ends_at < NOW()
	`)
	if err != nil {
		log.Printf("RunDunningCheck: trial expiry query error: %v", err)
		result.Errors++
		return result
	}
	defer expiredRows.Close()

	for expiredRows.Next() {
		var subID, subscriberID string
		if err := expiredRows.Scan(&subID, &subscriberID); err != nil {
			result.Errors++
			continue
		}
		_, _ = s.db.Exec(`
			UPDATE subscriptions SET status = 'expired', updated_at = NOW() WHERE id = $1
		`, subID)
		if err := s.suspendAPIToken(subscriberID); err != nil {
			log.Printf("RunDunningCheck: suspendAPIToken (trial expired) %s: %v", subscriberID, err)
		}
		result.TrialsExpired++
	}

	log.Printf("RunDunningCheck: checked=%d retried=%d suspended=%d trialsExpired=%d errors=%d",
		result.Checked, result.Retried, result.Suspended, result.TrialsExpired, result.Errors)
	return result
}
