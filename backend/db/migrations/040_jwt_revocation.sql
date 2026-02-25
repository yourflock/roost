-- 040_jwt_revocation.sql — JWT token revocation list for P22.2.
-- When a subscriber signs out or changes password, their current JWTs are
-- blocklisted by jti claim. Revoked entries expire automatically after
-- the JWT's expiry time (max 24 hours) to keep the table small.

CREATE TABLE IF NOT EXISTS revoked_tokens (
    jti         TEXT        NOT NULL PRIMARY KEY,   -- JWT ID claim (uuid)
    subscriber_id UUID      NOT NULL,               -- owner of the token
    revoked_at  TIMESTAMPTZ NOT NULL DEFAULT now(), -- when it was revoked
    expires_at  TIMESTAMPTZ NOT NULL,               -- when it can be pruned (= JWT exp)
    reason      TEXT        NOT NULL DEFAULT 'logout' -- logout | password_change | admin
);

-- Index for fast expiry cleanup by the background cron.
CREATE INDEX IF NOT EXISTS revoked_tokens_expires_at_idx ON revoked_tokens (expires_at);
-- Index for per-subscriber lookup (admin view).
CREATE INDEX IF NOT EXISTS revoked_tokens_subscriber_idx ON revoked_tokens (subscriber_id, revoked_at DESC);

-- Prune function — call periodically to remove expired entries.
CREATE OR REPLACE FUNCTION prune_revoked_tokens() RETURNS INTEGER AS $$
DECLARE
    deleted INTEGER;
BEGIN
    WITH deleted_rows AS (
        DELETE FROM revoked_tokens WHERE expires_at < now() RETURNING 1
    )
    SELECT count(*) INTO deleted FROM deleted_rows;
    RETURN deleted;
END;
$$ LANGUAGE plpgsql;
