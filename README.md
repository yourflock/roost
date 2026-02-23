# Roost

Managed IPTV backend service for Owl.

Roost is the community addon service for Owl — licensed live TV channels delivered through your Owl app. Subscribers get an API token, enter it in Owl's settings, and licensed channels appear automatically.

## How It Works

1. Subscribe at [roost.yourflock.com](https://roost.yourflock.com)
2. Get your API token
3. Open Owl, go to Settings > Community Addons, enter your token
4. Licensed live TV channels appear in your library

## Structure

```
roost/
├── backend/    # Go microservices + nSelf (stream ingest, billing, Owl addon API)
├── web/        # SvelteKit subscriber portal + admin panel
└── infra/      # Hetzner + Cloudflare infrastructure config
```

## License

Private — All rights reserved. Copyright 2026 Flock / Aric Camarata.
