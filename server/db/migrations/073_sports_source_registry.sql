-- 073_sports_source_registry.sql — Sports stream source registry
-- OSG.1.001: Multi-source IPTV registry, source channels, and game source assignments.
--
-- Rollback (run these in order to undo this migration):
-- DROP TABLE IF EXISTS sports_game_source_assignments;
-- DROP TABLE IF EXISTS sports_source_channels;
-- DROP TABLE IF EXISTS sports_stream_sources;

-- ─── sports_stream_sources ────────────────────────────────────────────────────
-- One row per registered IPTV source. Sources can be contributed by Roost Boost
-- participants or added manually by an admin.

CREATE TABLE IF NOT EXISTS sports_stream_sources (
  id                   UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
  name                 TEXT        NOT NULL,
  source_type          TEXT        NOT NULL
                         CHECK (source_type IN ('roost_boost', 'manual', 'iptv_url')),
  m3u_url              TEXT,
  roost_family_id      UUID,
  health_status        TEXT        NOT NULL DEFAULT 'unknown'
                         CHECK (health_status IN ('healthy', 'degraded', 'down', 'unknown')),
  last_health_check_at TIMESTAMPTZ,
  last_healthy_at      TIMESTAMPTZ,
  enabled              BOOLEAN     NOT NULL DEFAULT TRUE,
  created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at           TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_sports_stream_sources_health
  ON sports_stream_sources(health_status);
CREATE INDEX IF NOT EXISTS idx_sports_stream_sources_family
  ON sports_stream_sources(roost_family_id) WHERE roost_family_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_sports_stream_sources_enabled
  ON sports_stream_sources(enabled);

-- ─── sports_source_channels ──────────────────────────────────────────────────
-- One row per channel within a source's M3U playlist. Populated by the
-- channel-matching worker (OSG.2.001). match_confirmed = false requires admin review.

CREATE TABLE IF NOT EXISTS sports_source_channels (
  id                  UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
  source_id           UUID        NOT NULL
                        REFERENCES sports_stream_sources(id) ON DELETE CASCADE,
  channel_name        TEXT        NOT NULL,
  channel_url         TEXT        NOT NULL,
  group_title         TEXT,
  tvg_id              TEXT,
  matched_league_id   UUID        REFERENCES sports_leagues(id) ON DELETE SET NULL,
  match_confidence    FLOAT       NOT NULL DEFAULT 0.0,
  match_confirmed     BOOLEAN     NOT NULL DEFAULT FALSE,
  created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT uq_source_channel_url UNIQUE (source_id, channel_url)
);

CREATE INDEX IF NOT EXISTS idx_sports_source_channels_source
  ON sports_source_channels(source_id);
CREATE INDEX IF NOT EXISTS idx_sports_source_channels_league
  ON sports_source_channels(matched_league_id) WHERE matched_league_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_sports_source_channels_confidence
  ON sports_source_channels(source_id, match_confidence DESC);

-- ─── sports_game_source_assignments ──────────────────────────────────────────
-- Which source/channel is assigned to which game. Replaces the informal single
-- channel_m3u_url approach with a proper multi-source assignment system.
-- Only one row per game should have is_active = true at any time.

CREATE TABLE IF NOT EXISTS sports_game_source_assignments (
  id                UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
  game_id           UUID        NOT NULL
                      REFERENCES sports_events(id) ON DELETE CASCADE,
  source_channel_id UUID        NOT NULL
                      REFERENCES sports_source_channels(id),
  is_active         BOOLEAN     NOT NULL DEFAULT TRUE,
  assigned_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
  assigned_by       TEXT        NOT NULL DEFAULT 'auto'
                      CHECK (assigned_by IN ('auto', 'admin'))
);

CREATE INDEX IF NOT EXISTS idx_sports_game_source_assignments_game
  ON sports_game_source_assignments(game_id);
CREATE INDEX IF NOT EXISTS idx_sports_game_source_assignments_active
  ON sports_game_source_assignments(game_id, is_active) WHERE is_active = TRUE;
CREATE INDEX IF NOT EXISTS idx_sports_game_source_assignments_channel
  ON sports_game_source_assignments(source_channel_id);

-- ─── sports_game_events_log ──────────────────────────────────────────────────
-- Generic event log for sports game lifecycle events (stream failovers, etc.)

CREATE TABLE IF NOT EXISTS sports_game_events_log (
  id         UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
  game_id    UUID        NOT NULL REFERENCES sports_events(id) ON DELETE CASCADE,
  event_type TEXT        NOT NULL,
  payload    JSONB       NOT NULL DEFAULT '{}',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_sports_game_events_log_game
  ON sports_game_events_log(game_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_sports_game_events_log_type
  ON sports_game_events_log(event_type, created_at DESC);

-- Updated_at trigger for sports_stream_sources
CREATE OR REPLACE FUNCTION set_updated_at_sports_stream_sources()
RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
  NEW.updated_at = now();
  RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS trg_sports_stream_sources_updated_at ON sports_stream_sources;
CREATE TRIGGER trg_sports_stream_sources_updated_at
  BEFORE UPDATE ON sports_stream_sources
  FOR EACH ROW EXECUTE FUNCTION set_updated_at_sports_stream_sources();
