#!/usr/bin/env bash
# setup-server.sh — Roost production server initial setup
# Run ONCE on a fresh Hetzner CX23 server as root.
# Sets up: deploy user, Docker, fail2ban, cloudflared, UFW firewall.
#
# Usage (as root on the server):
#   curl -fsSL https://raw.githubusercontent.com/unyeco/roost/main/backend/scripts/setup-server.sh | bash
#
# Or copy manually:
#   scp backend/scripts/setup-server.sh root@167.235.195.186:/tmp/
#   ssh root@167.235.195.186 bash /tmp/setup-server.sh

set -euo pipefail

# ─────────────────────────────────────────────
# Variables
# ─────────────────────────────────────────────
DEPLOY_USER="deploy"
DEPLOY_HOME="/home/$DEPLOY_USER"
ROOST_DIR="/opt/roost"
# SSH public key — replace with the actual public key from ~/.ssh/flock_deploy.pub
DEPLOY_SSH_PUBKEY="ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAI... flock-deploy"

log() { echo "[$(date '+%H:%M:%S')] $*"; }

# ─────────────────────────────────────────────
# Step 1: System update
# ─────────────────────────────────────────────
log "Step 1: Updating system packages..."
apt-get update -qq
apt-get upgrade -y -qq
apt-get install -y -qq \
  curl wget git vim htop ufw fail2ban \
  ca-certificates gnupg lsb-release \
  postgresql-client

# ─────────────────────────────────────────────
# Step 2: Create deploy user
# ─────────────────────────────────────────────
log "Step 2: Creating deploy user..."
if ! id "$DEPLOY_USER" &>/dev/null; then
  useradd -m -s /bin/bash -G sudo "$DEPLOY_USER"
fi

mkdir -p "$DEPLOY_HOME/.ssh"
echo "$DEPLOY_SSH_PUBKEY" > "$DEPLOY_HOME/.ssh/authorized_keys"
chmod 700 "$DEPLOY_HOME/.ssh"
chmod 600 "$DEPLOY_HOME/.ssh/authorized_keys"
chown -R "$DEPLOY_USER:$DEPLOY_USER" "$DEPLOY_HOME/.ssh"

# Allow deploy user to run docker commands
usermod -aG docker "$DEPLOY_USER" 2>/dev/null || true

# ─────────────────────────────────────────────
# Step 3: Harden SSH
# ─────────────────────────────────────────────
log "Step 3: Hardening SSH..."
sed -i 's/^PermitRootLogin yes/PermitRootLogin no/' /etc/ssh/sshd_config
sed -i 's/^#PermitRootLogin/PermitRootLogin no\n#PermitRootLogin/' /etc/ssh/sshd_config
sed -i 's/^PasswordAuthentication yes/PasswordAuthentication no/' /etc/ssh/sshd_config
echo "AllowUsers $DEPLOY_USER" >> /etc/ssh/sshd_config
systemctl reload sshd

# ─────────────────────────────────────────────
# Step 4: UFW Firewall
# ─────────────────────────────────────────────
log "Step 4: Configuring UFW firewall..."
ufw --force reset
ufw default deny incoming
ufw default allow outgoing
ufw allow ssh
# HTTP/HTTPS only needed if NOT using Cloudflare Tunnel exclusively
# With CF Tunnel, these are technically not needed (tunnel is outbound)
# but keeping them for direct debugging access
ufw allow 80/tcp
ufw allow 443/tcp
ufw --force enable
ufw status

# ─────────────────────────────────────────────
# Step 5: fail2ban
# ─────────────────────────────────────────────
log "Step 5: Configuring fail2ban..."
cat > /etc/fail2ban/jail.local << 'FAIL2BAN'
[DEFAULT]
bantime = 1h
findtime = 10m
maxretry = 5

[sshd]
enabled = true
port = ssh
logpath = %(sshd_log)s
FAIL2BAN
systemctl enable fail2ban
systemctl restart fail2ban

# ─────────────────────────────────────────────
# Step 6: Docker
# ─────────────────────────────────────────────
log "Step 6: Installing Docker..."
if ! command -v docker &>/dev/null; then
  curl -fsSL https://get.docker.com | sh
fi
systemctl enable docker
systemctl start docker
docker --version

# Docker Compose v2 is bundled with Docker Engine — verify
docker compose version

# ─────────────────────────────────────────────
# Step 7: FFmpeg (for ingest service)
# ─────────────────────────────────────────────
log "Step 7: Installing FFmpeg..."
apt-get install -y -qq ffmpeg
ffmpeg -version 2>&1 | head -1

# ─────────────────────────────────────────────
# Step 8: cloudflared
# ─────────────────────────────────────────────
log "Step 8: Installing cloudflared..."
curl -fsSL https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-linux-amd64.deb \
  -o /tmp/cloudflared.deb
dpkg -i /tmp/cloudflared.deb
rm /tmp/cloudflared.deb
cloudflared --version

# ─────────────────────────────────────────────
# Step 9: Create directories
# ─────────────────────────────────────────────
log "Step 9: Creating application directories..."
mkdir -p "$ROOST_DIR"
chown "$DEPLOY_USER:$DEPLOY_USER" "$ROOST_DIR"
mkdir -p /data/segments
chown "$DEPLOY_USER:$DEPLOY_USER" /data/segments

# ─────────────────────────────────────────────
# Step 10: Set timezone
# ─────────────────────────────────────────────
log "Step 10: Setting timezone to UTC..."
timedatectl set-timezone UTC

# ─────────────────────────────────────────────
# Done
# ─────────────────────────────────────────────
log ""
log "Server setup complete!"
log ""
log "Next steps:"
log "  1. Update DEPLOY_SSH_PUBKEY in this script with the actual public key"
log "  2. Log in as deploy user: ssh -i ~/.ssh/flock_deploy deploy@167.235.195.186"
log "  3. Clone the repo: git clone https://github.com/unyeco/roost.git $ROOST_DIR"
log "  4. Create .env: cp $ROOST_DIR/backend/.env.production.example $ROOST_DIR/backend/.env"
log "  5. Initialize DB: $ROOST_DIR/backend/scripts/init-db.sh"
log "  6. Start stack: cd $ROOST_DIR/backend && docker compose -f docker-compose.prod.yml up -d"
log "  7. Set up Cloudflare Tunnel: cloudflared tunnel login && cloudflared tunnel create roost-prod"
