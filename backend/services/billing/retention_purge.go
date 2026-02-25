// retention_purge.go — Automated data retention enforcement (P22.3).
//
// Runs every 24 hours as a background goroutine. For each data category,
// deletes rows older than the configured retention window. Results are
// logged in data_purge_log (migration 039_data_retention.sql).
//
// Retention defaults (from migration):
//   stream_sessions     90 days  (30 for kids profiles — handled by COPPA rule)
//   watch_history       90 days
//   audit_log          365 days
//   stream_errors       30 days
//   email_events        90 days
//   trial_notifications 90 days
//   abuse_reports      180 days
package billing

import (
	"context"
	"log/slog"
	"time"
)

const retentionPurgeInterval = 24 * time.Hour

// startRetentionPurger starts the background purge job.
// Called from main.go alongside other background goroutines.
func (s *Server) startRetentionPurger(logger *slog.Logger) {
	go func() {
		logger.Info("retention purger: started")
		// Run once at start, then on 24h ticker.
		s.runRetentionPurgeCycle(logger)
		ticker := time.NewTicker(retentionPurgeInterval)
		defer ticker.Stop()
		for range ticker.C {
			s.runRetentionPurgeCycle(logger)
		}
	}()
}

// runRetentionPurgeCycle executes all configured purge rules for all categories.
func (s *Server) runRetentionPurgeCycle(logger *slog.Logger) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	rows, err := s.db.QueryContext(ctx, `
		SELECT category, table_name, date_column, retention_days
		FROM data_retention_policies
		WHERE is_active = TRUE
		ORDER BY retention_days ASC
	`)
	if err != nil {
		logger.Warn("retention purger: failed to load policies", "err", err)
		return
	}
	defer rows.Close()

	type policy struct {
		category      string
		tableName     string
		dateColumn    string
		retentionDays int
	}
	var policies []policy
	for rows.Next() {
		var p policy
		if err := rows.Scan(&p.category, &p.tableName, &p.dateColumn, &p.retentionDays); err != nil {
			continue
		}
		policies = append(policies, p)
	}

	for _, p := range policies {
		deleted, err := s.executePurge(ctx, p.tableName, p.dateColumn, p.retentionDays)
		if err != nil {
			logger.Warn("retention purger: purge failed",
				"category", p.category,
				"table", p.tableName,
				"err", err)
			continue
		}

		// Record in purge log (even if 0 rows — shows the job ran).
		_, _ = s.db.ExecContext(ctx, `
			INSERT INTO data_purge_log (category, table_name, rows_deleted, retention_days)
			VALUES ($1, $2, $3, $4)
		`, p.category, p.tableName, deleted, p.retentionDays)

		if deleted > 0 {
			logger.Info("retention purger: purged",
				"category", p.category,
				"table", p.tableName,
				"rows", deleted,
				"retention_days", p.retentionDays)
		}
	}

	// Also purge kids profile data at 30-day retention (COPPA rule 5).
	s.purgeKidsStreamSessions(ctx, logger)

	// Prune expired JWT revocations.
	var pruned int
	if err := s.db.QueryRowContext(ctx, `SELECT prune_revoked_tokens()`).Scan(&pruned); err == nil && pruned > 0 {
		logger.Info("retention purger: pruned revoked tokens", "count", pruned)
	}
}

// executePurge deletes rows in tableName where dateColumn is older than retentionDays.
// Returns the number of rows deleted.
func (s *Server) executePurge(ctx context.Context, tableName, dateColumn string, retentionDays int) (int64, error) {
	// Build query with safe table/column names.
	// These come from the DB (data_retention_policies table), not user input,
	// but we still validate to avoid any accidental injection.
	if !isSafeIdentifier(tableName) || !isSafeIdentifier(dateColumn) {
		return 0, nil
	}

	cutoff := time.Now().AddDate(0, 0, -retentionDays)
	result, err := s.db.ExecContext(ctx,
		`DELETE FROM `+tableName+` WHERE `+dateColumn+` < $1`,
		cutoff,
	)
	if err != nil {
		return 0, err
	}
	n, _ := result.RowsAffected()
	return n, nil
}

// purgeKidsStreamSessions enforces COPPA 30-day retention on kids profiles.
func (s *Server) purgeKidsStreamSessions(ctx context.Context, logger *slog.Logger) {
	cutoff := time.Now().AddDate(0, 0, -30)
	result, err := s.db.ExecContext(ctx, `
		DELETE FROM stream_sessions
		WHERE started_at < $1
		  AND profile_id IN (
			  SELECT id FROM subscriber_profiles WHERE is_kids_profile = TRUE
		  )
	`, cutoff)
	if err != nil {
		logger.Warn("retention purger: kids stream sessions purge failed", "err", err)
		return
	}
	if n, _ := result.RowsAffected(); n > 0 {
		logger.Info("retention purger: kids stream sessions purged (COPPA)", "rows", n)
	}

	// Same for watch_history of kids profiles.
	result, err = s.db.ExecContext(ctx, `
		DELETE FROM watch_history
		WHERE created_at < $1
		  AND profile_id IN (
			  SELECT id FROM subscriber_profiles WHERE is_kids_profile = TRUE
		  )
	`, cutoff)
	if err != nil {
		return
	}
	if n, _ := result.RowsAffected(); n > 0 {
		logger.Info("retention purger: kids watch history purged (COPPA)", "rows", n)
	}
}

// isSafeIdentifier returns true if the string is a safe SQL identifier
// (only lowercase letters, digits, underscores — no spaces, quotes, or dashes).
func isSafeIdentifier(s string) bool {
	if len(s) == 0 || len(s) > 64 {
		return false
	}
	for _, c := range s {
		if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_') {
			return false
		}
	}
	return true
}
