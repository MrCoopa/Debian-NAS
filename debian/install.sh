#!/usr/bin/env bash
set -euo pipefail

# ==============================================================================
# Debian NAS 1-Click Installer (Target: Debian 13 Trixie)
# Installs OpenZFS, Samba, NFS Kernel Server, Rclone, hd-idle and nasd Daemon
# ==============================================================================

if [ "$EUID" -ne 0 ]; then
  echo "[-] Bitte als root ausführen (sudo ./install.sh)"
  exit 1
fi

echo "[+] 1/6: Debian Repositories aktualisieren (contrib & non-free-firmware)..."
apt-get update
apt-get install -y curl wget git software-properties-common lsb-release

# Ensure contrib & non-free-firmware are active
sed -i 's/main$/main contrib non-free non-free-firmware/' /etc/apt/sources.list || true

echo "[+] 2/6: Kern-Pakete installieren (OpenZFS, Samba, NFS, hd-idle, smartmontools, ledmon, fio, rclone)..."
apt-get update
DEBIAN_FRONTEND=noninteractive apt-get install -y \
  linux-headers-$(uname -r) \
  zfs-dkms \
  zfsutils-linux \
  samba \
  smbclient \
  nfs-kernel-server \
  hd-idle \
  hdparm \
  smartmontools \
  ledmon \
  fio \
  rclone \
  golang-go

echo "[+] 3/6: System-Optimierungen für HDD-Standby & ZFS..."
# 1. Samba conf.d Ordner erstellen
mkdir -p /etc/samba/conf.d
if ! grep -q "include = /etc/samba/conf.d/\*.conf" /etc/samba/smb.conf; then
  cat << 'EOF' >> /etc/samba/smb.conf

# Include dynamic NAS shares
include = /etc/samba/conf.d/*.conf
EOF
fi

# 2. NFS Exports Ordner
mkdir -p /etc/exports.d

# 3. Config-Verzeichnis für nasd
mkdir -p /etc/nas

# 4. hd-idle Spindown nach 15 Minuten (900 Sekunden) standardmäßig aktivieren
cat << 'EOF' > /etc/default/hd-idle
# hd-idle default configuration
START_HD_IDLE=true
HD_IDLE_OPTS="-i 900 -l /var/log/hd-idle.log"
EOF
systemctl enable --now hd-idle || true

echo "[+] 4/6: Debian NAS Daemon kompilieren..."
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"

cd "$PROJECT_ROOT/backend"
# Build static binary
CGO_ENABLED=0 go build -ldflags="-s -w" -o /usr/local/bin/nasd cmd/nasd/main.go
chmod +x /usr/local/bin/nasd

echo "[+] 5/6: Systemd Service einrichten & aktivieren..."
cp "$PROJECT_ROOT/debian/nasd.service" /etc/systemd/system/nasd.service
systemctl daemon-reload
systemctl enable --now nasd.service

echo "[+] 6/6: ZFS Kernel-Modul prüfen..."
modprobe zfs || true

IP_ADDR=$(hostname -I | awk '{print $1}')
echo ""
echo "=========================================================================="
echo "  [SUCCESS] Debian NAS erfolgreich installiert und gestartet!"
echo "  Web-Oberfläche erreichbar unter: http://${IP_ADDR}:8080"
echo "=========================================================================="
