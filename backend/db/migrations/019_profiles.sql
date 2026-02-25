-- 019_profiles.sql — Subscriber profiles for family plans.
-- P12-T01-S01: subscriber_profiles table — multi-profile support per subscription.
-- P12-T01-S02: Primary profile auto-created trigger on new subscriber.
-- P12-T01-S03: watch_progress updated to be profile-aware.

BEGIN;

-- ─────────────────────────────────────────────────────────────────────────────
-- subscriber_profiles — multiple user profiles under one subscription
-- ─────────────────────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS subscriber_profiles (
    id               UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    subscriber_id    UUID        NOT NULL REFERENCES subscribers(id) ON DELETE CASCADE,
    name             VARCHAR(100) NOT NULL,
    avatar_url       TEXT,                              -- URL to profile picture (preset or custom upload)
    avatar_preset    VARCHAR(50),                       -- preset name: 'owl-1'..'owl-12'
    is_primary       BOOLEAN     NOT NULL DEFAULT FALSE, -- one per subscriber (the account owner)
    age_rating_limit VARCHAR(20),                       -- NULL=unrestricted, 'TV-G','TV-PG','TV-14','TV-MA'
    is_kids_profile  BOOLEAN     NOT NULL DEFAULT FALSE, -- simplified UI + strict content filtering
    pin_hash         TEXT,                              -- bcrypt hash of 4-digit PIN (NULL = no PIN)
    blocked_categories JSONB     NOT NULL DEFAULT '[]', -- array of category IDs to hide
    viewing_schedule   JSONB,                           -- {"allowed_hours":{"start":"08:00","end":"21:00"},"timezone":"America/New_York"}
    preferences        JSONB     NOT NULL DEFAULT '{}', -- language, subtitle preferences, etc.
    is_active        BOOLEAN     NOT NULL DEFAULT TRUE,  -- false when plan downgraded past profile limit
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT uq_subscriber_profile_name UNIQUE (subscriber_id, name)
);

-- Only one primary profile per subscriber
CREATE UNIQUE INDEX IF NOT EXISTS idx_profiles_primary
    ON subscriber_profiles (subscriber_id)
    WHERE is_primary = TRUE;

CREATE INDEX IF NOT EXISTS idx_profiles_subscriber
    ON subscriber_profiles (subscriber_id);

-- auto-update updated_at
CREATE OR REPLACE FUNCTION profiles_set_updated_at()
RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS profiles_updated_at ON subscriber_profiles;
CREATE TRIGGER profiles_updated_at
    BEFORE UPDATE ON subscriber_profiles
    FOR EACH ROW EXECUTE FUNCTION profiles_set_updated_at();

-- ─────────────────────────────────────────────────────────────────────────────
-- Auto-create primary profile when a new subscriber is inserted.
-- Called after auth service registration (P2-T02).
-- ─────────────────────────────────────────────────────────────────────────────
CREATE OR REPLACE FUNCTION create_primary_profile_for_subscriber()
RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO subscriber_profiles (subscriber_id, name, is_primary, age_rating_limit)
    VALUES (NEW.id, COALESCE(NEW.display_name, 'Primary'), TRUE, NULL)
    ON CONFLICT DO NOTHING;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS auto_create_primary_profile ON subscribers;
CREATE TRIGGER auto_create_primary_profile
    AFTER INSERT ON subscribers
    FOR EACH ROW EXECUTE FUNCTION create_primary_profile_for_subscriber();

-- Backfill: create primary profiles for any existing subscribers that don't have one.
INSERT INTO subscriber_profiles (subscriber_id, name, is_primary, age_rating_limit)
SELECT id, COALESCE(display_name, 'Primary'), TRUE, NULL
FROM subscribers s
WHERE NOT EXISTS (
    SELECT 1 FROM subscriber_profiles sp
    WHERE sp.subscriber_id = s.id AND sp.is_primary = TRUE
)
ON CONFLICT DO NOTHING;

-- ─────────────────────────────────────────────────────────────────────────────
-- Update watch_progress to be profile-aware.
-- P12-T01-S03: Add profile_id column; adjust unique constraint.
-- ─────────────────────────────────────────────────────────────────────────────

-- Add profile_id column (nullable during migration; backfill from primary profile)
ALTER TABLE watch_progress
    ADD COLUMN IF NOT EXISTS profile_id UUID REFERENCES subscriber_profiles(id) ON DELETE CASCADE;

-- Backfill: assign existing watch_progress rows to the subscriber's primary profile.
UPDATE watch_progress wp
SET profile_id = sp.id
FROM subscriber_profiles sp
WHERE sp.subscriber_id = wp.subscriber_id
  AND sp.is_primary = TRUE
  AND wp.profile_id IS NULL;

-- Now make profile_id NOT NULL (all rows backfilled).
ALTER TABLE watch_progress
    ALTER COLUMN profile_id SET NOT NULL;

-- Drop old subscriber-based unique constraint and add profile-scoped one.
ALTER TABLE watch_progress
    DROP CONSTRAINT IF EXISTS watch_progress_subscriber_id_content_type_content_id_key;

ALTER TABLE watch_progress
    ADD CONSTRAINT uq_watch_progress_profile_content
    UNIQUE (profile_id, content_type, content_id);

-- Update index to profile-scoped.
DROP INDEX IF EXISTS idx_watch_progress_sub_content;
CREATE INDEX IF NOT EXISTS idx_watch_progress_profile_content
    ON watch_progress (profile_id, content_type, content_id);

-- ─────────────────────────────────────────────────────────────────────────────
-- Add profile plan limits to subscription_plans.
-- P12-T02-S04: Basic=2, Premium=4, Family=6 profiles.
-- ─────────────────────────────────────────────────────────────────────────────
ALTER TABLE subscription_plans
    ADD COLUMN IF NOT EXISTS max_profiles INTEGER NOT NULL DEFAULT 2;

UPDATE subscription_plans SET max_profiles = 2 WHERE slug = 'basic';
UPDATE subscription_plans SET max_profiles = 4 WHERE slug = 'premium';
UPDATE subscription_plans SET max_profiles = 6 WHERE slug = 'family';
UPDATE subscription_plans SET max_profiles = 6 WHERE slug = 'founding';

COMMIT;
