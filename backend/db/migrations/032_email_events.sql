-- 032_email_events.sql — Email event tracking for P17-T04 onboarding emailer.
-- Tracks sent emails, delivery status, and onboarding-specific state.

-- email_events — log of every email sent by the system.
CREATE TABLE IF NOT EXISTS email_events (
  id              UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
  subscriber_id   UUID        NOT NULL REFERENCES subscribers(id) ON DELETE CASCADE,
  template        VARCHAR(60) NOT NULL,   -- e.g. "welcome", "trial_ending", "day2_setup"
  category        VARCHAR(30) NOT NULL,   -- "transactional" | "onboarding" | "marketing" | "winback"
  status          VARCHAR(20) NOT NULL DEFAULT 'sent'
                    CHECK (status IN ('sent', 'failed', 'bounced', 'opened', 'clicked')),
  elastic_email_id VARCHAR(120),          -- Elastic Email message ID for status tracking
  sent_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_email_events_subscriber ON email_events(subscriber_id, template);
CREATE INDEX IF NOT EXISTS idx_email_events_sent       ON email_events(sent_at DESC);

-- onboarding_progress — per-subscriber onboarding state.
CREATE TABLE IF NOT EXISTS onboarding_progress (
  id                      UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
  subscriber_id           UUID        NOT NULL UNIQUE REFERENCES subscribers(id) ON DELETE CASCADE,
  step_completed          INTEGER     NOT NULL DEFAULT 0,  -- highest step reached (0-5)
  token_copied            BOOLEAN     NOT NULL DEFAULT FALSE,
  owl_connected           BOOLEAN     NOT NULL DEFAULT FALSE,
  first_stream_at         TIMESTAMPTZ,
  onboarding_completed_at TIMESTAMPTZ,
  created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at              TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TRIGGER set_onboarding_updated_at
  BEFORE UPDATE ON onboarding_progress
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();

COMMENT ON TABLE email_events IS
  'Append-only log of all system emails. Used for deduplication, analytics, and delivery tracking.';
COMMENT ON TABLE onboarding_progress IS
  'Per-subscriber onboarding wizard state. Created automatically on subscription.';
COMMENT ON COLUMN email_events.category IS
  'transactional=always sent, onboarding=post-signup sequence, marketing=opt-in newsletters, winback=post-cancellation.';
