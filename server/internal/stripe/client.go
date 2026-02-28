// client.go — shared Stripe client initialization for the Roost billing service.
package stripe

import (
	"fmt"
	"log"
	"os"

	"github.com/stripe/stripe-go/v76"
	"github.com/stripe/stripe-go/v76/client"
)

// Client wraps the Stripe API client with Roost-specific helpers.
type Client struct {
	sc *client.API
}

// New initializes a Stripe client from the environment.
// Reads STRIPE_SECRET_KEY from environment.
func New() (*Client, error) {
	key := os.Getenv("STRIPE_SECRET_KEY")
	if key == "" {
		
	}
	if key == "" {
		return nil, fmt.Errorf("stripe not configured: set STRIPE_SECRET_KEY")
	}
	stripe.Key = key
	sc := &client.API{}
	sc.Init(key, nil)
	log.Printf("Stripe client initialized (key prefix: %s...)", safePrefix(key))
	return &Client{sc: sc}, nil
}

// IsTestMode returns true if the configured key is a Stripe test key.
func (c *Client) IsTestMode() bool {
	return len(stripe.Key) > 7 && stripe.Key[:7] == "sk_test"
}

// safePrefix returns the first 12 chars of the key for logging (never the full key).
func safePrefix(key string) string {
	if len(key) < 12 {
		return "***"
	}
	return key[:12]
}
