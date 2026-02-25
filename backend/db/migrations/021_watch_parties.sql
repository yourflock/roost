-- 021_watch_parties.sql — Watch party tables for P13-T05.
-- Creates watch_parties and watch_party_participants tables.

CREATE TABLE IF NOT EXISTS watch_parties (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  host_subscriber_id UUID NOT NULL REFERENCES subscribers(id) ON DELETE CASCADE,
  channel_id UUID REFERENCES channels(id) ON DELETE SET NULL,
  content_type VARCHAR(20) NOT NULL DEFAULT 'live' CHECK (content_type IN ('live','vod','dvr')),
  content_id UUID,
  status VARCHAR(20) NOT NULL DEFAULT 'active' CHECK (status IN ('active','ended')),
  invite_code VARCHAR(8) NOT NULL UNIQUE,
  max_participants INTEGER NOT NULL DEFAULT 10,
  started_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  ended_at TIMESTAMPTZ
);

CREATE TABLE IF NOT EXISTS watch_party_participants (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  party_id UUID NOT NULL REFERENCES watch_parties(id) ON DELETE CASCADE,
  subscriber_id UUID NOT NULL REFERENCES subscribers(id) ON DELETE CASCADE,
  profile_id UUID REFERENCES subscriber_profiles(id) ON DELETE SET NULL,
  joined_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  left_at TIMESTAMPTZ,
  UNIQUE(party_id, subscriber_id)
);

CREATE INDEX IF NOT EXISTS idx_watch_parties_invite ON watch_parties(invite_code, status);
CREATE INDEX IF NOT EXISTS idx_watch_party_participants ON watch_party_participants(party_id);
