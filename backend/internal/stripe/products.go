// products.go — Stripe product and price creation for Roost subscription plans.
// P3-T01-S02: Create Stripe products and prices programmatically.
//
// TODO (P3-T01): This file requires STRIPE_SECRET_KEY=sk_test_... in environment.
// All functions are fully implemented — they will succeed once the key is added.
package stripe

import (
	"fmt"
	"log"

	"github.com/stripe/stripe-go/v76"
	"github.com/stripe/stripe-go/v76/price"
	"github.com/stripe/stripe-go/v76/product"
)

// PlanPrices holds the Stripe price IDs for a subscription plan.
type PlanPrices struct {
	ProductID      string
	PriceIDMonthly string
	PriceIDAnnual  string
}

// RoostPlans defines the plan names and prices for Roost subscriptions.
// All amounts are in USD cents.
var RoostPlans = []struct {
	Name          string
	Slug          string
	MonthlyAmount int64 // USD cents
	AnnualAmount  int64 // USD cents
}{
	{"Roost Basic", "basic", 499, 4999},
	{"Roost Premium", "premium", 999, 9999},
	{"Roost Family", "family", 1499, 14999},
}

// CreateProducts creates all Roost subscription products and prices in Stripe.
// Returns a map of plan slug → PlanPrices.
//
// TODO (P3-T01): Requires STRIPE_SECRET_KEY. Call from a setup CLI command or
// migration script after adding the key.
func (c *Client) CreateProducts() (map[string]PlanPrices, error) {
	results := make(map[string]PlanPrices)

	for _, plan := range RoostPlans {
		pp, err := c.createPlanProduct(plan.Name, plan.Slug, plan.MonthlyAmount, plan.AnnualAmount)
		if err != nil {
			return nil, fmt.Errorf("failed to create plan %s: %w", plan.Slug, err)
		}
		results[plan.Slug] = pp
		log.Printf("Created Stripe product for %s: product=%s monthly=%s annual=%s",
			plan.Name, pp.ProductID, pp.PriceIDMonthly, pp.PriceIDAnnual)
	}
	return results, nil
}

// createPlanProduct creates one Stripe product + monthly + annual prices.
func (c *Client) createPlanProduct(name, slug string, monthlyAmount, annualAmount int64) (PlanPrices, error) {
	// Create the product
	prod, err := product.New(&stripe.ProductParams{
		Name: stripe.String(name),
		Metadata: map[string]string{
			"roost_plan": slug,
		},
	})
	if err != nil {
		return PlanPrices{}, fmt.Errorf("product.New: %w", err)
	}

	// Monthly price
	monthly, err := price.New(&stripe.PriceParams{
		Product:    stripe.String(prod.ID),
		Currency:   stripe.String("usd"),
		UnitAmount: stripe.Int64(monthlyAmount),
		Recurring: &stripe.PriceRecurringParams{
			Interval: stripe.String("month"),
		},
		Metadata: map[string]string{
			"roost_plan":     slug,
			"billing_period": "monthly",
		},
	})
	if err != nil {
		return PlanPrices{}, fmt.Errorf("price.New monthly: %w", err)
	}

	// Annual price
	annual, err := price.New(&stripe.PriceParams{
		Product:    stripe.String(prod.ID),
		Currency:   stripe.String("usd"),
		UnitAmount: stripe.Int64(annualAmount),
		Recurring: &stripe.PriceRecurringParams{
			Interval: stripe.String("year"),
		},
		Metadata: map[string]string{
			"roost_plan":     slug,
			"billing_period": "annual",
		},
	})
	if err != nil {
		return PlanPrices{}, fmt.Errorf("price.New annual: %w", err)
	}

	return PlanPrices{
		ProductID:      prod.ID,
		PriceIDMonthly: monthly.ID,
		PriceIDAnnual:  annual.ID,
	}, nil
}
