// routes.go — Route registration for the billing service.
// All routes documented here. Actual handler implementations are in handlers_*.go files.
// This file registers everything onto the provided mux.
package billing

import "net/http"

// RegisterRoutes registers all billing routes on the given mux.
// Called from Run() in main.go.
func (s *Server) RegisterRoutes(mux *http.ServeMux) {
	// ── Health ────────────────────────────────────────────────────────────────
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/health/detailed", s.handleDetailedHealth)
	mux.HandleFunc("/health/ready", s.handleReadinessProbe)

	// ── Checkout: P3-T02 ──────────────────────────────────────────────────────
	mux.HandleFunc("/billing/checkout", s.handleCheckout)

	// ── Webhook: P3-T03 ───────────────────────────────────────────────────────
	mux.HandleFunc("/billing/webhook", s.handleWebhook)

	// ── Subscription management: P3-T04 ───────────────────────────────────────
	mux.HandleFunc("/billing/subscription", s.handleSubscription)
	mux.HandleFunc("/billing/cancel", s.handleCancel)

	// ── Promo codes: P3-T07 ───────────────────────────────────────────────────
	mux.HandleFunc("/billing/promo/validate", s.handlePromoValidate)

	// ── Invoices + PDF: P3-T09 ────────────────────────────────────────────────
	mux.HandleFunc("/billing/invoices", s.handleInvoices)
	mux.HandleFunc("/billing/invoices/", s.handleInvoicePDF)

	// ── Pause / Resume: P3-T10 ────────────────────────────────────────────────
	mux.HandleFunc("/billing/pause", s.handlePause)
	mux.HandleFunc("/billing/resume", s.handleResume)

	// ── Refund: P3-T11 ────────────────────────────────────────────────────────
	mux.HandleFunc("/billing/refund", s.handleRefund)

	// ── Admin routes (superowner only) ────────────────────────────────────────
	mux.HandleFunc("/billing/admin/setup-stripe", s.handleSetupStripe)
	mux.HandleFunc("/billing/admin/promo", s.handleAdminPromo)
	mux.HandleFunc("/billing/admin/dunning-check", s.handleDunningCheck)

	// ── Profiles: P12-T02 ─────────────────────────────────────────────────────
	mux.HandleFunc("/profiles", s.handleProfiles)
	mux.HandleFunc("/profiles/limits", s.handleProfiles)
	mux.HandleFunc("/profiles/", s.handleProfile)

	// ── SSO OAuth: P13-T01, P13-T02 ───────────────────────────────────────────
	mux.HandleFunc("/auth/sso/login", s.handleSSOLogin)
	mux.HandleFunc("/auth/sso/callback", s.handleSSOCallback)
	mux.HandleFunc("/auth/sso/link", s.handleSSOAuthLink)

	// ── Parental settings webhook: P13-T04 ────────────────────────────────────
	mux.HandleFunc("/webhooks/parental-settings", s.handleParentalWebhook)

	// ── Watch parties: P13-T05 ────────────────────────────────────────────────
	mux.HandleFunc("/watch-party", s.handleWatchParty)
	mux.HandleFunc("/watch-party/join", s.handleJoinWatchParty)
	mux.HandleFunc("/watch-party/", s.handleWatchPartyByID)

	// ── CDN management: P14-T01 ────────────────────────────────────────────────
	mux.HandleFunc("/admin/cdn/health", s.handleCDNHealth)
	mux.HandleFunc("/admin/cdn/failover", s.handleCDNFailover)
	mux.HandleFunc("/admin/cdn/metrics", s.handleCDNMetrics)

	// ── Regional pricing: P14-T03 ──────────────────────────────────────────────
	mux.HandleFunc("/billing/plans", s.handleBillingPlans)
	mux.HandleFunc("/billing/plans/", s.handleBillingPlanPrice)
	mux.HandleFunc("/admin/billing/regional-prices", s.handleAdminRegionalPrices)

	// ── Reseller API: P14-T04/T05 ─────────────────────────────────────────────
	mux.HandleFunc("/reseller/auth", s.handleResellerAuth)
	mux.HandleFunc("/reseller/subscribers", s.handleResellerSubscribers)
	mux.HandleFunc("/reseller/subscribers/", s.handleResellerSubscriberByID)
	mux.HandleFunc("/reseller/revenue", s.handleResellerRevenue)
	mux.HandleFunc("/reseller/dashboard", s.handleResellerDashboard)
	mux.HandleFunc("/admin/resellers", s.handleAdminResellers)

	// ── Sports Notifications: P15-T06 ─────────────────────────────────────────
	mux.HandleFunc("/sports/preferences", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			s.handleGetSportsPreferences(w, r)
		} else {
			s.handleSportsPreferences(w, r)
		}
	})
	mux.HandleFunc("/sports/my-games", s.handleMyGames)

	// ── Audit log: P16-T01 ─────────────────────────────────────────────────────
	mux.HandleFunc("/admin/audit", s.handleAuditLog)

	// ── Privacy / GDPR: P16-T04 ────────────────────────────────────────────────
	mux.HandleFunc("/account/delete", s.handleAccountDelete)
	mux.HandleFunc("/account/delete/", s.handleAccountDeleteCancel)
	mux.HandleFunc("/account/export", s.handleAccountExport)
	mux.HandleFunc("/admin/privacy/process-deletions", s.handleProcessDeletions)

	// ── Abuse: P16-T05 ─────────────────────────────────────────────────────────
	mux.HandleFunc("/admin/abuse", s.handleAbuseList)
	mux.HandleFunc("/admin/abuse/", s.handleAbuseReview)

	// ── HLS Key Rotation: P16-T03 ──────────────────────────────────────────────
	mux.HandleFunc("/admin/keys/rotate-channel/", s.handleRotateChannelKey)

	// ── Free Trials: P17-T01 ──────────────────────────────────────────────────
	// POST /billing/trial     — start a 7-day free trial (no credit card required)
	// GET  /billing/trial     — check trial status for authenticated subscriber
	mux.HandleFunc("/billing/trial", s.handleTrial)

	// ── Referral Program: P17-T03 ─────────────────────────────────────────────
	// GET  /billing/referral          — get referral code + stats
	// GET  /billing/referral/list     — list referrals made by subscriber
	// POST /billing/referral/claim    — claim a referral code on signup
	mux.HandleFunc("/billing/referral", s.handleReferral)
	mux.HandleFunc("/billing/referral/list", s.handleReferralList)
	mux.HandleFunc("/billing/referral/claim", s.handleReferralClaim)

	// ── Promo Code Admin: P17-T02 ─────────────────────────────────────────────
	// GET  /admin/promo       — list all promo codes
	// PATCH /admin/promo/:id  — update max_uses, expires_at, is_active
	mux.HandleFunc("/admin/promo", s.handleAdminPromoList)
	mux.HandleFunc("/admin/promo/", s.handleAdminPromoUpdate)

	// ── Onboarding: P17-T04 ───────────────────────────────────────────────────
	// GET  /onboarding/progress    — current onboarding progress
	// POST /onboarding/step        — mark a step completed
	// POST /onboarding/complete    — mark onboarding finished
	mux.HandleFunc("/onboarding/progress", s.handleOnboardingProgress)
	mux.HandleFunc("/onboarding/step", s.handleOnboardingStep)
	mux.HandleFunc("/onboarding/complete", s.handleOnboardingComplete)

	// ── Email Preferences: P17-T05 ────────────────────────────────────────────
	// GET  /email/preferences   — get subscriber's email preferences
	// POST /email/preferences   — update email preferences
	// GET  /email/unsubscribe   — one-click unsubscribe (token in query param)
	mux.HandleFunc("/email/preferences", s.handleEmailPreferences)
	mux.HandleFunc("/email/unsubscribe", s.handleEmailUnsubscribe)

	// ── Analytics: P17-T06 ────────────────────────────────────────────────────
	// GET /admin/analytics/cohorts     — subscriber retention by cohort
	// GET /admin/analytics/summary     — MRR, churn, LTV snapshot
	mux.HandleFunc("/admin/analytics/cohorts", s.handleAnalyticsCohorts)
	mux.HandleFunc("/admin/analytics/summary", s.handleAnalyticsSummary)
	mux.HandleFunc("/admin/analytics/subscribers", s.handleAdminAnalytics)
	// ── COPPA Compliance: P22.4 ───────────────────────────────────────────────
	// POST /kids/profiles/:id/delete-data    — parent deletes all child data
	// GET  /kids/profiles/:id/settings       — COPPA-compliant settings for kids profile
	mux.HandleFunc("/kids/profiles/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "" && len(r.URL.Path) > 0 {
			if hasPathSuffix(r.URL.Path, "delete-data") {
				s.handleKidsDataDeletion(w, r)
			} else if hasPathSuffix(r.URL.Path, "settings") {
				s.handleKidsProfileSettings(w, r)
			} else {
				writeError(w, 404, "not_found", "endpoint not found")
			}
		}
	})

	// ── Update check: P22.6 ───────────────────────────────────────────────────
	// GET /update/check — version comparison for self-hosters (no auth required)
	mux.HandleFunc("/update/check", s.handleUpdateCheck)

	// ── GDPR erasure: P16-T04 ─────────────────────────────────────────────────
	// DELETE /gdpr/me — GDPR right to erasure (immediate hard-delete)
	mux.HandleFunc("/gdpr/me", s.handleGDPRErasure)

	// ── P22.2: JWT Revocation + P22.5: Security routes ───────────────────────
	s.registerRoutesP22(mux)
}

// RegisterRoutesP22 adds P22 security routes to an existing mux.
// Called from RegisterRoutes after all other routes are registered.
func (s *Server) registerRoutesP22(mux *http.ServeMux) {
	// ── P22.2: JWT Revocation ─────────────────────────────────────────────────
	// POST /auth/logout — revoke current JWT + invalidate refresh tokens
	mux.HandleFunc("/auth/logout", s.handleLogout)
}
