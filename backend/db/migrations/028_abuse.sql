-- 028_abuse.sql — Abuse detection and flagged subscribers table.
-- P16-T05: Abuse Detection
--
-- When the abuse detector identifies suspicious behaviour (e.g. a single IP
-- streaming with multiple different subscriber tokens — a clear indication of
-- token sharing), it records an AbuseEvent in audit_log and upserts a row into
-- flagged_subscribers. Admins can then review and resolve (clear/suspend/ban).

CREATE TABLE IF NOT EXISTS flagged_subscribers (
  id            UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
  subscriber_id UUID        NOT NULL REFERENCES subscribers(id) ON DELETE CASCADE UNIQUE,
  flag_reason   VARCHAR(100) NOT NULL,
  flag_details  JSONB        NOT NULL DEFAULT '{}',
  auto_flagged  BOOLEAN      NOT NULL DEFAULT TRUE,
  flagged_at    TIMESTAMPTZ  NOT NULL DEFAULT now(),
  reviewed_at   TIMESTAMPTZ,
  reviewed_by   UUID,
  resolution    VARCHAR(50)  CHECK (resolution IN ('cleared','suspended','banned'))
);

-- Admin review queue: unreviewed flags first.
CREATE INDEX IF NOT EXISTS idx_flagged_subscribers_unreviewed
  ON flagged_subscribers(flagged_at DESC)
  WHERE reviewed_at IS NULL;

COMMENT ON TABLE flagged_subscribers IS
  'Subscribers flagged by automated abuse detection or manual admin review.';
COMMENT ON COLUMN flagged_subscribers.resolution IS
  'Admin resolution: cleared=false alarm, suspended=account suspended, banned=permanent ban.';
