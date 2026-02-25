-- 033_analytics.sql — Subscriber analytics for P17-T05.
-- Daily metrics aggregation, lifecycle staging, engagement scoring, churn risk.

-- daily_metrics — one row per calendar day with aggregate subscriber stats.
CREATE TABLE IF NOT EXISTS daily_metrics (
  id                    UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
  metric_date           DATE        NOT NULL UNIQUE,
  total_subscribers     INTEGER     NOT NULL DEFAULT 0,  -- active paid
  trial_starts          INTEGER     NOT NULL DEFAULT 0,
  trial_conversions     INTEGER     NOT NULL DEFAULT 0,
  new_subscriptions     INTEGER     NOT NULL DEFAULT 0,
  cancellations         INTEGER     NOT NULL DEFAULT 0,
  mrr_cents             BIGINT      NOT NULL DEFAULT 0,  -- monthly recurring revenue in cents
  gross_revenue_cents   BIGINT      NOT NULL DEFAULT 0,
  refunds_cents         BIGINT      NOT NULL DEFAULT 0,
  stream_hours          DECIMAL(12,2) NOT NULL DEFAULT 0,
  unique_streamers      INTEGER     NOT NULL DEFAULT 0,
  computed_at           TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_daily_metrics_date ON daily_metrics(metric_date DESC);

-- subscriber_lifecycle_mv — materialized view refreshed hourly by analytics cron.
-- We use a regular table with upsert rather than a true PG materialized view
-- so we can refresh it in a Go goroutine without REFRESH MATERIALIZED VIEW locks.
CREATE TABLE IF NOT EXISTS subscriber_lifecycle (
  subscriber_id       UUID        PRIMARY KEY REFERENCES subscribers(id) ON DELETE CASCADE,
  lifecycle_stage     VARCHAR(20) NOT NULL DEFAULT 'new'
                        CHECK (lifecycle_stage IN ('trial', 'new', 'active', 'at_risk', 'dormant', 'churned')),
  last_stream_at      TIMESTAMPTZ,
  days_since_stream   INTEGER     NOT NULL DEFAULT 0,
  total_stream_hours  DECIMAL(10,2) NOT NULL DEFAULT 0,
  computed_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_lifecycle_stage ON subscriber_lifecycle(lifecycle_stage);

-- subscriber_engagement — daily engagement scores.
CREATE TABLE IF NOT EXISTS subscriber_engagement (
  subscriber_id        UUID    PRIMARY KEY REFERENCES subscribers(id) ON DELETE CASCADE,
  score                INTEGER NOT NULL DEFAULT 0 CHECK (score BETWEEN 0 AND 100),
  stream_hours_7d      DECIMAL(8,2) NOT NULL DEFAULT 0,
  days_active_7d       INTEGER NOT NULL DEFAULT 0,
  channels_watched_7d  INTEGER NOT NULL DEFAULT 0,
  computed_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- churn_risk — daily churn prediction flags.
CREATE TABLE IF NOT EXISTS churn_risk (
  subscriber_id  UUID        PRIMARY KEY REFERENCES subscribers(id) ON DELETE CASCADE,
  risk_level     VARCHAR(10) NOT NULL DEFAULT 'low'
                   CHECK (risk_level IN ('low', 'medium', 'high')),
  risk_flags     JSONB       NOT NULL DEFAULT '[]',
  computed_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_churn_risk_level ON churn_risk(risk_level);

COMMENT ON TABLE daily_metrics IS
  'Daily aggregate metrics: subscriber counts, revenue, stream hours. One row per day.';
COMMENT ON TABLE subscriber_lifecycle IS
  'Computed lifecycle stage for each subscriber. Refreshed hourly by analytics cron.';
COMMENT ON TABLE subscriber_engagement IS
  'Engagement score (0-100) and component metrics per subscriber. Refreshed daily.';
COMMENT ON TABLE churn_risk IS
  'Churn risk level and flag details per subscriber. Refreshed daily after engagement scoring.';
