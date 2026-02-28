-- 033_onboarding.sql — Subscriber onboarding progress tracking (P17-T04).
-- Tracks which onboarding steps each subscriber has completed.
-- Steps: 1=signup, 2=token copied, 3=owl downloaded, 4=roost added to owl, 5=first stream.

CREATE TABLE IF NOT EXISTS subscriber_onboarding (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    subscriber_id  UUID NOT NULL REFERENCES subscribers(id) ON DELETE CASCADE,
    step_completed INTEGER NOT NULL DEFAULT 1 CHECK (step_completed BETWEEN 1 AND 5),
    is_complete    BOOLEAN NOT NULL DEFAULT FALSE,
    completed_at   TIMESTAMPTZ,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (subscriber_id)
);

CREATE INDEX IF NOT EXISTS idx_subscriber_onboarding_sub ON subscriber_onboarding(subscriber_id);
CREATE INDEX IF NOT EXISTS idx_subscriber_onboarding_incomplete ON subscriber_onboarding(is_complete)
    WHERE is_complete = FALSE;

-- Auto-update updated_at trigger.
CREATE OR REPLACE TRIGGER set_subscriber_onboarding_updated_at
    BEFORE UPDATE ON subscriber_onboarding
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
