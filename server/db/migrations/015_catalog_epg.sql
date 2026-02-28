-- Migration 015: Catalog, EPG, and Owl API session tables
-- Phase P5: Content Catalog & EPG
-- Phase P6: Owl Addon API

-- Channel categories
CREATE TABLE IF NOT EXISTS channel_categories (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name        TEXT UNIQUE NOT NULL,
    slug        TEXT UNIQUE NOT NULL,
    sort_order  INT DEFAULT 0,
    created_at  TIMESTAMPTZ DEFAULT NOW()
);

-- Channels
CREATE TABLE IF NOT EXISTS channels (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    slug            TEXT UNIQUE NOT NULL,
    name            TEXT NOT NULL,
    category_id     UUID REFERENCES channel_categories(id) ON DELETE SET NULL,
    category        TEXT,                           -- denormalised for fast reads
    logo_url        TEXT,
    stream_url      TEXT NOT NULL,                  -- INTERNAL ONLY — never expose via Owl API
    epg_channel_id  TEXT,                           -- matches channel_id in XMLTV source
    sort_order      INT DEFAULT 0,
    is_active       BOOLEAN DEFAULT true,
    is_featured     BOOLEAN DEFAULT false,
    staff_pick_note TEXT,
    language_code   TEXT DEFAULT 'en',
    country_code    TEXT DEFAULT 'us',
    bitrate_config  JSONB DEFAULT '{}',             -- {"1080p": 8000, "720p": 4000, "480p": 2000}
    created_at      TIMESTAMPTZ DEFAULT NOW(),
    updated_at      TIMESTAMPTZ DEFAULT NOW()
);

-- Full-text search vector (generated column — auto-maintained by Postgres)
ALTER TABLE channels ADD COLUMN IF NOT EXISTS
    search_vector tsvector GENERATED ALWAYS AS (
        to_tsvector('english',
            name || ' ' ||
            coalesce(category, '') || ' ' ||
            coalesce(language_code, '') || ' ' ||
            coalesce(country_code, '')
        )
    ) STORED;

CREATE INDEX IF NOT EXISTS idx_channels_search      ON channels USING GIN(search_vector);
CREATE INDEX IF NOT EXISTS idx_channels_active      ON channels(is_active, sort_order);
CREATE INDEX IF NOT EXISTS idx_channels_featured    ON channels(is_featured) WHERE is_featured = true;
CREATE INDEX IF NOT EXISTS idx_channels_category    ON channels(category_id);

-- Featured lists (staff picks, "Best of Sports", etc.)
CREATE TABLE IF NOT EXISTS featured_lists (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    slug        TEXT UNIQUE NOT NULL,
    name        TEXT NOT NULL,
    description TEXT,
    is_active   BOOLEAN DEFAULT true,
    sort_order  INT DEFAULT 0,
    created_at  TIMESTAMPTZ DEFAULT NOW(),
    updated_at  TIMESTAMPTZ DEFAULT NOW()
);

-- Featured list → channel membership
CREATE TABLE IF NOT EXISTS channel_feature_entries (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    list_id     UUID NOT NULL REFERENCES featured_lists(id) ON DELETE CASCADE,
    channel_id  UUID NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    position    INT DEFAULT 0,
    created_at  TIMESTAMPTZ DEFAULT NOW(),
    UNIQUE (list_id, channel_id)
);

CREATE INDEX IF NOT EXISTS idx_feature_entries_list ON channel_feature_entries(list_id, position);

-- EPG sources (XMLTV feed URLs managed by admin)
CREATE TABLE IF NOT EXISTS epg_sources (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name            TEXT NOT NULL,
    url             TEXT NOT NULL,
    format          TEXT DEFAULT 'xmltv',           -- 'xmltv' only for now
    priority        INT DEFAULT 0,                  -- higher = preferred when multiple sources have same channel
    is_active       BOOLEAN DEFAULT true,
    last_synced_at  TIMESTAMPTZ,
    sync_status     TEXT DEFAULT 'pending',         -- 'pending','syncing','ok','error'
    sync_error      TEXT,
    created_at      TIMESTAMPTZ DEFAULT NOW(),
    updated_at      TIMESTAMPTZ DEFAULT NOW()
);

-- EPG programs (populated by the EPG sync service)
CREATE TABLE IF NOT EXISTS epg_programs (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    channel_id          UUID REFERENCES channels(id) ON DELETE CASCADE,
    source_id           UUID REFERENCES epg_sources(id) ON DELETE CASCADE,
    source_program_id   TEXT,                       -- external ID from XMLTV source
    title               TEXT NOT NULL,
    description         TEXT,
    start_time          TIMESTAMPTZ NOT NULL,
    end_time            TIMESTAMPTZ NOT NULL,
    category            TEXT,
    rating              TEXT,
    poster_url          TEXT,
    is_live             BOOLEAN DEFAULT false,
    is_new              BOOLEAN DEFAULT false,
    created_at          TIMESTAMPTZ DEFAULT NOW(),
    UNIQUE (channel_id, source_program_id)
);

-- Indexes optimised for EPG time-range queries (Owl EPG endpoint pattern)
CREATE INDEX IF NOT EXISTS idx_epg_programs_channel_time ON epg_programs(channel_id, start_time, end_time);
CREATE INDEX IF NOT EXISTS idx_epg_programs_start        ON epg_programs(start_time);
CREATE INDEX IF NOT EXISTS idx_epg_programs_window       ON epg_programs(start_time, end_time);

-- Owl API sessions (short-lived, 4-hour TTL; row deleted by background job after expiry)
-- Complements Redis cache — DB row is the authoritative record
CREATE TABLE IF NOT EXISTS owl_sessions (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    subscriber_id   UUID NOT NULL REFERENCES subscribers(id) ON DELETE CASCADE,
    session_token   TEXT UNIQUE NOT NULL,           -- random UUID, never hashed (short-lived)
    device_id       TEXT,
    platform        TEXT,                           -- 'web','android','ios','desktop','tv','antbox'
    client_version  TEXT,
    expires_at      TIMESTAMPTZ NOT NULL,
    created_at      TIMESTAMPTZ DEFAULT NOW(),
    last_used_at    TIMESTAMPTZ DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_owl_sessions_token   ON owl_sessions(session_token);
CREATE INDEX IF NOT EXISTS idx_owl_sessions_sub     ON owl_sessions(subscriber_id);
CREATE INDEX IF NOT EXISTS idx_owl_sessions_expires ON owl_sessions(expires_at);

-- Seed default featured list
INSERT INTO featured_lists (slug, name, description, sort_order)
VALUES ('staff-picks', 'Staff Picks', 'Hand-curated channels recommended by the Roost team', 0)
ON CONFLICT (slug) DO NOTHING;
