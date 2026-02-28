// billing_emails.go — Billing-specific transactional emails.
// P3-T08: Dunning emails (payment failed, account suspended).
// P3-T09: Invoice delivery email.
package email

import "fmt"

// SendDunningEmail sends a payment failure notification.
// attempt is 1, 2, or 3. invoiceURL is the Stripe-hosted invoice link (may be empty).
func SendDunningEmail(toEmail, displayName string, attempt int, invoiceURL string) error {
	baseURL := "https://roost.unity.dev"

	subject := "Your Roost payment failed"
	statusMsg := "We'll retry automatically. Your service continues during the grace period."
	if attempt >= 3 {
		subject = "Your Roost account has been suspended"
		statusMsg = "Your API token has been suspended. Update your payment method to restore access."
	}

	var invoiceSection string
	if invoiceURL != "" {
		invoiceSection = fmt.Sprintf("\nView your invoice: %s\n", invoiceURL)
	}

	body := fmt.Sprintf(`Hi %s,

We were unable to process your Roost subscription payment (attempt %d of 3).

%s
%s
Update your payment method here:
%s/subscribe/billing

If you have questions, reply to this email.

— The Roost Team`, displayName, attempt, statusMsg, invoiceSection, baseURL)

	return send(toEmail, subject, body)
}

// SendInvoiceEmail sends an invoice notification with PDF link.
func SendInvoiceEmail(toEmail, displayName, invoiceURL, period string) error {
	subject := "Your Roost invoice is ready"
	body := fmt.Sprintf(`Hi %s,

Your Roost invoice for %s is ready.

View and download your invoice:
%s

Thank you for being a Roost subscriber.

— The Roost Team`, displayName, period, invoiceURL)

	return send(toEmail, subject, body)
}

// SendRefundConfirmationEmail notifies the subscriber their refund has been processed.
func SendRefundConfirmationEmail(toEmail, displayName string, amountCents int64) error {
	amount := float64(amountCents) / 100
	subject := "Your Roost refund has been processed"
	body := fmt.Sprintf(`Hi %s,

We've processed your refund of $%.2f USD. It should appear on your statement within 5-10 business days.

If you have any questions, reply to this email.

— The Roost Team`, displayName, amount)

	return send(toEmail, subject, body)
}

// SendTrialEmail sends a trial lifecycle email (day5 or expiry notifications).
// subject and body are pre-constructed by the caller (trial_notifier.go).
func SendTrialEmail(toEmail, subject, body string) error {
	return send(toEmail, subject, body)
}

// SendReferralRewardEmail notifies a referrer they earned a reward.
func SendReferralRewardEmail(toEmail, displayName, refereeEmail string, rewardCents int) error {
	subject := "You earned a Roost reward!"
	body := fmt.Sprintf(`Hi %s,

Good news: someone you referred just subscribed to Roost!

Referred: %s
Reward: $%.2f credit applied to your next invoice.

The credit will appear on your next billing statement.

Thanks for spreading the word!

— The Roost Team`, displayName, refereeEmail, float64(rewardCents)/100)
	return send(toEmail, subject, body)
}
