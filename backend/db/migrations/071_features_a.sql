-- 071_features_a.sql
-- ROOST-FEATURES Group A: Dark VOD, Open Subtitles, Co-op Licensing,
-- Content Insurance, Stream Arbitrage, Submarine Mode, LAN-Only Mode,
-- and Ephemeral Streams.

-- ============================================================
-- Dark VOD — private/unlisted content distribution
-- ============================================================

CREATE TABLE IF NOT EXISTS dark_content (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    creator_id  UUID NOT NULL,
    title       TEXT NOT NULL,
    r2_path     TEXT NOT NULL,
    visibility  TEXT DEFAULT 'dark' CHECK (visibility IN ('dark')),
    created_at  TIMESTAMPTZ DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS dark_invite_codes (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    content_id   UUID NOT NULL REFERENCES dark_content(id) ON DELETE CASCADE,
    code         TEXT NOT NULL UNIQUE,
    redeemed_at  TIMESTAMPTZ,
    redeemed_by  UUID,
    created_at   TIMESTAMPTZ DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS dark_viewer_tokens (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    content_id  UUID NOT NULL REFERENCES dark_content(id) ON DELETE CASCADE,
    viewer_id   UUID,
    token       TEXT NOT NULL UNIQUE,
    expires_at  TIMESTAMPTZ,
    created_at  TIMESTAMPTZ DEFAULT NOW()
);

-- ============================================================
-- Subtitle files — OpenSubtitles cache
-- ============================================================

CREATE TABLE IF NOT EXISTS subtitle_files (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    content_id  TEXT NOT NULL,
    language    TEXT NOT NULL,
    r2_key      TEXT,
    score       DECIMAL(3,2) DEFAULT 0.5,
    fetched_at  TIMESTAMPTZ DEFAULT NOW(),
    UNIQUE (content_id, language)
);

CREATE INDEX IF NOT EXISTS subtitle_files_content_idx
    ON subtitle_files(content_id);

-- ============================================================
-- Co-op licensing pool
-- ============================================================

CREATE TABLE IF NOT EXISTS coop_pool (
    id                 UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    balance            DECIMAL(12,2) DEFAULT 0,
    total_contributed  DECIMAL(12,2) DEFAULT 0,
    updated_at         TIMESTAMPTZ DEFAULT NOW()
);

-- Seed with a single row (only one global pool)
INSERT INTO coop_pool (id, balance, total_contributed)
VALUES ('00000000-0000-0000-0000-c00p00000001', 0, 0)
ON CONFLICT DO NOTHING;

CREATE TABLE IF NOT EXISTS coop_contributions (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    subscriber_id   UUID NOT NULL,
    amount          DECIMAL(8,2) NOT NULL,
    payment_id      TEXT,
    contributed_at  TIMESTAMPTZ DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS coop_licenses (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    content_name   TEXT NOT NULL,
    licensor       TEXT NOT NULL,
    amount_paid    DECIMAL(8,2) NOT NULL,
    effective_date DATE,
    expiry_date    DATE,
    status         TEXT DEFAULT 'active' CHECK (status IN ('active','expired','pending')),
    created_at     TIMESTAMPTZ DEFAULT NOW()
);

-- ============================================================
-- Source quality log — stream arbitrage
-- ============================================================

CREATE TABLE IF NOT EXISTS source_quality_log (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    channel_id       UUID NOT NULL,
    source_id        UUID NOT NULL,
    source_url       TEXT NOT NULL,
    bitrate_kbps     INT,
    latency_ms       INT,
    packet_loss_pct  DECIMAL(5,2),
    score            DECIMAL(5,2),
    measured_at      TIMESTAMPTZ DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS source_quality_log_channel_idx
    ON source_quality_log(channel_id, measured_at DESC);

-- ============================================================
-- Content insurance events
-- ============================================================

CREATE TABLE IF NOT EXISTS content_insurance_events (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    channel_id       UUID,
    content_id       TEXT,
    event_type       TEXT CHECK (event_type IN (
                         'source_down',
                         'source_restored',
                         'license_expiry_warning',
                         'license_expired'
                     )),
    source_url       TEXT,
    replacement_url  TEXT,
    sla_seconds      INT,
    resolved_at      TIMESTAMPTZ,
    created_at       TIMESTAMPTZ DEFAULT NOW()
);

-- ============================================================
-- Ephemeral share links
-- ============================================================

CREATE TABLE IF NOT EXISTS ephemeral_links (
    id                          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    originator_subscriber_id    UUID NOT NULL,
    content_id                  TEXT NOT NULL,
    content_type                TEXT CHECK (content_type IN ('live','vod')),
    signed_jwt                  TEXT NOT NULL UNIQUE,
    max_concurrent              INT DEFAULT 1,
    expires_at                  TIMESTAMPTZ NOT NULL,
    view_count                  INT DEFAULT 0,
    created_at                  TIMESTAMPTZ DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS ephemeral_links_jwt_idx
    ON ephemeral_links(signed_jwt);
