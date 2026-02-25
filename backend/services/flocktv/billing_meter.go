// billing_meter.go — Stream-hour aggregation and monthly invoice generation (DB-wired).
// Phase FLOCKTV FTV.0.T06 / FTV.3.T01: runs monthly (cron: 1st of each month at 01:00 UTC).
// Aggregates stream_events per family, calculates charges, inserts into monthly_stream_hours,
// and provides the internal billing usage API for Flock's billing system.
//
// Pricing model:
//   Base:  $4.99/month (always charged while flocktv_tier is active)
//   Usage: $0.01 per stream-hour (logged via handleStreamStart/End)
package flocktv

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
)

// StreamHourAggregator aggregates stream_events into billable monthly_stream_hours rows.
type StreamHourAggregator struct {
	db     *sql.DB
	logger *slog.Logger
}

// FamilyUsage holds the computed usage and charges for one family in one billing month.
type FamilyUsage struct {
	FamilyID     string
	BillingMonth time.Time
	StreamHours  float64
	BaseCharge   float64
	UsageCharge  float64
	TotalCharge  float64
}

// NewStreamHourAggregator creates an aggregator.
func NewStreamHourAggregator(db *sql.DB, logger *slog.Logger) *StreamHourAggregator {
	return &StreamHourAggregator{db: db, logger: logger}
}

// AggregateForMonth queries stream_events for all families in the given calendar month
// and returns one FamilyUsage per family with any stream activity.
func (a *StreamHourAggregator) AggregateForMonth(ctx context.Context, month time.Time) ([]FamilyUsage, error) {
	monthStart := time.Date(month.Year(), month.Month(), 1, 0, 0, 0, 0, time.UTC)
	monthEnd := monthStart.AddDate(0, 1, 0)

	a.logger.Info("aggregating stream hours", "month", monthStart.Format("2006-01"))

	rows, err := a.db.QueryContext(ctx, `
		SELECT family_id, COALESCE(SUM(duration_sec), 0)::BIGINT AS total_sec
		FROM stream_events
		WHERE started_at >= $1
		  AND started_at < $2
		  AND duration_sec IS NOT NULL
		GROUP BY family_id`,
		monthStart, monthEnd,
	)
	if err != nil {
		return nil, fmt.Errorf("stream hour aggregation query failed: %w", err)
	}
	defer rows.Close()

	var usages []FamilyUsage
	for rows.Next() {
		var fid string
		var totalSec int64
		if scanErr := rows.Scan(&fid, &totalSec); scanErr != nil {
			continue
		}
		usages = append(usages, FamilyUsage{
			FamilyID:     fid,
			BillingMonth: monthStart,
			StreamHours:  float64(totalSec) / 3600.0,
		})
	}

	return usages, rows.Err()
}

// CalculateCharge computes the Flock TV bill for a FamilyUsage record.
// Base: $4.99/month. Usage: $0.01/stream-hour. Returns the updated record.
func CalculateCharge(usage FamilyUsage) FamilyUsage {
	usage.BaseCharge = 4.99
	usage.UsageCharge = roundToCents(usage.StreamHours * 0.01)
	usage.TotalCharge = roundToCents(usage.BaseCharge + usage.UsageCharge)
	return usage
}

// GenerateMonthlyInvoices aggregates the previous calendar month, calculates charges,
// upserts rows into monthly_stream_hours, and logs results.
// Call this from a cron job on the 1st of each month at 01:00 UTC.
func (a *StreamHourAggregator) GenerateMonthlyInvoices(ctx context.Context) error {
	lastMonth := time.Now().UTC().AddDate(0, -1, 0)
	billingMonth := time.Date(lastMonth.Year(), lastMonth.Month(), 1, 0, 0, 0, 0, time.UTC)

	usages, err := a.AggregateForMonth(ctx, billingMonth)
	if err != nil {
		return fmt.Errorf("stream hour aggregation failed for %s: %w", billingMonth.Format("2006-01"), err)
	}

	for _, raw := range usages {
		charged := CalculateCharge(raw)

		_, upsertErr := a.db.ExecContext(ctx, `
			INSERT INTO monthly_stream_hours
			  (family_id, billing_month, stream_hours, base_charge, usage_charge, updated_at)
			VALUES ($1, $2, $3, $4, $5, NOW())
			ON CONFLICT (family_id, billing_month) DO UPDATE
			  SET stream_hours  = EXCLUDED.stream_hours,
			      base_charge   = EXCLUDED.base_charge,
			      usage_charge  = EXCLUDED.usage_charge,
			      updated_at    = NOW()`,
			charged.FamilyID,
			charged.BillingMonth,
			charged.StreamHours,
			charged.BaseCharge,
			charged.UsageCharge,
		)
		if upsertErr != nil {
			a.logger.Error("monthly_stream_hours upsert failed",
				"family_id", charged.FamilyID,
				"billing_month", charged.BillingMonth.Format("2006-01"),
				"error", upsertErr.Error(),
			)
			continue
		}

		a.logger.Info("monthly invoice generated",
			"family_id", charged.FamilyID,
			"billing_month", charged.BillingMonth.Format("2006-01"),
			"stream_hours", fmt.Sprintf("%.2f", charged.StreamHours),
			"base_charge", charged.BaseCharge,
			"usage_charge", charged.UsageCharge,
			"total_charge", charged.TotalCharge,
		)
	}

	a.logger.Info("monthly invoice run complete",
		"billing_month", billingMonth.Format("2006-01"),
		"families_invoiced", len(usages),
	)
	return nil
}

// roundToCents rounds a float to 2 decimal places for monetary values.
func roundToCents(v float64) float64 {
	return float64(int(v*100+0.5)) / 100
}

// ──────────────────────────────────────────────────────────────────────────────
// Internal billing usage API handlers — FTV.0.T06
// ──────────────────────────────────────────────────────────────────────────────

// billingUsageRequest is the params for a single-family usage query.
type billingUsageRequest struct {
	FamilyID    string `json:"family_id"`
	PeriodStart string `json:"period_start"` // ISO8601 date
	PeriodEnd   string `json:"period_end"`   // ISO8601 date
}

// billingUsageResponse is the response for a single-family usage query.
type billingUsageResponse struct {
	FamilyID    string  `json:"family_id"`
	PeriodStart string  `json:"period_start"`
	PeriodEnd   string  `json:"period_end"`
	StreamHours float64 `json:"stream_hours"`
}

// parsePeriod parses an ISO8601 date or datetime string.
// Returns the time and an end-of-day adjustment bool (true if date-only).
func parsePeriod(s string) (time.Time, bool, error) {
	t, err := time.Parse(time.RFC3339, s)
	if err == nil {
		return t, false, nil
	}
	t, err = time.Parse("2006-01-02", s)
	if err == nil {
		return t, true, nil
	}
	return time.Time{}, false, fmt.Errorf("invalid date/time: %s", s)
}

// handleBillingUsage returns stream hours for a specific family and billing period.
// GET /internal/billing/usage?family_id={id}&period_start={ISO8601}&period_end={ISO8601}
// Protected by X-Flock-Internal-Secret header.
func (s *Server) handleBillingUsage(w http.ResponseWriter, r *http.Request) {
	if !checkInternalSecret(w, r) {
		return
	}

	q := r.URL.Query()
	familyID := q.Get("family_id")
	periodStart := q.Get("period_start")
	periodEnd := q.Get("period_end")

	if familyID == "" || periodStart == "" || periodEnd == "" {
		writeError(w, http.StatusBadRequest, "missing_params",
			"family_id, period_start, and period_end are required")
		return
	}

	start, _, err := parsePeriod(periodStart)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_period_start", err.Error())
		return
	}
	end, isDateOnly, err := parsePeriod(periodEnd)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_period_end", err.Error())
		return
	}
	if isDateOnly {
		end = end.AddDate(0, 0, 1)
	}

	// Reject periods longer than 35 days (billing period max).
	if end.Sub(start) > 35*24*time.Hour {
		writeError(w, http.StatusBadRequest, "period_too_long",
			"billing period must not exceed 35 days")
		return
	}

	// Try Redis cache first.
	cacheKey := fmt.Sprintf("billing:usage:%s:%s:%s", familyID, periodStart, periodEnd)
	if s.rdb != nil {
		if cached, cacheErr := s.rdb.Get(r.Context(), cacheKey).Result(); cacheErr == nil {
			var resp billingUsageResponse
			if jsonErr := json.Unmarshal([]byte(cached), &resp); jsonErr == nil {
				writeJSON(w, http.StatusOK, resp)
				return
			}
		}
	}

	var totalSeconds int64
	if s.db != nil {
		dbErr := s.db.QueryRowContext(r.Context(), `
			SELECT COALESCE(SUM(duration_sec), 0)
			FROM stream_events
			WHERE family_id = $1
			  AND started_at >= $2
			  AND started_at < $3`,
			familyID, start, end,
		).Scan(&totalSeconds)
		if dbErr != nil {
			s.logger.Error("billing usage query failed", "error", dbErr.Error())
			writeError(w, http.StatusInternalServerError, "db_error", "failed to calculate usage")
			return
		}
	}

	streamHours := float64(totalSeconds) / 3600.0
	resp := billingUsageResponse{
		FamilyID:    familyID,
		PeriodStart: periodStart,
		PeriodEnd:   periodEnd,
		StreamHours: roundToCents(streamHours),
	}

	// Cache for 1 hour.
	if s.rdb != nil {
		if jsonBytes, jsonErr := json.Marshal(resp); jsonErr == nil {
			_ = s.rdb.Set(r.Context(), cacheKey, string(jsonBytes), time.Hour).Err()
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

// batchUsageRequest is the body for POST /internal/billing/usage/batch.
type batchUsageRequest struct {
	Families []billingUsageRequest `json:"families"`
}

// handleBillingUsageBatch returns stream hours for multiple families in one call.
// POST /internal/billing/usage/batch
// Prevents N sequential calls from Flock's billing cron.
func (s *Server) handleBillingUsageBatch(w http.ResponseWriter, r *http.Request) {
	if !checkInternalSecret(w, r) {
		return
	}

	var req batchUsageRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	if len(req.Families) == 0 {
		writeError(w, http.StatusBadRequest, "missing_families", "families array is required")
		return
	}
	if len(req.Families) > 500 {
		writeError(w, http.StatusBadRequest, "too_many_families",
			"batch size must not exceed 500")
		return
	}

	results := make([]billingUsageResponse, 0, len(req.Families))

	for _, fam := range req.Families {
		start, _, err := parsePeriod(fam.PeriodStart)
		if err != nil {
			continue
		}
		end, isDateOnly, err := parsePeriod(fam.PeriodEnd)
		if err != nil {
			continue
		}
		if isDateOnly {
			end = end.AddDate(0, 0, 1)
		}
		if end.Sub(start) > 35*24*time.Hour {
			continue
		}

		var totalSeconds int64
		if s.db != nil {
			_ = s.db.QueryRowContext(r.Context(), `
				SELECT COALESCE(SUM(duration_sec), 0)
				FROM stream_events
				WHERE family_id = $1
				  AND started_at >= $2
				  AND started_at < $3`,
				fam.FamilyID, start, end,
			).Scan(&totalSeconds)
		}

		results = append(results, billingUsageResponse{
			FamilyID:    fam.FamilyID,
			PeriodStart: fam.PeriodStart,
			PeriodEnd:   fam.PeriodEnd,
			StreamHours: roundToCents(float64(totalSeconds) / 3600.0),
		})
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"results": results,
		"count":   len(results),
	})
}

// handleAcquisitionStatus returns the acquisition status of a canonical_id.
// GET /flocktv/acquire/{canonical_id}
func (s *Server) handleAcquisitionStatus(w http.ResponseWriter, r *http.Request) {
	canonicalID := chi.URLParam(r, "canonical_id")
	if canonicalID == "" {
		writeError(w, http.StatusBadRequest, "missing_param", "canonical_id is required")
		return
	}

	if s.db == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"canonical_id": canonicalID,
			"status":       "unknown",
		})
		return
	}

	var status, contentType string
	var r2Path *string
	var retryCount int
	err := s.db.QueryRowContext(r.Context(), `
		SELECT status, content_type, r2_path, retry_count
		FROM acquisition_queue
		WHERE canonical_id = $1
		ORDER BY queued_at DESC
		LIMIT 1`,
		canonicalID,
	).Scan(&status, &contentType, &r2Path, &retryCount)

	if err == sql.ErrNoRows {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"canonical_id": canonicalID,
			"status":       "not_queued",
		})
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "failed to check acquisition status")
		return
	}

	resp := map[string]interface{}{
		"canonical_id":  canonicalID,
		"content_type":  contentType,
		"status":        status,
		"retry_count":   retryCount,
	}
	if r2Path != nil {
		resp["r2_path"] = *r2Path
	}

	writeJSON(w, http.StatusOK, resp)
}

// Ensure http is used (for IDE satisfaction).
var _ http.Handler
