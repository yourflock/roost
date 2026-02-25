-- Migration: Roost admin foundation
-- Date: 2026-02-24
-- Creates roost_users (access control) and admin_audit_log (immutable audit trail)

-- Role enum for roost_users
CREATE TYPE roost_user_role AS ENUM ('owner', 'admin', 'member', 'guest');

-- Which Flock accounts have access to which Roost server, and at what role
CREATE TABLE roost_users (
    id            UUID             NOT NULL PRIMARY KEY DEFAULT gen_random_uuid(),
    roost_id      UUID             NOT NULL, -- internal Roost server UUID
    flock_user_id TEXT             NOT NULL, -- Flock account ID (external)
    role          roost_user_role  NOT NULL DEFAULT 'member',
    invited_by    TEXT,                      -- flock_user_id of the inviter (NULL for owner)
    added_at      TIMESTAMPTZ      NOT NULL DEFAULT NOW(),
    UNIQUE (roost_id, flock_user_id)
);

CREATE INDEX idx_roost_users_roost   ON roost_users(roost_id);
CREATE INDEX idx_roost_users_flock   ON roost_users(flock_user_id);

-- Immutable audit trail — every admin write action logs here.
-- APPLICATION RULE: Never UPDATE or DELETE rows from this table.
-- It is insert-only. No CASCADE DELETE from any foreign key.
CREATE TABLE admin_audit_log (
    id              UUID        NOT NULL PRIMARY KEY DEFAULT gen_random_uuid(),
    roost_id        UUID        NOT NULL,
    flock_user_id   TEXT        NOT NULL,
    action          TEXT        NOT NULL, -- e.g. "storage.scan", "user.role_change", "stream.kill"
    target_id       TEXT,                 -- optional: ID of the affected resource
    details         JSONB,                -- optional: before/after or extra context
    -- IP sourced from r.RemoteAddr, not X-Forwarded-For (unless behind trusted proxy)
    ip_address      INET,
    occurred_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_audit_log_roost      ON admin_audit_log(roost_id, occurred_at DESC);
CREATE INDEX idx_audit_log_user       ON admin_audit_log(flock_user_id, occurred_at DESC);
CREATE INDEX idx_audit_log_action     ON admin_audit_log(action);
