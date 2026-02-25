// trial_notifier.go — Trial lifecycle cron job for P17-T01-S04.
//
// Runs every hour via goroutine started in main.go.
// Handles:
//  1. Day-5 (48h before expiry): sends "trial ends in 2 days" email.
//  2. Expiry (trial_end <= now()): deactivates token, sends "trial ended" email.
//
// Notifications table prevents duplicates — each (subscription_id, type) is unique.
package billing

import (
	"fmt"
	"log"
	"time"

	"github.com/yourflock/roost/internal/email"
)

// trialNotifierInterval controls how often the cron fires.
const trialNotifierInterval = 1 * time.Hour

// startTrialNotifier runs the trial lifecycle cron in a background goroutine.
// Called from StartBillingService in main.go.
func (s *Server) startTrialNotifier() {
	go func() {
		log.Println("Trial notifier: started")
		for {
			s.runTrialNotifierCycle()
			time.Sleep(trialNotifierInterval)
		}
	}()
}

// runTrialNotifierCycle performs one full sweep of active trials.
func (s *Server) runTrialNotifierCycle() {
	s.sendDay5Notifications()
	s.expireTrials()
}

// sendDay5Notifications sends the "trial ends in 2 days" email once per trial.
func (s *Server) sendDay5Notifications() {
	rows, err := s.db.Query(`
		SELECT s.id, sub.email, sub.display_name
		FROM subscriptions s
		JOIN subscribers sub ON sub.id = s.subscriber_id
		WHERE s.is_trial = TRUE
		  AND s.status = 'trialing'
		  AND s.trial_end - now() <= INTERVAL '48 hours'
		  AND s.trial_end - now() > INTERVAL '0 seconds'
		  AND NOT EXISTS (
			  SELECT 1 FROM trial_notifications tn
			  WHERE tn.subscription_id = s.id AND tn.type = 'day5'
		  )
	`)
	if err != nil {
		log.Printf("Trial notifier: day5 query error: %v", err)
		return
	}
	defer rows.Close()

	for rows.Next() {
		var subID, emailAddr, name string
		if err := rows.Scan(&subID, &emailAddr, &name); err != nil {
			continue
		}
		if err := s.sendTrialDay5Email(emailAddr, name); err != nil {
			log.Printf("Trial notifier: day5 email failed for %s: %v", subID, err)
			continue
		}
		// Record sent — unique constraint prevents duplicates.
		_, _ = s.db.Exec(`
			INSERT INTO trial_notifications (subscription_id, type) VALUES ($1, 'day5')
			ON CONFLICT DO NOTHING
		`, subID)
		log.Printf("Trial notifier: day5 email sent to %s", subID)
	}
}

// expireTrials deactivates tokens and sends expiry emails for expired trials.
func (s *Server) expireTrials() {
	rows, err := s.db.Query(`
		SELECT s.id, sub.email, sub.display_name
		FROM subscriptions s
		JOIN subscribers sub ON sub.id = s.subscriber_id
		WHERE s.is_trial = TRUE
		  AND s.status = 'trialing'
		  AND s.trial_end <= now()
		  AND NOT EXISTS (
			  SELECT 1 FROM trial_notifications tn
			  WHERE tn.subscription_id = s.id AND tn.type = 'expiry'
		  )
	`)
	if err != nil {
		log.Printf("Trial notifier: expiry query error: %v", err)
		return
	}
	defer rows.Close()

	for rows.Next() {
		var subID, emailAddr, name string
		if err := rows.Scan(&subID, &emailAddr, &name); err != nil {
			continue
		}

		// Deactivate token.
		if _, err := s.db.Exec(`
			UPDATE api_tokens SET is_active = FALSE
			WHERE subscription_id = $1
		`, subID); err != nil {
			log.Printf("Trial notifier: token deactivation failed for %s: %v", subID, err)
			continue
		}

		// Update subscription status.
		if _, err := s.db.Exec(`
			UPDATE subscriptions SET status = 'trial_expired' WHERE id = $1
		`, subID); err != nil {
			log.Printf("Trial notifier: status update failed for %s: %v", subID, err)
			continue
		}

		// Send expiry email.
		_ = s.sendTrialExpiredEmail(emailAddr, name)

		// Record notification.
		_, _ = s.db.Exec(`
			INSERT INTO trial_notifications (subscription_id, type) VALUES ($1, 'expiry')
			ON CONFLICT DO NOTHING
		`, subID)
		log.Printf("Trial notifier: trial expired for subscription %s", subID)
	}
}

// sendTrialDay5Email sends the "2 days left" email via Elastic Email.
func (s *Server) sendTrialDay5Email(to, name string) error {
	baseURL := getEnv("ROOST_BASE_URL", "https://roost.yourflock.com")
	subject := "Your Roost trial ends in 2 days"
	body := fmt.Sprintf(`Hi %s,

Your Roost free trial ends in 2 days. After that, your API token will stop working and channels will disappear from Owl.

Subscribe now to keep watching: %s/subscribe

Questions? Reply to this email.

The Roost Team`, name, baseURL)

	return email.SendTrialEmail(to, subject, body)
}

// sendTrialExpiredEmail sends the "trial ended" email via Elastic Email.
func (s *Server) sendTrialExpiredEmail(to, name string) error {
	baseURL := getEnv("ROOST_BASE_URL", "https://roost.yourflock.com")
	subject := "Your Roost trial has ended"
	body := fmt.Sprintf(`Hi %s,

Your 7-day Roost trial has ended and your token has been deactivated.

Subscribe to continue watching: %s/subscribe

Your watchlist and preferences are saved — they'll be waiting for you.

The Roost Team`, name, baseURL)

	return email.SendTrialEmail(to, subject, body)
}
