-- 044_coppa_controls.sql — COPPA compliance controls (P22.4.001).
--
-- Adds is_kid_profile flag to subscribers and a coppa_data_requests table
-- for parent-initiated export and deletion requests per COPPA § 312.6.

-- is_kid_profile: set when the subscriber was created via Flock SSO with kid_profile:true.
ALTER TABLE subscribers
  ADD COLUMN IF NOT EXISTS is_kid_profile BOOLEAN NOT NULL DEFAULT false;

-- parent_subscriber_id: the Roost subscriber who is the registered parent.
-- Set during Flock SSO linking when family_role='parent' is detected.
ALTER TABLE subscribers
  ADD COLUMN IF NOT EXISTS parent_subscriber_id UUID REFERENCES subscribers(id) ON DELETE SET NULL;

-- Partial index: fast lookup for all kid profiles.
CREATE INDEX IF NOT EXISTS idx_subscribers_kid_profiles
  ON subscribers (is_kid_profile)
  WHERE is_kid_profile = true;

-- coppa_data_requests: parent-initiated data access and deletion requests.
-- Separate from GDPR data_deletion_requests because COPPA has different timelines
-- and requires parental verification rather than subscriber self-verification.
CREATE TABLE IF NOT EXISTS coppa_data_requests (
  id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  subscriber_id UUID NOT NULL REFERENCES subscribers(id) ON DELETE CASCADE,
  requested_by  UUID NOT NULL,  -- parent subscriber_id
  request_type  TEXT NOT NULL   CHECK (request_type IN ('export', 'delete')),
  status        TEXT NOT NULL DEFAULT 'pending'
                  CHECK (status IN ('pending','processing','completed','failed')),
  created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  completed_at  TIMESTAMPTZ,
  notes         TEXT
);

CREATE INDEX IF NOT EXISTS idx_coppa_requests_subscriber
  ON coppa_data_requests (subscriber_id, status);

CREATE INDEX IF NOT EXISTS idx_coppa_requests_parent
  ON coppa_data_requests (requested_by, created_at DESC);

COMMENT ON TABLE coppa_data_requests IS
  'COPPA § 312.6 parent-initiated data access and deletion requests for child accounts.';
COMMENT ON COLUMN subscribers.is_kid_profile IS
  'TRUE when this subscriber was created as a child profile via Flock SSO (kid_profile:true JWT claim).';
COMMENT ON COLUMN subscribers.parent_subscriber_id IS
  'References the parent subscriber who manages this kid profile.';
