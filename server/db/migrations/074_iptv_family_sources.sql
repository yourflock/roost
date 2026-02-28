-- Migration 074: Family-scoped IPTV source registry for Roost Boost.
-- Tracks IPTV sources contributed by families (Roost Boost pool) and
-- the per-family channels derived from those sources.
--
-- NOTE: The admin-managed iptv_sources table (migration 059) tracks Roost's
-- own licensed stream sources. This table tracks family-contributed IPTV
-- streams that pool into the Roost Boost live TV feature.

CREATE TABLE IF NOT EXISTS family_iptv_sources (
  id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  family_id     UUID NOT NULL,
  name          TEXT NOT NULL,
  m3u8_url      TEXT NOT NULL,
  username      TEXT,
  password      TEXT,   -- stored encrypted (AES-256-GCM via ROOST_ENCRYPTION_KEY)
  channel_count INTEGER DEFAULT 0,
  last_sync_at  TIMESTAMPTZ,
  health_status TEXT    NOT NULL DEFAULT 'unknown'
                CHECK (health_status IN ('healthy', 'degraded', 'down', 'unknown')),
  created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_family_iptv_sources_family ON family_iptv_sources(family_id);

CREATE TABLE IF NOT EXISTS family_iptv_channels (
  id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  source_id         UUID NOT NULL REFERENCES family_iptv_sources(id) ON DELETE CASCADE,
  slug              TEXT NOT NULL,
  name              TEXT NOT NULL,
  logo_url          TEXT,
  group_title       TEXT,
  tvg_id            TEXT,
  stream_url        TEXT NOT NULL,  -- raw source URL; never returned to clients
  health_status     TEXT NOT NULL DEFAULT 'unknown'
                    CHECK (health_status IN ('healthy', 'degraded', 'down', 'unknown')),
  last_health_check TIMESTAMPTZ,
  created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_family_iptv_channels_source ON family_iptv_channels(source_id);
CREATE INDEX IF NOT EXISTS idx_family_iptv_channels_group  ON family_iptv_channels(group_title);
CREATE UNIQUE INDEX IF NOT EXISTS idx_family_iptv_channels_slug ON family_iptv_channels(slug);
