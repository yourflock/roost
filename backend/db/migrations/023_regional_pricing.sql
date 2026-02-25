-- 023_regional_pricing.sql — Region-based subscription pricing (P14-T03)
-- Allows different prices per plan per region, with per-currency Stripe price IDs.

CREATE TABLE plan_regional_prices (
  id                      UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
  plan_id                 UUID        NOT NULL REFERENCES subscription_plans(id) ON DELETE CASCADE,
  region_id               UUID        NOT NULL REFERENCES regions(id)            ON DELETE CASCADE,
  currency                VARCHAR(3)  NOT NULL,
  monthly_price_cents     INTEGER     NOT NULL,
  annual_price_cents      INTEGER     NOT NULL,
  stripe_price_id_monthly VARCHAR(100),
  stripe_price_id_annual  VARCHAR(100),
  UNIQUE(plan_id, region_id)
);

CREATE INDEX idx_plan_regional_prices_plan   ON plan_regional_prices(plan_id);
CREATE INDEX idx_plan_regional_prices_region ON plan_regional_prices(region_id);

-- NOTE: Seed inserts use slug lookup. subscription_plans were seeded without a slug column
-- (see 002_subscriptions.sql). We use name matching instead — safe for initial seed.
-- If plans table lacks 'slug', the INSERT below is a no-op (sub-select returns 0 rows).

-- Seed: Standard plan USD prices for US region
INSERT INTO plan_regional_prices (plan_id, region_id, currency, monthly_price_cents, annual_price_cents)
SELECT p.id, r.id, 'usd', 499, 4999
FROM subscription_plans p, regions r
WHERE p.name = 'Standard' AND r.code = 'us'
ON CONFLICT (plan_id, region_id) DO NOTHING;

-- Seed: Founder plan USD prices for US region (billing_exempt = true in practice, but price row needed)
INSERT INTO plan_regional_prices (plan_id, region_id, currency, monthly_price_cents, annual_price_cents)
SELECT p.id, r.id, 'usd', 0, 0
FROM subscription_plans p, regions r
WHERE p.name = 'Founder' AND r.code = 'us'
ON CONFLICT (plan_id, region_id) DO NOTHING;
