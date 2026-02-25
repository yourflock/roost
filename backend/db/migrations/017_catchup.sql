-- Migration 017: Catchup Recording Metadata
-- Phase P10: VOD & Catchup Content
-- Tracks per-channel, per-hour recording status and storage stats

CREATE TABLE IF NOT EXISTS catchup_recordings (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    channel_id     UUID NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    date           DATE NOT NULL,
    hour           INTEGER NOT NULL CHECK (hour >= 0 AND hour <= 23),
    segment_count  INTEGER DEFAULT 0,
    total_bytes    BIGINT DEFAULT 0,
    status         VARCHAR(20) DEFAULT 'recording' CHECK (
                       status IN ('recording','complete','archived','error')
                   ),
    created_at     TIMESTAMPTZ DEFAULT NOW(),
    updated_at     TIMESTAMPTZ DEFAULT NOW(),
    UNIQUE (channel_id, date, hour)
);

CREATE INDEX IF NOT EXISTS idx_catchup_channel_date ON catchup_recordings(channel_id, date);
CREATE INDEX IF NOT EXISTS idx_catchup_status       ON catchup_recordings(status);
CREATE INDEX IF NOT EXISTS idx_catchup_date         ON catchup_recordings(date);

CREATE TRIGGER catchup_recordings_updated_at
    BEFORE UPDATE ON catchup_recordings
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- Catchup settings per channel (opt-in; admin-controlled)
CREATE TABLE IF NOT EXISTS catchup_settings (
    channel_id      UUID PRIMARY KEY REFERENCES channels(id) ON DELETE CASCADE,
    enabled         BOOLEAN DEFAULT FALSE,
    retention_days  INTEGER DEFAULT 7 CHECK (retention_days >= 1 AND retention_days <= 30),
    updated_at      TIMESTAMPTZ DEFAULT NOW()
);

CREATE TRIGGER catchup_settings_updated_at
    BEFORE UPDATE ON catchup_settings
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
