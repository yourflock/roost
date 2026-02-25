CREATE TABLE subscription_plans (
  id                        UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  name                      VARCHAR(100) NOT NULL,
  stripe_product_id         VARCHAR(100),
  stripe_price_id_monthly   VARCHAR(100),
  stripe_price_id_annual    VARCHAR(100),
  max_concurrent_streams    INTEGER DEFAULT 2,
  features                  JSONB DEFAULT '{}',
  is_active                 BOOLEAN DEFAULT TRUE,
  created_at                TIMESTAMPTZ DEFAULT now()
);

CREATE TABLE subscriptions (
  id                      UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  subscriber_id           UUID NOT NULL REFERENCES subscribers(id),
  plan_id                 UUID NOT NULL REFERENCES subscription_plans(id),
  stripe_subscription_id  VARCHAR(100) UNIQUE,
  stripe_customer_id      VARCHAR(100),
  status                  VARCHAR(20) DEFAULT 'trialing' CHECK (status IN ('trialing','active','past_due','cancelled','expired')),
  current_period_start    TIMESTAMPTZ,
  current_period_end      TIMESTAMPTZ,
  cancel_at_period_end    BOOLEAN DEFAULT FALSE,
  created_at              TIMESTAMPTZ DEFAULT now(),
  updated_at              TIMESTAMPTZ DEFAULT now()
);

CREATE INDEX idx_subscriptions_subscriber ON subscriptions(subscriber_id);
CREATE INDEX idx_subscriptions_stripe_id ON subscriptions(stripe_subscription_id);
CREATE INDEX idx_subscriptions_status ON subscriptions(status);

CREATE TRIGGER trg_subscriptions_updated_at
  BEFORE UPDATE ON subscriptions
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE payment_events (
  id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  subscription_id   UUID REFERENCES subscriptions(id),
  stripe_event_id   VARCHAR(100) UNIQUE NOT NULL,
  event_type        VARCHAR(50) NOT NULL,
  amount_cents      INTEGER,
  currency          VARCHAR(3) DEFAULT 'usd',
  status            VARCHAR(20),
  metadata          JSONB DEFAULT '{}',
  created_at        TIMESTAMPTZ DEFAULT now()
);

CREATE INDEX idx_payment_events_subscription ON payment_events(subscription_id);

-- Seed: founder plan (first 500 families, $0 forever)
INSERT INTO subscription_plans (name, max_concurrent_streams, features, is_active)
VALUES ('Founder', 5, '{"unlimited":true,"founder":true}', true);

-- Seed: standard plan ($9.99/yr)
INSERT INTO subscription_plans (name, max_concurrent_streams, features, is_active)
VALUES ('Standard', 2, '{"hd":true}', true);
