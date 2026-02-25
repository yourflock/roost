-- Migration 067: Commercial detection tables.
-- Stores per-channel commercial break events detected via Chromaprint or FFmpeg filters.

CREATE TABLE IF NOT EXISTS commercial_fingerprints (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    fingerprint_hash TEXT NOT NULL,
    duration_seconds INT NOT NULL,
    advertiser       TEXT,
    description      TEXT,
    network          TEXT,   -- CBS, NBC, ESPN, FOX
    sport            TEXT,   -- if sport-specific placement
    source           TEXT NOT NULL DEFAULT 'submitted' CHECK (source IN ('submitted', 'auto_learned')),
    confidence       REAL NOT NULL DEFAULT 1.0,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_fingerprints_hash    ON commercial_fingerprints(fingerprint_hash);
CREATE INDEX idx_fingerprints_network ON commercial_fingerprints(network) WHERE network IS NOT NULL;

CREATE TABLE IF NOT EXISTS commercial_events (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    channel_id          UUID NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    started_at          TIMESTAMPTZ NOT NULL,
    ended_at            TIMESTAMPTZ,
    duration_seconds    INT,
    detection_method    TEXT NOT NULL DEFAULT 'silence' CHECK (detection_method IN ('chromaprint', 'blackframe', 'silence', 'manual')),
    confidence          REAL NOT NULL DEFAULT 0.8,
    segment_start_index INT,   -- HLS segment index where break began
    segment_end_index   INT,
    status              TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'ended', 'false_positive')),
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_commercial_events_channel    ON commercial_events(channel_id, started_at DESC);
CREATE INDEX idx_commercial_events_active     ON commercial_events(channel_id, status) WHERE status = 'active';
CREATE INDEX idx_commercial_markers_channel   ON commercial_events(channel_id, started_at);

-- Subscriber commercial skip tracking (for stats computation)
CREATE TABLE IF NOT EXISTS subscriber_commercial_skips (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    subscriber_id   UUID NOT NULL,
    channel_id      UUID REFERENCES channels(id) ON DELETE SET NULL,
    event_id        UUID REFERENCES commercial_events(id) ON DELETE SET NULL,
    skipped_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    saved_seconds   INT NOT NULL DEFAULT 0
);

CREATE INDEX idx_commercial_skips_subscriber ON subscriber_commercial_skips(subscriber_id, skipped_at DESC);

-- Aggregate stats table (populated by daily background job)
CREATE TABLE IF NOT EXISTS subscriber_commercial_stats (
    subscriber_id           UUID NOT NULL,
    period_date             DATE NOT NULL,
    commercials_skipped     INT NOT NULL DEFAULT 0,
    time_saved_seconds      INT NOT NULL DEFAULT 0,
    commercial_free_watched INT NOT NULL DEFAULT 0,
    PRIMARY KEY (subscriber_id, period_date)
);

CREATE INDEX idx_commercial_stats_subscriber ON subscriber_commercial_stats(subscriber_id, period_date DESC);
