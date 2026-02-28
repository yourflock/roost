// customer.go — Stripe customer management helpers.
// P3-T02: Used by checkout flow to create/retrieve Stripe customers.
package stripe

import (
	"github.com/stripe/stripe-go/v76"
	"github.com/stripe/stripe-go/v76/customer"
)

// CreateCustomer creates a new Stripe customer with the subscriber's email and metadata.
// Returns the Stripe customer ID (e.g., "cus_Nffrfeopo12345").
//
// TODO (P3-T01): Requires STRIPE_SECRET_KEY in environment.
func (c *Client) CreateCustomer(email, displayName, roostSubscriberID string) (string, error) {
	cust, err := customer.New(&stripe.CustomerParams{
		Email: stripe.String(email),
		Name:  stripe.String(displayName),
		Metadata: map[string]string{
			"roost_subscriber_id": roostSubscriberID,
		},
	})
	if err != nil {
		return "", err
	}
	return cust.ID, nil
}
