-- 010_billing_tables.sql
-- P3-T04: Subscription management tables
-- P3-T06: Free trial support (trial_ends_at)
-- P3-T07: Promo codes (pending_checkouts, stripe_coupon_id)
-- P3-T08: Payment dunning (dunning_count, dunning_next_retry_at)
-- P3-T09: Invoice storage (roost_invoices)
-- P3-T10: Subscription pause/resume (paused_at, resumed_at)
-- P3-T11: Refund tracking (roost_refunds)
-- P3-T12: Founding family / billing exempt (billing_exempt column on subscribers)

BEGIN;

-- ─────────────────────────────────────────────────────────────────────────────
-- subscription_plans — catalog of available plans (seeded below)
-- ─────────────────────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS subscription_plans (
    id                      UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    slug                    TEXT NOT NULL UNIQUE,       -- 'basic', 'premium', 'family'
    name                    TEXT NOT NULL,
    description             TEXT,
    monthly_price_cents     INTEGER NOT NULL DEFAULT 0,
    annual_price_cents      INTEGER NOT NULL DEFAULT 0,
    -- Stripe IDs — populated by /billing/admin/setup-stripe (P3-T01)
    stripe_product_id       TEXT,
    stripe_price_id_monthly TEXT,
    stripe_price_id_annual  TEXT,
    is_active               BOOLEAN NOT NULL DEFAULT true,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- ─────────────────────────────────────────────────────────────────────────────
-- Add Stripe customer ID and billing_exempt to subscribers
-- ─────────────────────────────────────────────────────────────────────────────
ALTER TABLE subscribers
    ADD COLUMN IF NOT EXISTS stripe_customer_id TEXT,           -- Stripe cus_xxx
    ADD COLUMN IF NOT EXISTS billing_exempt      BOOLEAN NOT NULL DEFAULT false; -- P3-T12

CREATE UNIQUE INDEX IF NOT EXISTS idx_subscribers_stripe_customer
    ON subscribers (stripe_customer_id)
    WHERE stripe_customer_id IS NOT NULL;

-- ─────────────────────────────────────────────────────────────────────────────
-- subscriptions — one active subscription per subscriber
-- ─────────────────────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS subscriptions (
    id                      UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    subscriber_id           UUID NOT NULL REFERENCES subscribers(id) ON DELETE CASCADE,
    plan_slug               TEXT NOT NULL REFERENCES subscription_plans(slug),
    billing_period          TEXT NOT NULL CHECK (billing_period IN ('monthly', 'annual')),
    status                  TEXT NOT NULL DEFAULT 'trialing'
                                CHECK (status IN (
                                    'trialing', 'active', 'past_due', 'suspended',
                                    'paused', 'canceled', 'founding'
                                )),
    -- Stripe identifiers
    stripe_subscription_id  TEXT UNIQUE,
    stripe_customer_id      TEXT,
    -- Trial (P3-T06)
    trial_ends_at           TIMESTAMPTZ,
    -- Billing periods
    current_period_start    TIMESTAMPTZ,
    current_period_end      TIMESTAMPTZ,
    -- Dunning (P3-T08)
    dunning_count           INTEGER NOT NULL DEFAULT 0,
    dunning_next_retry_at   TIMESTAMPTZ,
    -- Pause/Resume (P3-T10)
    paused_at               TIMESTAMPTZ,
    pause_resumes_at        TIMESTAMPTZ,
    resumed_at              TIMESTAMPTZ,
    -- Cancellation
    cancel_at_period_end    BOOLEAN NOT NULL DEFAULT false,
    canceled_at             TIMESTAMPTZ,
    cancellation_reason     TEXT,
    -- Promo (P3-T07)
    stripe_coupon_id        TEXT,
    discount_percent        INTEGER,
    -- Timestamps
    created_at              TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT uq_subscription_subscriber UNIQUE (subscriber_id)
);

CREATE INDEX IF NOT EXISTS idx_subscriptions_stripe_sub
    ON subscriptions (stripe_subscription_id)
    WHERE stripe_subscription_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_subscriptions_status
    ON subscriptions (status);

CREATE INDEX IF NOT EXISTS idx_subscriptions_period_end
    ON subscriptions (current_period_end)
    WHERE status IN ('active', 'past_due', 'trialing');

-- ─────────────────────────────────────────────────────────────────────────────
-- pending_checkouts — transient table correlating checkout sessions to subscribers
-- ─────────────────────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS pending_checkouts (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    subscriber_id       UUID NOT NULL REFERENCES subscribers(id) ON DELETE CASCADE,
    stripe_session_id   TEXT NOT NULL UNIQUE,
    plan_slug           TEXT NOT NULL,
    billing_period      TEXT NOT NULL,
    promo_code          TEXT,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at          TIMESTAMPTZ NOT NULL DEFAULT NOW() + INTERVAL '24 hours'
);

CREATE INDEX IF NOT EXISTS idx_pending_checkouts_expires
    ON pending_checkouts (expires_at);

-- ─────────────────────────────────────────────────────────────────────────────
-- stripe_events — idempotency table for webhook processing (P3-T03)
-- ─────────────────────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS stripe_events (
    stripe_event_id TEXT PRIMARY KEY,
    processed_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Auto-expire old event records after 30 days (keep table lean)
CREATE INDEX IF NOT EXISTS idx_stripe_events_processed
    ON stripe_events (processed_at);

-- ─────────────────────────────────────────────────────────────────────────────
-- roost_invoices — invoice records (P3-T09)
-- ─────────────────────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS roost_invoices (
    id                      UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    stripe_invoice_id       TEXT NOT NULL UNIQUE,
    subscriber_id           UUID REFERENCES subscribers(id) ON DELETE SET NULL,
    stripe_subscription_id  TEXT,
    amount_cents            BIGINT NOT NULL DEFAULT 0,
    currency                TEXT NOT NULL DEFAULT 'usd',
    status                  TEXT NOT NULL DEFAULT 'open',  -- open, paid, void, uncollectible
    period_start            TIMESTAMPTZ,
    period_end              TIMESTAMPTZ,
    hosted_invoice_url      TEXT,   -- Stripe-hosted PDF link
    invoice_pdf_url         TEXT,   -- Stripe PDF direct URL
    pdf_generated_at        TIMESTAMPTZ,  -- when we generated our own branded PDF
    pdf_storage_path        TEXT,         -- internal storage path for branded PDF
    created_at              TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_invoices_subscriber
    ON roost_invoices (subscriber_id);

CREATE INDEX IF NOT EXISTS idx_invoices_period
    ON roost_invoices (period_start, period_end);

-- ─────────────────────────────────────────────────────────────────────────────
-- roost_refunds — refund tracking (P3-T11)
-- ─────────────────────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS roost_refunds (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    subscriber_id       UUID NOT NULL REFERENCES subscribers(id) ON DELETE CASCADE,
    stripe_invoice_id   TEXT REFERENCES roost_invoices(stripe_invoice_id),
    stripe_refund_id    TEXT UNIQUE,
    amount_cents        BIGINT NOT NULL,
    currency            TEXT NOT NULL DEFAULT 'usd',
    reason              TEXT,    -- 'subscriber_request', 'service_issue', 'admin_override'
    status              TEXT NOT NULL DEFAULT 'pending',  -- pending, succeeded, failed
    requested_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    processed_at        TIMESTAMPTZ,
    processed_by        TEXT,    -- admin email or 'system'
    notes               TEXT,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_refunds_subscriber
    ON roost_refunds (subscriber_id);

-- ─────────────────────────────────────────────────────────────────────────────
-- promo_codes — local promo code registry (P3-T07)
-- Stripe also has promotion codes — this table tracks Roost-managed codes
-- and their redemption counts.
-- ─────────────────────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS promo_codes (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    code                TEXT NOT NULL UNIQUE,
    stripe_coupon_id    TEXT,           -- linked Stripe coupon
    discount_percent    INTEGER,        -- e.g. 20 for 20% off
    discount_amount_cents INTEGER,      -- flat discount in cents (alternative to percent)
    max_redemptions     INTEGER,        -- NULL = unlimited
    times_redeemed      INTEGER NOT NULL DEFAULT 0,
    valid_from          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    valid_until         TIMESTAMPTZ,    -- NULL = no expiry
    is_active           BOOLEAN NOT NULL DEFAULT true,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_promo_codes_code
    ON promo_codes (code)
    WHERE is_active = true;

-- ─────────────────────────────────────────────────────────────────────────────
-- Seed: subscription plan catalog
-- ─────────────────────────────────────────────────────────────────────────────
INSERT INTO subscription_plans (slug, name, description, monthly_price_cents, annual_price_cents)
VALUES
    ('basic',   'Roost Basic',   'Core live TV + EPG, up to 2 streams',   499,   4999),
    ('premium', 'Roost Premium', 'Full catalog + sports + DVR, 5 streams', 999,   9999),
    ('family',  'Roost Family',  'Everything + 10 streams + family plans', 1499, 14999),
    ('founding','Roost Founding','Founding family — $0 forever',            0,       0)
ON CONFLICT (slug) DO NOTHING;

-- ─────────────────────────────────────────────────────────────────────────────
-- P3-T12: Founding Family seed
-- camarata@yourflock.org is billing_exempt = true forever.
-- Their subscription record uses the 'founding' plan at $0.
-- ─────────────────────────────────────────────────────────────────────────────
DO $$
DECLARE
    founder_id UUID;
BEGIN
    SELECT id INTO founder_id FROM subscribers WHERE email = 'camarata@yourflock.org';
    IF founder_id IS NOT NULL THEN
        -- Mark as billing exempt
        UPDATE subscribers SET billing_exempt = true WHERE id = founder_id;
        -- Insert or update founding subscription
        INSERT INTO subscriptions (
            subscriber_id, plan_slug, billing_period, status
        ) VALUES (
            founder_id, 'founding', 'annual', 'founding'
        )
        ON CONFLICT (subscriber_id) DO UPDATE SET
            plan_slug = 'founding',
            status = 'founding',
            billing_period = 'annual',
            updated_at = NOW();
        RAISE NOTICE 'Founding family camarata@yourflock.org seeded (billing_exempt=true)';
    ELSE
        RAISE NOTICE 'Founder email not found — skipping founding seed (will apply on registration)';
    END IF;
END $$;

COMMIT;
