-- 013_stream_sessions_channel_optional.sql
-- Make channel_id nullable in stream_sessions so the relay can insert
-- sessions using only channel_slug (without joining to find channel_id).
-- The slug-based approach is faster (no join) and the slug is the primary
-- identifier used by the relay service.

ALTER TABLE stream_sessions
  ALTER COLUMN channel_id DROP NOT NULL;

COMMENT ON COLUMN stream_sessions.channel_id IS 'FK to channels. Nullable — relay inserts use channel_slug instead.';
