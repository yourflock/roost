-- 053_roost_boost.sql — Roost Boost: IPTV contribution pool + canonical channel registry.
-- Phase FLOCKTV FTV.2: subscribers contribute M3U/Xtream accounts to unlock live TV.
-- Credentials stored as AES-256-GCM encrypted blobs — NEVER plaintext.

-- IPTV source contributions from subscribers.
-- encrypted_creds: AES-256-GCM ciphertext of the M3U URL or Xtream credentials JSON.
-- creds_nonce: GCM nonce (12 bytes) stored alongside ciphertext for decryption.
CREATE TABLE IF NOT EXISTS iptv_contributions (
  id               UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
  subscriber_id    UUID        NOT NULL REFERENCES subscribers(id) ON DELETE CASCADE,
  source_type      TEXT        NOT NULL CHECK (source_type IN ('m3u_url','xtream')),
  -- Encrypted with AES-256-GCM. Key from ROOST_CREDS_KEY env var (never in DB).
  encrypted_creds  BYTEA       NOT NULL,
  creds_nonce      BYTEA       NOT NULL,
  channel_count    INT         NOT NULL DEFAULT 0,
  last_verified    TIMESTAMPTZ,
  active           BOOLEAN     NOT NULL DEFAULT true,
  created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  label            TEXT        -- user-given label, e.g., "My IPTV Provider"
);

CREATE INDEX IF NOT EXISTS iptv_contributions_subscriber_idx
  ON iptv_contributions (subscriber_id, active);

CREATE INDEX IF NOT EXISTS iptv_contributions_active_idx
  ON iptv_contributions (active, last_verified);

-- Canonical channel registry: one row per unique channel across all contributors.
-- Deduplication: same channel from N contributors = 1 canonical entry + N source rows.
CREATE TABLE IF NOT EXISTS canonical_channels (
  id                   UUID    PRIMARY KEY DEFAULT gen_random_uuid(),
  tvg_id               TEXT    UNIQUE,                   -- tvg-id from M3U (may be null)
  normalized_name      TEXT    NOT NULL UNIQUE,          -- lowercased, punctuation-stripped name
  canonical_name       TEXT    NOT NULL,                 -- display name
  country              TEXT,
  category             TEXT,
  logo_url             TEXT,
  active_source_count  INT     NOT NULL DEFAULT 0,
  ingest_active        BOOLEAN NOT NULL DEFAULT false,   -- whether always-on ingest is running
  created_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at           TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS canonical_channels_ingest_idx
  ON canonical_channels (ingest_active, active_source_count DESC);

CREATE INDEX IF NOT EXISTS canonical_channels_category_idx
  ON canonical_channels (category, country);

-- Channel sources: maps a canonical channel to one contributor stream URL.
-- priority 0 = primary ingest source. priority 1,2,... = failover backups.
-- source_url is the resolved stream URL, refreshed hourly by the ingest service.
CREATE TABLE IF NOT EXISTS channel_sources (
  id                   UUID     PRIMARY KEY DEFAULT gen_random_uuid(),
  canonical_channel_id UUID     NOT NULL REFERENCES canonical_channels(id) ON DELETE CASCADE,
  contribution_id      UUID     NOT NULL REFERENCES iptv_contributions(id) ON DELETE CASCADE,
  source_url           TEXT     NOT NULL,  -- resolved stream URL (refresh hourly)
  priority             INT      NOT NULL DEFAULT 0,  -- 0=primary, 1+=backup
  health_score         SMALLINT NOT NULL DEFAULT 100 CHECK (health_score BETWEEN 0 AND 100),
  last_checked         TIMESTAMPTZ,
  created_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  UNIQUE (canonical_channel_id, contribution_id)
);

CREATE INDEX IF NOT EXISTS channel_sources_canonical_idx
  ON channel_sources (canonical_channel_id, priority, health_score DESC);

CREATE INDEX IF NOT EXISTS channel_sources_health_idx
  ON channel_sources (health_score, last_checked);
