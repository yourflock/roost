// fixtures.go — Test data seed helpers.
// Provides canonical test fixtures for subscribers, plans, channels.
package testutil

import (
	"database/sql"
	"fmt"
	"testing"
	"time"
)

// Subscriber represents a minimal test subscriber.
type Subscriber struct {
	ID    string
	Email string
	Name  string
}

// Plan represents a minimal test subscription plan.
type Plan struct {
	ID               string
	Name             string
	MonthlyPriceCents int
}

// SeedSubscriber inserts a test subscriber and returns its ID.
func SeedSubscriber(t *testing.T, db *sql.DB) *Subscriber {
	t.Helper()
	sub := &Subscriber{
		Email: fmt.Sprintf("test-%d@example.com", time.Now().UnixNano()),
		Name:  "Test User",
	}
	err := db.QueryRow(`
		INSERT INTO subscribers (email, display_name, password_hash, email_verified, status)
		VALUES ($1, $2, '$2a$12$fakehashfortest', TRUE, 'active')
		RETURNING id
	`, sub.Email, sub.Name).Scan(&sub.ID)
	if err != nil {
		t.Fatalf("seed subscriber: %v", err)
	}
	return sub
}

// SeedPlan inserts a test subscription plan and returns its ID.
func SeedPlan(t *testing.T, db *sql.DB, name string, monthlyCents int) *Plan {
	t.Helper()
	plan := &Plan{
		Name:              name,
		MonthlyPriceCents: monthlyCents,
	}
	err := db.QueryRow(`
		INSERT INTO subscription_plans (name, monthly_price_cents, annual_price_cents, max_streams)
		VALUES ($1, $2, $3, 3)
		ON CONFLICT (name) DO UPDATE SET monthly_price_cents = EXCLUDED.monthly_price_cents
		RETURNING id
	`, name, monthlyCents, monthlyCents*10).Scan(&plan.ID)
	if err != nil {
		t.Fatalf("seed plan: %v", err)
	}
	return plan
}

// CleanupSubscriber removes a test subscriber by ID.
func CleanupSubscriber(db *sql.DB, subscriberID string) {
	_, _ = db.Exec(`DELETE FROM subscribers WHERE id = $1`, subscriberID)
}
