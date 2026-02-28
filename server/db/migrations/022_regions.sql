-- 022_regions.sql — Regional content packages (P14-T02)
-- Adds regions, channel-region mapping, and subscriber region assignment.

CREATE TABLE regions (
  id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  name       VARCHAR(100) NOT NULL,
  code       VARCHAR(10)  NOT NULL UNIQUE,
  countries  JSONB        NOT NULL DEFAULT '[]',
  is_active  BOOLEAN      NOT NULL DEFAULT TRUE,
  created_at TIMESTAMPTZ  NOT NULL DEFAULT now()
);

CREATE TABLE channel_regions (
  channel_id UUID NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
  region_id  UUID NOT NULL REFERENCES regions(id)  ON DELETE CASCADE,
  PRIMARY KEY (channel_id, region_id)
);

ALTER TABLE subscribers
  ADD COLUMN IF NOT EXISTS region_id UUID REFERENCES regions(id);

CREATE INDEX idx_subscribers_region    ON subscribers(region_id);
CREATE INDEX idx_channel_regions_region ON channel_regions(region_id);

-- Seed default regions
INSERT INTO regions (code, name, countries) VALUES
  ('us',    'North America',               '["US","CA","MX"]'),
  ('eu',    'Europe',                      '["GB","DE","FR","IT","ES","NL","BE","PL","SE","NO","DK","FI","AT","CH","PT","IE","GR","CZ","HU","RO","BG","HR","SK","SI"]'),
  ('mena',  'Middle East & North Africa',  '["SA","AE","EG","IQ","JO","KW","LB","LY","MA","OM","PS","QA","SY","TN","YE","BH","DZ"]'),
  ('apac',  'Asia-Pacific',                '["AU","NZ","SG","MY","ID","PH","TH","VN","IN","JP","KR","HK","TW"]'),
  ('latam', 'Latin America',               '["BR","AR","CO","CL","PE","VE","EC","BO","PY","UY"]');
