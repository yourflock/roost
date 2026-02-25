// onboarding_emailer.go — Post-signup onboarding email sequence for P17-T04.
//
// Cron runs every hour. Emails triggered by time since subscription + onboarding state.
//
//   Day 0: Welcome email with API token (sent at subscription creation — see webhook handler).
//   Day 1: "Need help setting up?" — only if owl_connected = false.
//   Day 3: "Discover what's on Roost" — only if first_stream_at IS NULL.
//   Day 7: "How's Roost working for you?" — feedback/NPS.
//
// Emails stop once onboarding_completed_at is set.
// Deduplication via email_events table: unique(subscriber_id, template).
package billing

import (
	"fmt"
	"log"
	"time"

	"github.com/yourflock/roost/internal/email"
)

const onboardingEmailerInterval = 1 * time.Hour

// startOnboardingEmailer runs the onboarding cron in a background goroutine.
func (s *Server) startOnboardingEmailer() {
	go func() {
		log.Println("Onboarding emailer: started")
		for {
			s.runOnboardingEmailCycle()
			time.Sleep(onboardingEmailerInterval)
		}
	}()
}

// runOnboardingEmailCycle sends all pending onboarding emails for this hour.
func (s *Server) runOnboardingEmailCycle() {
	s.sendOnboardingDay1()
	s.sendOnboardingDay3()
	s.sendOnboardingDay7()
}

// sendOnboardingDay1 sends "Need help setting up?" to subscribers who haven't
// connected Owl yet and whose subscription is at least 1 day old.
func (s *Server) sendOnboardingDay1() {
	rows, err := s.db.Query(`
		SELECT sub.id, sub.email, sub.display_name
		FROM subscribers sub
		JOIN subscriptions s ON s.subscriber_id = sub.id
		JOIN onboarding_progress op ON op.subscriber_id = sub.id
		WHERE s.status IN ('active', 'trialing')
		  AND s.created_at <= now() - INTERVAL '1 day'
		  AND op.owl_connected = FALSE
		  AND op.onboarding_completed_at IS NULL
		  AND NOT EXISTS (
			  SELECT 1 FROM email_events ee
			  WHERE ee.subscriber_id = sub.id AND ee.template = 'onboarding_day1'
		  )
		LIMIT 100
	`)
	if err != nil {
		log.Printf("Onboarding emailer: day1 query error: %v", err)
		return
	}
	defer rows.Close()

	baseURL := getEnv("ROOST_BASE_URL", "https://roost.yourflock.com")

	for rows.Next() {
		var subID, emailAddr, name string
		if err := rows.Scan(&subID, &emailAddr, &name); err != nil {
			continue
		}

		body := fmt.Sprintf(`Hi %s,

It looks like you haven't connected Owl to Roost yet. Here's how:

1. Download Owl: %s/owl
2. Open Owl -> Settings -> Community Addons
3. Paste your Roost API token

Need your token? Find it at: %s/dashboard/token

Any trouble? Reply to this email and we'll sort it out.

The Roost Team`, name, baseURL, baseURL)

		if err := email.Send(emailAddr, "Need help setting up Roost?", body); err != nil {
			log.Printf("Onboarding emailer: day1 send failed for %s: %v", subID, err)
			continue
		}
		s.recordEmailEvent(subID, "onboarding_day1", "onboarding", "sent")
	}
}

// sendOnboardingDay3 sends channel highlights to subscribers who haven't streamed yet
// and whose subscription is at least 3 days old.
func (s *Server) sendOnboardingDay3() {
	rows, err := s.db.Query(`
		SELECT sub.id, sub.email, sub.display_name
		FROM subscribers sub
		JOIN subscriptions s ON s.subscriber_id = sub.id
		JOIN onboarding_progress op ON op.subscriber_id = sub.id
		WHERE s.status IN ('active', 'trialing')
		  AND s.created_at <= now() - INTERVAL '3 days'
		  AND op.first_stream_at IS NULL
		  AND op.onboarding_completed_at IS NULL
		  AND NOT EXISTS (
			  SELECT 1 FROM email_events ee
			  WHERE ee.subscriber_id = sub.id AND ee.template = 'onboarding_day3'
		  )
		LIMIT 100
	`)
	if err != nil {
		log.Printf("Onboarding emailer: day3 query error: %v", err)
		return
	}
	defer rows.Close()

	baseURL := getEnv("ROOST_BASE_URL", "https://roost.yourflock.com")

	for rows.Next() {
		var subID, emailAddr, name string
		if err := rows.Scan(&subID, &emailAddr, &name); err != nil {
			continue
		}

		body := fmt.Sprintf(`Hi %s,

Roost has hundreds of channels waiting for you. Here's what's popular right now:

- Live sports: NFL, NBA, Premier League
- News: CNN International, BBC World
- Movies: 24/7 classic and new releases
- Kids: Cartoon Network, Disney Channel

Browse the full lineup: %s/channels

The Roost Team`, name, baseURL)

		if err := email.Send(emailAddr, "Discover what's on Roost", body); err != nil {
			log.Printf("Onboarding emailer: day3 send failed for %s: %v", subID, err)
			continue
		}
		s.recordEmailEvent(subID, "onboarding_day3", "onboarding", "sent")
	}
}

// sendOnboardingDay7 sends a feedback/NPS survey to all active subscribers
// whose subscription is at least 7 days old.
func (s *Server) sendOnboardingDay7() {
	rows, err := s.db.Query(`
		SELECT sub.id, sub.email, sub.display_name
		FROM subscribers sub
		JOIN subscriptions s ON s.subscriber_id = sub.id
		WHERE s.status IN ('active', 'trialing')
		  AND s.created_at <= now() - INTERVAL '7 days'
		  AND NOT EXISTS (
			  SELECT 1 FROM email_events ee
			  WHERE ee.subscriber_id = sub.id AND ee.template = 'onboarding_day7'
		  )
		LIMIT 100
	`)
	if err != nil {
		log.Printf("Onboarding emailer: day7 query error: %v", err)
		return
	}
	defer rows.Close()

	baseURL := getEnv("ROOST_BASE_URL", "https://roost.yourflock.com")

	for rows.Next() {
		var subID, emailAddr, name string
		if err := rows.Scan(&subID, &emailAddr, &name); err != nil {
			continue
		}

		body := fmt.Sprintf(`Hi %s,

You've been with Roost for a week. How's it going?

We'd love to know what you think. It takes 2 minutes:
%s/feedback

Found a bug? Want a channel we don't have? Just reply to this email.

Thanks for being a Roost subscriber.

The Roost Team`, name, baseURL)

		if err := email.Send(emailAddr, "How's Roost working for you?", body); err != nil {
			log.Printf("Onboarding emailer: day7 send failed for %s: %v", subID, err)
			continue
		}
		s.recordEmailEvent(subID, "onboarding_day7", "onboarding", "sent")
	}
}

// recordEmailEvent logs a sent email to the email_events table.
// Uses INSERT ... ON CONFLICT DO NOTHING for idempotency on retries.
func (s *Server) recordEmailEvent(subscriberID, template, category, status string) {
	_, _ = s.db.Exec(`
		INSERT INTO email_events (subscriber_id, template, category, status)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT DO NOTHING
	`, subscriberID, template, category, status)
}

// sendElasticEmail is a package-level helper for non-Server callers.
// Kept for backwards compatibility with any existing internal callers.
func sendElasticEmail(to, subject, body, _ string) error {
	return email.Send(to, subject, body)
}
