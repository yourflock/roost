-- Phase 2: Two-factor authentication (TOTP)
-- TOTP secret stored encrypted; backup codes stored as SHA-256 hashes

ALTER TABLE subscribers
  ADD COLUMN totp_secret_encrypted TEXT,
  ADD COLUMN totp_enabled          BOOLEAN DEFAULT FALSE,
  ADD COLUMN totp_verified_at      TIMESTAMPTZ;

CREATE TABLE totp_backup_codes (
  id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  subscriber_id UUID NOT NULL REFERENCES subscribers(id) ON DELETE CASCADE,
  code_hash     TEXT NOT NULL,
  used_at       TIMESTAMPTZ,
  created_at    TIMESTAMPTZ DEFAULT now()
);

CREATE INDEX idx_totp_backup_subscriber ON totp_backup_codes(subscriber_id);
