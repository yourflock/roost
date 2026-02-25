-- 043_consent_tracking.sql — Granular consent records (P22.3.002).
--
-- Creates the consent_records table for tracking analytics, marketing, and
-- functional consent separately per subscriber, as required by GDPR Art. 7.
--
-- NOTE: migration 038 created subscriber_consent for terms/privacy_policy.
-- consent_records adds fine-grained per-category tracking with expiry and source.

CREATE TABLE IF NOT EXISTS consent_records (
  id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  subscriber_id  UUID NOT NULL REFERENCES subscribers(id) ON DELETE CASCADE,
  consent_type   TEXT NOT NULL,      -- 'analytics', 'marketing', 'functional'
  granted        BOOLEAN NOT NULL,   -- TRUE = given, FALSE = withdrawn
  ip_hash        BYTEA,              -- SHA-256 of client IP (GDPR-safe)
  user_agent_hash BYTEA,             -- SHA-256 of User-Agent (GDPR-safe)
  granted_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  expires_at     TIMESTAMPTZ,        -- NULL = no expiry (most consents)
  source         TEXT NOT NULL,      -- 'signup_flow', 'settings', 'api'
  CONSTRAINT valid_consent_type CHECK (consent_type IN ('analytics','marketing','functional'))
);

-- Composite index for the GET /gdpr/consent query (subscriber + type lookups).
CREATE INDEX IF NOT EXISTS consent_subscriber_idx
  ON consent_records (subscriber_id, consent_type);

-- Index for finding active (non-expired) consents.
CREATE INDEX IF NOT EXISTS consent_active_idx
  ON consent_records (subscriber_id, granted_at DESC)
  WHERE expires_at IS NULL OR expires_at > NOW();

-- View: latest consent state per subscriber per type.
-- Consumers read this view to know the current consent status.
CREATE OR REPLACE VIEW subscriber_current_consent AS
SELECT DISTINCT ON (subscriber_id, consent_type)
    subscriber_id,
    consent_type,
    granted,
    source,
    granted_at,
    expires_at
FROM consent_records
ORDER BY subscriber_id, consent_type, granted_at DESC;

COMMENT ON TABLE consent_records IS
  'Immutable append-only consent events. Each row is one consent grant or withdrawal. '
  'Query subscriber_current_consent view for current state.';
