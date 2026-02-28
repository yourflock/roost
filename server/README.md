# Roost Backend

Multi-service Go backend for the Roost managed IPTV platform.

## Services

| Service | Port | Description |
|---------|------|-------------|
| `catalog` | 8095 | Channel catalog, categories, EPG sources |
| `ingest` | 8094 | FFmpeg stream ingest, provider sync |
| `relay` | 8096 | HLS segment relay + auth |
| `epg` | 8093 | EPG data sync (XMLTV, Xtream) |
| `owl_api` | 8091 | Owl Community Addon API |
| `sports` | 8097 | Sports events, live scores, metadata display |
| `billing` | 8092 | Stripe subscription billing |
| `catchup` | 8098 | Catchup/timeshift TV |
| `dvr` | 8099 | DVR recording scheduler |
| `grid_compositor` | 8100 | Multi-stream grid compositor |
| `recommendations` | 8101 | Content recommendations |
| `vod` | 8102 | VOD catalog |
| `flocktv` | 8103 | Flock TV multi-tenant services |

## Prerequisites

### Required

- **Go 1.24+**
- **PostgreSQL 15+**
- **Redis 7+**

### Required for commercial detection (04S supplement)

- **ffmpeg** — must be in PATH for silence/black-frame detection.
  - Ubuntu/Debian: `apt-get install ffmpeg`
  - macOS: `brew install ffmpeg`
  - The `detector.go` implementation will return an error on startup if ffmpeg is not found.

### Optional for Chromaprint fingerprint detection

- **fpcalc** (from `libchromaprint-tools` / `chromaprint`) — provides audio fingerprinting.
  - Ubuntu/Debian: `apt-get install libchromaprint-tools`
  - macOS: `brew install chromaprint`
  - Without fpcalc, the system falls back to silence+black-frame detection (~70% accuracy vs ~90%).

## Build

```bash
# From roost/ repo root (uses go.work workspace)
go build ./backend/...

# Build a single service
go build ./backend/services/ingest/cmd/ingest/

# Run all tests
go test -short ./backend/...
```

## Module Structure

Services with their own `go.mod` (separate modules in the workspace):

- `services/ingest` — flexible ingest providers, commercial detection
- `services/grid_compositor` — EPG grid compositor, multi-stream grids
- `services/sports` — sports data, live scores, metadata display API
- `services/relay`, `services/epg`, `services/dvr`, `services/catchup`
- `services/recommendations`, `services/vod`

Services using the root module (`github.com/unyeco/roost`):

- `services/catalog` — channel catalog + TMDB metadata
- `services/auth`, `services/billing`, `services/owl_api`

## Supplement Phases (04S, 11S, 15C, 15D)

### 04S — Flexible Ingest

Provider implementations in `services/ingest/internal/providers/`:
- `m3u_provider.go` — M3U playlist bulk import (streaming parser, 5000+ channels)
- `xtream_provider.go` — Xtream Codes API (credentials encrypted, never logged)
- `hls_provider.go` — Direct HLS URL validation + health check
- `sync_worker.go` — 6-hour background sync, channel upsert, source_removed flag
- `registry.go` — Provider factory interface

DB: migration `066_ingest_providers.sql`

### 11S — Grid Compositor

EPG grid in `services/grid_compositor/internal/epg/`:
- `grid_compositor.go` — Multi-channel EPG grid with CSS grid-span math
- `grid_test.go` — Tests: midnight boundary, short programs, gap fill, ProgressPct

DB: migration `068_metadata_cache.sql` (composite_channels table)

### 15C — Commercial Detection

Commercial detection in `services/ingest/internal/commercials/`:
- `detector.go` — FFmpeg silence+black-frame detection, marker merging
- `skip_api.go` — GET /stream/{channel_id}/skip-markers?position={seconds}
- `commercial_test.go` — Unit tests for all detection paths

**Requires ffmpeg in PATH** (see Prerequisites above).

DB: migration `067_commercial_markers.sql`

### 15D — Metadata Display

Metadata display in multiple services:
- `services/catalog/cmd/catalog/metadata_display.go` — ContentMetadata struct, TMDB cache
- `services/catalog/cmd/catalog/tmdb_client.go` — TMDB REST API client
- `services/sports/metadata_display.go` — Channel metadata, sport config, ticker, SSE

DB: migration `068_metadata_cache.sql` (metadata_cache, sport_display_config tables)
