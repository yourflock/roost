-- Migration 016: VOD Content Library
-- Phase P10: VOD & Catchup Content
-- Tables: vod_catalog, vod_series, vod_episodes, watch_progress

-- Shared updated_at trigger function (idempotent)
CREATE OR REPLACE FUNCTION set_updated_at()
RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$;

-- VOD catalog: movies and series (top-level entries)
CREATE TABLE IF NOT EXISTS vod_catalog (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    title           VARCHAR(500) NOT NULL,
    slug            VARCHAR(500) UNIQUE NOT NULL,
    type            VARCHAR(20) NOT NULL CHECK (type IN ('movie','series')),
    description     TEXT,
    genre           VARCHAR(100),
    rating          VARCHAR(20),          -- TV-PG, TV-MA, PG-13, R, NR, etc.
    release_year    INTEGER,
    duration_seconds INTEGER,             -- NULL for series (per-episode), set for movies
    poster_url      TEXT,
    backdrop_url    TEXT,
    trailer_url     TEXT,
    source_url      TEXT NOT NULL,        -- HLS URL — INTERNAL ONLY, never expose via API
    is_active       BOOLEAN DEFAULT TRUE,
    sort_order      INTEGER DEFAULT 0,
    metadata        JSONB DEFAULT '{}',   -- director, cast, language, country, tmdb_id
    created_at      TIMESTAMPTZ DEFAULT NOW(),
    updated_at      TIMESTAMPTZ DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_vod_catalog_slug     ON vod_catalog(slug);
CREATE INDEX IF NOT EXISTS idx_vod_catalog_type     ON vod_catalog(type);
CREATE INDEX IF NOT EXISTS idx_vod_catalog_genre    ON vod_catalog(genre);
CREATE INDEX IF NOT EXISTS idx_vod_catalog_active   ON vod_catalog(is_active, sort_order);
CREATE INDEX IF NOT EXISTS idx_vod_catalog_year     ON vod_catalog(release_year);

-- Full-text search on VOD catalog
ALTER TABLE vod_catalog ADD COLUMN IF NOT EXISTS
    search_vector tsvector GENERATED ALWAYS AS (
        to_tsvector('english',
            title || ' ' ||
            COALESCE(description, '') || ' ' ||
            COALESCE(genre, '')
        )
    ) STORED;
CREATE INDEX IF NOT EXISTS idx_vod_catalog_search ON vod_catalog USING GIN(search_vector);

CREATE TRIGGER vod_catalog_updated_at
    BEFORE UPDATE ON vod_catalog
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- Series seasons
CREATE TABLE IF NOT EXISTS vod_series (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    catalog_id      UUID NOT NULL REFERENCES vod_catalog(id) ON DELETE CASCADE,
    season_number   INTEGER NOT NULL,
    title           VARCHAR(200),         -- optional season title
    description     TEXT,
    poster_url      TEXT,
    sort_order      INTEGER DEFAULT 0,
    created_at      TIMESTAMPTZ DEFAULT NOW(),
    UNIQUE (catalog_id, season_number)
);

CREATE INDEX IF NOT EXISTS idx_vod_series_catalog ON vod_series(catalog_id, season_number);

-- Series episodes
CREATE TABLE IF NOT EXISTS vod_episodes (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    series_id        UUID NOT NULL REFERENCES vod_series(id) ON DELETE CASCADE,
    episode_number   INTEGER NOT NULL,
    title            VARCHAR(500) NOT NULL,
    description      TEXT,
    duration_seconds INTEGER NOT NULL,
    source_url       TEXT NOT NULL,       -- HLS URL — INTERNAL ONLY
    thumbnail_url    TEXT,
    air_date         DATE,
    sort_order       INTEGER DEFAULT 0,
    created_at       TIMESTAMPTZ DEFAULT NOW(),
    UNIQUE (series_id, episode_number)
);

CREATE INDEX IF NOT EXISTS idx_vod_episodes_series ON vod_episodes(series_id, episode_number);

-- Watch progress: per-subscriber resume tracking (polymorphic: movie or episode)
CREATE TABLE IF NOT EXISTS watch_progress (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    subscriber_id    UUID NOT NULL REFERENCES subscribers(id) ON DELETE CASCADE,
    content_type     VARCHAR(20) NOT NULL CHECK (content_type IN ('movie','episode')),
    content_id       UUID NOT NULL,       -- vod_catalog.id for movies, vod_episodes.id for episodes
    position_seconds INTEGER DEFAULT 0,
    duration_seconds INTEGER NOT NULL,
    completed        BOOLEAN DEFAULT FALSE, -- true when position > 90% of duration
    last_watched_at  TIMESTAMPTZ DEFAULT NOW(),
    created_at       TIMESTAMPTZ DEFAULT NOW(),
    updated_at       TIMESTAMPTZ DEFAULT NOW(),
    UNIQUE (subscriber_id, content_type, content_id)
);

CREATE INDEX IF NOT EXISTS idx_watch_progress_sub_recent
    ON watch_progress(subscriber_id, last_watched_at DESC);
CREATE INDEX IF NOT EXISTS idx_watch_progress_sub_content
    ON watch_progress(subscriber_id, content_type, content_id);

CREATE TRIGGER watch_progress_updated_at
    BEFORE UPDATE ON watch_progress
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
