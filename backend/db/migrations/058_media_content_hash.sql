-- Migration: Add content hash and duration to recordings for duplicate detection
-- Date: 2026-02-24

ALTER TABLE recordings ADD COLUMN IF NOT EXISTS content_hash TEXT;
ALTER TABLE recordings ADD COLUMN IF NOT EXISTS duration_seconds FLOAT8;

-- Partial index: only hash-indexed rows that have a hash (nulls excluded)
CREATE INDEX idx_recordings_content_hash ON recordings(content_hash)
    WHERE content_hash IS NOT NULL;
