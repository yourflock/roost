-- 034_email_preferences.sql — Per-category email opt-in/out tracking (P17-T05).
-- Categories: billing, trials (mandatory), marketing, product, sports (optional).

CREATE TABLE IF NOT EXISTS email_preferences (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    subscriber_id  UUID NOT NULL REFERENCES subscribers(id) ON DELETE CASCADE,
    category       VARCHAR(50) NOT NULL,
    is_opted_in    BOOLEAN NOT NULL DEFAULT TRUE,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (subscriber_id, category)
);

CREATE INDEX IF NOT EXISTS idx_email_prefs_subscriber ON email_preferences(subscriber_id);
CREATE INDEX IF NOT EXISTS idx_email_prefs_category ON email_preferences(category, is_opted_in);

CREATE OR REPLACE TRIGGER set_email_preferences_updated_at
    BEFORE UPDATE ON email_preferences
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- Pre-populate default preferences for all existing subscribers.
INSERT INTO email_preferences (subscriber_id, category, is_opted_in)
SELECT id, cat, TRUE
FROM subscribers
CROSS JOIN (VALUES ('billing'), ('trials'), ('marketing'), ('product'), ('sports')) AS cats(cat)
ON CONFLICT DO NOTHING;
