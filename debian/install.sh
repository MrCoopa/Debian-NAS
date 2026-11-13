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
apt-get install -y curl wget git

# Ensure contrib & non-free-firmware are active (Standard debian.sources or sources.list)
if [ -f /etc/apt/sources.list.d/debian.sources ]; then
  sed -i 's/^Components: .*/Components: main contrib non-free non-free-firmware/' /etc/apt/sources.list.d/debian.sources || true
fi
if [ -f /etc/apt/sources.list ]; then
  sed -i 's/main$/main contrib non-free non-free-firmware/' /etc/apt/sources.list || true
  sed -i 's/main contrib$/main contrib non-free non-free-firmware/' /etc/apt/sources.list || true
fi

echo "[+] 2/6: Kern-Pakete installieren (OpenZFS, Samba, NFS, hd-idle, smartmontools, ledmon, fio, rclone)..."
apt-get update

# Check if ZFS is already installed (e.g. Proxmox VE native ZFS)
ZFS_PKG="zfsutils-linux"
if ! command -v zpool &>/dev/null; then
  ZFS_PKG="zfs-dkms zfsutils-linux"
fi

DEBIAN_FRONTEND=noninteractive apt-get install -y \
  $ZFS_PKG \
  samba \
  smbclient \
  nfs-kernel-server \
  rpcbind \
  psmisc \
  hdparm \
  smartmontools \
  gdisk \
  parted \
  util-linux \
  fio \
  rclone \
  golang-go

# Rclone bare-metal Installation sicherstellen (offizielles Skript für neueste Features)
if ! command -v rclone &>/dev/null; then
  echo "[+] Installiere Rclone bare-metal..."
  curl -fsSL https://rclone.org/install.sh | bash || true
fi
mkdir -p /root/.config/rclone /etc/nas
chmod 0700 /root/.config/rclone /etc/nas

# Optional hardware & Windows 10/11 Network Discovery (wsdd2 for Trixie, wsdd for older Debian) tools
DEBIAN_FRONTEND=noninteractive apt-get install -y wsdd2 || DEBIAN_FRONTEND=noninteractive apt-get install -y wsdd || DEBIAN_FRONTEND=noninteractive apt-get install -y wsdd-server || true
DEBIAN_FRONTEND=noninteractive apt-get install -y hd-idle ledmon || true

echo "[+] 3/6: System-Optimierungen für HDD-Standby & ZFS..."
# 0. Gruppe nasusers für Samba/NAS-Speicherzugriffe anlegen
groupadd -f nasusers || true

# 1. Samba conf.d Ordner & Master-Include (/etc/samba/nas-shares.conf) erstellen
# Hinweis: Samba unterstützt KEINE Wildcards (*.conf) bei "include =", daher wird /etc/samba/nas-shares.conf
# direkt in der [global]-Sektion eingebunden (nicht am Dateiende, wo es von [printers] geschluckt wird).
mkdir -p /etc/samba/conf.d
touch /etc/samba/nas-shares.conf
chmod 0644 /etc/samba/nas-shares.conf

if [ -f /etc/samba/smb.conf ]; then
  # Alte fehlerhafte Includes entfernen
  sed -i '/include = \/etc\/samba\/conf\.d\/\*\.conf/d' /etc/samba/smb.conf || true
  sed -i '/include = \/etc\/samba\/nas-shares\.conf/d' /etc/samba/smb.conf || true

  # Direkt nach [global]-Startwerte setzen; include wird am Ende durch nasd sauber hinter [global] platziert
  if grep -q "\[global\]" /etc/samba/smb.conf; then
    if ! grep -q "nas-shares.conf" /etc/samba/smb.conf; then
      printf '\n# Dynamic Debian NAS shares include\ninclude = /etc/samba/nas-shares.conf\n' >> /etc/samba/smb.conf
    fi
  else
    cat << 'EOF' > /etc/samba/smb.conf
[global]
   workgroup = WORKGROUP
   server string = %h (Debian NAS)
   server role = standalone server
   map to guest = Bad User
   usershare allow guests = yes
   browseable = yes

# Dynamic Debian NAS shares include
include = /etc/samba/nas-shares.conf
EOF
  fi
fi

systemctl restart smbd nmbd || true
systemctl enable --now wsdd2 2>/dev/null || systemctl enable --now wsdd 2>/dev/null || systemctl enable --now wsdd-server 2>/dev/null || true
systemctl restart wsdd2 2>/dev/null || systemctl restart wsdd 2>/dev/null || systemctl restart wsdd-server 2>/dev/null || true

# 2. NFS Exports Ordner & Dienste aktivieren
mkdir -p /etc/exports.d
systemctl enable --now rpcbind 2>/dev/null || true
systemctl enable --now nfs-kernel-server 2>/dev/null || true

# 3. Config-Verzeichnis für nasd (restriktive Rechte 0700)
mkdir -p /etc/nas
chmod 0700 /etc/nas

# 4. hd-idle Spindown nach 15 Minuten (900 Sekunden) standardmäßig aktivieren
cat << 'EOF' > /etc/default/hd-idle
# hd-idle default configuration
START_HD_IDLE=true
HD_IDLE_OPTS="-i 900 -l /var/log/hd-idle.log"
EOF
systemctl enable --now hd-idle || true

# 5. smartmontools / smartd mit -n standby (Festplatten nicht aus Standby wecken) aktivieren
if [ -f /etc/smartd.conf ]; then
  if grep -q "^DEVICESCAN" /etc/smartd.conf && ! grep -q -- "-n standby" /etc/smartd.conf; then
    sed -i 's/^DEVICESCAN\(.*\)/DEVICESCAN\1 -n standby/' /etc/smartd.conf || true
  fi
fi
systemctl enable --now smartd 2>/dev/null || systemctl enable --now smartmontools 2>/dev/null || true

# 6. ZFS Spindown-Sicherheit: SCSI/SATA Device Timeout auf 120s setzen (verhindert 'SUSPENDED' bei trägem Spin-up)
cat << 'EOF' > /etc/udev/rules.d/99-zfs-spindown-timeout.rules
# Increase SCSI disk timeout to 120s for rotational drives to prevent ZFS pool suspension during spin-up
ACTION=="add|change", SUBSYSTEM=="block", KERNEL=="sd[a-z]*", ATTR{queue/rotational}=="1", ATTR{device/timeout}="120"
EOF
udevadm control --reload-rules 2>/dev/null || true
udevadm trigger 2>/dev/null || true
for d in /sys/block/sd*/device/timeout; do [ -f "$d" ] && echo 120 > "$d" 2>/dev/null || true; done

# 7. SATA Link Power Management (ALPM) auf max_performance für fehlerfreien Standby-Wakeup
for i in /sys/class/scsi_host/host*/link_power_management_policy; do
  [ -f "$i" ] && echo max_performance > "$i" 2>/dev/null || true
done

echo "[+] 4/6: Debian NAS Daemon unter /opt/debian-nas einrichten & kompilieren..."
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"
INSTALL_DIR="/opt/debian-nas"

mkdir -p "$INSTALL_DIR/bin"

# Falls das Skript außerhalb von /opt/debian-nas aufgerufen wurde, Projekt nach /opt/debian-nas synchronisieren
if [ "$PROJECT_ROOT" != "$INSTALL_DIR" ]; then
  cp -r "$PROJECT_ROOT/"* "$INSTALL_DIR/"
fi

cd "$INSTALL_DIR/backend"
# Statische Linux-Binary direkt nach /opt/debian-nas/bin/nasd bauen (und Symlink nach /usr/local/bin/nasd)
CGO_ENABLED=0 go build -ldflags="-s -w" -o "$INSTALL_DIR/bin/nasd" ./cmd/nasd
chmod +x "$INSTALL_DIR/bin/nasd"
ln -sf "$INSTALL_DIR/bin/nasd" /usr/local/bin/nasd

echo "[+] 5/6: Systemd Service einrichten & aktivieren..."
systemctl stop nasd.service 2>/dev/null || true
cp "$INSTALL_DIR/debian/nasd.service" /etc/systemd/system/nasd.service
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
