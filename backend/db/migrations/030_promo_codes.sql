-- 030_promo_codes.sql — Promotional codes and discount coupons for P17-T02.
-- Extends the existing promo_codes table (created in P3) or creates it
-- with the full schema required for P17.

-- promotional_codes — richer version of existing promo_codes with type support.
CREATE TABLE IF NOT EXISTS promotional_codes (
  id               UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
  code             VARCHAR(50) UNIQUE NOT NULL,  -- always stored uppercase
  type             VARCHAR(20) NOT NULL CHECK (type IN ('percentage', 'fixed_amount', 'trial_extension', 'free_month')),
  value            INTEGER     NOT NULL,          -- meaning depends on type
  currency         VARCHAR(3)  NOT NULL DEFAULT 'usd',
  stripe_coupon_id VARCHAR(100),
  max_uses         INTEGER,                       -- NULL = unlimited
  current_uses     INTEGER     NOT NULL DEFAULT 0,
  min_plan_id      UUID        REFERENCES subscription_plans(id) ON DELETE SET NULL,
  expires_at       TIMESTAMPTZ,                   -- NULL = never expires
  is_active        BOOLEAN     NOT NULL DEFAULT TRUE,
  created_by       UUID        REFERENCES subscribers(id) ON DELETE SET NULL,
  created_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_promo_codes_code      ON promotional_codes(code) WHERE is_active = TRUE;
CREATE INDEX IF NOT EXISTS idx_promo_codes_expires   ON promotional_codes(expires_at) WHERE is_active = TRUE;

-- promo_code_redemptions — one per subscriber per code.
CREATE TABLE IF NOT EXISTS promo_code_redemptions (
  id                  UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
  code_id             UUID        NOT NULL REFERENCES promotional_codes(id) ON DELETE CASCADE,
  subscriber_id       UUID        NOT NULL REFERENCES subscribers(id) ON DELETE CASCADE,
  redeemed_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
  stripe_discount_id  VARCHAR(100),
  UNIQUE (code_id, subscriber_id)
);

CREATE INDEX IF NOT EXISTS idx_promo_redemptions_subscriber ON promo_code_redemptions(subscriber_id);
CREATE INDEX IF NOT EXISTS idx_promo_redemptions_code       ON promo_code_redemptions(code_id);

COMMENT ON TABLE promotional_codes IS
  'Promotional codes with typed discounts: percentage, fixed, trial extension, or free month.';
COMMENT ON TABLE promo_code_redemptions IS
  'Tracks which subscribers have redeemed each code. One redemption per subscriber per code.';
COMMENT ON COLUMN promotional_codes.type IS
  'percentage: value=percent off (1-100). fixed_amount: value=cents off. trial_extension: value=extra days. free_month: value=months free.';
