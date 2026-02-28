-- Phase 2: Email verification tokens
-- Tokens are stored as SHA-256 hashes; raw token is never persisted

CREATE TABLE email_verification_tokens (
  id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  subscriber_id UUID NOT NULL REFERENCES subscribers(id) ON DELETE CASCADE,
  token_hash    TEXT NOT NULL,
  expires_at    TIMESTAMPTZ NOT NULL,
  used_at       TIMESTAMPTZ,
  created_at    TIMESTAMPTZ DEFAULT now()
);

CREATE INDEX idx_email_verify_hash       ON email_verification_tokens(token_hash);
CREATE INDEX idx_email_verify_subscriber ON email_verification_tokens(subscriber_id);
