-- 036_data_retention.sql — Data retention schedule (P22.3.002).
-- Defines retention policies per data category.
-- Automated purge jobs read this table to determine what to delete.

CREATE TABLE IF NOT EXISTS data_retention_policies (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    data_category VARCHAR(100) NOT NULL UNIQUE,  -- e.g. 'stream_sessions', 'audit_log'
    retention_days INTEGER NOT NULL,
    description   TEXT,
    is_active     BOOLEAN NOT NULL DEFAULT TRUE,
    last_purge_at TIMESTAMPTZ,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Default retention policies.
INSERT INTO data_retention_policies (data_category, retention_days, description) VALUES
    ('stream_sessions', 90, 'HLS stream session records for billing and analytics'),
    ('audit_log', 365, 'Admin action audit trail (compliance requirement)'),
    ('email_events', 180, 'Email delivery and open tracking events'),
    ('trial_notifications', 365, 'Trial lifecycle notification records'),
    ('abuse_flags_resolved', 90, 'Resolved abuse flags (after resolution)'),
    ('oauth_state_tokens', 1, 'Short-lived OAuth CSRF state tokens (auto-expire in Redis)'),
    ('analytics_daily_metrics', 1095, 'Daily business metrics (3 years for trend analysis)'),
    ('subscriber_consent', 2555, 'Consent records (7 years for legal compliance)')
ON CONFLICT (data_category) DO NOTHING;

-- Purge log: records each automated purge run.
CREATE TABLE IF NOT EXISTS data_purge_log (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    data_category   VARCHAR(100) NOT NULL,
    rows_deleted    INTEGER NOT NULL DEFAULT 0,
    purge_cutoff    TIMESTAMPTZ NOT NULL,
    started_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at    TIMESTAMPTZ,
    error_message   TEXT
);

CREATE INDEX IF NOT EXISTS idx_purge_log_category ON data_purge_log(data_category, started_at DESC);
