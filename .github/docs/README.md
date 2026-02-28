# Roost — Developer Documentation

Internal developer documentation for the Roost media backend.

## Index

- [Deployment Guide](guides/deployment.md) — first deploy, rolling updates, rollback, monitoring
- [Skip Format](skip-format.md) — `.skip` sidecar format and Scene Skip standard
- [Commit Convention](commit-convention.md) — conventional commits, scopes, branch naming
- [Roost Home](Home.md) — project overview and install guide

## Architecture

Roost is a self-hosted media backend. Users deploy it behind a reverse proxy (nginx included
in `packages/docker/`). All traffic enters via nginx, which routes to Go microservices.

```text
Client (Owl app)
    │ (HTTPS)
    ▼
nginx:80/443 (reverse proxy — packages/docker/nginx.conf)
    ├── /owl/*        → owl_api:8091
    ├── /billing/*    → billing:8085
    ├── /relay/*      → relay:8090
    ├── /catalog/*    → catalog:8095
    ├── /epg/*        → epg:8096
    └── /ingest/*     → ingest:8094 (internal only)
```

## Services

| Service | Port | Purpose |
| --- | --- | --- |
| `postgres` | 5433 (local) | Primary database |
| `redis` | 6379 | Rate limiting and caching |
| `minio` | 9000 (local) | Object storage (DVR, HLS segments) |
| `billing` | 8085 | Stripe subscriptions (public mode only) |
| `ingest` | 8094 | FFmpeg stream pipeline manager |
| `relay` | 8090 | HLS segment delivery |
| `catalog` | 8095 | Channel catalog + logos |
| `epg` | 8096 | XMLTV/EPG data service |
| `owl_api` | 8091 | Owl Community Addon API |
| `nginx` | 80/443 | Reverse proxy + TLS termination |

## Stack

- **Backend**: Go microservices
- **Database**: PostgreSQL 16
- **Object storage**: Cloudflare R2 (or local MinIO)
- **Billing**: Stripe (optional, public mode only)
- **Go workspace**: `server/go.work` spans all service modules
