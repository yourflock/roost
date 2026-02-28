-- 054_family_docker.sql — Per-family Docker container provisioning registry.
-- Phase FLOCKTV: each family gets a PostgreSQL-only container (~50-100 MB RAM).
-- This table tracks provisioning state, host assignment, and port allocation
-- on the roost-prod Hetzner server (167.235.195.186).

CREATE TABLE IF NOT EXISTS family_containers (
  id             UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
  family_id      UUID        NOT NULL UNIQUE,
  docker_host    TEXT        NOT NULL,  -- e.g., 'roost-prod-1.internal' or '127.0.0.1'
  container_id   TEXT,                 -- Docker container ID (set after provisioning)
  postgres_port  INT         NOT NULL, -- host port mapped to container :5432
  status         TEXT        NOT NULL DEFAULT 'provisioning'
                   CHECK (status IN ('provisioning','active','suspended','deprovisioned')),
  provisioned_at TIMESTAMPTZ,
  last_heartbeat TIMESTAMPTZ,
  db_size_mb     INT         NOT NULL DEFAULT 0,
  -- R2 prefix for family-private data isolation (structural, not policy-only)
  r2_prefix      TEXT        NOT NULL, -- 'roost-family-private/{family_id}/'
  created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS family_containers_host_idx
  ON family_containers (docker_host, status);

CREATE INDEX IF NOT EXISTS family_containers_status_idx
  ON family_containers (status, last_heartbeat);

-- Port allocation ledger: tracks which ports are in use per host.
-- Prevents port conflicts when provisioning new family containers.
CREATE TABLE IF NOT EXISTS docker_port_allocations (
  id          UUID    PRIMARY KEY DEFAULT gen_random_uuid(),
  docker_host TEXT    NOT NULL,
  port        INT     NOT NULL,
  family_id   UUID    REFERENCES family_containers(family_id) ON DELETE CASCADE,
  allocated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  UNIQUE (docker_host, port)
);

CREATE INDEX IF NOT EXISTS docker_port_allocations_host_idx
  ON docker_port_allocations (docker_host, port);
