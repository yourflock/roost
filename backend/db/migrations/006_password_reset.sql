-- Phase 2: Password reset tokens
-- Structure mirrors email_verification_tokens but expires in 1 hour

CREATE TABLE password_reset_tokens (
  id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  subscriber_id UUID NOT NULL REFERENCES subscribers(id) ON DELETE CASCADE,
  token_hash    TEXT NOT NULL,
  expires_at    TIMESTAMPTZ NOT NULL,
  used_at       TIMESTAMPTZ,
  created_at    TIMESTAMPTZ DEFAULT now()
);

CREATE INDEX idx_password_reset_hash       ON password_reset_tokens(token_hash);
CREATE INDEX idx_password_reset_subscriber ON password_reset_tokens(subscriber_id);
