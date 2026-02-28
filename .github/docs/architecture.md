# Roost — Architecture

Roost is a set of Go microservices behind an nginx reverse proxy. Each service has a
single responsibility and its own port. They share a PostgreSQL database and an optional
Redis cache.

---

## Request Flow

```text
Owl app (or any HTTP client)
    │ (HTTPS)
    ▼
nginx (packages/docker/nginx.conf)
    ├── /owl/*         → owl_api:8091
    ├── /relay/*       → relay:8090
    ├── /catalog/*     → catalog:8095
    ├── /epg/*         → epg:8096
    ├── /billing/*     → billing:8085
    └── /ingest/*      → ingest:8094  (internal only)
```

---

## Services

### Core API

| Service | Port | Binary | Description |
| --- | --- | --- | --- |
| `api` | 3001 | `server/cmd/api` | Auth, tokens, profiles, rate limiting |
| `owl_api` | 8091 | `server/services/owl_api` | Owl Community Addon API |

### Stream Delivery

| Service | Port | Description |
| --- | --- | --- |
| `ingest` | 8094 | FFmpeg pipeline: pulls IPTV sources, transcodes to HLS |
| `relay` | 8090 | HLS segment cache and signed-URL delivery to clients |
| `catchup` | — | 7-day rolling archive from ingest recordings |
| `dvr` | — | User-scheduled recordings, stored to object storage |

### Content & Metadata

| Service | Port | Description |
| --- | --- | --- |
| `catalog` | 8095 | Channel list, logos, source metadata |
| `epg` | 8096 | XMLTV/EPG ingestion, program schedule queries |
| `metadata` | — | TMDB/TVDB/MusicBrainz enrichment for VOD content |
| `vod` | — | VOD ingest pipeline, file scanning, stream URL generation |
| `sports` | — | Sports schedule matching, auto-DVR triggers, game detection |
| `skip` | — | Scene skip markers (commercial detection, skip timestamps) |

### Infrastructure Services

| Service | Description |
| --- | --- |
| `boost` | Worker pool management for concurrent ingest jobs |
| `streams` | Stream health monitoring, failover, source routing |
| `pool` | Shared resource pooling (DB connections, stream slots) |
| `recommendations` | Watch history analysis, personalized suggestions |

### Advanced Content

| Service | Description |
| --- | --- |
| `podcasts` | RSS/Podcast Index ingestion, episode tracking, Whisper transcription |
| `games` | ROM library scanning, LibRetro core management, cloud save sync |
| `grid_compositor` | Multi-channel grid layout for guide views |
| `content_acquirer` | Automated content acquisition pipeline |
| `clips` | Short-form clip creation from live recordings |
| `franchise` | Series/franchise grouping for VOD catalog |

### Optional / Public Mode

| Service | Port | Description |
| --- | --- | --- |
| `billing` | 8085 | Stripe subscription management (public mode only) |
| `ai_guide` | — | AI-powered content guide (uses OpenAI API) |
| `arbitrage` | — | Multi-source stream quality arbitrage |
| `dark_vod` | — | Unlisted/private VOD content |
| `ephemeral` | — | Time-limited ephemeral content |
| `aggregator` | — | Cross-source content aggregation |
| `broadcast` | — | Live broadcast channel management |
| `channel` | — | Channel management API |
| `auth` | — | User auth service (alternative to built-in) |

---

## Infrastructure

| Component | Description |
| --- | --- |
| PostgreSQL 16 | Primary database. 74 migrations in `server/db/migrations/`. |
| Redis | Rate limiting and session caching. Optional: Roost degrades gracefully without it. |
| MinIO | Local object storage for DVR recordings and HLS segments (self-hosted). |
| Cloudflare R2 | Cloud object storage alternative (zero egress cost). |
| nginx | Reverse proxy and TLS termination. Config: `packages/docker/nginx.conf`. |

---

## Go Workspace

All services live under `server/`. The workspace file at `server/go.work` lets you
work across service modules without `replace` directives.

```text
server/
├── go.mod          — root module (api, auth, billing)
├── go.work         — workspace declaration
├── go.work.sum     — workspace checksums
├── cmd/
│   ├── api/        — main API binary
│   ├── billing/    — standalone billing daemon
│   ├── roost/      — primary service daemon
│   ├── seed/       — dev database seeder
│   └── contract-test/ — integration contract tests
├── services/       — all microservices (each with own go.mod)
├── internal/       — shared internal packages (not exported)
└── pkg/            — shared public packages
```

To build locally:

```bash
cd server
go build ./cmd/api/
go build ./services/owl_api/cmd/owl_api/
```

To work across all modules:

```bash
cd server
go work sync
```

---

## Deployment Modes

Roost runs in one of two modes, set by `ROOST_MODE` in `server/.env`.

### Private mode (default)

Personal or family media server. No billing, no account required. Serves your own
content to authenticated users on your network.

Required env vars: `ROOST_SECRET_KEY`, `POSTGRES_PASSWORD`

### Public mode (`ROOST_MODE=public`)

Adds subscriber management, Stripe billing, and CDN relay for stream delivery.
Used when running Roost as a service for external subscribers.

Additional required vars: `STRIPE_SECRET_KEY`, `STRIPE_WEBHOOK_SECRET`,
`CDN_RELAY_URL`, `CDN_HMAC_SECRET`

---

## Database

74 migrations in `server/db/migrations/`, applied in order. The migration files
group logically:

- `001–008` — Core subscriber auth (registration, sessions, email, 2FA)
- `009–018` — Billing, content catalog, VOD, catchup, DVR
- `019–028` — Profiles, regional pricing, resellers, compliance (GDPR, COPPA)
- `029–043` — Growth features: trials, promos, referrals, analytics, consent
- `051–074` — Advanced: content acquisition, ingest providers, commercial markers,
  scene skip, sports source registry

Apply migrations:

```bash
cd server
go run ./cmd/api/ --migrate
```
