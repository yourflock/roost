-- 056_flocktv_sports.sql — Flock TV FTV.4: family sports picks + notifications.
-- Phase FLOCKTV: per-family team subscriptions, picks leaderboard,
-- and content-ready notification records.

-- Family sports picks: families select teams they support.
-- Drives score push notifications and leaderboard calculations.
CREATE TABLE IF NOT EXISTS family_sports_picks (
  id          UUID    PRIMARY KEY DEFAULT gen_random_uuid(),
  family_id   UUID    NOT NULL,
  team_id     UUID    NOT NULL REFERENCES sports_teams(id) ON DELETE CASCADE,
  is_active   BOOLEAN NOT NULL DEFAULT true,
  added_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  UNIQUE (family_id, team_id)
);

CREATE INDEX IF NOT EXISTS family_sports_picks_family_idx
  ON family_sports_picks (family_id, is_active);

CREATE INDEX IF NOT EXISTS family_sports_picks_team_idx
  ON family_sports_picks (team_id, is_active);

-- Add current_period column to sports_events (if not already present).
ALTER TABLE sports_events
  ADD COLUMN IF NOT EXISTS current_period TEXT;

-- Family content-ready notification records.
-- Populated when acquisition_queue.status → 'complete'.
-- Families poll /ftv/notifications/stream to consume.
CREATE TABLE IF NOT EXISTS family_notifications (
  id           UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
  family_id    UUID        NOT NULL,
  canonical_id TEXT        NOT NULL,
  content_type TEXT        NOT NULL,
  event_type   TEXT        NOT NULL DEFAULT 'content_ready',
  read_at      TIMESTAMPTZ,
  created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  -- Prevent duplicate notifications for the same family + canonical_id.
  CONSTRAINT family_notifications_unique UNIQUE (family_id, canonical_id, event_type)
);

CREATE INDEX IF NOT EXISTS family_notifications_family_idx
  ON family_notifications (family_id, created_at DESC);

CREATE INDEX IF NOT EXISTS family_notifications_unread_idx
  ON family_notifications (family_id, read_at)
  WHERE read_at IS NULL;

-- flock_sso_keys: stores per-family Flock JWT public keys.
-- Each family's Flock-issued JWT is signed with a key registered at provision time.
CREATE TABLE IF NOT EXISTS flock_sso_keys (
  id              UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
  family_id       UUID        NOT NULL UNIQUE,
  public_key_pem  TEXT        NOT NULL,
  algorithm       TEXT        NOT NULL DEFAULT 'ES256',
  active          BOOLEAN     NOT NULL DEFAULT true,
  created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS flock_sso_keys_family_idx
  ON flock_sso_keys (family_id, active);
