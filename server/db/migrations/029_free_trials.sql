-- 029_free_trials.sql — Free trial system for P17-T01.
-- Adds trial columns to subscriptions and creates trial abuse tracking table.

-- Trial columns on subscriptions table.
ALTER TABLE subscriptions
  ADD COLUMN IF NOT EXISTS is_trial             BOOLEAN      NOT NULL DEFAULT FALSE,
  ADD COLUMN IF NOT EXISTS trial_start          TIMESTAMPTZ,
  ADD COLUMN IF NOT EXISTS trial_end            TIMESTAMPTZ,
  ADD COLUMN IF NOT EXISTS trial_converted_at   TIMESTAMPTZ;

-- trial_abuse_tracking prevents repeated free trials using the same email or IP.
CREATE TABLE IF NOT EXISTS trial_abuse_tracking (
  id          UUID   PRIMARY KEY DEFAULT gen_random_uuid(),
  email_hash  TEXT   NOT NULL,  -- SHA-256 of lowercase normalised email — never store plaintext
  ip_address  INET,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_trial_abuse_email  ON trial_abuse_tracking(email_hash);
CREATE INDEX IF NOT EXISTS idx_trial_abuse_ip     ON trial_abuse_tracking(ip_address);
CREATE INDEX IF NOT EXISTS idx_trial_abuse_ip_ts  ON trial_abuse_tracking(ip_address, created_at);

-- trial_notifications tracks which emails have been sent so we never duplicate.
CREATE TABLE IF NOT EXISTS trial_notifications (
  id               UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
  subscription_id  UUID        NOT NULL REFERENCES subscriptions(id) ON DELETE CASCADE,
  type             VARCHAR(20) NOT NULL CHECK (type IN ('day5', 'expiry')),
  sent_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (subscription_id, type)
);

COMMENT ON TABLE trial_abuse_tracking IS
  'SHA-256 hashes of trial email addresses and IPs — prevents multi-trial abuse.';
COMMENT ON TABLE trial_notifications IS
  'Tracks which trial lifecycle emails have been sent to avoid duplicates.';
COMMENT ON COLUMN subscriptions.is_trial IS
  'TRUE while this is an active free trial subscription.';
COMMENT ON COLUMN subscriptions.trial_start IS
  'When the trial period began.';
COMMENT ON COLUMN subscriptions.trial_end IS
  'When the trial period ends (or ended).';
COMMENT ON COLUMN subscriptions.trial_converted_at IS
  'Set when a trialling subscriber converts to a paid plan.';
