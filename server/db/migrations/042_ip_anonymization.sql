-- 042_ip_anonymization.sql — IP anonymization for GDPR (P22.3.001).
--
-- Replaces raw IP addresses in audit_log with a BYTEA SHA-256 hash.
-- Raw IPs are personal data under GDPR; hashing prevents direct identification
-- while preserving the ability to correlate events from the same source.
--
-- Migration note: ip_address (INET) is KEPT alongside ip_hash during the
-- transition period. Once all application code writes ip_hash instead of
-- ip_address, a follow-up migration will DROP ip_address. This avoids
-- breaking existing queries during the rollout.

-- Add ip_hash column to audit_log (stores SHA-256 of the client IP).
ALTER TABLE audit_log
  ADD COLUMN IF NOT EXISTS ip_hash BYTEA;

-- Add a comment clarifying the anonymization approach.
COMMENT ON COLUMN audit_log.ip_hash IS
  'SHA-256 hash of the client IP address. Replaces ip_address for GDPR compliance. '
  'Never stores the raw IP. Allows correlation without identification.';

COMMENT ON COLUMN audit_log.ip_address IS
  'DEPRECATED: Raw IP address. Will be removed once ip_hash is fully adopted. '
  'New code must write ip_hash, not ip_address.';

-- Add ip_hash to subscriber_consent table (P22.3.002 uses consent_records but
-- subscriber_consent already exists from migration 038; add ip_hash there too).
ALTER TABLE subscriber_consent
  ADD COLUMN IF NOT EXISTS ip_hash BYTEA;

COMMENT ON COLUMN subscriber_consent.ip_hash IS
  'SHA-256 hash of the IP at time of consent. GDPR-safe alternative to storing raw IP.';

-- Index for correlating audit events by IP hash (security investigations).
CREATE INDEX IF NOT EXISTS idx_audit_log_ip_hash
  ON audit_log (ip_hash)
  WHERE ip_hash IS NOT NULL;
