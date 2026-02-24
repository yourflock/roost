# Roost

Universal media backend for Owl. Open source (MIT).

Roost runs on your NAS, VPS, or any Linux server and serves all your media through Owl clients. It handles scanning, metadata, transcoding, live TV ingest, EPG, and streaming. Owl connects to it and presents a unified library across all platforms.

## What Roost Serves

- Movies and TV (local files, TMDB metadata)
- Live TV and IPTV (M3U, Xtream Codes, Stalker Portal, HDHomeRun, AntBox USB tuners)
- Music (local FLAC/MP3/AAC, MusicBrainz, Last.fm, AcoustID)
- Podcasts (RSS, iTunes, Podcast Index)
- Games and emulation (ROMs, LibRetro cores, IGDB metadata, cloud saves)
- Live sports (EPG matching, auto-DVR, commercial detection)

## Two Modes

### Private mode (default)

Personal or family media server. Serves your own files. Connect via LAN IP or DynDNS. No billing, no account required. Optional family sharing via Flock SSO.

### Public mode (`ROOST_MODE=public`)

Turns Roost into a licensed content provider. Adds subscriber management, Stripe billing, CDN relay for source URL obfuscation, and content licensing integration. This is how `roost.yourflock.com` operates.

## Install

### NAS (no terminal required)

- Synology: install the SPK package from Package Center
- QNAP: install the QPKG from App Center
- Unraid: find the Roost template in Community Applications

### Docker / VPS

```sh
docker run -v /your/media:/media roost/roost
```

### Managed

Subscribe at [roost.yourflock.com](https://roost.yourflock.com). Get an API token. Enter it in Owl under Settings > Community Addons.

## Structure

```
roost/
├── backend/    # Go microservices + nSelf (ingest, catalog, billing, Owl addon API)
├── web/        # SvelteKit (subscriber portal + admin panel)
└── infra/      # Hetzner + Cloudflare infrastructure config
```

## License

MIT — Copyright 2026 Flock / Aric Camarata.
