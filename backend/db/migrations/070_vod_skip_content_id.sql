-- 070_vod_skip_content_id.sql
-- Add skip_content_id to vod_catalog for linking scene skip data. — SKIP.6.2
--
-- skip_content_id uses the scene skip canonical format: {source}:{id}
-- e.g. "imdb:tt1375666", "tmdb:movie:27205"
-- Populated from metadata.imdb_id or metadata.tmdb_id when content is added.

BEGIN;

ALTER TABLE vod_catalog
  ADD COLUMN IF NOT EXISTS skip_content_id TEXT;

CREATE INDEX IF NOT EXISTS idx_vod_skip_content_id ON vod_catalog (skip_content_id)
  WHERE skip_content_id IS NOT NULL;

-- Backfill from existing metadata.imdb_id (most common) or tmdb_id.
UPDATE vod_catalog
SET skip_content_id = CASE
  WHEN metadata->>'imdb_id' IS NOT NULL
    THEN 'imdb:' || (metadata->>'imdb_id')
  WHEN metadata->>'tmdb_id' IS NOT NULL AND type = 'movie'
    THEN 'tmdb:movie:' || (metadata->>'tmdb_id')
  WHEN metadata->>'tmdb_id' IS NOT NULL AND type = 'series'
    THEN 'tmdb:tv:' || (metadata->>'tmdb_id')
  ELSE NULL
END
WHERE skip_content_id IS NULL
  AND (metadata->>'imdb_id' IS NOT NULL OR metadata->>'tmdb_id' IS NOT NULL);

COMMIT;
