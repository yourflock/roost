-- 026_audit_log.sql — Audit log table for compliance and security tracking.
-- P16-T01: Structured Logging & Audit Trail
--
-- Records every significant state change performed by admins, subscribers,
-- resellers, or automated system processes. Used for incident response,
-- GDPR compliance, and abuse investigation.
--
-- NOTE: Migration 007 created an earlier audit_log with fewer columns.
-- This migration extends it with the P16 columns. Uses IF NOT EXISTS
-- on column additions so it's safe to re-run.

-- Create the table if migration 007 never ran (fresh install).
CREATE TABLE IF NOT EXISTS audit_log (
  id            UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
  created_at    TIMESTAMPTZ  NOT NULL DEFAULT now()
);

-- Add extended P16 columns — safe to run even if 007 already created the table.
ALTER TABLE audit_log
  ADD COLUMN IF NOT EXISTS actor_type    VARCHAR(20)  NOT NULL DEFAULT 'system',
  ADD COLUMN IF NOT EXISTS actor_id      UUID,
  ADD COLUMN IF NOT EXISTS action        VARCHAR(100),
  ADD COLUMN IF NOT EXISTS resource_type VARCHAR(50)  NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS resource_id   UUID,
  ADD COLUMN IF NOT EXISTS details       JSONB        NOT NULL DEFAULT '{}',
  ADD COLUMN IF NOT EXISTS ip_address    INET,
  ADD COLUMN IF NOT EXISTS user_agent    TEXT;

-- Drop the old constraint if it exists (from 007's action NOT NULL), re-add properly.
ALTER TABLE audit_log ALTER COLUMN action SET NOT NULL;

-- Add CHECK constraint only if it doesn't exist yet.
DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint
    WHERE conname = 'audit_log_actor_type_check'
  ) THEN
    ALTER TABLE audit_log
      ADD CONSTRAINT audit_log_actor_type_check
      CHECK (actor_type IN ('admin','subscriber','system','reseller'));
  END IF;
END$$;

-- Drop old indexes from 007 (ignore errors if they don't exist).
DROP INDEX IF EXISTS idx_audit_log_subscriber;

-- Add P16 indexes.
CREATE INDEX IF NOT EXISTS idx_audit_log_created_at  ON audit_log(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_audit_log_actor_id    ON audit_log(actor_id) WHERE actor_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_audit_log_resource_id ON audit_log(resource_id) WHERE resource_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_audit_log_action      ON audit_log(action);

COMMENT ON TABLE audit_log IS 'Immutable append-only audit trail for all Roost state changes.';
