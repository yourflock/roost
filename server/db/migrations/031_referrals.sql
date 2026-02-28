-- 031_referrals.sql — Referral program for P17-T03.
-- Each subscriber gets a unique short code. When a referee converts to paid,
-- the referrer earns a Stripe credit (1 month free) after a 7-day qualification window.

-- referral_codes — one per subscriber, auto-generated short alphanumeric code.
CREATE TABLE IF NOT EXISTS referral_codes (
  id             UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
  subscriber_id  UUID        NOT NULL REFERENCES subscribers(id) ON DELETE CASCADE,
  code           VARCHAR(20) UNIQUE NOT NULL,  -- e.g. "ROOST-A7K9M"
  created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (subscriber_id)  -- one code per subscriber
);

CREATE INDEX IF NOT EXISTS idx_referral_codes_code ON referral_codes(code);

-- referrals — one row per referred subscriber.
CREATE TABLE IF NOT EXISTS referrals (
  id                  UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
  referrer_id         UUID        NOT NULL REFERENCES subscribers(id) ON DELETE CASCADE,
  referee_id          UUID        NOT NULL REFERENCES subscribers(id) ON DELETE CASCADE,
  referral_code_id    UUID        NOT NULL REFERENCES referral_codes(id) ON DELETE CASCADE,
  status              VARCHAR(20) NOT NULL DEFAULT 'pending'
                        CHECK (status IN ('pending', 'qualified', 'rewarded', 'expired', 'clawed_back')),
  qualified_at        TIMESTAMPTZ,       -- when referee first paid
  reward_applied_at   TIMESTAMPTZ,
  reward_amount_cents INTEGER,
  stripe_credit_id    VARCHAR(100),
  created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (referee_id)  -- one referrer per person
);

CREATE INDEX IF NOT EXISTS idx_referrals_referrer ON referrals(referrer_id);
CREATE INDEX IF NOT EXISTS idx_referrals_status   ON referrals(status, qualified_at);

-- referral_flags — suspicious patterns flagged for admin review.
CREATE TABLE IF NOT EXISTS referral_flags (
  id           UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
  referral_id  UUID        NOT NULL REFERENCES referrals(id) ON DELETE CASCADE,
  flag_type    VARCHAR(50) NOT NULL,  -- e.g. "same_ip", "disposable_email", "yearly_cap"
  details      JSONB       NOT NULL DEFAULT '{}',
  reviewed     BOOLEAN     NOT NULL DEFAULT FALSE,
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_referral_flags_referral   ON referral_flags(referral_id);
CREATE INDEX IF NOT EXISTS idx_referral_flags_unreviewed ON referral_flags(created_at DESC) WHERE reviewed = FALSE;

COMMENT ON TABLE referral_codes IS
  'One short alphanumeric referral code per subscriber for sharing.';
COMMENT ON TABLE referrals IS
  'Tracks referral relationships. Referee can only have one referrer.';
COMMENT ON TABLE referral_flags IS
  'Suspicious referral patterns queued for admin review.';
COMMENT ON COLUMN referrals.status IS
  'pending=referee registered, qualified=referee paid 7+ days, rewarded=referrer credited, clawed_back=referee cancelled within 30 days.';
