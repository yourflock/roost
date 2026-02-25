-- 020_flock_link.sql — Add Flock SSO link fields to subscribers table.
-- P13-T01: Flock SSO OAuth Integration

ALTER TABLE subscribers
  ADD COLUMN IF NOT EXISTS flock_user_id VARCHAR(100) UNIQUE,
  ADD COLUMN IF NOT EXISTS flock_family_id VARCHAR(100);

CREATE INDEX IF NOT EXISTS idx_subscribers_flock_user ON subscribers(flock_user_id);
