// analytics_collector.go — Daily subscriber analytics aggregator for P17-T05.
//
// Runs daily at midnight UTC via goroutine cron.
// Computes and upserts:
//   - daily_metrics: aggregate counts, MRR, stream hours for the previous calendar day
//   - subscriber_lifecycle: lifecycle stage per subscriber
//   - subscriber_engagement: engagement score per subscriber
//   - churn_risk: churn risk flags per subscriber
//
// GET /admin/analytics/subscribers — last 30 days of daily_metrics
package billing

import (
	"strings"
	"log"
	"math"
	"net/http"
	"time"

	"github.com/yourflock/roost/internal/auth"
)

const analyticsCollectorInterval = 1 * time.Hour // check every hour, compute once per day

// startAnalyticsCollector runs the analytics cron.
func (s *Server) startAnalyticsCollector() {
	go func() {
		log.Println("Analytics collector: started")
		lastRun := time.Time{}
		for {
			now := time.Now().UTC()
			// Run once per day — when the day changes since last run.
			if now.Day() != lastRun.Day() || now.Month() != lastRun.Month() {
				s.runAnalyticsCycle(now)
				lastRun = now
			}
			time.Sleep(analyticsCollectorInterval)
		}
	}()
}

// runAnalyticsCycle computes all analytics for the given time.
func (s *Server) runAnalyticsCycle(now time.Time) {
	yesterday := now.AddDate(0, 0, -1).Format("2006-01-02")
	log.Printf("Analytics: computing metrics for %s", yesterday)
	s.computeDailyMetrics(yesterday)
	s.computeLifecycleStages()
	s.computeEngagementScores()
	s.computeChurnRisk()
}

// computeDailyMetrics aggregates yesterday's subscriber and revenue stats.
func (s *Server) computeDailyMetrics(dateStr string) {
	var totalSubs, trialStarts, trialConversions, newSubs, cancellations int
	var mrrCents, grossRevenueCents, refundCents int64
	var streamHours float64
	var uniqueStreamers int

	// Active paid subscribers at end of day.
	_ = s.db.QueryRow(`
		SELECT COUNT(*) FROM subscriptions
		WHERE status = 'active' AND created_at::date <= $1
	`, dateStr).Scan(&totalSubs)

	// Trial starts on this day.
	_ = s.db.QueryRow(`
		SELECT COUNT(*) FROM subscriptions
		WHERE is_trial = TRUE AND trial_start::date = $1
	`, dateStr).Scan(&trialStarts)

	// Trial conversions on this day.
	_ = s.db.QueryRow(`
		SELECT COUNT(*) FROM subscriptions
		WHERE trial_converted_at::date = $1
	`, dateStr).Scan(&trialConversions)

	// New paid subscriptions (non-trial) on this day.
	_ = s.db.QueryRow(`
		SELECT COUNT(*) FROM subscriptions
		WHERE status = 'active' AND is_trial = FALSE AND created_at::date = $1
	`, dateStr).Scan(&newSubs)

	// Cancellations on this day.
	_ = s.db.QueryRow(`
		SELECT COUNT(*) FROM subscriptions
		WHERE status = 'canceled' AND updated_at::date = $1
	`, dateStr).Scan(&cancellations)

	// MRR: sum of monthly plan prices for all active subscriptions.
	_ = s.db.QueryRow(`
		SELECT COALESCE(SUM(
			CASE s.billing_period
				WHEN 'monthly' THEN sp.price_monthly_cents
				WHEN 'annual'  THEN sp.price_annual_cents / 12
				ELSE 0
			END
		), 0)
		FROM subscriptions s
		JOIN subscription_plans sp ON sp.id = s.plan_id
		WHERE s.status = 'active' AND s.created_at::date <= $1
	`, dateStr).Scan(&mrrCents)

	// Stream hours and unique streamers (from stream_sessions table if it exists).
	_ = s.db.QueryRow(`
		SELECT
			COALESCE(SUM(EXTRACT(EPOCH FROM (ended_at - started_at)) / 3600), 0),
			COUNT(DISTINCT subscriber_id)
		FROM stream_sessions
		WHERE started_at::date = $1 AND ended_at IS NOT NULL
	`, dateStr).Scan(&streamHours, &uniqueStreamers)

	_, err := s.db.Exec(`
		INSERT INTO daily_metrics
			(metric_date, total_subscribers, trial_starts, trial_conversions,
			 new_subscriptions, cancellations, mrr_cents, gross_revenue_cents,
			 refunds_cents, stream_hours, unique_streamers)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		ON CONFLICT (metric_date) DO UPDATE SET
			total_subscribers  = EXCLUDED.total_subscribers,
			trial_starts       = EXCLUDED.trial_starts,
			trial_conversions  = EXCLUDED.trial_conversions,
			new_subscriptions  = EXCLUDED.new_subscriptions,
			cancellations      = EXCLUDED.cancellations,
			mrr_cents          = EXCLUDED.mrr_cents,
			gross_revenue_cents = EXCLUDED.gross_revenue_cents,
			refunds_cents      = EXCLUDED.refunds_cents,
			stream_hours       = EXCLUDED.stream_hours,
			unique_streamers   = EXCLUDED.unique_streamers,
			computed_at        = now()
	`, dateStr, totalSubs, trialStarts, trialConversions, newSubs, cancellations,
		mrrCents, grossRevenueCents, refundCents, math.Round(streamHours*100)/100, uniqueStreamers)
	if err != nil {
		log.Printf("Analytics: daily_metrics upsert failed: %v", err)
	}
}

// computeLifecycleStages computes the lifecycle stage for every subscriber.
func (s *Server) computeLifecycleStages() {
	_, err := s.db.Exec(`
		INSERT INTO subscriber_lifecycle (subscriber_id, lifecycle_stage, last_stream_at, days_since_stream, total_stream_hours, computed_at)
		SELECT
			sub.id,
			CASE
				WHEN s.is_trial = TRUE AND s.status = 'trialing'  THEN 'trial'
				WHEN s.status IN ('canceled','trial_expired')      THEN 'churned'
				WHEN ss.last_stream IS NULL AND s.created_at > now() - INTERVAL '30 days' THEN 'new'
				WHEN ss.last_stream >= now() - INTERVAL '14 days' THEN 'active'
				WHEN ss.last_stream >= now() - INTERVAL '30 days' THEN 'at_risk'
				WHEN ss.last_stream IS NOT NULL                    THEN 'dormant'
				ELSE 'new'
			END,
			ss.last_stream,
			COALESCE(EXTRACT(DAY FROM now() - ss.last_stream)::int, 0),
			COALESCE(ss.total_hours, 0),
			now()
		FROM subscribers sub
		LEFT JOIN subscriptions s ON s.subscriber_id = sub.id AND s.id = (
			SELECT id FROM subscriptions WHERE subscriber_id = sub.id ORDER BY created_at DESC LIMIT 1
		)
		LEFT JOIN (
			SELECT
				subscriber_id,
				MAX(started_at) AS last_stream,
				SUM(EXTRACT(EPOCH FROM (ended_at - started_at)) / 3600) AS total_hours
			FROM stream_sessions WHERE ended_at IS NOT NULL
			GROUP BY subscriber_id
		) ss ON ss.subscriber_id = sub.id
		ON CONFLICT (subscriber_id) DO UPDATE SET
			lifecycle_stage    = EXCLUDED.lifecycle_stage,
			last_stream_at     = EXCLUDED.last_stream_at,
			days_since_stream  = EXCLUDED.days_since_stream,
			total_stream_hours = EXCLUDED.total_stream_hours,
			computed_at        = EXCLUDED.computed_at
	`)
	if err != nil {
		log.Printf("Analytics: lifecycle compute failed: %v", err)
	}
}

// computeEngagementScores computes engagement scores (0-100) for all active subscribers.
func (s *Server) computeEngagementScores() {
	_, err := s.db.Exec(`
		INSERT INTO subscriber_engagement (subscriber_id, score, stream_hours_7d, days_active_7d, channels_watched_7d, computed_at)
		SELECT
			sub.id,
			-- Hours score: 0h=0, 1h=10, 5h=20, 10h=30, 20h+=40
			LEAST(40, CASE
				WHEN COALESCE(w.hours_7d, 0) >= 20 THEN 40
				WHEN COALESCE(w.hours_7d, 0) >= 10 THEN 30
				WHEN COALESCE(w.hours_7d, 0) >= 5  THEN 20
				WHEN COALESCE(w.hours_7d, 0) >= 1  THEN 10
				ELSE 0
			END)
			-- Days active score: 0=0, 1-2=10, 3-4=20, 5-7=30
			+ CASE
				WHEN COALESCE(w.days_7d, 0) >= 5 THEN 30
				WHEN COALESCE(w.days_7d, 0) >= 3 THEN 20
				WHEN COALESCE(w.days_7d, 0) >= 1 THEN 10
				ELSE 0
			END
			-- Channel variety score: 1=5, 2-3=10, 4+=15
			+ CASE
				WHEN COALESCE(w.channels_7d, 0) >= 4 THEN 15
				WHEN COALESCE(w.channels_7d, 0) >= 2 THEN 10
				WHEN COALESCE(w.channels_7d, 0) >= 1 THEN 5
				ELSE 0
			END AS score,
			COALESCE(w.hours_7d, 0),
			COALESCE(w.days_7d, 0),
			COALESCE(w.channels_7d, 0),
			now()
		FROM subscribers sub
		LEFT JOIN (
			SELECT
				subscriber_id,
				SUM(EXTRACT(EPOCH FROM (ended_at - started_at)) / 3600) AS hours_7d,
				COUNT(DISTINCT started_at::date) AS days_7d,
				COUNT(DISTINCT channel_id) AS channels_7d
			FROM stream_sessions
			WHERE started_at >= now() - INTERVAL '7 days' AND ended_at IS NOT NULL
			GROUP BY subscriber_id
		) w ON w.subscriber_id = sub.id
		ON CONFLICT (subscriber_id) DO UPDATE SET
			score               = EXCLUDED.score,
			stream_hours_7d     = EXCLUDED.stream_hours_7d,
			days_active_7d      = EXCLUDED.days_active_7d,
			channels_watched_7d = EXCLUDED.channels_watched_7d,
			computed_at         = EXCLUDED.computed_at
	`)
	if err != nil {
		log.Printf("Analytics: engagement compute failed: %v", err)
	}
}

// computeChurnRisk computes risk flags and risk level for each subscriber.
func (s *Server) computeChurnRisk() {
	rows, err := s.db.Query(`
		SELECT
			sub.id,
			s.status,
			COALESCE(se.score, 0),
			COALESCE(sl.days_since_stream, 0),
			COALESCE(sl.lifecycle_stage, 'new'),
			COALESCE(se.channels_watched_7d, 0)
		FROM subscribers sub
		LEFT JOIN subscriptions s ON s.subscriber_id = sub.id AND s.id = (
			SELECT id FROM subscriptions WHERE subscriber_id = sub.id ORDER BY created_at DESC LIMIT 1
		)
		LEFT JOIN subscriber_engagement se ON se.subscriber_id = sub.id
		LEFT JOIN subscriber_lifecycle sl ON sl.subscriber_id = sub.id
		WHERE s.status IN ('active', 'trialing', 'past_due')
	`)
	if err != nil {
		log.Printf("Analytics: churn risk query failed: %v", err)
		return
	}
	defer rows.Close()

	for rows.Next() {
		var subID, subStatus, lifecycleStage string
		var score, daysSinceStream, channelsWatched7d int
		if err := rows.Scan(&subID, &subStatus, &score, &daysSinceStream, &lifecycleStage, &channelsWatched7d); err != nil {
			continue
		}

		var flags []string
		if subStatus == "past_due" {
			flags = append(flags, "payment_failed")
		}
		if daysSinceStream >= 14 {
			flags = append(flags, "no_recent_stream")
		}
		if channelsWatched7d <= 1 && daysSinceStream < 14 {
			flags = append(flags, "single_channel")
		}

		riskLevel := "low"
		switch {
		case len(flags) >= 3:
			riskLevel = "high"
		case len(flags) >= 1:
			riskLevel = "medium"
		}

		flagsJSON := "[]"
		if len(flags) > 0 {
			flagsJSON = `["` + strings.Join(flags, `","`) + `"]`
		}

		_, _ = s.db.Exec(`
			INSERT INTO churn_risk (subscriber_id, risk_level, risk_flags, computed_at)
			VALUES ($1, $2, $3::jsonb, now())
			ON CONFLICT (subscriber_id) DO UPDATE SET
				risk_level  = EXCLUDED.risk_level,
				risk_flags  = EXCLUDED.risk_flags,
				computed_at = EXCLUDED.computed_at
		`, subID, riskLevel, flagsJSON)
	}
}

// handleAdminAnalytics serves GET /admin/analytics/subscribers.
// Returns last 30 days of daily_metrics plus summary stats.
func (s *Server) handleAdminAnalytics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		auth.WriteError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET required")
		return
	}
	claims, err := auth.ValidateJWT(r)
	if err != nil {
		auth.WriteError(w, http.StatusUnauthorized, "unauthorized", "valid JWT required")
		return
	}
	var isSuperowner bool
	_ = s.db.QueryRow(`SELECT is_superowner FROM subscribers WHERE id = $1`, claims.Subject).Scan(&isSuperowner)
	if !isSuperowner {
		auth.WriteError(w, http.StatusForbidden, "forbidden", "superowner access required")
		return
	}

	// Fetch last 30 days.
	rows, err := s.db.Query(`
		SELECT
			metric_date, total_subscribers, trial_starts, trial_conversions,
			new_subscriptions, cancellations, mrr_cents, stream_hours, unique_streamers
		FROM daily_metrics
		ORDER BY metric_date DESC
		LIMIT 30
	`)
	if err != nil {
		auth.WriteError(w, http.StatusInternalServerError, "db_error", "failed to query metrics")
		return
	}
	defer rows.Close()

	type dayRow struct {
		Date             string  `json:"date"`
		TotalSubscribers int     `json:"total_subscribers"`
		TrialStarts      int     `json:"trial_starts"`
		TrialConversions int     `json:"trial_conversions"`
		NewSubscriptions int     `json:"new_subscriptions"`
		Cancellations    int     `json:"cancellations"`
		MRRCents         int64   `json:"mrr_cents"`
		StreamHours      float64 `json:"stream_hours"`
		UniqueStreamers  int     `json:"unique_streamers"`
	}

	var days []dayRow
	for rows.Next() {
		var d dayRow
		var date time.Time
		if err := rows.Scan(&date, &d.TotalSubscribers, &d.TrialStarts, &d.TrialConversions,
			&d.NewSubscriptions, &d.Cancellations, &d.MRRCents, &d.StreamHours, &d.UniqueStreamers); err != nil {
			continue
		}
		d.Date = date.Format("2006-01-02")
		days = append(days, d)
	}
	if days == nil {
		days = []dayRow{}
	}

	// Summary: most recent day's MRR + churn rate estimate.
	var latestMRR int64
	var churnRate float64
	if len(days) > 0 {
		latestMRR = days[0].MRRCents
	}
	var cancelledCount, activeCount int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM subscriptions WHERE status='canceled' AND updated_at >= now() - INTERVAL '30 days'`).Scan(&cancelledCount)
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM subscriptions WHERE status='active'`).Scan(&activeCount)
	if activeCount > 0 {
		churnRate = math.Round(float64(cancelledCount)/float64(activeCount)*10000) / 100
	}

	// LTV estimate: MRR / churn rate (basic).
	var ltvCents int64
	if churnRate > 0 {
		ltvCents = int64(float64(latestMRR) / (churnRate / 100))
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"days":              days,
		"latest_mrr_cents":  latestMRR,
		"churn_rate_pct":    churnRate,
		"ltv_estimate_cents": ltvCents,
		"active_subscribers": activeCount,
	})
}
