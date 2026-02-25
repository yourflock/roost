-- 012_stream_sessions.sql — P4: Stream ingest additions.
-- stream_sessions table already created in 003_channels.sql.
-- This migration adds channel_slug index and ensures ingest-needed columns exist.

-- Add channel_slug to stream_sessions for fast relay lookups without join
ALTER TABLE stream_sessions
  ADD COLUMN IF NOT EXISTS channel_slug TEXT;

-- Populate channel_slug for existing rows (if any)
UPDATE stream_sessions ss
SET channel_slug = c.slug
FROM channels c
WHERE ss.channel_id = c.id
  AND ss.channel_slug IS NULL;

-- Index for relay concurrent-stream queries
CREATE INDEX IF NOT EXISTS idx_sessions_subscriber_active
  ON stream_sessions(subscriber_id, ended_at)
  WHERE ended_at IS NULL;

-- Index for relay session lookup by device
CREATE INDEX IF NOT EXISTS idx_sessions_device_channel
  ON stream_sessions(channel_slug, device_id);

-- Add quality default
ALTER TABLE stream_sessions
  ALTER COLUMN quality SET DEFAULT '720p';

-- Ensure bitrate_config and is_active exist on channels (already in 003, safe to re-add)
ALTER TABLE channels
  ADD COLUMN IF NOT EXISTS bitrate_config JSONB DEFAULT '{"mode":"passthrough"}',
  ADD COLUMN IF NOT EXISTS is_active BOOLEAN DEFAULT true;

-- Comment for audit trail
COMMENT ON COLUMN stream_sessions.channel_slug IS 'Denormalized for fast relay lookups without join to channels table.';
