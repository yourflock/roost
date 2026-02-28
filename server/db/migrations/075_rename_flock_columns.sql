-- 075_rename_flock_columns.sql
-- Rename Flock-branded columns to generic SSO names.
-- The values and semantics are unchanged — only the column names are updated.

-- subscribers: flock_user_id → sso_user_id, flock_family_id → sso_external_id
ALTER TABLE subscribers
  RENAME COLUMN flock_user_id TO sso_user_id;

ALTER TABLE subscribers
  RENAME COLUMN flock_family_id TO sso_external_id;

-- Update the index
DROP INDEX IF EXISTS idx_subscribers_flock_user;
CREATE UNIQUE INDEX IF NOT EXISTS idx_subscribers_sso_user ON subscribers(sso_user_id);

-- roost_users: flock_user_id → user_id
ALTER TABLE roost_users
  RENAME COLUMN flock_user_id TO user_id;

DROP INDEX IF EXISTS idx_roost_users_flock;
CREATE INDEX IF NOT EXISTS idx_roost_users_user ON roost_users(user_id);

-- Update the unique constraint (must drop and recreate)
ALTER TABLE roost_users
  DROP CONSTRAINT IF EXISTS roost_users_roost_id_flock_user_id_key;

ALTER TABLE roost_users
  ADD CONSTRAINT roost_users_roost_id_user_id_key UNIQUE (roost_id, user_id);

-- audit_log: flock_user_id → user_id (if that column exists)
DO $$
BEGIN
  IF EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_name = 'audit_log' AND column_name = 'flock_user_id'
  ) THEN
    ALTER TABLE audit_log RENAME COLUMN flock_user_id TO user_id;
    DROP INDEX IF EXISTS idx_audit_log_flock_user;
    CREATE INDEX IF NOT EXISTS idx_audit_log_user ON audit_log(user_id);
  END IF;
END $$;
