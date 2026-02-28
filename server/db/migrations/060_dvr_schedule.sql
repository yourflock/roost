-- Migration: DVR schedule and series recording tables
-- Date: 2026-02-24
-- Roost DVR records from IPTV streams (iptv_sources), not OTA antenna (AntBox)

CREATE TYPE dvr_recording_status AS ENUM (
    'scheduled', 'recording', 'complete', 'failed', 'cancelled'
);

CREATE TABLE dvr_schedule (
    id                  UUID                    NOT NULL PRIMARY KEY DEFAULT gen_random_uuid(),
    roost_id            UUID                    NOT NULL,
    channel_id          UUID                    NOT NULL REFERENCES iptv_sources(id) ON DELETE CASCADE,
    title               TEXT                    NOT NULL,
    start_time          TIMESTAMPTZ             NOT NULL,
    end_time            TIMESTAMPTZ             NOT NULL,
    padding_before_secs INTEGER                 NOT NULL DEFAULT 0,
    padding_after_secs  INTEGER                 NOT NULL DEFAULT 0,
    storage_path_id     UUID                    REFERENCES roost_storage_paths(id),
    status              dvr_recording_status    NOT NULL DEFAULT 'scheduled',
    file_path           TEXT,                   -- set when recording completes
    file_size_bytes     BIGINT,
    storage_path_id_2   UUID,                   -- redundant for FK clarity; see storage_path_id
    scheduled_by        TEXT                    NOT NULL, -- flock_user_id
    created_at          TIMESTAMPTZ             NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_dvr_schedule_roost_time ON dvr_schedule(roost_id, start_time);
CREATE INDEX idx_dvr_schedule_status      ON dvr_schedule(status, start_time);

-- Series recording rules: auto-schedule new recordings when EPG matches show_title
CREATE TABLE dvr_series (
    id              UUID        NOT NULL PRIMARY KEY DEFAULT gen_random_uuid(),
    roost_id        UUID        NOT NULL,
    channel_id      UUID        NOT NULL REFERENCES iptv_sources(id) ON DELETE CASCADE,
    show_title      TEXT        NOT NULL,
    always_record   BOOLEAN     NOT NULL DEFAULT TRUE,
    -- keep_last_n: NULL = keep all recordings; >0 = keep only N most recent
    keep_last_n     INTEGER CHECK (keep_last_n IS NULL OR keep_last_n > 0),
    storage_path_id UUID        REFERENCES roost_storage_paths(id),
    scheduled_by    TEXT        NOT NULL, -- flock_user_id
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_dvr_series_roost ON dvr_series(roost_id);
