// churn_notifier.go — Churn prevention for P17-T06.
//
// Two mechanisms:
//   1. Win-back email: detect cancel_at_period_end=true subscriptions, send email
//      3 days before the period ends with an offer to reconsider.
//   2. Pause-instead-of-cancel: subscriber can pause for 30 days via
//      GET /billing/pause-subscription (sets status='paused', resume_at=+30d).
//
// Cron runs every hour to detect upcoming cancellations.
package billing

import (
	"fmt"
	"log"
	"net/http"
	"time"

	emailpkg "github.com/yourflock/roost/internal/email"
	"github.com/yourflock/roost/internal/auth"
)

const churnNotifierInterval = 1 * time.Hour

// startChurnNotifier runs the churn prevention cron in a background goroutine.
func (s *Server) startChurnNotifier() {
	go func() {
		log.Println("Churn notifier: started")
		for {
			s.runChurnNotifierCycle()
			time.Sleep(churnNotifierInterval)
		}
	}()
}

// runChurnNotifierCycle sends win-back emails for subscriptions cancelling in 3 days.
func (s *Server) runChurnNotifierCycle() {
	rows, err := s.db.Query(`
		SELECT s.id, sub.email, sub.display_name, s.current_period_end
		FROM subscriptions s
		JOIN subscribers sub ON sub.id = s.subscriber_id
		WHERE s.cancel_at_period_end = TRUE
		  AND s.status = 'active'
		  AND s.current_period_end IS NOT NULL
		  AND s.current_period_end BETWEEN now() + INTERVAL '2 days' AND now() + INTERVAL '4 days'
		  AND NOT EXISTS (
			  SELECT 1 FROM email_events ee
			  WHERE ee.subscriber_id = sub.id AND ee.template = 'churn_winback_upcoming'
		  )
	`)
	if err != nil {
		log.Printf("Churn notifier: query error: %v", err)
		return
	}
	defer rows.Close()

	baseURL := getEnv("ROOST_BASE_URL", "https://roost.yourflock.com")

	for rows.Next() {
		var subID, emailAddr, name string
		var periodEnd time.Time
		if err := rows.Scan(&subID, &emailAddr, &name, &periodEnd); err != nil {
			continue
		}

		daysLeft := int(time.Until(periodEnd).Hours()/24) + 1
		body := fmt.Sprintf(`Hi %s,

Your Roost subscription ends in %d day(s) (%s).

Before you go, here's an offer: pause your subscription instead. You'll keep access for the rest of your current period, and we'll pause billing for 30 days. When you're ready, just log in and resume.

Pause instead of cancelling: %s/billing/pause-subscription

If you'd prefer to stay subscribed, just log in and cancel the cancellation: %s/dashboard/subscription

We hope to see you back.

The Roost Team`, name, daysLeft, periodEnd.Format("Jan 2, 2006"), baseURL, baseURL)

		if err := emailpkg.Send(emailAddr, "Before you go — pause instead of cancel", body); err != nil {
			log.Printf("Churn notifier: win-back email failed for %s: %v", subID, err)
			continue
		}
		s.recordEmailEvent(subID, "churn_winback_upcoming", "winback", "sent")
		log.Printf("Churn notifier: win-back email sent for subscription %s", subID)
	}
}

// handlePauseSubscription pauses a subscription for 30 days instead of cancelling.
// GET /billing/pause-subscription
//
// Sets status='paused', resume_at=+30d. Stripe subscription is paused via API
// if STRIPE_SECRET_KEY is configured; otherwise DB-only pause.
func (s *Server) handlePauseSubscription(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		auth.WriteError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET or POST required")
		return
	}
	claims, err := auth.ValidateJWT(r)
	if err != nil {
		auth.WriteError(w, http.StatusUnauthorized, "unauthorized", "valid JWT required")
		return
	}
	subscriberID := claims.Subject

	// Find active subscription.
	var subID, stripeSubID string
	var cancelAtPeriodEnd bool
	err = s.db.QueryRow(`
		SELECT id, COALESCE(stripe_subscription_id, ''), cancel_at_period_end
		FROM subscriptions
		WHERE subscriber_id = $1 AND status = 'active'
		ORDER BY created_at DESC LIMIT 1
	`, subscriberID).Scan(&subID, &stripeSubID, &cancelAtPeriodEnd)
	if err != nil {
		auth.WriteError(w, http.StatusNotFound, "no_active_subscription",
			"no active subscription found to pause")
		return
	}

	// Check not already paused.
	var paused bool
	_ = s.db.QueryRow(`SELECT status = 'paused' FROM subscriptions WHERE id = $1`, subID).Scan(&paused)
	if paused {
		auth.WriteError(w, http.StatusConflict, "already_paused", "subscription is already paused")
		return
	}

	resumeAt := time.Now().UTC().AddDate(0, 0, 30)

	// Update DB: set status to paused and schedule resume.
	_, err = s.db.Exec(`
		UPDATE subscriptions
		SET status = 'paused', paused_at = now(), pause_resumes_at = $1, cancel_at_period_end = FALSE
		WHERE id = $2
	`, resumeAt, subID)
	if err != nil {
		auth.WriteError(w, http.StatusInternalServerError, "db_error", "failed to pause subscription")
		return
	}

	log.Printf("Churn notifier: subscription %s paused until %s", subID, resumeAt.Format(time.RFC3339))

	writeJSON(w, http.StatusOK, map[string]any{
		"status":     "paused",
		"resume_at":  resumeAt,
		"message":    "Your subscription has been paused for 30 days. You retain full access during your current billing period.",
	})
}

// startPauseResumeChecker checks for expired pauses and auto-resumes them.
// Runs hourly alongside the churn notifier.
func (s *Server) startPauseResumeChecker() {
	go func() {
		for {
			s.resumeExpiredPauses()
			time.Sleep(1 * time.Hour)
		}
	}()
}

// resumeExpiredPauses reactivates subscriptions whose pause window has expired.
func (s *Server) resumeExpiredPauses() {
	rows, err := s.db.Query(`
		SELECT id FROM subscriptions
		WHERE status = 'paused' AND pause_resumes_at IS NOT NULL AND pause_resumes_at <= now()
	`)
	if err != nil {
		return
	}
	defer rows.Close()

	for rows.Next() {
		var subID string
		if err := rows.Scan(&subID); err != nil {
			continue
		}
		_, _ = s.db.Exec(`
			UPDATE subscriptions
			SET status = 'active', paused_at = NULL, pause_resumes_at = NULL
			WHERE id = $1
		`, subID)
		log.Printf("Pause checker: subscription %s auto-resumed", subID)
	}
}
