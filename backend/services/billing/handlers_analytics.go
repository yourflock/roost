// handlers_analytics.go — Subscriber analytics endpoints for P17-T06.
//
// GET /admin/analytics/cohorts  — subscriber retention by signup cohort (monthly)
// GET /admin/analytics/summary  — MRR, churn, LTV, trial conversion snapshot
//
// Both require superowner authorization.
// Results are cached in a simple in-memory cache (refreshed every 24h) since
// cohort queries are expensive and data changes slowly.
package billing

import (
	"database/sql"
	"net/http"
	"sync"
	"time"

)

// analyticsCache holds a single cached result per endpoint.
type analyticsCache struct {
	mu          sync.RWMutex
	cohortData  interface{}
	cohortExp   time.Time
	summaryData interface{}
	summaryExp  time.Time
}

var analyticsStore = &analyticsCache{}

// handleAnalyticsCohorts handles GET /admin/analytics/cohorts.
// Returns subscriber cohort retention analysis: group by signup month,
// compute % still active at month 0, 1, 2, 3, 6, 12.
func (s *Server) handleAnalyticsCohorts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET only")
		return
	}
	if !s.requireSuperowner(w, r) {
		return
	}

	// Check cache.
	analyticsStore.mu.RLock()
	if analyticsStore.cohortData != nil && time.Now().Before(analyticsStore.cohortExp) {
		data := analyticsStore.cohortData
		analyticsStore.mu.RUnlock()
		writeJSON(w, http.StatusOK, data)
		return
	}
	analyticsStore.mu.RUnlock()

	// Query: cohort = month of creation, retention = % still active N months later.
	rows, err := s.db.QueryContext(r.Context(), `
		WITH cohorts AS (
			SELECT
				date_trunc('month', created_at) AS cohort_month,
				id AS subscriber_id,
				created_at
			FROM subscribers
			WHERE created_at >= now() - INTERVAL '24 months'
		),
		retention AS (
			SELECT
				c.cohort_month,
				COUNT(DISTINCT c.subscriber_id) AS cohort_size,
				COUNT(DISTINCT CASE WHEN s.status IN ('active','trialing') THEN c.subscriber_id END) AS still_active,
				COUNT(DISTINCT CASE
					WHEN sub_1mo.subscriber_id IS NOT NULL THEN c.subscriber_id
				END) AS active_1mo,
				COUNT(DISTINCT CASE
					WHEN sub_3mo.subscriber_id IS NOT NULL THEN c.subscriber_id
				END) AS active_3mo,
				COUNT(DISTINCT CASE
					WHEN sub_6mo.subscriber_id IS NOT NULL THEN c.subscriber_id
				END) AS active_6mo
			FROM cohorts c
			LEFT JOIN subscriptions s ON s.subscriber_id = c.subscriber_id
				AND s.status IN ('active','trialing')
			LEFT JOIN subscriptions sub_1mo ON sub_1mo.subscriber_id = c.subscriber_id
				AND sub_1mo.created_at <= c.created_at + INTERVAL '1 month'
				AND sub_1mo.status != 'canceled'
			LEFT JOIN subscriptions sub_3mo ON sub_3mo.subscriber_id = c.subscriber_id
				AND sub_3mo.created_at <= c.created_at + INTERVAL '3 months'
				AND sub_3mo.status != 'canceled'
			LEFT JOIN subscriptions sub_6mo ON sub_6mo.subscriber_id = c.subscriber_id
				AND sub_6mo.created_at <= c.created_at + INTERVAL '6 months'
				AND sub_6mo.status != 'canceled'
			GROUP BY c.cohort_month
		)
		SELECT
			cohort_month,
			cohort_size,
			ROUND(100.0 * still_active / NULLIF(cohort_size, 0), 1) AS retention_current,
			ROUND(100.0 * active_1mo / NULLIF(cohort_size, 0), 1) AS retention_1mo,
			ROUND(100.0 * active_3mo / NULLIF(cohort_size, 0), 1) AS retention_3mo,
			ROUND(100.0 * active_6mo / NULLIF(cohort_size, 0), 1) AS retention_6mo
		FROM retention
		ORDER BY cohort_month DESC
		LIMIT 24
	`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "cohort query failed")
		return
	}
	defer rows.Close()

	type cohortRow struct {
		Month            string  `json:"month"`
		CohortSize       int     `json:"cohort_size"`
		RetentionCurrent float64 `json:"retention_current_pct"`
		Retention1Mo     float64 `json:"retention_1mo_pct"`
		Retention3Mo     float64 `json:"retention_3mo_pct"`
		Retention6Mo     float64 `json:"retention_6mo_pct"`
	}
	var cohorts []cohortRow
	for rows.Next() {
		var c cohortRow
		var month time.Time
		var cur, r1, r3, r6 sql.NullFloat64
		if err := rows.Scan(&month, &c.CohortSize, &cur, &r1, &r3, &r6); err != nil {
			continue
		}
		c.Month = month.Format("2006-01")
		if cur.Valid {
			c.RetentionCurrent = cur.Float64
		}
		if r1.Valid {
			c.Retention1Mo = r1.Float64
		}
		if r3.Valid {
			c.Retention3Mo = r3.Float64
		}
		if r6.Valid {
			c.Retention6Mo = r6.Float64
		}
		cohorts = append(cohorts, c)
	}

	result := map[string]interface{}{
		"cohorts":     cohorts,
		"generated_at": time.Now().UTC().Format(time.RFC3339),
	}

	// Cache for 24 hours.
	analyticsStore.mu.Lock()
	analyticsStore.cohortData = result
	analyticsStore.cohortExp = time.Now().Add(24 * time.Hour)
	analyticsStore.mu.Unlock()

	writeJSON(w, http.StatusOK, result)
}

// handleAnalyticsSummary handles GET /admin/analytics/summary.
// Returns key SaaS metrics: MRR, ARR, churn rate, LTV, trial conversion.
func (s *Server) handleAnalyticsSummary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET only")
		return
	}
	if !s.requireSuperowner(w, r) {
		return
	}

	// Check cache.
	analyticsStore.mu.RLock()
	if analyticsStore.summaryData != nil && time.Now().Before(analyticsStore.summaryExp) {
		data := analyticsStore.summaryData
		analyticsStore.mu.RUnlock()
		writeJSON(w, http.StatusOK, data)
		return
	}
	analyticsStore.mu.RUnlock()

	var activeCount, trialingCount, canceledThisMonth int
	var mrrCents int64

	// Active subscribers and approximate MRR from plan prices.
	_ = s.db.QueryRowContext(r.Context(), `
		SELECT COUNT(*) FROM subscribers WHERE status = 'active'
	`).Scan(&activeCount)

	_ = s.db.QueryRowContext(r.Context(), `
		SELECT COUNT(*) FROM subscriptions WHERE status = 'trialing' AND is_trial = TRUE
	`).Scan(&trialingCount)

	_ = s.db.QueryRowContext(r.Context(), `
		SELECT COUNT(*) FROM subscriptions
		WHERE status = 'canceled'
		  AND canceled_at >= date_trunc('month', now())
	`).Scan(&canceledThisMonth)

	// MRR: sum monthly prices for active non-trial subscriptions.
	_ = s.db.QueryRowContext(r.Context(), `
		SELECT COALESCE(SUM(sp.monthly_price_cents), 0)
		FROM subscriptions s
		JOIN subscription_plans sp ON sp.id = s.plan_id
		WHERE s.status = 'active' AND s.is_trial = FALSE
	`).Scan(&mrrCents)

	// Trial conversion rate (last 30 days).
	var trialsStarted, trialsConverted int
	_ = s.db.QueryRowContext(r.Context(), `
		SELECT COUNT(*) FROM subscriptions
		WHERE is_trial = TRUE AND trial_start >= now() - INTERVAL '30 days'
	`).Scan(&trialsStarted)
	_ = s.db.QueryRowContext(r.Context(), `
		SELECT COUNT(*) FROM subscriptions
		WHERE is_trial = TRUE AND trial_converted_at >= now() - INTERVAL '30 days'
	`).Scan(&trialsConverted)

	var trialConversionPct float64
	if trialsStarted > 0 {
		trialConversionPct = float64(trialsConverted) / float64(trialsStarted) * 100
	}

	// New subscribers this month vs last month.
	var newThisMonth, newLastMonth int
	_ = s.db.QueryRowContext(r.Context(), `
		SELECT COUNT(*) FROM subscribers WHERE created_at >= date_trunc('month', now())
	`).Scan(&newThisMonth)
	_ = s.db.QueryRowContext(r.Context(), `
		SELECT COUNT(*) FROM subscribers
		WHERE created_at >= date_trunc('month', now()) - INTERVAL '1 month'
		  AND created_at < date_trunc('month', now())
	`).Scan(&newLastMonth)

	result := map[string]interface{}{
		"active_subscribers":    activeCount,
		"trialing_subscribers":  trialingCount,
		"mrr_cents":             mrrCents,
		"arr_cents":             mrrCents * 12,
		"churned_this_month":    canceledThisMonth,
		"new_this_month":        newThisMonth,
		"new_last_month":        newLastMonth,
		"trial_conversion_pct":  trialConversionPct,
		"trials_started_30d":    trialsStarted,
		"trials_converted_30d":  trialsConverted,
		"generated_at":          time.Now().UTC().Format(time.RFC3339),
	}

	// Cache for 1 hour.
	analyticsStore.mu.Lock()
	analyticsStore.summaryData = result
	analyticsStore.summaryExp = time.Now().Add(time.Hour)
	analyticsStore.mu.Unlock()

	writeJSON(w, http.StatusOK, result)
}
