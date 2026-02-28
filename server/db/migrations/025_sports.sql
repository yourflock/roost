-- 025_sports.sql — Sports Intelligence schema
-- P15-T01: Sports leagues, teams, events, channel mappings, subscriber preferences.

CREATE TABLE sports_leagues (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  name VARCHAR(200) NOT NULL,
  abbreviation VARCHAR(20) NOT NULL UNIQUE,
  sport VARCHAR(50) NOT NULL,
  country_code VARCHAR(3),
  logo_url TEXT,
  season_structure JSONB DEFAULT '{}',
  thesportsdb_id VARCHAR(50),
  api_football_id VARCHAR(50),
  is_active BOOLEAN NOT NULL DEFAULT TRUE,
  sort_order INTEGER DEFAULT 0,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE sports_teams (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  league_id UUID NOT NULL REFERENCES sports_leagues(id) ON DELETE CASCADE,
  name VARCHAR(200) NOT NULL,
  short_name VARCHAR(50),
  abbreviation VARCHAR(10) NOT NULL,
  city VARCHAR(100),
  venue VARCHAR(200),
  logo_url TEXT,
  primary_color VARCHAR(7),
  secondary_color VARCHAR(7),
  conference VARCHAR(50),
  division VARCHAR(50),
  thesportsdb_id VARCHAR(50),
  metadata JSONB DEFAULT '{}',
  is_active BOOLEAN NOT NULL DEFAULT TRUE,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE(league_id, abbreviation)
);

CREATE TABLE sports_events (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  league_id UUID NOT NULL REFERENCES sports_leagues(id) ON DELETE CASCADE,
  home_team_id UUID REFERENCES sports_teams(id),
  away_team_id UUID REFERENCES sports_teams(id),
  season VARCHAR(20) NOT NULL,
  season_type VARCHAR(30) NOT NULL DEFAULT 'regular',
  week VARCHAR(20),
  venue VARCHAR(200),
  scheduled_time TIMESTAMPTZ NOT NULL,
  actual_start_time TIMESTAMPTZ,
  status VARCHAR(20) NOT NULL DEFAULT 'scheduled',
  home_score INTEGER DEFAULT 0,
  away_score INTEGER DEFAULT 0,
  period VARCHAR(20),
  period_scores JSONB DEFAULT '[]',
  broadcast_info JSONB DEFAULT '{}',
  thesportsdb_event_id VARCHAR(50),
  metadata JSONB DEFAULT '{}',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT chk_different_teams CHECK (away_team_id IS NULL OR home_team_id IS NULL OR away_team_id != home_team_id)
);

CREATE TABLE sports_channel_mappings (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  event_id UUID NOT NULL REFERENCES sports_events(id) ON DELETE CASCADE,
  channel_id UUID REFERENCES channels(id) ON DELETE SET NULL,
  start_time TIMESTAMPTZ NOT NULL,
  end_time TIMESTAMPTZ NOT NULL,
  is_primary BOOLEAN NOT NULL DEFAULT TRUE,
  notes VARCHAR(500),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT chk_end_after_start CHECK (end_time > start_time)
);

CREATE TABLE subscriber_sports_preferences (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  subscriber_id UUID NOT NULL REFERENCES subscribers(id) ON DELETE CASCADE,
  profile_id UUID,
  team_id UUID NOT NULL REFERENCES sports_teams(id) ON DELETE CASCADE,
  notification_level VARCHAR(20) NOT NULL DEFAULT 'all',
  auto_dvr BOOLEAN NOT NULL DEFAULT TRUE,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Indexes
CREATE INDEX idx_sports_events_league_time ON sports_events(league_id, scheduled_time);
CREATE INDEX idx_sports_events_home_time ON sports_events(home_team_id, scheduled_time);
CREATE INDEX idx_sports_events_away_time ON sports_events(away_team_id, scheduled_time);
CREATE INDEX idx_sports_events_status ON sports_events(status);
CREATE INDEX idx_sports_channel_mappings_channel ON sports_channel_mappings(channel_id, start_time, end_time);
CREATE INDEX idx_subscriber_sports_prefs ON subscriber_sports_preferences(subscriber_id);
CREATE UNIQUE INDEX idx_unique_sub_profile_team ON subscriber_sports_preferences(subscriber_id, COALESCE(profile_id, '00000000-0000-0000-0000-000000000000'::uuid), team_id);

-- Updated_at triggers (reuse set_updated_at defined in 001_subscribers.sql)
CREATE TRIGGER set_sports_leagues_updated_at BEFORE UPDATE ON sports_leagues FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER set_sports_teams_updated_at BEFORE UPDATE ON sports_teams FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER set_sports_events_updated_at BEFORE UPDATE ON sports_events FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- Seed major US leagues
INSERT INTO sports_leagues (name, abbreviation, sport, country_code, sort_order, season_structure) VALUES
  ('National Football League', 'NFL', 'american_football', 'US', 1,
   '{"type":"seasonal","periods":["regular","postseason"],"period_names":{"Q1":"1st Quarter","Q2":"2nd Quarter","Q3":"3rd Quarter","Q4":"4th Quarter","OT":"Overtime"}}'),
  ('National Basketball Association', 'NBA', 'basketball', 'US', 2,
   '{"type":"seasonal","period_names":{"Q1":"1st Quarter","Q2":"2nd Quarter","Q3":"3rd Quarter","Q4":"4th Quarter","OT":"Overtime"}}'),
  ('Major League Baseball', 'MLB', 'baseball', 'US', 3,
   '{"type":"seasonal","period_names":{"1":"1st Inning","2":"2nd Inning","3":"3rd Inning","4":"4th Inning","5":"5th Inning","6":"6th Inning","7":"7th Inning","8":"8th Inning","9":"9th Inning","E":"Extra Innings"}}'),
  ('National Hockey League', 'NHL', 'ice_hockey', 'US', 4,
   '{"type":"seasonal","period_names":{"P1":"1st Period","P2":"2nd Period","P3":"3rd Period","OT":"Overtime","SO":"Shootout"}}');
