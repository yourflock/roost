-- Phase 2: Refresh token storage for server-side session management
-- Supports token rotation and revocation

CREATE TABLE refresh_tokens (
  id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  subscriber_id UUID NOT NULL REFERENCES subscribers(id) ON DELETE CASCADE,
  token_hash    TEXT NOT NULL,
  expires_at    TIMESTAMPTZ NOT NULL,
  revoked_at    TIMESTAMPTZ,
  created_at    TIMESTAMPTZ DEFAULT now()
);

CREATE INDEX idx_refresh_tokens_hash       ON refresh_tokens(token_hash);
CREATE INDEX idx_refresh_tokens_subscriber ON refresh_tokens(subscriber_id);
CREATE INDEX idx_refresh_tokens_expires    ON refresh_tokens(expires_at);
