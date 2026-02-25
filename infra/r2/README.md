# Flock TV R2 Buckets

Five Cloudflare R2 buckets power the Flock TV shared content pool and family-private storage.

## Bucket Map

| Bucket | Purpose | Access |
| ------ | ------- | ------ |
| `flock-content` | Shared media pool: movies, shows, music, games, podcasts | All subscribers read via content-serve Worker |
| `flock-family-private` | Per-family isolated data: photos, diary, social posts | Each family reads only their `{family_id}/` prefix |
| `flock-live` | Always-on IPTV DVR segments (7-day rolling window) | Roost Boost subscribers via live-serve Worker |
| `flock-media` | General Flock media: avatars, cover art, thumbnails | Public CDN read |
| `flock-backups` | PostgreSQL dumps from all family containers | Write-only for backup service, read by ops |

## Key Design Decisions

**Shared content pool**: Common content (movies, shows, music, games) is stored exactly once in
`flock-content/` keyed by canonical ID (e.g., `imdb:tt0111161/720p/manifest.m3u8`). All families
who have "selected" a title read the same R2 object. Storage cost is amortized across all subscribers.

**Structural isolation for private data**: The `flock-family-private` bucket enforces per-family
isolation structurally, not just by policy. The `private-serve.ts` CF Worker always prefixes every
R2 key with the `family_id` extracted from the subscriber JWT. A subscriber cannot access another
family's data even with a crafted path, because the prefix injection happens server-side in the Worker.

**Live TV DVR pattern**: The `flock-live` bucket stores HLS segments as immutable objects.
Segments are `{channel_id}/{YYYYMMDD_HHMM}/{sequence}.ts`. The always-on ingest service writes
segments continuously. DVR window: 7 days. Segments older than 7 days are deleted by a lifecycle
rule. Manifests (`.m3u8`) are regenerated and replaced — they are never cached.

## R2 Key Patterns

```
flock-content/
  imdb:tt0111161/
    manifest.m3u8          # master HLS manifest
    720p/
      manifest.m3u8
      seg_000001.ts
      seg_000002.ts
  tvdb:79169/s01e01/
    manifest.m3u8
    ...

flock-family-private/
  {family_id}/
    photos/2025-06-12-vacation.jpg
    diary/2025-06-12.md
    drive/documents/taxes-2025.pdf

flock-live/
  {canonical_channel_id}/
    20260224_1400/
      playlist.m3u8
      seg_000001.ts
      seg_000002.ts
```

## Setup

```bash
export HCLOUD_TOKEN=<your-hetzner-api-token>  # set your credentials
./infra/r2/setup.sh
```

## CF Worker Deployment

```bash
cd infra/workers/flock-tv
wrangler deploy --config wrangler.toml
wrangler secret put FLOCK_JWT_SECRET
```

## Cost Model

- Storage: $0.015/GB/month (after 10 GB free tier)
- Operations: Class A (writes) $4.50/million, Class B (reads) $0.36/million
- **Egress: $0.00** — zero egress cost is the primary reason for using R2 over Hetzner OBS
  (Hetzner OBS egress would cost ~$99/day at scale)
