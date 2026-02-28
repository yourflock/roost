-- 014_stream_sessions_device_tag.sql
-- Add device_tag (text) to stream_sessions so the relay can track device IDs
-- from Owl clients (which use string identifiers, not UUID FKs to subscriber_devices).
-- The existing device_id UUID FK remains for future use (pairing Owl device with
-- a registered subscriber_devices row). The relay uses device_tag exclusively.

ALTER TABLE stream_sessions
  ADD COLUMN IF NOT EXISTS device_tag TEXT;

CREATE INDEX IF NOT EXISTS idx_sessions_device_tag
  ON stream_sessions(subscriber_id, device_tag)
  WHERE ended_at IS NULL;

COMMENT ON COLUMN stream_sessions.device_tag IS 'Free-text device identifier from Owl client (e.g. "dev1", "apple-tv-kitchen"). Not a FK.';
