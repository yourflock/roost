# Roost — Developer Documentation

Internal developer documentation for the Roost IPTV backend.

## Guides

- [Deployment](guides/deployment.md) — First deploy, rolling updates, rollback, monitoring

## Architecture

Roost is a managed IPTV backend for Owl. It runs on a single Hetzner CX23 VPS behind
a Cloudflare Tunnel. All traffic enters via Cloudflare (zero-trust), exits to nginx,
which routes to Go microservices.

```
Cloudflare Edge
    │ (HTTPS, CDN)
    ▼
Cloudflare Tunnel (outbound from VPS — no inbound port exposure)
    │
    ▼
nginx:80 (reverse proxy)
    ├── /owl/*        → owl_api:8091
    ├── /billing/*    → billing:8085
    ├── /relay/*      → relay:8090
    ├── /catalog/*    → catalog:8095
    ├── /epg/*        → epg:8096
    ├── /ingest/*     → ingest:8094 (internal only)
    ├── /v1/graphql   → hasura:8080
    ├── /v1/auth/     → auth:4000
    └── /             → subscriber portal (SvelteKit)
```

## Services

| Service | Port | Purpose |
|---------|------|---------|
| `postgres` | 5433 (local) | Primary database |
| `redis` | 6379 | Session cache + rate limiting |
| `hasura` | 8081 (local) | GraphQL API |
| `auth` | 4000 (local) | nHost auth |
| `billing` | 8085 | Stripe subscriptions, invoices |
| `ingest` | 8094 | FFmpeg stream pipeline manager |
| `relay` | 8090 | HLS segment delivery |
| `catalog` | 8095 | Channel catalog + logos |
| `epg` | 8096 | XMLTV/EPG data service |
| `owl_api` | 8091 | Owl Community Addon API |
| `nginx` | 80 | Reverse proxy |
| `cloudflared` | — | Cloudflare Tunnel (outbound) |

## Stack

- **Backend**: nSelf + Go microservices
- **Web portals**: SvelteKit (web/subscribe, web/admin)
- **Database**: PostgreSQL 16
- **CDN / Tunnel**: Cloudflare (zero egress on R2)
- **Media storage**: Cloudflare R2
- **Billing**: Stripe
- **Server**: Hetzner CX23 (fsn1) — upgrades via API when load demands
