# Debian NAS (Debian 13 "Trixie" + Go + HTMX)

Ein kompromisslos schnelles, leichtgewichtiges NAS-Betriebssystem-Frontend und Daemon auf Basis von **Debian 13 (Trixie)**, nativer **OpenZFS**-Speicherarchitektur, **Samba**, **NFS** und **Rclone** – entwickelt für maximale Performance, intuitive Konfiguration und **echten, funktionierenden HDD-Standby**.

---

## Highlights

* **OpenMediaVault 6 / 7 Dark Dashboard Design:**
  * **Authentisches OMV-Designsystem:** Markante blaue Menüleiste (`#008be5`), dunkle Seitenleiste (`#181818`), Breadcrumb-Navigation (`🏠 | Dashboard`) und 3-spaltiges Dashboard-Raster.
  * **Echte Dashboard-Widgets:** CPU RRD-Graph, CPU-Auslastungs-Donut, konzentrische Load-Average-Ringe (1m, 5m, 15m), Memory-Donut (mit ZFS ARC-Erkennung), aktive Dienst-Pills (SSH, SMB, ZFS), Systemzeit, Uptime, Netzwerk-Interface-Übersicht und S.M.A.R.T.-Festplatten-Kacheln.
  * **100% englischsprachiges Web-Interface:** Durchgängig in englischer Sprache gehalten (Dashboard, Tabellen, Konfigurations-Modale und Benachrichtigungen).
* **Nativer OpenZFS-Unterbau (First-Class Core Engine):**
  * **Kein optionales Plugin:** Anders als bei OpenMediaVault, wo ZFS lediglich über Third-Party-Plugins (omv-extras) nachgerüstet wird, ist OpenZFS in **Debian NAS** das fundamentale, native Kernspeichersystem.
  * **Umfassende ZFS-Verwaltung:** Pools, VDEV-Topologien, Datasets, Zvols, Point-in-Time Snapshots, Klone, Scrubbing, TRIM und zerstörungsgeschützte Danger-Modals direkt integriert.
* **Modernes Zero-JavaScript-Build Frontend (Go + HTMX + Alpine.js + Idiomorph):**
  * **Kein Virtual-DOM- oder SPA-Ballast:** Saubere, modulare Go-Templates (`html/template`) statt monolithischem JavaScript.
  * **Flackerfreie Live-Telemetrie:** ZFS-Scrub-Fortschritt, Systemmetriken und Standby-Zustände werden via **Idiomorph DOM-Morphing** ohne Springen oder Fokusverlust aktualisiert.
  * **Alpine.js:** Leichtgewichtige Client-Zustände für Tabs, Modals, Slide-Over-Drawer und Toasts.
  * **Tailwind CSS v4:** Integriert über die offizielle Standalone CLI (kein Node.js / kein npm nötig).
* **HDD-Standby funktioniert wirklich:**
  * System- und Statusabfragen wecken schlafende Festplatten **niemals** auf (`smartctl -n standby` und non-intrusives `hdparm -C`).
  * Alle ZFS-Datasets werden standardmäßig mit **`atime=off`** angelegt, sodass Lesezugriffe die Festplatten nicht permanent wachhalten.
  * Automatisches Spindown-Management via `hd-idle` und konfigurierbare Timer.
* **Single-Binary-Architektur (Go):**
  * Der gesamte Daemon inklusive Web-UI (HTML-Templates, CSS, Icons, HTMX, Alpine) wird in **eine einzige statische Binärdatei** kompiliert (`nasd`).
  * Startzeit: < 10 Millisekunden.
  * RAM-Verbrauch im Leerlauf: **nur ca. 15–20 MB RSS**.
* **Volle OpenZFS-Enterprise-Features:**
  * **ZFS-Pools:** Status, Scrub-Steuerung, Kapazitätsanzeige, VDEV-Topologie, TRIM, Pool-Import/Export und zerstörungsgeschützte Danger-Modals.
  * **Datasets & Zvols:** Dateisysteme und Block-Volumes mit `lz4`/`zstd`-Kompression, Recordsize, Quota und Schreibschutz.
  * **Snapshots & Clones:** Unveränderliche Point-in-Time Schnappschüsse, 1-Klick-Rollback und beschreibbare Klone.
* **Dateifreigaben & Benutzerverwaltung:**
  * **Samba (SMB):** Inklusive nativer Windows-Netzwerkerkennung (`wsdd2` / `wsdd`), macOS Time Machine-Unterstützung und Multi-User-Zuordnung (Gast-Zugriff vs. eingeschränkte Benutzer).
  * **NFS Kernel Server:** Hohe I/O-Performance für Linux-Clients, Proxmox VE und Virtualisierung.
* **Cloud Sync (Rclone Engine):**
  * Zeitgesteuerte oder manuelle Synchronisation zu Backblaze B2, Google Drive, OneDrive, S3, WebDAV und Nextcloud.
* **Hardware-Agnostisch:**
  * Identifiziert Disks über persistente Pfade (`/dev/disk/by-id/`).
  * 45Drives Houston Chassis & Drive-Bay Erkennung, SMART-Selbsttests und Locate-LED Steuerung.

---

## Projekt-Struktur

```text
Debian-Nas/
├── backend/
│   ├── cmd/nasd/main.go            # Daemon-Einstiegspunkt
│   ├── internal/
│   │   ├── api/                    # REST API & UI HTMX Handlers
│   │   │   ├── api.go
│   │   │   └── ui_handlers.go
│   │   ├── web/                    # Go Template Engine & Web-Assets
│   │   │   ├── web.go              # //go:embed templates/* & static/*
│   │   │   ├── templates/          # Modulare HTML-Templates
│   │   │   │   ├── layout.html     # Cockpit-Hauptlayout & Alpine State
│   │   │   │   ├── partials/       # Topbar, Metriken, Pools, Disks, Shares, etc.
│   │   │   │   └── modals/         # 13 Modaldialoge (Shares, Pools, Disks, Snaps)
│   │   │   └── static/             # Embedded Assets (HTMX, Alpine, CSS)
│   │   │       ├── htmx.min.js
│   │   │       ├── alpine.min.js
│   │   │       ├── idiomorph-ext.min.js
│   │   │       ├── tailwind.css    # Kompiliertes Tailwind CSS
│   │   │       ├── style.css       # PatternFly Enterprise Design System
│   │   │       └── input.css       # Tailwind Quell-CSS
│   │   ├── zfs/                    # OpenZFS Pools, Datasets, Scrubs & atime=off
│   │   ├── disks/                  # HDD-Erkennung, Standby-Check, SMART, Spindown
│   │   ├── shares/                 # Samba (smb.conf), NFS (/etc/exports) & User-Management
│   │   ├── cloudsync/              # Rclone-Integration (B2, Drive, S3, etc.)
│   │   ├── config/                 # Konfigurationsmanager
│   │   └── system/                 # Linux-Kernel Telemetrie & Netzwerk
│   ├── static/                     # Synchroner Asset-Spiegel
│   ├── go.mod
│   └── go.sum
├── debian/
│   ├── install.sh                  # 1-Klick Installer für Debian 13 (/opt/debian-nas)
│   └── nasd.service                # Systemd Service Unit
├── scripts/
│   ├── build-css.sh                # Tailwind Standalone CLI Builder (Linux)
│   └── build-css.ps1               # Tailwind Standalone CLI Builder (Windows)
├── Makefile                        # Build-, CSS- und Deployment-Skripte
└── README.md
```

---

## Schnellstart auf Debian 13 (Trixie)

### 1. Minimales Debian 13 installieren
Installiere Debian 13 (z. B. via Minimal-Netinst-ISO) auf einer dedizierten Boot-SSD/NVMe.

### 2. Projekt nach `/opt/debian-nas` klonen & Installer ausführen
```bash
sudo git clone https://github.com/MrCoopa/Debian-NAS.git /opt/debian-nas
cd /opt/debian-nas
sudo ./debian/install.sh
```

Das Skript:
1. Aktiviert `contrib` und `non-free-firmware` in den Paketquellen.
2. Installiert `zfsutils-linux`, `samba`, `wsdd2` (bzw. `wsdd`), `nfs-kernel-server`, `hd-idle`, `smartmontools`, `fio` und `rclone`.
3. Kompiliert die statische Go-Binärdatei nach `/opt/debian-nas/bin/nasd` (Symlink `/usr/local/bin/nasd`).
4. Konfiguriert Samba mit `/etc/samba/nas-shares.conf` und Windows-Discovery.
5. Richtet den Systemd-Service `nasd.service` ein und startet ihn sofort.

### 3. Web-Interface aufrufen
Öffne im Browser:
```text
http://<ip-deines-nas>:8080
```

---

## Aktualisierung auf einem bestehenden Server

Wenn du Änderungen aus dem Git-Repository übernehmen möchtest, reicht ein Einzeiler auf dem NAS:

```bash
cd /opt/debian-nas
git fetch origin main
git reset --hard origin/main
systemctl stop nasd.service
rm -f bin/nasd
cd backend && CGO_ENABLED=0 go build -v -ldflags="-s -w" -o ../bin/nasd ./cmd/nasd
cd /opt/debian-nas
systemctl start nasd.service
```

---

## TrueNAS & OpenZFS Pool-Migration (Feature-Flags & Upgrade-Guide)

Wenn ein ZFS-Pool von einem externen System (wie **TrueNAS SCALE ab Version 24.10 "Electric Eel"**) importiert werden soll, kann folgende Fehlermeldung auftreten:

```text
cannot import 'DATA': unsupported version or feature
This pool uses the following feature(s) not supported by this system:
com.truenas:block_cloning_endian (Fixes BRT ZAP endianness on new pools.)
All unsupported features are only required for writing to the pool.
The pool can be imported using '-o readonly=on'.
```

### Hintergrund
TrueNAS SCALE 24.10 nutzt OpenZFS 2.4+, welches das Feature `com.truenas:block_cloning_endian` aktiviert hat. Debian 13 (Trixie) liefert standardmäßig OpenZFS 2.3.9 aus. Da ZFS-Feature-Flags eine Einbahnstraße sind, verweigert OpenZFS 2.3 den beschreibbaren Import, um Datenkorruption zu verhindern.

### Weg A: Upgrade auf OpenZFS 2.4.4 (Vollständige Schreib- & Leserechte)
In Debian **Sid (Unstable)** ist OpenZFS **2.4.4** bereits fertig paketiert. Über **APT-Pinning** wird sichergestellt, dass **ausschließlich OpenZFS** aus Sid bezogen wird, während das Restsystem zu 100% stabil auf Debian 13 (Trixie) verbleibt:

```bash
# 1. Sid-Repository eintragen
echo "deb http://deb.debian.org/debian sid main contrib non-free-firmware" | sudo tee /etc/apt/sources.list.d/sid.list

# 2. APT-Pinning einrichten (Priorität 100 = Sid-Pakete werden nie automatisch installiert)
sudo tee /etc/apt/preferences.d/sid << 'EOF'
Package: *
Pin: release a=sid
Pin-Priority: 100
EOF

# 3. Paketlisten aktualisieren
sudo apt update

# 4. Kernel-Headers deines aktuellen Trixie-Kernels sicherstellen
sudo apt install -y linux-headers-$(uname -r)

# 5. OpenZFS 2.4 gezielt aus Sid installieren (baut das Kernelmodul für deinen 6.12 Kernel)
sudo apt install -t sid -y zfs-dkms zfsutils-linux

# 6. Sid-Quelle wieder entfernen (System bleibt 100% Trixie)
sudo rm -f /etc/apt/sources.list.d/sid.list /etc/apt/preferences.d/sid
sudo apt update

# 7. NAS neu starten, um das neu kompilierte Kernel-Modul zu laden
sudo reboot
```

Nach dem Neustart unterstützt dein System alle TrueNAS 24.10+ Features und der Pool kann ganz normal mit Lese- und Schreibrechten importiert werden:
```bash
zpool import DATA
```

### Weg B: Schreibgeschützter Import & Daten-Migration (Empfehlung für Standard-Debian)
Falls du dein System nicht mit Paketen aus Sid mischen möchtest:
1. **Schreibgeschützt importieren:**
   - In der WebUI: Beim Import-Dialog die Option **"Read-Only Import (-o readonly=on)"** anhaken.
   - Per CLI:
     ```bash
     zpool import -f -o readonly=on DATA
     zfs mount -a
     ```
2. **Daten auf den Hauptpool sichern:**
   ```bash
   rsync -avP /mnt/DATA/ /mnt/MEDIA-12TB/data/
   ```
3. **Pool zerstören und auf Debian neu anlegen:**
   ```bash
   zpool destroy DATA
   ```
   Danach in der WebUI unter **Storage Pools -> Create Pool** den Pool neu anlegen. Er ist dann 100% nativ mit Debian 13 kompatibel.

---

## HDD-Standby & Power Management Guide

Debian NAS wurde von Grund auf dafür entwickelt, **echten, funktionierenden HDD-Standby** zu gewährleisten:

1. **Non-intrusive Statusabfragen:**
   Das System nutzt für Dashboard- und Disk-Status-Abfragen ausschließlich `smartctl -n standby -i <disk>` und `hdparm -C <disk>`. Diese Befehle lesen den Status aus dem Controller-Cache, **ohne** schlafende Spindeln aufzuwecken.
2. **S.M.A.R.T.-Daemon Schutz (`smartd`):**
   Der S.M.A.R.T.-Hintergrundüberwachungsdienst (`smartd`) wird in `/etc/smartd.conf` automatisch mit `-n standby` konfiguriert:
   ```text
   DEVICESCAN -d removable -n standby -m root -M exec /usr/share/smartmontools/smartd-runner
   ```
   Dadurch werden Festplatten nicht alle 30 Minuten für Telemetrie-Checks geweckt.
3. **ZFS `atime=off`:**
   Alle über die WebUI erstellten Datasets setzen standardmäßig `atime=off`. Dadurch führen reine Lesezugriffe nicht zu Schreiboperationen für Zugriffszeitstempel.
4. **Spindown-Timer via `hd-idle`:**
   In `/etc/default/hd-idle` wird der Spindown-Timer (z. B. 900 Sekunden = 15 Minuten) hinterlegt. Die WebUI erlaubt zusätzlich die granulare Steuerung pro Festplatte (Always On, 5 Min, 10 Min, 15 Min, 30 Min, 1 Std, 2 Std).

---

## Netzwerkdienste & Windows Discovery

* **Windows 10/11 Explorer Network Discovery (`wsdd2`):**
  Unter Debian 13 (Trixie) wurde das alte Python-Paket `wsdd` durch das native, ressourcensparende C-Paket `wsdd2` ersetzt. Es wird vom Installer automatisch eingerichtet und gestartet, sodass das NAS im Windows-Explorer unter „Netzwerk“ sofort erscheint.
* **Samba (SMB) Freigaben:**
  Konfiguriert über `/etc/samba/nas-shares.conf`. Unterstützt Gast-Zugriff, Benutzerbeschränkungen (Gruppe `nasusers`) und macOS Time Machine.
* **NFS Kernel Server:**
  Exports unter `/etc/exports.d/` für latenzarme Linux- und Proxmox-Verbindungen.

---

## ZFS Snapshot Management & Paginierung

* **Pool-Gruppierung:** Snapshots werden automatisch nach Speicherpool gruppiert und in separaten Tabellen dargestellt.
* **Server-Side Pagination:** Um bei Dutzenden oder Hunderten automatischen Snapshots den Browser nicht zu überlasten, liefert das Backend exakt **10 Snapshots pro Seite** aus.
* **Live-Filter & Aktionen:** Snapshots lassen sich pro Pool in Echtzeit durchsuchen, auf Knopfdruck zurückrollen (`Rollback`), klonen (`Clone`) oder löschen (`Destroy`).

---

## Tailwind CSS & Styling (Zero Node.js)

Das System nutzt Tailwind CSS v4 über die offizielle **Standalone CLI** von Tailwind Labs. Du benötigst weder Node.js noch npm:

### Auf Linux / Debian:
```bash
make css         # Kompiliert und minifiziert tailwind.css
make watch-css   # Live-Watcher: Kompiliert bei Template-Änderungen automatisch neu
```

### Auf Windows (PowerShell):
```powershell
.\scripts\build-css.ps1         # Einmaliger Build
.\scripts\build-css.ps1 -Watch  # Live-Watcher
```

Die fertige Datei `tailwind.css` ist bereits im Repository eingecheckt und wird beim `go build` direkt in die Binary eingebettet.

---

## Lokales Testen (Mock-Modus)

Du kannst den Daemon auch auf jedem Entwicklungsrechner (auch unter Windows/macOS ohne ZFS) im **Mock-Modus** starten:

```bash
cd backend
go run cmd/nasd/main.go -mock -port 8080
```
Rufe anschließend `http://localhost:8080` auf. Die GUI simuliert Festplatten, ZFS-Pools, I/O-Raten und SMART-Daten komplett im Speicher.

---

## Tests ausführen

Das gesamte Backend verfügt über eine vollständige Test-Suite für ZFS, Disks, Shares, User-Rechte, Templates und UI-Endpunkte:

```bash
cd backend
go test -v ./...
```
