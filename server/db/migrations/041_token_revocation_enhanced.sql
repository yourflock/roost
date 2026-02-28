-- 041_token_revocation_enhanced.sql — Enhanced JWT revocation (P22.2.002).
-- NOTE: 040_jwt_revocation.sql created the revoked_tokens table.
-- This migration adds metadata columns and a blocking middleware index.

-- Add revoked_by column (which admin/subscriber triggered the revocation).
ALTER TABLE revoked_tokens
  ADD COLUMN IF NOT EXISTS revoked_by UUID;

-- Add reason column if not already present (040 used DEFAULT 'logout' but no column in all versions).
-- Use DO block to be safe across fresh install vs upgrade.
DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_name = 'revoked_tokens' AND column_name = 'reason'
  ) THEN
    ALTER TABLE revoked_tokens ADD COLUMN reason TEXT NOT NULL DEFAULT 'logout';
  END IF;
END$$;

-- Index for fast expiry lookups (the middleware hotpath — checks jti + expires_at).
CREATE INDEX IF NOT EXISTS jwt_revocation_expires_idx
  ON revoked_tokens (expires_at);

-- Index for per-subscriber revocation (e.g., "revoke all tokens for subscriber X").
CREATE INDEX IF NOT EXISTS jwt_revocation_subscriber_idx
  ON revoked_tokens (subscriber_id, revoked_at DESC)
  WHERE subscriber_id IS NOT NULL;

-- Extend the prune function to accept the new columns gracefully.
CREATE OR REPLACE FUNCTION prune_revoked_tokens() RETURNS INTEGER AS $$
DECLARE
    deleted INTEGER;
BEGIN
    WITH deleted_rows AS (
        DELETE FROM revoked_tokens WHERE expires_at < now() RETURNING 1
    )
    SELECT count(*) INTO deleted FROM deleted_rows;
    RETURN deleted;
END;
$$ LANGUAGE plpgsql;

COMMENT ON COLUMN revoked_tokens.revoked_by IS
  'UUID of the actor that revoked this token (admin ID, subscriber ID, or NULL for system).';
COMMENT ON COLUMN revoked_tokens.reason IS
  'Reason for revocation: logout | password_change | admin_revoke | coppa_deletion | gdpr_erasure';
