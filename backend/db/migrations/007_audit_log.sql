-- Phase 2: Audit log for security-sensitive actions
-- Used by lockout notifications, account deletion, device revocation, admin actions

CREATE TABLE audit_log (
  id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  subscriber_id UUID REFERENCES subscribers(id) ON DELETE SET NULL,
  action        VARCHAR(100) NOT NULL,
  metadata      JSONB DEFAULT '{}',
  ip_address    INET,
  user_agent    TEXT,
  created_at    TIMESTAMPTZ DEFAULT now()
);

CREATE INDEX idx_audit_log_subscriber ON audit_log(subscriber_id);
CREATE INDEX idx_audit_log_action     ON audit_log(action);
CREATE INDEX idx_audit_log_created    ON audit_log(created_at);
