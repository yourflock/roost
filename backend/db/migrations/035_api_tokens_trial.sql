-- 035_api_tokens_trial.sql — Extend api_tokens for trial subscriptions (P17-T01).
-- Adds subscription_id link so tokens can be deactivated when trials expire.
-- Also adds a raw token column for the subset of tokens shown once at issuance.

-- Link to subscriptions (nullable — tokens created before trials don't have one).
ALTER TABLE api_tokens
  ADD COLUMN IF NOT EXISTS subscription_id UUID REFERENCES subscriptions(id) ON DELETE CASCADE,
  ADD COLUMN IF NOT EXISTS token           TEXT; -- raw token for issuance; stored separately from hash

-- Index for expiry lookups in the trial notifier.
CREATE INDEX IF NOT EXISTS idx_api_tokens_subscription ON api_tokens(subscription_id)
  WHERE subscription_id IS NOT NULL;

-- Alias 'token' -> store alongside hash for trial token issuance flow.
COMMENT ON COLUMN api_tokens.token IS 'Raw token (base64). Populated at issuance; cleared after 24h by ops.';
