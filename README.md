# Roost

Self-hosted media backend for Owl. Run it on your server, NAS, or cloud.

Roost handles the backend: scanning, metadata, transcoding, live TV ingest, EPG, DVR, and streaming. Install it once, then connect Owl on any device. Your entire media library appears in a single unified interface.

## What Roost Serves

- Movies and TV (local files, TMDB metadata)
- Live TV and IPTV (M3U, Xtream Codes, Stalker Portal, HDHomeRun, AntBox USB tuners)
- Music (local FLAC/MP3/AAC, MusicBrainz, Last.fm, AcoustID)
- Podcasts (RSS, iTunes, Podcast Index)
- Games and emulation (ROMs, LibRetro cores, IGDB metadata, cloud saves)
- Live sports (EPG matching, auto-DVR, commercial detection)

## Install

Full install guide: [packages/README.md](packages/README.md)

### NAS (no terminal required)

- Synology: install the SPK package from Package Center
- QNAP: install the QPKG from App Center
- Unraid: find the Roost template in Community Applications

### Docker / VPS

```bash
git clone https://github.com/unyeco/roost.git
cd roost
cp server/.env.example server/.env
nano server/.env   # set ROOST_SECRET_KEY and POSTGRES_PASSWORD at minimum
docker compose -f packages/docker/docker-compose.yml up -d
```

### macOS

```bash
brew install unyeco/tap/roost
brew services start roost
```

### Linux

Download the `.deb` or `.rpm` from the [Releases page](https://github.com/unyeco/roost/releases).

## Structure

```text
roost/
├── server/    # Go source (API, microservices, migrations)
├── packages/  # Platform installers and Docker config
└── .github/   # CI/CD and documentation
```

## License

MIT — Copyright 2026 Aric Camarata.
