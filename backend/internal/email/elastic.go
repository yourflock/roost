// Package email provides Elastic Email HTTP API integration for transactional emails.
// Uses HTTP API v2 (not SMTP) — more reliable for programmatic sending.
// Raw tokens are NEVER passed to this package; callers pass only safe display strings.
package email

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const elasticAPIURL = "https://api.elasticemail.com/v2/email/send"

var httpClient = &http.Client{Timeout: 10 * time.Second}

// SendVerificationEmail sends a verification link to the subscriber.
// verifyURL is the full https://{domain}/verify?token={raw_token} URL.
// The raw token is embedded in the URL only; it is not logged by this function.
func SendVerificationEmail(toEmail, displayName, verifyURL string) error {
	subject := "Verify your Roost email"
	body := fmt.Sprintf(`Hello %s,

Welcome to Roost! Please verify your email address to get started.

Click the link below to verify your email:
%s

This link expires in 24 hours.

If you didn't create a Roost account, you can safely ignore this email.

— The Roost Team`, displayName, verifyURL)

	return send(toEmail, subject, body)
}

// SendPasswordResetEmail sends a password reset link to the subscriber.
// resetURL contains the reset token embedded in the URL; not logged here.
func SendPasswordResetEmail(toEmail, displayName, resetURL string) error {
	subject := "Reset your Roost password"
	body := fmt.Sprintf(`Hello %s,

We received a request to reset your Roost password.

Click the link below to set a new password:
%s

This link expires in 1 hour. If you didn't request a password reset, no action is needed.

— The Roost Team`, displayName, resetURL)

	return send(toEmail, subject, body)
}

// SendLockoutNotificationEmail informs the subscriber their account is temporarily locked.
func SendLockoutNotificationEmail(toEmail, displayName string, lockoutMinutes int) error {
	subject := "Roost: Multiple failed login attempts detected"
	body := fmt.Sprintf(`Hello %s,

We detected multiple failed login attempts on your Roost account.
Your account has been temporarily locked for %d minutes as a security measure.

If this was you, please wait and try again later.
If this wasn't you, we recommend resetting your password when the lockout clears.

— The Roost Team`, displayName, lockoutMinutes)

	return send(toEmail, subject, body)
}

// send is the internal implementation using Elastic Email HTTP API v2.
// API key is read from ELASTIC_EMAIL_API_KEY env var. Never logs the API key.
func send(toEmail, subject, body string) error {
	apiKey := os.Getenv("ELASTIC_EMAIL_API_KEY")
	if apiKey == "" {
		return fmt.Errorf("ELASTIC_EMAIL_API_KEY not set")
	}

	sender := os.Getenv("ELASTIC_EMAIL_SENDER")
	if sender == "" {
		sender = "noreply@nself.org"
	}

	params := url.Values{}
	params.Set("apikey", apiKey)
	params.Set("from", sender)
	params.Set("to", toEmail)
	params.Set("subject", subject)
	params.Set("bodyText", body)
	params.Set("isTransactional", "true")

	resp, err := httpClient.Post(elasticAPIURL, "application/x-www-form-urlencoded",
		strings.NewReader(params.Encode()))
	if err != nil {
		return fmt.Errorf("elastic email request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("elastic email API error %d: %s", resp.StatusCode, string(respBody))
	}

	return nil
}

// Send is a general-purpose exported wrapper for sending any email via Elastic Email.
// Use this when specific typed helpers (SendVerificationEmail, etc.) don't apply.
func Send(toEmail, subject, body string) error {
	return send(toEmail, subject, body)
}
