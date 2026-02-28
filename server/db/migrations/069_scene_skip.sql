-- 069_scene_skip.sql
-- Scene Skip Standard: crowd-sourced content filter sidecar data.
-- Roost is the canonical store; Owl fetches and caches per-client.

BEGIN;

-- ── Category + action enums ─────────────────────────────────────────────────

CREATE TYPE skip_category AS ENUM (
  'sex', 'nudity', 'kissing', 'romance',
  'violence', 'gore', 'language', 'drugs',
  'jump_scare', 'scary'
);

CREATE TYPE skip_action AS ENUM ('skip', 'blur', 'mute', 'warn');

-- ── skip_scenes ─────────────────────────────────────────────────────────────
-- Community-submitted scene timestamps with category + action.

CREATE TABLE skip_scenes (
  id             UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
  content_id     TEXT        NOT NULL,         -- e.g. "imdb:tt1375666"
  content_type   TEXT        NOT NULL DEFAULT 'movie',  -- movie|show_episode|short
  start_seconds  INT         NOT NULL CHECK (start_seconds >= 0),
  end_seconds    INT         NOT NULL CHECK (end_seconds > start_seconds),
  category       skip_category NOT NULL,
  severity       SMALLINT    NOT NULL CHECK (severity BETWEEN 1 AND 5),
  action         skip_action NOT NULL,
  description    TEXT,
  submitted_by   UUID        NOT NULL,         -- Roost subscriber or Flock user UUID
  submitted_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  vote_count     INT         NOT NULL DEFAULT 0,
  disputed       BOOLEAN     NOT NULL DEFAULT FALSE,
  approved       BOOLEAN     NOT NULL DEFAULT FALSE  -- auto-set at vote_count >= 5
);

CREATE INDEX idx_skip_scenes_content  ON skip_scenes (content_id);
CREATE INDEX idx_skip_scenes_approved ON skip_scenes (content_id, approved) WHERE approved = TRUE;
CREATE INDEX idx_skip_scenes_disputed ON skip_scenes (disputed) WHERE disputed = TRUE;

-- ── skip_votes ──────────────────────────────────────────────────────────────
-- 1 vote per user per scene. vote: +1 confirms, -1 disputes.

CREATE TABLE skip_votes (
  scene_id    UUID  NOT NULL REFERENCES skip_scenes (id) ON DELETE CASCADE,
  user_id     UUID  NOT NULL,
  vote        SMALLINT NOT NULL CHECK (vote IN (-1, 1)),
  voted_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  PRIMARY KEY (scene_id, user_id)
);

CREATE INDEX idx_skip_votes_scene ON skip_votes (scene_id);

-- Trigger: recompute vote_count and approve/dispute on every vote change.
CREATE OR REPLACE FUNCTION update_scene_votes()
RETURNS TRIGGER AS $$
DECLARE
  v_up   INT;
  v_down INT;
BEGIN
  SELECT
    COALESCE(SUM(CASE WHEN vote = 1  THEN 1 ELSE 0 END), 0),
    COALESCE(SUM(CASE WHEN vote = -1 THEN 1 ELSE 0 END), 0)
  INTO v_up, v_down
  FROM skip_votes
  WHERE scene_id = COALESCE(NEW.scene_id, OLD.scene_id);

  UPDATE skip_scenes
  SET
    vote_count = v_up - v_down,
    approved   = (v_up >= 5),
    disputed   = (v_down > v_up + 3)
  WHERE id = COALESCE(NEW.scene_id, OLD.scene_id);

  RETURN NULL;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_skip_votes_change
AFTER INSERT OR UPDATE OR DELETE ON skip_votes
FOR EACH ROW EXECUTE FUNCTION update_scene_votes();

-- ── family_skip_overrides ───────────────────────────────────────────────────
-- Family-level per-content overrides (e.g. play this specific scene despite policy).
-- overrides: [{scene_id, action: 'play'|'skip'|'blur'|'mute'|'warn'}, ...]

CREATE TABLE family_skip_overrides (
  id           UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
  family_id    UUID        NOT NULL,
  content_id   TEXT        NOT NULL,
  profile_type TEXT        NOT NULL DEFAULT 'all',  -- kids|teen|adult|family|all
  overrides    JSONB       NOT NULL DEFAULT '[]',
  updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  UNIQUE (family_id, content_id, profile_type)
);

CREATE INDEX idx_family_skip_overrides_lookup ON family_skip_overrides (family_id, content_id);

-- ── profile_skip_policies ───────────────────────────────────────────────────
-- Per-profile global content filter policy (category → action + min_severity).
-- category_actions: {sex: {action: 'skip', min_severity: 1}, ...}

CREATE TABLE profile_skip_policies (
  profile_id       UUID        PRIMARY KEY,
  category_actions JSONB       NOT NULL DEFAULT '{}',
  filtering_on     BOOLEAN     NOT NULL DEFAULT TRUE,
  severity_floor   SMALLINT    NOT NULL DEFAULT 1 CHECK (severity_floor BETWEEN 1 AND 5),
  updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- ── Default policies seeded per profile type ─────────────────────────────────
-- (Applied at profile creation time by the application layer; stored per-profile.)
-- This comment documents canonical defaults:
--   kids    : sex→skip/1, nudity→skip/1, gore→skip/1, drugs→skip/1, language→mute/1, jump_scare→warn/1
--   teen    : sex→skip/3, nudity→skip/3, gore→warn/2, drugs→warn/1
--   adult   : (empty — all play)
--   family  : sex→skip/1, nudity→skip/1, gore→warn/2

-- ── family_content_rating_overrides ──────────────────────────────────────────
-- Inferred smart ratings for Flock families (populated by rating inference engine).

CREATE TABLE family_content_rating_overrides (
  content_id      TEXT     NOT NULL,
  inferred_rating TEXT     NOT NULL,  -- G|PG|PG-13|R|NC-17|UNRATED
  scene_summary   JSONB    NOT NULL DEFAULT '{}',
  -- {violence: 3, language: 1, nudity: 0, ...}
  computed_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  PRIMARY KEY (content_id)
);

COMMIT;
