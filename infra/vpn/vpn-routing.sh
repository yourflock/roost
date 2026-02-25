#!/usr/bin/env bash
# vpn-routing.sh — Selective VPN routing for Roost content acquisition.
#
# PURPOSE:
#   Route ONLY content source provider IPs through the VPN tunnel.
#   All other traffic (subscriber delivery, Cloudflare, Hetzner management,
#   Postgres, internal Docker network) uses the normal internet path.
#
# WHY SELECTIVE ROUTING:
#   Routing all traffic through a VPN would:
#   1. Add latency to subscriber delivery (we want Cloudflare CDN for that)
#   2. Reduce throughput for stream relay
#   3. Expose Cloudflare Worker → Origin traffic to the VPN provider
#
#   Selective routing keeps subscriber delivery fast and private (via CF)
#   while only the content acquisition traffic goes through the VPN.
#
# CALLED BY:
#   OpenVPN `up` and `down` script hooks (see openvpn-client.conf).
#   Script receives OpenVPN variables via environment.
#
# USAGE:
#   Not called directly. OpenVPN invokes this after tunnel establishment.
#   To test manually: VPN_ACTION=up CONTENT_SOURCE_RANGES=... ./vpn-routing.sh
#
# ENVIRONMENT VARIABLES:
#   VPN_ACTION — "up" or "down" (defaults to OpenVPN's $script_type)
#   CONTENT_SOURCE_RANGES — colon-separated CIDR list of source IPs to route
#     via VPN. Loaded from ROOST_CONTENT_SOURCE_RANGES vault var at runtime.
#   VPN_IFACE — VPN tunnel interface name (default: tun0, set by OpenVPN as $dev)

set -euo pipefail

# ---- Configuration ----------------------------------------------------------

SCRIPT_TYPE="${script_type:-${VPN_ACTION:-up}}"
VPN_IFACE="${dev:-${VPN_IFACE:-tun0}}"
LOG_TAG="roost-vpn-routing"

# CONTENT_SOURCE_RANGES: colon-separated CIDRs of content provider IPs.
# Loaded from the ROOST_CONTENT_SOURCE_RANGES environment variable at runtime.
# Format: "1.2.3.0/24:5.6.7.0/24:203.0.113.0/24"
#
# These are the IP ranges of your IPTV source providers (M3U/Xtream endpoints).
# Determine them by resolving your source URLs:
#   dig +short your-iptv-source.com
#   whois $IP | grep CIDR
#
# DO NOT hardcode provider IPs here — they change. Load from vault at runtime.
CONTENT_SOURCE_RANGES="${ROOST_CONTENT_SOURCE_RANGES:-}"

# ---- Logging ----------------------------------------------------------------

log() {
    logger -t "${LOG_TAG}" "$*" || echo "[${LOG_TAG}] $*" >&2
}

# ---- Main -------------------------------------------------------------------

main() {
    case "${SCRIPT_TYPE}" in
        up)   setup_routes ;;
        down) teardown_routes ;;
        *)
            log "Unknown script type: ${SCRIPT_TYPE}"
            exit 1
            ;;
    esac
}

# ---- Route setup (VPN tunnel UP) --------------------------------------------

setup_routes() {
    log "VPN tunnel ${VPN_IFACE} up — configuring selective routing"

    if [[ -z "${CONTENT_SOURCE_RANGES}" ]]; then
        log "WARNING: ROOST_CONTENT_SOURCE_RANGES is empty. No source traffic will route via VPN."
        log "Set ROOST_CONTENT_SOURCE_RANGES in your secrets file and reload."
        exit 0
    fi

    # Ensure ip command is available.
    if ! command -v ip &>/dev/null; then
        log "ERROR: 'ip' command not found. Install iproute2."
        exit 1
    fi

    # Create a separate routing table for VPN traffic (table 100).
    # This allows policy routing: only marked packets use the VPN table.
    #
    # Table 100 routes all traffic via the VPN default route.
    # The main table continues to route everything else normally.
    if ! ip route show table 100 &>/dev/null 2>&1; then
        ip route add default dev "${VPN_IFACE}" table 100 || true
        log "Created VPN routing table 100 (default via ${VPN_IFACE})"
    fi

    # For each content source CIDR, add a policy rule that marks matching
    # packets to use routing table 100 (VPN).
    IFS=':' read -ra RANGES <<< "${CONTENT_SOURCE_RANGES}"
    for cidr in "${RANGES[@]}"; do
        if [[ -z "${cidr}" ]]; then continue; fi

        # Add fwmark rule: packets destined for this CIDR use VPN table.
        # Priority 100: checked before the main table (priority 32766).
        if ! ip rule show | grep -q "to ${cidr} lookup 100"; then
            ip rule add to "${cidr}" table 100 priority 100 || {
                log "WARNING: Failed to add rule for ${cidr} — skipping"
                continue
            }
            log "Routing ${cidr} via VPN (${VPN_IFACE})"
        else
            log "Rule for ${cidr} already exists — skipping"
        fi
    done

    # Flush routing cache to apply new rules immediately.
    ip route flush cache 2>/dev/null || true

    log "Selective VPN routing configured. Subscriber traffic remains on direct path."
}

# ---- Route teardown (VPN tunnel DOWN) ---------------------------------------

teardown_routes() {
    log "VPN tunnel ${VPN_IFACE} down — removing selective routing rules"

    # Remove all rules pointing to table 100.
    while ip rule show | grep -q "lookup 100"; do
        # Extract the first matching rule and delete it.
        rule=$(ip rule show | grep "lookup 100" | head -1 | awk '{$1=""; print $0}' | xargs)
        ip rule del ${rule} 2>/dev/null || break
        log "Removed routing rule: ${rule}"
    done

    # Remove table 100 default route.
    ip route del default dev "${VPN_IFACE}" table 100 2>/dev/null || true

    # Flush routing cache.
    ip route flush cache 2>/dev/null || true

    log "VPN routing rules removed. All traffic now on direct path."
}

# ---- Entry point ------------------------------------------------------------

main "$@"
