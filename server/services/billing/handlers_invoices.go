// handlers_invoices.go — Invoice listing and PDF delivery.
// P3-T09: Invoice PDF Generation & Delivery
//
// GET  /billing/invoices         — list invoices for authenticated subscriber
// GET  /billing/invoices/{id}    — get single invoice detail + PDF URL
//
// PDF strategy:
//   - Stripe generates hosted invoices (PDF link available via StripeInvoice.InvoicePDF)
//   - We store the Stripe PDF URL in roost_invoices.invoice_pdf_url
//   - For branded PDFs: we generate our own via a Go template + wkhtmltopdf or similar
//   - For now: redirect to Stripe-hosted PDF (immediate value) with TODO for branded version
package billing

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/yourflock/roost/internal/auth"
)

// invoiceListItem is a single row in GET /billing/invoices.
type invoiceListItem struct {
	ID                 string    `json:"id"`
	StripeInvoiceID    string    `json:"stripe_invoice_id"`
	AmountCents        int64     `json:"amount_cents"`
	Currency           string    `json:"currency"`
	Status             string    `json:"status"`
	PeriodStart        *time.Time `json:"period_start,omitempty"`
	PeriodEnd          *time.Time `json:"period_end,omitempty"`
	HostedInvoiceURL   string    `json:"hosted_invoice_url,omitempty"`
	InvoicePDFURL      string    `json:"invoice_pdf_url,omitempty"`
	CreatedAt          time.Time `json:"created_at"`
}

// handleInvoices lists all invoices for the authenticated subscriber.
// GET /billing/invoices
func (s *Server) handleInvoices(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		auth.WriteError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET required")
		return
	}

	claims, err := auth.ValidateJWT(r)
	if err != nil {
		auth.WriteError(w, http.StatusUnauthorized, "unauthorized", "valid JWT required")
		return
	}
	subscriberID := claims.Subject

	rows, err := s.db.Query(`
		SELECT
			id, stripe_invoice_id, amount_cents, currency, status,
			period_start, period_end, hosted_invoice_url, invoice_pdf_url, created_at
		FROM roost_invoices
		WHERE subscriber_id = $1
		ORDER BY created_at DESC
		LIMIT 50
	`, subscriberID)
	if err != nil {
		auth.WriteError(w, http.StatusInternalServerError, "db_error", "failed to list invoices")
		return
	}
	defer rows.Close()

	var invoices []invoiceListItem
	for rows.Next() {
		var inv invoiceListItem
		if err := rows.Scan(
			&inv.ID, &inv.StripeInvoiceID, &inv.AmountCents, &inv.Currency, &inv.Status,
			&inv.PeriodStart, &inv.PeriodEnd, &inv.HostedInvoiceURL, &inv.InvoicePDFURL, &inv.CreatedAt,
		); err != nil {
			continue
		}
		invoices = append(invoices, inv)
	}

	if invoices == nil {
		invoices = []invoiceListItem{}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"invoices": invoices,
		"count":    len(invoices),
	})
}

// handleInvoicePDF handles GET /billing/invoices/{id} — returns invoice detail and PDF redirect.
// For subscribers: redirect to the Stripe-hosted PDF.
// TODO: Add branded PDF generation (wkhtmltopdf or chromedp headless render).
func (s *Server) handleInvoicePDF(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		auth.WriteError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET required")
		return
	}

	claims, err := auth.ValidateJWT(r)
	if err != nil {
		auth.WriteError(w, http.StatusUnauthorized, "unauthorized", "valid JWT required")
		return
	}
	subscriberID := claims.Subject

	// Extract invoice ID from path /billing/invoices/{id}
	invoiceID := strings.TrimPrefix(r.URL.Path, "/billing/invoices/")
	if invoiceID == "" {
		auth.WriteError(w, http.StatusBadRequest, "missing_id", "invoice ID required in path")
		return
	}

	var inv invoiceListItem
	err = s.db.QueryRow(`
		SELECT
			id, stripe_invoice_id, amount_cents, currency, status,
			period_start, period_end, hosted_invoice_url, invoice_pdf_url, created_at
		FROM roost_invoices
		WHERE id = $1 AND subscriber_id = $2
	`, invoiceID, subscriberID).Scan(
		&inv.ID, &inv.StripeInvoiceID, &inv.AmountCents, &inv.Currency, &inv.Status,
		&inv.PeriodStart, &inv.PeriodEnd, &inv.HostedInvoiceURL, &inv.InvoicePDFURL, &inv.CreatedAt,
	)
	if err != nil {
		auth.WriteError(w, http.StatusNotFound, "invoice_not_found", "invoice not found")
		return
	}

	// If requesting PDF directly (Accept: application/pdf or ?format=pdf)
	if r.URL.Query().Get("format") == "pdf" || r.Header.Get("Accept") == "application/pdf" {
		if inv.InvoicePDFURL != "" {
			http.Redirect(w, r, inv.InvoicePDFURL, http.StatusTemporaryRedirect)
			return
		}
		// TODO (P3-T09): Generate branded PDF here
		// For now, redirect to Stripe-hosted invoice
		if inv.HostedInvoiceURL != "" {
			http.Redirect(w, r, inv.HostedInvoiceURL, http.StatusTemporaryRedirect)
			return
		}
		auth.WriteError(w, http.StatusNotFound, "pdf_not_available", "PDF not yet available for this invoice")
		return
	}

	// Return JSON invoice detail
	writeJSON(w, http.StatusOK, map[string]any{
		"invoice":       inv,
		"pdf_url":       inv.InvoicePDFURL,
		"hosted_url":    inv.HostedInvoiceURL,
		"download_url":  fmt.Sprintf("/billing/invoices/%s?format=pdf", inv.ID),
	})
}
