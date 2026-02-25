-- 024_resellers.sql — Reseller API (P14-T04/T05)
-- Resellers are third-party partners who can create and manage subscribers on behalf of Roost.
-- Each reseller authenticates via an API key. Revenue is tracked per subscriber.

CREATE TABLE resellers (
  id                    UUID           PRIMARY KEY DEFAULT gen_random_uuid(),
  name                  VARCHAR(200)   NOT NULL,
  contact_email         VARCHAR(255)   NOT NULL UNIQUE,
  api_key_hash          TEXT           NOT NULL,
  api_key_prefix        VARCHAR(8)     NOT NULL,
  revenue_share_percent DECIMAL(5,2)   NOT NULL DEFAULT 30.00,
  branding              JSONB          NOT NULL DEFAULT '{}',
  is_active             BOOLEAN        NOT NULL DEFAULT TRUE,
  created_at            TIMESTAMPTZ    NOT NULL DEFAULT now()
);

CREATE TABLE reseller_subscribers (
  reseller_id   UUID        NOT NULL REFERENCES resellers(id)   ON DELETE CASCADE,
  subscriber_id UUID        NOT NULL REFERENCES subscribers(id) ON DELETE CASCADE,
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (reseller_id, subscriber_id)
);

CREATE TABLE reseller_revenue (
  id                   UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
  reseller_id          UUID        NOT NULL REFERENCES resellers(id),
  subscriber_id        UUID        NOT NULL REFERENCES subscribers(id),
  gross_amount_cents   INTEGER     NOT NULL,
  reseller_share_cents INTEGER     NOT NULL,
  currency             VARCHAR(3)  NOT NULL DEFAULT 'usd',
  created_at           TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_resellers_email           ON resellers(contact_email);
CREATE INDEX idx_resellers_active          ON resellers(is_active);
CREATE INDEX idx_reseller_subscribers      ON reseller_subscribers(reseller_id);
CREATE INDEX idx_reseller_sub_subscriber   ON reseller_subscribers(subscriber_id);
CREATE INDEX idx_reseller_revenue          ON reseller_revenue(reseller_id, created_at DESC);
CREATE INDEX idx_reseller_revenue_sub      ON reseller_revenue(subscriber_id);
