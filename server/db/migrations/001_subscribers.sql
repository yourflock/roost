CREATE EXTENSION IF NOT EXISTS "pgcrypto";

-- Reusable updated_at trigger
CREATE OR REPLACE FUNCTION set_updated_at()
RETURNS TRIGGER AS $$
BEGIN
  NEW.updated_at = now();
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TABLE subscribers (
  id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  email          VARCHAR(255) UNIQUE NOT NULL,
  password_hash  TEXT NOT NULL,
  display_name   VARCHAR(100),
  email_verified BOOLEAN DEFAULT FALSE,
  status         VARCHAR(20) DEFAULT 'pending' CHECK (status IN ('pending','active','suspended','cancelled')),
  is_superowner  BOOLEAN DEFAULT FALSE,
  billing_exempt BOOLEAN DEFAULT FALSE,
  created_at     TIMESTAMPTZ DEFAULT now(),
  updated_at     TIMESTAMPTZ DEFAULT now()
);

CREATE INDEX idx_subscribers_email ON subscribers(email);
CREATE INDEX idx_subscribers_status ON subscribers(status);

CREATE TRIGGER trg_subscribers_updated_at
  BEFORE UPDATE ON subscribers
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE api_tokens (
  id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  subscriber_id UUID NOT NULL REFERENCES subscribers(id) ON DELETE CASCADE,
  token_hash   TEXT NOT NULL,
  token_prefix VARCHAR(8) NOT NULL,
  is_active    BOOLEAN DEFAULT TRUE,
  last_used_at TIMESTAMPTZ,
  created_at   TIMESTAMPTZ DEFAULT now()
);

CREATE INDEX idx_api_tokens_hash ON api_tokens(token_hash);
CREATE INDEX idx_api_tokens_subscriber ON api_tokens(subscriber_id);

CREATE TABLE subscriber_devices (
  id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  subscriber_id UUID NOT NULL REFERENCES subscribers(id) ON DELETE CASCADE,
  device_id     VARCHAR(255),
  device_name   VARCHAR(100),
  ip_address    INET,
  user_agent    TEXT,
  last_active_at TIMESTAMPTZ DEFAULT now(),
  is_active     BOOLEAN DEFAULT TRUE,
  created_at    TIMESTAMPTZ DEFAULT now()
);

CREATE INDEX idx_devices_subscriber_active ON subscriber_devices(subscriber_id, is_active);
