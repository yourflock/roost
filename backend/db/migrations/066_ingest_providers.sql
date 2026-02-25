-- Migration 066: Ingest providers table for flexible multi-source stream ingest.
-- Supports M3U playlist, Xtream Codes API, direct HLS URL, and AntBox push sources.

CREATE TABLE IF NOT EXISTS ingest_providers (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name            TEXT NOT NULL,
    provider_type   TEXT NOT NULL CHECK (provider_type IN ('m3u', 'xtream', 'hls', 'rtsp', 'antbox', 'rtmp', 'srt')),
    config          JSONB NOT NULL DEFAULT '{}',  -- encrypted credentials + URLs
    is_active       BOOLEAN NOT NULL DEFAULT true,
    last_sync       TIMESTAMPTZ,
    channel_count   INT NOT NULL DEFAULT 0,
    health_status   TEXT NOT NULL DEFAULT 'unknown' CHECK (health_status IN ('healthy', 'degraded', 'down', 'unknown')),
    last_sync_status TEXT,
    last_sync_channel_count INT,
    sync_interval_hours INT NOT NULL DEFAULT 24,
    stream_key_hash TEXT,       -- hashed stream key for push-type providers (rtmp/srt/antbox)
    stream_key_version INT NOT NULL DEFAULT 1,  -- for rotation
    epg_url         TEXT,       -- extracted EPG URL from M3U header
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_ingest_providers_type     ON ingest_providers(provider_type);
CREATE INDEX idx_ingest_providers_active   ON ingest_providers(is_active) WHERE is_active = true;
CREATE INDEX idx_ingest_providers_sync     ON ingest_providers(last_sync) WHERE is_active = true;

-- Add provider_id FK to channels table (nullable — existing channels have no provider)
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_name = 'channels' AND column_name = 'provider_id'
    ) THEN
        ALTER TABLE channels ADD COLUMN provider_id UUID REFERENCES ingest_providers(id) ON DELETE SET NULL;
        ALTER TABLE channels ADD COLUMN source_external_id TEXT;  -- provider's internal stream ID
        ALTER TABLE channels ADD COLUMN source_removed BOOLEAN NOT NULL DEFAULT false;
        CREATE INDEX idx_channels_provider ON channels(provider_id) WHERE provider_id IS NOT NULL;
    END IF;
END$$;

-- Channel sources table for multi-source failover
CREATE TABLE IF NOT EXISTS channel_sources (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    channel_id      UUID NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    priority        INT NOT NULL DEFAULT 0,  -- lower = higher priority
    source_type     TEXT NOT NULL CHECK (source_type IN ('hls', 'rtmp', 'srt', 'xtream', 'm3u')),
    url             TEXT,            -- null for push sources
    provider_id     UUID REFERENCES ingest_providers(id) ON DELETE SET NULL,
    is_active       BOOLEAN NOT NULL DEFAULT false,
    last_health_check TIMESTAMPTZ,
    health_status   TEXT NOT NULL DEFAULT 'unknown' CHECK (health_status IN ('healthy', 'degraded', 'dead', 'unknown')),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_channel_sources_channel  ON channel_sources(channel_id, priority);
CREATE INDEX idx_channel_sources_active   ON channel_sources(channel_id) WHERE is_active = true;
CREATE UNIQUE INDEX idx_channel_sources_primary ON channel_sources(channel_id) WHERE is_active = true;

-- Stream events log for failover history
CREATE TABLE IF NOT EXISTS stream_events (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    channel_id      UUID NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    event_type      TEXT NOT NULL,   -- 'failover', 'source_down', 'source_up', 'provider_sync'
    from_source_id  UUID REFERENCES channel_sources(id) ON DELETE SET NULL,
    to_source_id    UUID REFERENCES channel_sources(id) ON DELETE SET NULL,
    reason          TEXT,
    metadata        JSONB NOT NULL DEFAULT '{}',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_stream_events_channel ON stream_events(channel_id, created_at DESC);

-- AntBox devices table
CREATE TABLE IF NOT EXISTS antbox_devices (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    provider_id     UUID NOT NULL REFERENCES ingest_providers(id) ON DELETE CASCADE,
    device_name     TEXT NOT NULL,
    pairing_code    TEXT,
    pairing_expires TIMESTAMPTZ,
    last_heartbeat  TIMESTAMPTZ,
    firmware_version TEXT,
    tuner_count     INT NOT NULL DEFAULT 0,
    stream_status   TEXT NOT NULL DEFAULT 'offline' CHECK (stream_status IN ('online', 'offline', 'pushing')),
    metadata        JSONB NOT NULL DEFAULT '{}',  -- detected tuners, capabilities
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_antbox_provider ON antbox_devices(provider_id);
