-- 072_features_b: Boost Storage, Broadcast Studio, Channel Playlists, Clip Economy,
--                 Franchise Mode, Neighborhood Pool, Family Streaming Aggregator, AI Program Guide

-- ─── Boost Storage ──────────────────────────────────────────────────────────

CREATE TABLE IF NOT EXISTS boost_uploads (
  id            UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
  family_id     UUID        NOT NULL,
  uploader_id   UUID        NOT NULL,
  file_key      TEXT        NOT NULL,
  face_count    INT         NOT NULL DEFAULT 0,
  event_label   TEXT,
  upload_date   DATE,
  r2_key        TEXT        NOT NULL,
  status        TEXT        NOT NULL DEFAULT 'pending',
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_boost_uploads_family  ON boost_uploads(family_id);
CREATE INDEX IF NOT EXISTS idx_boost_uploads_status  ON boost_uploads(status);
CREATE INDEX IF NOT EXISTS idx_boost_uploads_date    ON boost_uploads(upload_date DESC NULLS LAST);

CREATE TABLE IF NOT EXISTS boost_face_clusters (
  id          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
  family_id   UUID        NOT NULL,
  label       TEXT        NOT NULL,
  member_id   UUID,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_boost_clusters_family ON boost_face_clusters(family_id);

CREATE TABLE IF NOT EXISTS boost_photo_faces (
  photo_id    UUID  NOT NULL REFERENCES boost_uploads(id)       ON DELETE CASCADE,
  cluster_id  UUID  NOT NULL REFERENCES boost_face_clusters(id) ON DELETE CASCADE,
  confidence  FLOAT NOT NULL DEFAULT 0.0,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (photo_id, cluster_id)
);
CREATE INDEX IF NOT EXISTS idx_boost_photo_faces_cluster ON boost_photo_faces(cluster_id);

-- ─── Broadcast Studio ────────────────────────────────────────────────────────

CREATE TABLE IF NOT EXISTS broadcast_sessions (
  id               UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
  family_id        UUID        NOT NULL,
  stream_key       TEXT        NOT NULL UNIQUE,
  title            TEXT        NOT NULL,
  status           TEXT        NOT NULL DEFAULT 'idle',  -- idle | live | ended
  hls_manifest_key TEXT,
  viewer_count     INT         NOT NULL DEFAULT 0,
  started_at       TIMESTAMPTZ,
  ended_at         TIMESTAMPTZ,
  created_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_broadcast_sessions_family ON broadcast_sessions(family_id);
CREATE INDEX IF NOT EXISTS idx_broadcast_sessions_status ON broadcast_sessions(status);

-- ─── Family Channel Playlists ────────────────────────────────────────────────

CREATE TABLE IF NOT EXISTS channel_playlists (
  id            UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
  family_id     UUID        NOT NULL,
  name          TEXT        NOT NULL,
  description   TEXT,
  items         JSONB       NOT NULL DEFAULT '[]',
  schedule_type TEXT        NOT NULL DEFAULT 'sequential',  -- sequential | shuffle | round_robin
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_channel_playlists_family ON channel_playlists(family_id);

-- ─── Clip Economy ─────────────────────────────────────────────────────────────

CREATE TABLE IF NOT EXISTS clips (
  id                  UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
  family_id           UUID        NOT NULL,
  source_segment_key  TEXT        NOT NULL,
  title               TEXT        NOT NULL,
  duration_secs       INT         NOT NULL DEFAULT 0,
  thumbnail_key       TEXT,
  share_count         INT         NOT NULL DEFAULT 0,
  deleted_at          TIMESTAMPTZ,
  created_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_clips_family  ON clips(family_id);
CREATE INDEX IF NOT EXISTS idx_clips_active  ON clips(family_id) WHERE deleted_at IS NULL;

-- ─── Franchise Mode ───────────────────────────────────────────────────────────

CREATE TABLE IF NOT EXISTS franchise_operators (
  id               UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
  operator_name    TEXT        NOT NULL,
  owner_user_id    UUID        NOT NULL,
  stripe_account_id TEXT,
  subdomain        TEXT        NOT NULL UNIQUE,
  config           JSONB       NOT NULL DEFAULT '{}',
  status           TEXT        NOT NULL DEFAULT 'pending',  -- pending | active | suspended
  created_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_franchise_operators_owner  ON franchise_operators(owner_user_id);
CREATE INDEX IF NOT EXISTS idx_franchise_operators_status ON franchise_operators(status);

CREATE TABLE IF NOT EXISTS franchise_subscriptions (
  id                  UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
  operator_id         UUID        NOT NULL REFERENCES franchise_operators(id) ON DELETE CASCADE,
  subscriber_user_id  UUID        NOT NULL,
  plan_id             TEXT        NOT NULL,
  stripe_sub_id       TEXT,
  status              TEXT        NOT NULL DEFAULT 'active',  -- active | cancelled | past_due
  created_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_franchise_subs_operator ON franchise_subscriptions(operator_id);
CREATE INDEX IF NOT EXISTS idx_franchise_subs_user     ON franchise_subscriptions(subscriber_user_id);

-- ─── Neighborhood Pool ────────────────────────────────────────────────────────

CREATE TABLE IF NOT EXISTS pool_groups (
  id              UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
  name            TEXT        NOT NULL,
  invite_code     TEXT        NOT NULL UNIQUE,
  owner_family_id UUID        NOT NULL,
  max_members     INT         NOT NULL DEFAULT 10,
  created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_pool_groups_owner ON pool_groups(owner_family_id);

CREATE TABLE IF NOT EXISTS pool_members (
  pool_id    UUID        NOT NULL REFERENCES pool_groups(id) ON DELETE CASCADE,
  family_id  UUID        NOT NULL,
  role       TEXT        NOT NULL DEFAULT 'member',  -- owner | member
  joined_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (pool_id, family_id)
);

CREATE TABLE IF NOT EXISTS pool_sources (
  id              UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
  pool_id         UUID        NOT NULL REFERENCES pool_groups(id) ON DELETE CASCADE,
  family_id       UUID        NOT NULL,
  source_url      TEXT        NOT NULL,
  source_type     TEXT        NOT NULL,  -- iptv | nas | vps
  health_score    FLOAT       NOT NULL DEFAULT 1.0,
  last_checked_at TIMESTAMPTZ,
  created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_pool_sources_pool   ON pool_sources(pool_id);
CREATE INDEX IF NOT EXISTS idx_pool_sources_family ON pool_sources(family_id);

-- ─── Family Streaming Aggregator ─────────────────────────────────────────────

CREATE TABLE IF NOT EXISTS aggregator_sources (
  id           UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
  family_id    UUID        NOT NULL,
  source_url   TEXT        NOT NULL,
  source_type  TEXT        NOT NULL,  -- iptv | nas | roost | owl
  last_sync_at TIMESTAMPTZ,
  dedup_hash   TEXT,
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_aggregator_sources_family ON aggregator_sources(family_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_aggregator_sources_dedup ON aggregator_sources(family_id, dedup_hash) WHERE dedup_hash IS NOT NULL;

-- ─── AI Program Guide ─────────────────────────────────────────────────────────

CREATE TABLE IF NOT EXISTS ai_guide_recommendations (
  id           UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
  family_id    UUID        NOT NULL,
  content_id   TEXT        NOT NULL,
  content_type TEXT        NOT NULL,  -- movie | show | live | podcast | game
  score        FLOAT       NOT NULL DEFAULT 0.0,
  reason       TEXT,
  expires_at   TIMESTAMPTZ NOT NULL,
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_ai_guide_family  ON ai_guide_recommendations(family_id);
CREATE INDEX IF NOT EXISTS idx_ai_guide_expires ON ai_guide_recommendations(expires_at);
CREATE INDEX IF NOT EXISTS idx_ai_guide_score   ON ai_guide_recommendations(family_id, score DESC);

CREATE TABLE IF NOT EXISTS ai_guide_feedback (
  id           UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
  family_id    UUID        NOT NULL,
  content_id   TEXT        NOT NULL,
  feedback     TEXT        NOT NULL,  -- like | dislike | not_interested | already_seen
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_ai_feedback_family  ON ai_guide_feedback(family_id);
CREATE INDEX IF NOT EXISTS idx_ai_feedback_content ON ai_guide_feedback(content_id);
