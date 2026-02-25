CREATE TABLE channel_categories (
  id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  name       VARCHAR(100) NOT NULL UNIQUE,
  sort_order INTEGER DEFAULT 0,
  created_at TIMESTAMPTZ DEFAULT now()
);

CREATE TABLE channels (
  id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  name          VARCHAR(200) NOT NULL,
  slug          VARCHAR(200) UNIQUE NOT NULL,
  category_id   UUID REFERENCES channel_categories(id),
  logo_url      TEXT,
  country_code  VARCHAR(3),
  language_code VARCHAR(5),
  source_url    TEXT NOT NULL,
  source_type   VARCHAR(20) DEFAULT 'hls' CHECK (source_type IN ('hls','rtmp','mpegts')),
  bitrate_config JSONB DEFAULT '{"variants":["720p","1080p"]}',
  is_active     BOOLEAN DEFAULT TRUE,
  epg_channel_id VARCHAR(100),
  sort_order    INTEGER DEFAULT 0,
  created_at    TIMESTAMPTZ DEFAULT now(),
  updated_at    TIMESTAMPTZ DEFAULT now()
);

CREATE INDEX idx_channels_slug ON channels(slug);
CREATE INDEX idx_channels_category ON channels(category_id);
CREATE INDEX idx_channels_active ON channels(is_active);
CREATE INDEX idx_channels_epg ON channels(epg_channel_id);

CREATE TRIGGER trg_channels_updated_at
  BEFORE UPDATE ON channels
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE stream_sessions (
  id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  subscriber_id   UUID NOT NULL REFERENCES subscribers(id),
  channel_id      UUID NOT NULL REFERENCES channels(id),
  device_id       UUID REFERENCES subscriber_devices(id),
  started_at      TIMESTAMPTZ DEFAULT now(),
  ended_at        TIMESTAMPTZ,
  bytes_transferred BIGINT DEFAULT 0,
  ip_address      INET,
  quality         VARCHAR(10)
);

CREATE INDEX idx_sessions_subscriber_ended ON stream_sessions(subscriber_id, ended_at);
CREATE INDEX idx_sessions_channel_started ON stream_sessions(channel_id, started_at);

-- Seed categories
INSERT INTO channel_categories (name, sort_order) VALUES
  ('Sports', 10),
  ('News', 20),
  ('Entertainment', 30),
  ('Kids', 40),
  ('Movies', 50),
  ('Music', 60);
