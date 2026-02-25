# Roost VPN — Content Acquisition

## Architecture

Stream delivery and content acquisition use **separate network paths**:

```
Content Source (M3U/Xtream provider)
    ↓
  [VPN Tunnel — tun0]
    ↓
Roost ingest service (Go)
    ↓
HLS segments → Cloudflare R2
    ↓
  [Cloudflare CDN — direct, no VPN]
    ↓
Subscriber (via Cloudflare Worker)
```

The VPN is only in the acquisition leg. Subscriber delivery never touches the VPN.

## Files

| File | Purpose |
|------|---------|
| `openvpn-client.conf` | OpenVPN client config template |
| `vpn-routing.sh` | Selective routing — only source provider CIDRs via VPN |
| `certs/` | Certificate files (gitignored — distributed out-of-band) |

## Required Vault Variables

Set in `your local secrets file (e.g. ~/.roost-secrets.env)`:

```bash
ROOST_VPN_USERNAME=your_vpn_username
ROOST_VPN_PASSWORD=your_vpn_password
ROOST_CONTENT_SOURCE_RANGES=1.2.3.0/24:5.6.7.0/24   # colon-separated CIDRs
```

## Deployment

The VPN runs as a Docker sidecar alongside the ingest service:

```yaml
# In backend/docker-compose.prod.yml (ingest service section):
ingest:
  network_mode: "service:vpn"   # share network namespace with VPN container
  depends_on: [vpn]

vpn:
  image: dperson/openvpn-client
  cap_add: [NET_ADMIN]
  devices: [/dev/net/tun]
  volumes:
    - ./infra/vpn/openvpn-client.conf:/etc/openvpn/client.conf
    - ./infra/vpn/vpn-routing.sh:/etc/openvpn/vpn-routing.sh
    - vpn-certs:/etc/openvpn/certs
  environment:
    - ROOST_CONTENT_SOURCE_RANGES=${ROOST_CONTENT_SOURCE_RANGES}
```

## What Routes Through the VPN

Only IP ranges listed in `ROOST_CONTENT_SOURCE_RANGES`. Determined by:

```bash
# Resolve your content source URLs and look up their CIDR ranges
dig +short your-iptv-source.example.com
# For each IP:
whois $IP | grep -E "CIDR|route:"
```

## What Does NOT Route Through the VPN

- Subscriber delivery (Cloudflare CDN handles this)
- Cloudflare Tunnel connections
- Postgres and internal Docker traffic
- Hetzner management / SSH
- Any monitoring or metrics
