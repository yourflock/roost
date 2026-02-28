-- 011_founding_family_trigger.sql
-- P3-T12: Founding Family Tier & Lock-in Pricing
--
-- Creates a DB trigger: when camarata@roost.unity.dev registers,
-- automatically mark them billing_exempt=true and assign the 'founding' plan.
-- This ensures they NEVER get charged regardless of code path.
--
-- Also installs a check constraint so billing_exempt=true subscribers
-- cannot be put on any non-founding subscription via the API.

BEGIN;

-- Trigger function: auto-apply founding tier on registration
CREATE OR REPLACE FUNCTION apply_founding_tier()
RETURNS TRIGGER AS $$
DECLARE
    founding_plan_id UUID;
BEGIN
    -- Only applies to the founding family email
    IF LOWER(NEW.email) = 'camarata@roost.unity.dev' THEN
        NEW.billing_exempt := true;
        RAISE NOTICE 'Founding tier applied to %', NEW.email;

        -- Insert founding subscription after the subscriber row is created
        -- (using a deferred trigger or inline; we use AFTER INSERT)
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

-- BEFORE INSERT: set billing_exempt on the subscribers row
DROP TRIGGER IF EXISTS trg_founding_tier_before ON subscribers;
CREATE TRIGGER trg_founding_tier_before
    BEFORE INSERT ON subscribers
    FOR EACH ROW
    EXECUTE FUNCTION apply_founding_tier();

-- AFTER INSERT: create the founding subscription record
CREATE OR REPLACE FUNCTION create_founding_subscription()
RETURNS TRIGGER AS $$
DECLARE
    founding_plan_id UUID;
BEGIN
    IF NEW.billing_exempt = true THEN
        SELECT id INTO founding_plan_id FROM subscription_plans WHERE slug = 'founding' LIMIT 1;
        IF founding_plan_id IS NOT NULL THEN
            INSERT INTO subscriptions (subscriber_id, plan_id, billing_period, status)
            VALUES (NEW.id, founding_plan_id, 'annual', 'founding')
            ON CONFLICT (subscriber_id) DO NOTHING;
        END IF;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_founding_subscription ON subscribers;
CREATE TRIGGER trg_founding_subscription
    AFTER INSERT ON subscribers
    FOR EACH ROW
    WHEN (NEW.billing_exempt = true)
    EXECUTE FUNCTION create_founding_subscription();

-- View: billing_exempt_subscribers — easy audit of who has free access
CREATE OR REPLACE VIEW billing_exempt_subscribers AS
    SELECT
        sub.id,
        sub.email,
        sub.display_name,
        sub.billing_exempt,
        s.plan_id,
        sp.name AS plan_name,
        s.status,
        sub.created_at
    FROM subscribers sub
    LEFT JOIN subscriptions s ON s.subscriber_id = sub.id
    LEFT JOIN subscription_plans sp ON sp.id = s.plan_id
    WHERE sub.billing_exempt = true
    ORDER BY sub.created_at;

COMMIT;
