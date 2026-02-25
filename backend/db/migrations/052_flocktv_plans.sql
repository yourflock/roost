-- 052_flocktv_plans.sql — Flock TV subscriber tier columns + stream-hour billing aggregation.
-- Phase FLOCKTV: three tiers (flock_family_tv, roost_standalone, self_hosted) and
-- monthly stream-hour billing ledger.

-- Add Flock TV tier and Roost Boost fields to existing subscribers table.
ALTER TABLE subscribers
  ADD COLUMN IF NOT EXISTS flocktv_tier TEXT
    CHECK (flocktv_tier IN ('flock_family_tv','roost_standalone','self_hosted'));

ALTER TABLE subscribers
  ADD COLUMN IF NOT EXISTS flocktv_active_since TIMESTAMPTZ;

ALTER TABLE subscribers
  ADD COLUMN IF NOT EXISTS roost_boost_active BOOLEAN NOT NULL DEFAULT false;

ALTER TABLE subscribers
  ADD COLUMN IF NOT EXISTS iptv_contribution_count INT NOT NULL DEFAULT 0;

-- Monthly stream-hour billing aggregation.
-- One row per family per calendar month. Invoiced after month end.
-- total_charge is computed: base_charge + usage_charge.
CREATE TABLE IF NOT EXISTS monthly_stream_hours (
  id                UUID          PRIMARY KEY DEFAULT gen_random_uuid(),
  family_id         UUID          NOT NULL,
  billing_month     DATE          NOT NULL,   -- always the 1st of the month
  stream_hours      DECIMAL(10,2) NOT NULL DEFAULT 0,
  base_charge       DECIMAL(8,2)  NOT NULL DEFAULT 4.99,
  usage_charge      DECIMAL(8,2)  NOT NULL DEFAULT 0,
  total_charge      DECIMAL(8,2)  GENERATED ALWAYS AS (base_charge + usage_charge) STORED,
  invoiced_at       TIMESTAMPTZ,
  stripe_invoice_id TEXT,
  created_at        TIMESTAMPTZ   NOT NULL DEFAULT NOW(),
  updated_at        TIMESTAMPTZ   NOT NULL DEFAULT NOW(),
  UNIQUE (family_id, billing_month)
);

CREATE INDEX IF NOT EXISTS monthly_stream_hours_month_idx
  ON monthly_stream_hours (billing_month, invoiced_at);

CREATE INDEX IF NOT EXISTS monthly_stream_hours_family_idx
  ON monthly_stream_hours (family_id, billing_month DESC);
