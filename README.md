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
  * **Samba (SMB):** Inklusive nativer Windows-Netzwerkerkennung (`wsdd`), macOS Time Machine-Unterstützung und Multi-User-Zuordnung (Gast-Zugriff vs. eingeschränkte Benutzer).
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
2. Installiert `zfsutils-linux`, `samba`, `wsdd`, `nfs-kernel-server`, `hd-idle`, `smartmontools`, `fio` und `rclone`.
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
cd /opt/debian-nas && git pull && cd backend && go build -ldflags="-s -w" -o /usr/local/bin/nasd cmd/nasd/main.go && systemctl restart nasd.service
```

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
