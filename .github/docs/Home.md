# Roost — Documentation

**Roost v1.0.0** — Open-source, self-hosted media backend for Owl. Handle all your content: Live TV, Movies, VOD, Shows, Music, Podcasts, Games, and Sports.

[![License: MIT](https://img.shields.io/badge/License-MIT-green.svg)](https://github.com/yourflock/roost/blob/main/LICENSE)
[![GitHub release](https://img.shields.io/github/v/release/yourflock/roost)](https://github.com/yourflock/roost/releases/latest)

## What is Roost?

Roost is the backend that powers Owl. Run it on your own server and Owl clients connect to it
as a source — getting your personal library of movies, shows, music, podcasts, games, and live TV.

Run Roost on your own server. Owl clients connect to it as a content source.

## Install in 60 seconds

```bash
git clone https://github.com/yourflock/roost.git
cd roost
cp server/.env.example server/.env
nano server/.env   # set ROOST_SECRET_KEY and POSTGRES_PASSWORD at minimum
docker compose -f packages/docker/docker-compose.yml up -d
```

Full install guide: [packages/README.md](../../packages/README.md)

## Quick Links

| Guide | Description |
| ----- | ----------- |
| [Self-Hosting Guide](https://github.com/yourflock/roost/blob/main/docs/self-hosting.md) | Full setup on VPS, NAS, or bare metal |
| [Docker Compose Reference](https://github.com/yourflock/roost/blob/main/packages/docker/docker-compose.yml) | Configuration options |
| [Owl Addon API](https://github.com/yourflock/roost/blob/main/docs/owl-addon-api.md) | How Owl discovers and streams from Roost |
| [Content Types](https://github.com/yourflock/roost/blob/main/docs/content-types.md) | IPTV, VOD, Music, Podcasts, Games |
| [Contributing](https://github.com/yourflock/roost/blob/main/CONTRIBUTING.md) | How to contribute |
| [Changelog](https://github.com/yourflock/roost/releases) | Release history |

## Content Types Supported (v1.0.0)

| Type | Format | Notes |
| ---- | ------ | ----- |
| Live TV | HLS / IPTV M3U | AntBox tuner or IPTV playlist |
| Movies | H.264, H.265, AV1 | Local files + TMDB metadata |
| Shows | H.264, H.265, AV1 | Episode tracking, TVDB metadata |
| Music | FLAC, MP3, AAC, OPUS | MusicBrainz metadata |
| Podcasts | RSS (podcast:namespace) | Whisper auto-transcription |
| Games | LibRetro cores | IGDB metadata, cloud saves |
| Sports | HLS (via IPTV) | EPG, DVR, commercial skip |

## How Roost connects to Owl

1. Install Roost on your server
2. In the Owl app: Settings > Sources > Add Roost
3. Enter your Roost server URL (e.g., `https://roost.yourdomain.com`)
4. Owl discovers all your content and adds it to the unified library

Roost exposes the Community Addon API at `/owl/v1` — the same API used by other
third-party Owl source providers.

## Requirements

- Docker + Docker Compose (recommended)
- 2 GB RAM minimum (4 GB recommended for transcoding)
- 20 GB disk for OS + transcoding buffer (media lives on your NAS or object storage)
- Linux (amd64 or arm64)

## License

MIT. See [LICENSE](https://github.com/yourflock/roost/blob/main/LICENSE).
Use it however you like — self-host for your family, fork it, contribute back.

## Community

- Issues and feature requests: [github.com/yourflock/roost/issues](https://github.com/yourflock/roost/issues)
- Discussions: [github.com/yourflock/roost/discussions](https://github.com/yourflock/roost/discussions)
- Owl media app: [github.com/yourflock/owl](https://github.com/yourflock/owl)
