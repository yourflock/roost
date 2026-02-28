-- 010_billing_p3_additions.sql
-- P3 additions layered on top of existing Phase 1 billing schema.
-- The existing schema uses plan_id (UUID FK) not plan_slug (TEXT).
-- This migration adds all missing P3 columns and tables without touching the existing FK structure.

BEGIN;

-- ─────────────────────────────────────────────────────────────────────────────
-- Add slug column to subscription_plans for the API (P3-T04)
-- ─────────────────────────────────────────────────────────────────────────────
ALTER TABLE subscription_plans
    ADD COLUMN IF NOT EXISTS slug                TEXT,
    ADD COLUMN IF NOT EXISTS description         TEXT,
    ADD COLUMN IF NOT EXISTS monthly_price_cents INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS annual_price_cents  INTEGER NOT NULL DEFAULT 0;

-- Backfill slug from name
UPDATE subscription_plans SET slug = LOWER(REPLACE(name, ' ', '_')) WHERE slug IS NULL;

-- Add UNIQUE constraint on slug
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_indexes WHERE indexname = 'idx_subscription_plans_slug'
    ) THEN
        CREATE UNIQUE INDEX idx_subscription_plans_slug ON subscription_plans (slug);
    END IF;
END $$;

-- ─────────────────────────────────────────────────────────────────────────────
-- Add missing columns to subscriptions for P3 (trial, dunning, pause/cancel, promo)
-- ─────────────────────────────────────────────────────────────────────────────
ALTER TABLE subscriptions
    ADD COLUMN IF NOT EXISTS billing_period          TEXT DEFAULT 'monthly',
    ADD COLUMN IF NOT EXISTS trial_ends_at           TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS dunning_count           INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS dunning_next_retry_at   TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS paused_at               TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS pause_resumes_at        TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS resumed_at              TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS canceled_at             TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS cancellation_reason     TEXT,
    ADD COLUMN IF NOT EXISTS stripe_coupon_id        TEXT,
    ADD COLUMN IF NOT EXISTS discount_percent        INTEGER;

-- Extend status check to include suspended, paused, founding
ALTER TABLE subscriptions DROP CONSTRAINT IF EXISTS subscriptions_status_check;
ALTER TABLE subscriptions ADD CONSTRAINT subscriptions_status_check
    CHECK (status IN (
        'trialing', 'active', 'past_due', 'suspended',
        'paused', 'cancelled', 'canceled', 'expired', 'founding'
    ));

-- Add subscriber uniqueness constraint (one active sub per subscriber)
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_indexes WHERE indexname = 'idx_subscriptions_subscriber_unique'
    ) THEN
        CREATE UNIQUE INDEX idx_subscriptions_subscriber_unique
            ON subscriptions (subscriber_id);
    END IF;
END $$;

-- ─────────────────────────────────────────────────────────────────────────────
-- Add stripe_customer_id to subscribers (P3-T02)
-- ─────────────────────────────────────────────────────────────────────────────
ALTER TABLE subscribers
    ADD COLUMN IF NOT EXISTS stripe_customer_id TEXT;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_indexes WHERE indexname = 'idx_subscribers_stripe_customer'
    ) THEN
        CREATE UNIQUE INDEX idx_subscribers_stripe_customer
            ON subscribers (stripe_customer_id)
            WHERE stripe_customer_id IS NOT NULL;
    END IF;
END $$;

-- ─────────────────────────────────────────────────────────────────────────────
-- pending_checkouts — correlate Stripe sessions to subscribers (P3-T02)
-- ─────────────────────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS pending_checkouts (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    subscriber_id       UUID NOT NULL REFERENCES subscribers(id) ON DELETE CASCADE,
    stripe_session_id   TEXT NOT NULL UNIQUE,
    plan_id             UUID REFERENCES subscription_plans(id),
    billing_period      TEXT NOT NULL DEFAULT 'monthly',
    promo_code          TEXT,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at          TIMESTAMPTZ NOT NULL DEFAULT NOW() + INTERVAL '24 hours'
);

CREATE INDEX IF NOT EXISTS idx_pending_checkouts_expires
    ON pending_checkouts (expires_at);

-- ─────────────────────────────────────────────────────────────────────────────
-- stripe_events — idempotency for webhook processing (P3-T03)
-- (payment_events table already exists for event log; this is for idempotency)
-- ─────────────────────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS stripe_events (
    stripe_event_id TEXT PRIMARY KEY,
    processed_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_stripe_events_processed
    ON stripe_events (processed_at);

-- ─────────────────────────────────────────────────────────────────────────────
-- roost_invoices — invoice storage and PDF tracking (P3-T09)
-- ─────────────────────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS roost_invoices (
    id                      UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    stripe_invoice_id       TEXT NOT NULL UNIQUE,
    subscriber_id           UUID REFERENCES subscribers(id) ON DELETE SET NULL,
    stripe_subscription_id  TEXT,
    amount_cents            BIGINT NOT NULL DEFAULT 0,
    currency                TEXT NOT NULL DEFAULT 'usd',
    status                  TEXT NOT NULL DEFAULT 'open',
    period_start            TIMESTAMPTZ,
    period_end              TIMESTAMPTZ,
    hosted_invoice_url      TEXT,
    invoice_pdf_url         TEXT,
    pdf_generated_at        TIMESTAMPTZ,
    pdf_storage_path        TEXT,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_invoices_subscriber
    ON roost_invoices (subscriber_id);

-- ─────────────────────────────────────────────────────────────────────────────
-- roost_refunds — refund tracking (P3-T11)
-- ─────────────────────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS roost_refunds (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    subscriber_id       UUID NOT NULL REFERENCES subscribers(id) ON DELETE CASCADE,
    stripe_invoice_id   TEXT,
    stripe_refund_id    TEXT UNIQUE,
    amount_cents        BIGINT NOT NULL,
    currency            TEXT NOT NULL DEFAULT 'usd',
    reason              TEXT,
    status              TEXT NOT NULL DEFAULT 'pending',
    requested_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    processed_at        TIMESTAMPTZ,
    processed_by        TEXT,
    notes               TEXT,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_refunds_subscriber
    ON roost_refunds (subscriber_id);

-- ─────────────────────────────────────────────────────────────────────────────
-- promo_codes — Roost-managed promo code registry (P3-T07)
-- ─────────────────────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS promo_codes (
    id                      UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    code                    TEXT NOT NULL UNIQUE,
    stripe_coupon_id        TEXT,
    discount_percent        INTEGER,
    discount_amount_cents   INTEGER,
    max_redemptions         INTEGER,
    times_redeemed          INTEGER NOT NULL DEFAULT 0,
    valid_from              TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    valid_until             TIMESTAMPTZ,
    is_active               BOOLEAN NOT NULL DEFAULT true,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_promo_codes_active
    ON promo_codes (code)
    WHERE is_active = true;

-- ─────────────────────────────────────────────────────────────────────────────
-- Seed: update plan names and add slug/pricing for existing plans
-- ─────────────────────────────────────────────────────────────────────────────
UPDATE subscription_plans
SET slug = 'founding', monthly_price_cents = 0, annual_price_cents = 0
WHERE name = 'Founder' AND (slug IS NULL OR slug != 'founding');

UPDATE subscription_plans
SET slug = 'premium', monthly_price_cents = 999, annual_price_cents = 9999
WHERE name = 'Standard' AND (slug IS NULL OR slug != 'premium');

-- Insert additional plans if not already present (by slug)
INSERT INTO subscription_plans (name, slug, monthly_price_cents, annual_price_cents, max_concurrent_streams, features)
SELECT 'Roost Basic', 'basic', 499, 4999, 2, '{"hd": true}'::jsonb
WHERE NOT EXISTS (SELECT 1 FROM subscription_plans WHERE slug = 'basic');

INSERT INTO subscription_plans (name, slug, monthly_price_cents, annual_price_cents, max_concurrent_streams, features)
SELECT 'Roost Family', 'family', 1499, 14999, 10, '{"hd": true, "family": true}'::jsonb
WHERE NOT EXISTS (SELECT 1 FROM subscription_plans WHERE slug = 'family');

-- ─────────────────────────────────────────────────────────────────────────────
-- P3-T12: Founding Family seed — camarata@roost.unity.dev billing_exempt forever
-- ─────────────────────────────────────────────────────────────────────────────
DO $$
DECLARE
    founder_id UUID;
    founder_plan_id UUID;
BEGIN
    SELECT id INTO founder_id FROM subscribers WHERE email = 'camarata@roost.unity.dev';
    SELECT id INTO founder_plan_id FROM subscription_plans WHERE slug = 'founding' LIMIT 1;

    IF founder_id IS NOT NULL THEN
        UPDATE subscribers SET billing_exempt = true WHERE id = founder_id;

        IF founder_plan_id IS NOT NULL THEN
            INSERT INTO subscriptions (subscriber_id, plan_id, billing_period, status)
            VALUES (founder_id, founder_plan_id, 'annual', 'founding')
            ON CONFLICT (subscriber_id) DO UPDATE SET
                plan_id = EXCLUDED.plan_id,
                status = 'founding',
                billing_period = 'annual',
                updated_at = NOW();
            RAISE NOTICE 'Founding family camarata@roost.unity.dev seeded (billing_exempt=true, plan=founding)';
        END IF;
    ELSE
        RAISE NOTICE 'Founder email not found — skip (will apply on registration)';
    END IF;
END $$;

COMMIT;
