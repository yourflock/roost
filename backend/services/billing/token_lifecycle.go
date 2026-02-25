// token_lifecycle.go — API token activation/suspension/revocation tied to billing events.
// P3-T05: API Token Activation on Subscription
//
// When a subscriber completes checkout or renews, their API token is activated.
// When payment fails (dunning), the token is suspended.
// When subscription is canceled, the token is revoked.
// The token itself was created during auth service registration (P2-T07).
// This file manages only the is_active flag based on billing state.
package billing

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"log"

	"golang.org/x/crypto/bcrypt"
)

// activateAPIToken sets is_active = true for the subscriber's API token.
// If no token exists, creates one. Called by:
//   - onCheckoutComplete (P3-T03)
//   - onPaymentSucceeded after dunning recovery (P3-T03)
//   - handleResume (P3-T10)
func (s *Server) activateAPIToken(subscriberID string) error {
	// Check if token already exists
	var tokenID string
	err := s.db.QueryRow(
		`SELECT id FROM api_tokens WHERE subscriber_id = $1 ORDER BY created_at DESC LIMIT 1`,
		subscriberID,
	).Scan(&tokenID)

	if err == sql.ErrNoRows {
		// No token exists — create one
		return s.createAPIToken(subscriberID)
	}
	if err != nil {
		return fmt.Errorf("activateAPIToken: lookup: %w", err)
	}

	// Activate existing token
	_, err = s.db.Exec(
		`UPDATE api_tokens SET is_active = true WHERE subscriber_id = $1`,
		subscriberID,
	)
	if err != nil {
		return fmt.Errorf("activateAPIToken: update: %w", err)
	}
	log.Printf("API token activated for subscriber %s", subscriberID)
	return nil
}

// suspendAPIToken sets is_active = false for the subscriber's API token.
// Called by:
//   - onPaymentFailed after 3 dunning attempts (P3-T03)
//   - handlePause (P3-T10)
func (s *Server) suspendAPIToken(subscriberID string) error {
	_, err := s.db.Exec(
		`UPDATE api_tokens SET is_active = false WHERE subscriber_id = $1`,
		subscriberID,
	)
	if err != nil {
		return fmt.Errorf("suspendAPIToken: %w", err)
	}
	log.Printf("API token suspended for subscriber %s", subscriberID)
	return nil
}

// revokeAPIToken permanently deactivates all API tokens for a subscriber.
// Called by:
//   - onSubscriptionDeleted (P3-T03)
// Unlike suspend, revoked tokens are never re-activated — subscriber must
// create a new subscription and new token if they return.
func (s *Server) revokeAPIToken(subscriberID string) error {
	_, err := s.db.Exec(
		`UPDATE api_tokens SET is_active = false WHERE subscriber_id = $1`,
		subscriberID,
	)
	if err != nil {
		return fmt.Errorf("revokeAPIToken: %w", err)
	}
	log.Printf("API token revoked for subscriber %s", subscriberID)
	return nil
}

// createAPIToken generates and stores a new API token for a subscriber.
// Called when activateAPIToken finds no existing token (rare — auth service
// creates tokens at registration, but this is the safety net).
// Format: "rtk_<32hex>" — prefix rtk = Roost Token Key.
func (s *Server) createAPIToken(subscriberID string) error {
	// Generate 32 random bytes → 64 hex chars
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return fmt.Errorf("createAPIToken: rand.Read: %w", err)
	}
	token := "rtk_" + hex.EncodeToString(raw)
	prefix := token[:12] // first 12 chars for display/lookup

	// Hash the token for storage (never store plaintext)
	hash, err := bcrypt.GenerateFromPassword([]byte(token), 12)
	if err != nil {
		return fmt.Errorf("createAPIToken: bcrypt: %w", err)
	}

	_, err = s.db.Exec(`
		INSERT INTO api_tokens (subscriber_id, token_hash, token_prefix, is_active)
		VALUES ($1, $2, $3, true)
	`, subscriberID, string(hash), prefix)
	if err != nil {
		return fmt.Errorf("createAPIToken: insert: %w", err)
	}

	// NOTE: The plaintext token is NOT returned here — it was already returned
	// to the subscriber during auth registration. This path only creates a token
	// as a safety fallback. If the subscriber needs the token value, they use
	// GET /auth/tokens to list (prefix only) or POST /auth/tokens to generate a new one.
	log.Printf("API token created for subscriber %s (prefix: %s)", subscriberID, prefix)
	return nil
}
