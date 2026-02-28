// main.go — Roost Billing Service entrypoint.
// Starts the billing HTTP service on port 8085 (default).
package main

import (
	"github.com/unyeco/roost/services/billing"
)

func main() {
	billing.StartBillingService()
}
