-- 018_dvr.sql — Cloud DVR recording management.
-- Subscriber-initiated recordings of live programs with quota enforcement.
-- DVR quota per plan: Basic=10h, Premium=50h, Family=100h.

CREATE TABLE IF NOT EXISTS dvr_recordings (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    subscriber_id   UUID NOT NULL REFERENCES subscribers(id) ON DELETE CASCADE,
    channel_id      UUID NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    program_id      UUID,                                        -- nullable: manual recordings may not have EPG entry
    title           VARCHAR(500) NOT NULL,
    start_time      TIMESTAMPTZ NOT NULL,
    end_time        TIMESTAMPTZ NOT NULL,
    status          VARCHAR(20)  NOT NULL DEFAULT 'scheduled'
                        CHECK (status IN ('scheduled','recording','complete','failed','deleted')),
    storage_path    TEXT,                                        -- object storage path after upload
    file_size_bytes BIGINT NOT NULL DEFAULT 0,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_dvr_recordings_subscriber_status
    ON dvr_recordings (subscriber_id, status);

CREATE INDEX IF NOT EXISTS idx_dvr_recordings_start_time
    ON dvr_recordings (start_time)
    WHERE status = 'scheduled';

-- Trigger: auto-update updated_at on row change.
CREATE OR REPLACE FUNCTION dvr_set_updated_at()
RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS trg_dvr_recordings_updated ON dvr_recordings;
CREATE TRIGGER trg_dvr_recordings_updated
    BEFORE UPDATE ON dvr_recordings
    FOR EACH ROW EXECUTE FUNCTION dvr_set_updated_at();

-- View: per-subscriber quota usage in hours.
CREATE OR REPLACE VIEW dvr_quota_usage AS
SELECT
    subscriber_id,
    ROUND(SUM(EXTRACT(EPOCH FROM (end_time - start_time)) / 3600)::NUMERIC, 2) AS used_hours
FROM dvr_recordings
WHERE status IN ('scheduled', 'recording', 'complete')
GROUP BY subscriber_id;

COMMENT ON TABLE dvr_recordings IS
    'Cloud DVR recording jobs — scheduled, active, and completed recordings per subscriber.';
