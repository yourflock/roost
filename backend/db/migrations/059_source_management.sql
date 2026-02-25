-- Migration: Source management tables (AntBox, IPTV, Addons)
-- Date: 2026-02-24

-- AntBox tuner devices connected to this Roost
-- antbox_token stored hashed by application layer before insert
CREATE TABLE antboxes (
    id              UUID        NOT NULL PRIMARY KEY DEFAULT gen_random_uuid(),
    roost_id        UUID        NOT NULL,
    display_name    TEXT        NOT NULL,
    location        TEXT,                    -- free text, e.g. "Living room"
    antbox_token    TEXT        NOT NULL,    -- must be stored as bcrypt hash, not plaintext
    tuner_count     INTEGER     NOT NULL DEFAULT 1,
    last_seen_at    TIMESTAMPTZ,
    firmware_version TEXT,
    is_active       BOOLEAN     NOT NULL DEFAULT TRUE,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_antboxes_roost ON antboxes(roost_id);

-- IPTV source configurations
-- config JSONB stores type-specific fields. Application MUST encrypt sensitive fields
-- (password for Xtream, mac for Stalker) using AES-256-GCM before inserting.
CREATE TYPE iptv_source_type AS ENUM ('m3u', 'xtream', 'stalker');

CREATE TABLE iptv_sources (
    id              UUID                NOT NULL PRIMARY KEY DEFAULT gen_random_uuid(),
    roost_id        UUID                NOT NULL,
    display_name    TEXT                NOT NULL,
    source_type     iptv_source_type    NOT NULL,
    config          JSONB               NOT NULL,  -- type-specific; sensitive fields encrypted
    channel_count   INTEGER,
    last_refreshed_at TIMESTAMPTZ,
    is_active       BOOLEAN             NOT NULL DEFAULT TRUE,
    created_at      TIMESTAMPTZ         NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_iptv_sources_roost ON iptv_sources(roost_id);

-- Community addons installed on this Roost (manifest URL must be HTTPS)
CREATE TABLE roost_addons (
    id              UUID        NOT NULL PRIMARY KEY DEFAULT gen_random_uuid(),
    roost_id        UUID        NOT NULL,
    manifest_url    TEXT        NOT NULL,
    display_name    TEXT        NOT NULL,
    version         TEXT,
    catalog_count   INTEGER,
    last_refreshed_at TIMESTAMPTZ,
    is_active       BOOLEAN     NOT NULL DEFAULT TRUE,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (roost_id, manifest_url)
);
CREATE INDEX idx_roost_addons_roost ON roost_addons(roost_id);
