-- Migration 068: Metadata cache for external API responses (TMDB, TVDB, MusicBrainz, IGDB).
-- Also adds sport config, composite channels, sports display preferences, and card templates.

CREATE TABLE IF NOT EXISTS metadata_cache (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    content_type TEXT NOT NULL,
    external_id  TEXT NOT NULL,
    source       TEXT NOT NULL CHECK (source IN ('tmdb', 'tvdb', 'musicbrainz', 'igdb', 'local')),
    metadata     JSONB NOT NULL,
    fetched_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at   TIMESTAMPTZ NOT NULL DEFAULT (now() + INTERVAL '7 days'),
    UNIQUE (content_type, external_id, source)
);

CREATE INDEX idx_metadata_cache_lookup  ON metadata_cache(content_type, external_id);
CREATE INDEX idx_metadata_cache_expires ON metadata_cache(expires_at);
CREATE INDEX idx_metadata_cache_source  ON metadata_cache(source, content_type);

-- Sport display configuration (defines per-sport rendering rules for Owl)
CREATE TABLE IF NOT EXISTS sport_display_config (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    sport               TEXT NOT NULL UNIQUE,    -- 'NFL', 'EPL', 'NBA', etc.
    period_name         TEXT NOT NULL DEFAULT 'Period',
    period_count        INT NOT NULL DEFAULT 4,
    has_overtime        BOOLEAN NOT NULL DEFAULT true,
    clock_direction     TEXT NOT NULL DEFAULT 'down' CHECK (clock_direction IN ('down', 'up')),
    score_format        TEXT NOT NULL DEFAULT 'integer' CHECK (score_format IN ('integer', 'decimal')),
    halftime_label      TEXT NOT NULL DEFAULT 'Halftime',
    league_badge_color  TEXT,
    sport_data_fields   JSONB NOT NULL DEFAULT '[]',  -- [{key, label, type}, ...]
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_sport_config_sport ON sport_display_config(sport);

-- Composite channels (multi-stream grid channels)
CREATE TABLE IF NOT EXISTS composite_channels (
    id                      UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name                    TEXT NOT NULL,
    description             TEXT,
    layout_config           JSONB NOT NULL DEFAULT '{"cols":2,"rows":2,"outputW":1920,"outputH":1080}',
    inputs                  JSONB NOT NULL DEFAULT '[]',  -- [{channel_id, priority, label}, ...]
    audio_focus_index       INT NOT NULL DEFAULT 0,
    auto_populate_sport     TEXT,
    auto_populate_league    TEXT,
    subscriber_customizable BOOLEAN NOT NULL DEFAULT false,
    enabled                 BOOLEAN NOT NULL DEFAULT true,
    channel_id              UUID REFERENCES channels(id) ON DELETE SET NULL,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_composite_channels_enabled ON composite_channels(enabled) WHERE enabled = true;
CREATE INDEX idx_composite_channels_sport   ON composite_channels(auto_populate_sport) WHERE auto_populate_sport IS NOT NULL;

-- Per-subscriber composite preferences (for customizable composites)
CREATE TABLE IF NOT EXISTS subscriber_composite_prefs (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    subscriber_id       UUID NOT NULL,
    composite_channel_id UUID NOT NULL REFERENCES composite_channels(id) ON DELETE CASCADE,
    input_overrides     JSONB NOT NULL DEFAULT '{}',
    audio_focus_index   INT NOT NULL DEFAULT 0,
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (subscriber_id, composite_channel_id)
);

CREATE INDEX idx_composite_prefs_subscriber ON subscriber_composite_prefs(subscriber_id);

-- Sports display preferences per subscriber/profile
CREATE TABLE IF NOT EXISTS subscriber_sports_preferences (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    subscriber_id       UUID NOT NULL,
    profile_id          UUID,
    display_preferences JSONB NOT NULL DEFAULT '{
        "scores_visible": true,
        "ticker_visible": true,
        "ticker_sports": [],
        "live_overlay_visible": true,
        "cards_visible": true,
        "card_auto_show": true,
        "favorite_teams": [],
        "favorite_team_notify": true,
        "spoiler_delay_hours": 0
    }',
    commercial_handling JSONB NOT NULL DEFAULT '{
        "mode": "mute",
        "screensaver_style": "sports_scores",
        "volume_duck_level": -20
    }',
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (subscriber_id, COALESCE(profile_id, '00000000-0000-0000-0000-000000000000'::UUID))
);

CREATE INDEX idx_sports_prefs_subscriber ON subscriber_sports_preferences(subscriber_id);

-- Channel cards (custom promotional cards for non-sport channels)
CREATE TABLE IF NOT EXISTS channel_cards (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    channel_id  UUID NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    card_type   TEXT NOT NULL DEFAULT 'custom' CHECK (card_type IN ('pregame','live','halftime','final','commercial','custom')),
    sections    JSONB NOT NULL DEFAULT '[]',
    valid_from  TIMESTAMPTZ,
    valid_until TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_channel_cards_channel ON channel_cards(channel_id, card_type);
CREATE INDEX idx_channel_cards_valid   ON channel_cards(channel_id, valid_from, valid_until);

-- DVR recording commercial markers join (adds commercial_free flag to DVR)
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_name = 'dvr_recordings' AND column_name = 'has_commercial_free_vod'
    ) THEN
        -- Only add if dvr_recordings table exists
        IF EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = 'dvr_recordings') THEN
            ALTER TABLE dvr_recordings ADD COLUMN has_commercial_free_vod BOOLEAN NOT NULL DEFAULT false;
            ALTER TABLE dvr_recordings ADD COLUMN commercial_free_vod_id UUID;
            ALTER TABLE dvr_recordings ADD COLUMN commercial_free_job_status TEXT DEFAULT 'pending';
        END IF;
    END IF;
END$$;
