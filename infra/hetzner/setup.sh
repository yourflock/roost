#!/usr/bin/env bash
# setup.sh — Initial Roost server setup on a fresh Hetzner VPS.
# P18-T06: Infrastructure as Code
#
# Installs: Docker, Docker Compose plugin, FFmpeg, Nginx, Cloudflare Tunnel,
# UFW firewall, Fail2ban, and creates the /opt/roost directory structure.
#
# Idempotent: safe to re-run on an already-configured server to update packages.
#
# Usage (run as root or with sudo from your local machine):
#   ssh root@SERVER_IP 'bash -s' < setup.sh
#
# Or copy and run on the server:
#   scp setup.sh root@SERVER_IP:/tmp/setup.sh
#   ssh root@SERVER_IP 'bash /tmp/setup.sh'
#
# Environment:
#   ROOST_USER   — non-root deploy user (default: deploy)
#   ROOST_DIR    — application directory (default: /opt/roost)

set -euo pipefail

ROOST_USER="${ROOST_USER:-deploy}"
ROOST_DIR="${ROOST_DIR:-/opt/roost}"

log() {
    echo "[$(date '+%Y-%m-%d %H:%M:%S')] $*"
}

# ─────────────────────────────────────────────────────────
# 0. Detect OS
# ─────────────────────────────────────────────────────────

if [ ! -f /etc/os-release ]; then
    echo "ERROR: Cannot determine OS. Expected Ubuntu 22.04 LTS."
    exit 1
fi
. /etc/os-release
if [ "$ID" != "ubuntu" ]; then
    echo "WARNING: This script is designed for Ubuntu. Detected: $ID $VERSION_ID"
fi

# ─────────────────────────────────────────────────────────
# 1. System update
# ─────────────────────────────────────────────────────────

log "Updating system packages..."
apt-get update -qq
apt-get upgrade -y -qq
apt-get install -y -qq \
    curl wget git unzip jq \
    ca-certificates gnupg lsb-release \
    ufw fail2ban \
    ffmpeg \
    postgresql-client \
    rclone

# ─────────────────────────────────────────────────────────
# 2. Docker
# ─────────────────────────────────────────────────────────

if ! command -v docker &>/dev/null; then
    log "Installing Docker..."
    curl -fsSL https://download.docker.com/linux/ubuntu/gpg \
        | gpg --dearmor -o /usr/share/keyrings/docker-archive-keyring.gpg
    echo "deb [arch=$(dpkg --print-architecture) signed-by=/usr/share/keyrings/docker-archive-keyring.gpg] \
        https://download.docker.com/linux/ubuntu $(lsb_release -cs) stable" \
        > /etc/apt/sources.list.d/docker.list
    apt-get update -qq
    apt-get install -y -qq docker-ce docker-ce-cli containerd.io docker-compose-plugin
    systemctl enable docker
    systemctl start docker
    log "Docker installed: $(docker --version)"
else
    log "Docker already installed: $(docker --version)"
fi

# ─────────────────────────────────────────────────────────
# 3. Deploy user
# ─────────────────────────────────────────────────────────

if ! id "$ROOST_USER" &>/dev/null; then
    log "Creating deploy user: $ROOST_USER"
    useradd -m -s /bin/bash -G docker "$ROOST_USER"
else
    log "Deploy user $ROOST_USER already exists."
    # Ensure they are in the docker group
    usermod -aG docker "$ROOST_USER"
fi

# Copy SSH authorized keys from root to deploy user
if [ -f /root/.ssh/authorized_keys ]; then
    mkdir -p "/home/$ROOST_USER/.ssh"
    cp /root/.ssh/authorized_keys "/home/$ROOST_USER/.ssh/authorized_keys"
    chown -R "$ROOST_USER:$ROOST_USER" "/home/$ROOST_USER/.ssh"
    chmod 700 "/home/$ROOST_USER/.ssh"
    chmod 600 "/home/$ROOST_USER/.ssh/authorized_keys"
    log "SSH keys copied to $ROOST_USER."
fi

# ─────────────────────────────────────────────────────────
# 4. SSH hardening
# ─────────────────────────────────────────────────────────

log "Hardening SSH..."
# Disable root login, enforce key auth only
sed -i 's/^#\?PermitRootLogin.*/PermitRootLogin no/' /etc/ssh/sshd_config
sed -i 's/^#\?PasswordAuthentication.*/PasswordAuthentication no/' /etc/ssh/sshd_config
sed -i 's/^#\?PubkeyAuthentication.*/PubkeyAuthentication yes/' /etc/ssh/sshd_config
systemctl reload sshd
log "SSH: root login disabled, password auth disabled."

# ─────────────────────────────────────────────────────────
# 5. Firewall (UFW)
# ─────────────────────────────────────────────────────────

log "Configuring UFW firewall..."
ufw --force reset
ufw default deny incoming
ufw default allow outgoing

# Allow SSH (essential — do this first to avoid locking yourself out)
ufw allow 22/tcp comment "SSH"

# Allow HTTP and HTTPS (Cloudflare Tunnel uses outbound, but Nginx serves local)
ufw allow 80/tcp  comment "HTTP"
ufw allow 443/tcp comment "HTTPS"

# Block direct access to service ports — all traffic goes through Nginx
# Services bind to 127.0.0.1 only; no direct external access needed.

ufw --force enable
log "UFW enabled. Rules: SSH(22), HTTP(80), HTTPS(443) open."

# ─────────────────────────────────────────────────────────
# 6. Fail2ban
# ─────────────────────────────────────────────────────────

log "Configuring Fail2ban..."
cat > /etc/fail2ban/jail.local << 'JAIL'
[DEFAULT]
bantime  = 3600
findtime = 600
maxretry = 5

[sshd]
enabled = true
port    = ssh
logpath = %(sshd_log)s
backend = %(syslog_backend)s
JAIL

systemctl enable fail2ban
systemctl restart fail2ban
log "Fail2ban configured (SSH brute-force protection)."

# ─────────────────────────────────────────────────────────
# 7. System timezone
# ─────────────────────────────────────────────────────────

log "Setting timezone to UTC..."
timedatectl set-timezone UTC

# ─────────────────────────────────────────────────────────
# 8. Cloudflare Tunnel (cloudflared)
# ─────────────────────────────────────────────────────────

if ! command -v cloudflared &>/dev/null; then
    log "Installing Cloudflare Tunnel (cloudflared)..."
    ARCH=$(dpkg --print-architecture)
    wget -q "https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-linux-${ARCH}.deb" \
        -O /tmp/cloudflared.deb
    dpkg -i /tmp/cloudflared.deb
    rm /tmp/cloudflared.deb
    log "cloudflared installed: $(cloudflared --version)"
else
    log "cloudflared already installed: $(cloudflared --version)"
fi

# Note: Tunnel configuration (token) must be set separately via:
#   cloudflared service install --token TUNNEL_TOKEN
# This requires the token from the Cloudflare dashboard.
log "NOTE: Cloudflare Tunnel token must be configured separately."
log "  Run: cloudflared service install --token YOUR_TUNNEL_TOKEN"

# ─────────────────────────────────────────────────────────
# 9. Application directory structure
# ─────────────────────────────────────────────────────────

log "Creating application directory structure at $ROOST_DIR..."
mkdir -p "$ROOST_DIR"/{backend,logs,backups,ssl}
chown -R "$ROOST_USER:$ROOST_USER" "$ROOST_DIR"
chmod 750 "$ROOST_DIR"
log "Directory structure created."

# ─────────────────────────────────────────────────────────
# 10. Log rotation
# ─────────────────────────────────────────────────────────

cat > /etc/logrotate.d/roost << 'LOGROTATE'
/opt/roost/logs/*.log {
    daily
    rotate 14
    compress
    delaycompress
    missingok
    notifempty
    create 0640 deploy deploy
}
LOGROTATE
log "Log rotation configured."

# ─────────────────────────────────────────────────────────
# Summary
# ─────────────────────────────────────────────────────────

log "─────────────────────────────────────"
log "Setup complete. Summary:"
log "  Deploy user: $ROOST_USER"
log "  App dir:     $ROOST_DIR"
log "  Docker:      $(docker --version)"
log "  FFmpeg:      $(ffmpeg -version 2>&1 | head -1)"
log "  UFW:         $(ufw status | head -1)"
log ""
log "Next steps:"
log "  1. Copy application files to $ROOST_DIR"
log "  2. Create $ROOST_DIR/.env with production credentials"
log "  3. Configure Cloudflare Tunnel: cloudflared service install --token TOKEN"
log "  4. Start services: cd $ROOST_DIR/backend && docker compose -f docker-compose.prod.yml up -d"
log "─────────────────────────────────────"
