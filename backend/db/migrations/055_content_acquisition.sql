-- 055_content_acquisition.sql — Content acquisition queue for shared R2 pool.
-- Phase FLOCKTV: when a family requests a canonical_id not yet in the shared pool,
-- it enters the acquisition queue. The acquisition worker downloads, transcodes,
-- and stores to r2://flock-content/ keyed by canonical_id.
-- Only one in-flight job per canonical_id (enforced by partial unique index).

CREATE TABLE IF NOT EXISTS acquisition_queue (
  id            UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
  canonical_id  TEXT        NOT NULL,
  content_type  TEXT        NOT NULL
                  CHECK (content_type IN ('movie','show','episode','music','game','podcast')),
  requested_by  UUID        REFERENCES subscribers(id) ON DELETE SET NULL,
  status        TEXT        NOT NULL DEFAULT 'queued'
                  CHECK (status IN ('queued','downloading','transcoding','complete','failed')),
  -- R2 path where content is stored after acquisition completes
  r2_path       TEXT,
  error_msg     TEXT,
  queued_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  started_at    TIMESTAMPTZ,
  completed_at  TIMESTAMPTZ,
  retry_count   INT         NOT NULL DEFAULT 0,
  updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS acquisition_queue_status_idx
  ON acquisition_queue (status, queued_at ASC);

-- Only one active (non-terminal) job per canonical_id.
-- complete/failed rows are kept for audit; new requests for same canonical_id
-- are rejected until the prior job reaches terminal state.
CREATE UNIQUE INDEX IF NOT EXISTS acquisition_queue_canonical_active_idx
  ON acquisition_queue (canonical_id)
  WHERE status NOT IN ('complete','failed');

CREATE INDEX IF NOT EXISTS acquisition_queue_canonical_idx
  ON acquisition_queue (canonical_id, status);
