-- 035_gdpr_consent.sql — GDPR consent tracking (P22.3.003).
-- Tracks subscriber consent to terms of service, privacy policy, and marketing.
-- Each consent event is immutable (append-only for audit trail).

CREATE TABLE IF NOT EXISTS subscriber_consent (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    subscriber_id   UUID NOT NULL REFERENCES subscribers(id) ON DELETE CASCADE,
    consent_type    VARCHAR(50) NOT NULL, -- 'terms', 'privacy_policy', 'marketing', 'data_processing'
    consented       BOOLEAN NOT NULL,     -- TRUE = granted, FALSE = withdrawn
    consent_version VARCHAR(20),          -- policy version at time of consent
    ip_address      INET,                 -- for audit; anonymized per P22.3.005
    user_agent      TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_consent_subscriber ON subscriber_consent(subscriber_id, consent_type);
CREATE INDEX IF NOT EXISTS idx_consent_type ON subscriber_consent(consent_type, consented);

-- View: latest consent state per subscriber per type.
CREATE OR REPLACE VIEW subscriber_latest_consent AS
SELECT DISTINCT ON (subscriber_id, consent_type)
    subscriber_id,
    consent_type,
    consented,
    consent_version,
    created_at
FROM subscriber_consent
ORDER BY subscriber_id, consent_type, created_at DESC;
