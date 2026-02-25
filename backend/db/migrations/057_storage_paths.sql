-- Migration: Roost storage path configuration
-- Date: 2026-02-24

CREATE TYPE storage_path_type AS ENUM ('local', 'minio', 's3', 'nfs');

-- Records every storage location configured for a Roost server.
-- Credentials for minio/s3 are stored in env/vault — never in this table.
CREATE TABLE roost_storage_paths (
    id              UUID                NOT NULL PRIMARY KEY DEFAULT gen_random_uuid(),
    roost_id        UUID                NOT NULL,
    display_name    TEXT                NOT NULL,
    path_type       storage_path_type   NOT NULL,
    -- For local/nfs: absolute filesystem path. For minio/s3: bucket name only.
    path            TEXT                NOT NULL,
    -- Endpoint override for MinIO (URL pattern, no credentials). Empty = use AWS S3 default.
    endpoint        TEXT,
    -- Cached usage stats — nullable until first scan completes
    total_bytes     BIGINT,
    used_bytes      BIGINT,
    item_count      INTEGER,
    last_scanned_at TIMESTAMPTZ,
    is_active       BOOLEAN             NOT NULL DEFAULT TRUE,
    created_at      TIMESTAMPTZ         NOT NULL DEFAULT NOW(),
    UNIQUE (roost_id, path)
);

CREATE INDEX idx_storage_paths_roost ON roost_storage_paths(roost_id);
