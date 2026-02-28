# Roost on Unraid

Roost is a self-hosted media backend for Owl. It handles Live TV, DVR, EPG, VOD, music, podcasts, and games.

## Install via Community Applications

1. Open **Apps** in Unraid
2. Search for **Roost**
3. Click **Install**
4. Fill in the required settings (see below)
5. Click **Apply**

If Roost is not yet in the Community Applications catalog, install via template URL:

1. In the CA plugin, click **Add Container**
2. Paste the template URL:
   `https://raw.githubusercontent.com/unyeco/roost/main/packages/unraid/roost.xml`

## Required Settings

| Setting | Value | Notes |
|---------|-------|-------|
| Secret Key | random 32+ char string | Generate: `openssl rand -hex 32` |
| Postgres Password | any strong password | Used internally |
| App Data / Config | `/mnt/user/appdata/roost` | Where Roost stores its database |

## Optional Settings

| Setting | Default | Notes |
|---------|---------|-------|
| Media Library | `/mnt/user/Media` | Read-only. Point to your existing media share |
| DVR Recordings | `/mnt/user/Recordings/Roost` | Read-write. Where recordings are saved |
| HTTP Port | `7979` | Change if port is already in use |
| Operating Mode | `private` | Use `public` only for managed deployments |

## Connecting to Owl

Once Roost is running:

1. Open the Owl app on any device
2. Go to **Settings** → **Community Addons**
3. Enter your Roost URL: `http://UNRAID-IP:7979`
4. Click **Add**

Your media, live channels, and DVR will appear in Owl's unified library.

## Running Without the Community Applications Plugin

Use Docker Compose directly. A Compose file optimized for Unraid paths is included in
the repository at `packaging/unraid/owl-compose.yml`.

```bash
mkdir -p /mnt/user/appdata/roost
cp packaging/unraid/owl-compose.yml /mnt/user/appdata/roost/docker-compose.yml
# Edit the compose file to set ROOST_SECRET_KEY and POSTGRES_PASSWORD
docker compose -f /mnt/user/appdata/roost/docker-compose.yml up -d
```

## Updating

In Community Applications, click **Check for Updates** and then **Update** when a new
version is available.

Or via Docker:

```bash
docker pull ghcr.io/yourflock/roost:latest
docker stop roost_app
docker rm roost_app
# Re-create using CA or docker compose up -d
```

## Troubleshooting

**Roost won't start — "ROOST_SECRET_KEY is required"**
Set the Secret Key field in the container settings. It must be at least 32 characters.

**Can't connect from Owl — "connection refused"**
Check the HTTP port. The default is 7979. Make sure no firewall is blocking it.
Test from a browser: `http://UNRAID-IP:7979/health` should return `{"status":"ok"}`.

**Postgres errors on startup**
Set POSTGRES_PASSWORD in the container settings. It must match across Roost and the
Postgres container. If you changed it, stop both containers and recreate them.

**Logs**
```bash
docker logs roost_app --tail 100 --follow
```
