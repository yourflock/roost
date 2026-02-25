-- 027_gdpr.sql — GDPR data subject rights: deletion requests table.
-- P16-T04: Privacy & GDPR Compliance
--
-- Implements the right to erasure (GDPR Art. 17) with a 30-day grace period.
-- Subscribers can cancel their deletion request within the 30-day window.
-- After scheduled_deletion_at passes, the admin cron (or manual trigger)
-- processes the deletion by removing the subscriber and all associated data.

CREATE TABLE IF NOT EXISTS data_deletion_requests (
  id                    UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
  subscriber_id         UUID         NOT NULL REFERENCES subscribers(id) ON DELETE CASCADE,
  requested_at          TIMESTAMPTZ  NOT NULL DEFAULT now(),
  scheduled_deletion_at TIMESTAMPTZ  NOT NULL DEFAULT (now() + INTERVAL '30 days'),
  completed_at          TIMESTAMPTZ,
  status                VARCHAR(20)  NOT NULL DEFAULT 'pending'
                          CHECK (status IN ('pending','processing','completed','cancelled')),
  requested_ip          INET,
  notes                 TEXT
);

-- Fast lookup for cron job: pending requests past their scheduled date.
CREATE INDEX IF NOT EXISTS idx_deletion_requests_status
  ON data_deletion_requests(status, scheduled_deletion_at);

-- One pending/processing request per subscriber at a time.
CREATE UNIQUE INDEX IF NOT EXISTS idx_deletion_requests_subscriber_active
  ON data_deletion_requests(subscriber_id)
  WHERE status IN ('pending', 'processing');

COMMENT ON TABLE data_deletion_requests IS
  'GDPR right-to-erasure requests. 30-day grace period before processing.';
COMMENT ON COLUMN data_deletion_requests.scheduled_deletion_at IS
  'Earliest date the deletion may be processed. Subscriber may cancel before this date.';
