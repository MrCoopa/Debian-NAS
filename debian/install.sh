
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
  hdparm \
  smartmontools \
  gdisk \
  parted \
  util-linux \
  fio \
  rclone \
  golang-go

# Optional hardware & Windows 10/11 Network Discovery (wsdd) tools
DEBIAN_FRONTEND=noninteractive apt-get install -y hd-idle ledmon wsdd || DEBIAN_FRONTEND=noninteractive apt-get install -y hd-idle ledmon wsdd-server || true

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

  # Direkt in die [global]-Sektion einfügen
  if grep -q "\[global\]" /etc/samba/smb.conf; then
    sed -i '/\[global\]/a \   include = /etc/samba/nas-shares.conf\n   map to guest = Bad User\n   usershare allow guests = yes\n   browseable = yes' /etc/samba/smb.conf || true
  else
    cat << 'EOF' > /etc/samba/smb.conf
[global]
   workgroup = WORKGROUP
   server string = %h (Debian NAS)
   server role = standalone server
   include = /etc/samba/nas-shares.conf
   map to guest = Bad User
   usershare allow guests = yes
   browseable = yes
EOF
  fi
fi

systemctl restart smbd nmbd || true
systemctl restart wsdd 2>/dev/null || systemctl restart wsdd-server 2>/dev/null || true

# 2. NFS Exports Ordner
mkdir -p /etc/exports.d

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
