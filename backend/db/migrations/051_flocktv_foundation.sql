-- 051_flocktv_foundation.sql — Flock TV: per-family content selections + stream events.
-- Phase FLOCKTV: per-family content library and privacy-preserving stream event logging.
-- Each family's DB stores selections and watch history only — no shared content metadata.

-- Per-family content selections (what a family has "added" from the shared pool)
CREATE TABLE IF NOT EXISTS content_selections (
  id              UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
  family_id       UUID        NOT NULL,
  canonical_id    TEXT        NOT NULL,     -- e.g., imdb:tt0111161, tvdb:79169, mb:abc123
  content_type    TEXT        NOT NULL,     -- movie, show, episode, music, game, podcast, live
  added_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  added_by        UUID        REFERENCES subscribers(id) ON DELETE SET NULL,
  watch_progress  JSONB       NOT NULL DEFAULT '{}',  -- per-episode/track progress map
  personal_rating SMALLINT    CHECK (personal_rating BETWEEN 1 AND 10),
  personal_notes  TEXT,
  custom_tags     TEXT[]      NOT NULL DEFAULT '{}',
  CONSTRAINT content_type_valid CHECK (
    content_type IN ('movie','show','episode','music','game','podcast','live')
  ),
  UNIQUE (family_id, canonical_id)
);

CREATE INDEX IF NOT EXISTS content_selections_family_idx
  ON content_selections (family_id, content_type);

CREATE INDEX IF NOT EXISTS content_selections_canonical_idx
  ON content_selections (canonical_id);

CREATE INDEX IF NOT EXISTS content_selections_added_at_idx
  ON content_selections (family_id, added_at DESC);

-- Stream events: privacy-preserving billing log.
-- NO user_id beyond family_id, NO IP address, NO device fingerprint.
CREATE TABLE IF NOT EXISTS stream_events (
  id            UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
  family_id     UUID        NOT NULL,
  canonical_id  TEXT        NOT NULL,
  quality       TEXT        NOT NULL CHECK (quality IN ('360p','480p','720p','1080p','4k')),
  started_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  ended_at      TIMESTAMPTZ,
  duration_sec  INT         GENERATED ALWAYS AS (
    CASE WHEN ended_at IS NOT NULL THEN
      EXTRACT(EPOCH FROM (ended_at - started_at))::INT
    ELSE NULL END
  ) STORED
  -- NO user_id beyond family_id
  -- NO source IP address
  -- NO device fingerprint or user-agent
);

CREATE INDEX IF NOT EXISTS stream_events_family_billing_idx
  ON stream_events (family_id, started_at DESC);

CREATE INDEX IF NOT EXISTS stream_events_billing_month_idx
  ON stream_events (family_id, date_trunc('month', started_at));
