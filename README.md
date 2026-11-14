# Debian NAS (Debian 13 "Trixie" + Go + HTMX)

<div align="center">

| [🇩🇪 Tab 1: Deutsch](#-deutsch) | [🇬🇧 Tab 2: English](#-english) | [📄 Dedicated File: README.en.md](README.en.md) |
| :---: | :---: | :---: |

</div>

---

<div id="-deutsch"></div>

# 🇩🇪 Deutsch

Ein kompromisslos schnelles, leichtgewichtiges NAS-Betriebssystem-Frontend und Daemon auf Basis von **Debian 13 (Trixie)**, nativer **OpenZFS**-Speicherarchitektur, **Samba**, **NFS** und **Rclone** – entwickelt für maximale Performance, intuitive Konfiguration und **echten, funktionierenden HDD-Standby**.

[➡️ Switch to English documentation](#-english)

---

## Highlights (DE)

* **OpenMediaVault 6 / 7 Dark Dashboard Design:**
  * **Authentisches OMV-Designsystem:** Markante blaue Menüleiste (`#008be5`), dunkle Seitenleiste (`#181818`), Breadcrumb-Navigation (`🏠 | Dashboard`) und 3-spaltiges Dashboard-Raster.
  * **Echte Dashboard-Widgets:** CPU RRD-Graph, CPU-Auslastungs-Donut, konzentrische Load-Average-Ringe (1m, 5m, 15m), Memory-Donut (mit ZFS ARC-Erkennung), aktive Dienst-Pills (SSH, SMB, ZFS), Systemzeit, Uptime, Netzwerk-Interface-Übersicht und S.M.A.R.T.-Festplatten-Kacheln.
  * **100% englischsprachiges Web-Interface:** Durchgängig in englischer Sprache gehalten (Dashboard, Tabellen, Konfigurations-Modale und Benachrichtigungen).
* **Nativer OpenZFS-Unterbau (First-Class Core Engine):**
  * **Kein optionales Plugin:** Anders als bei OpenMediaVault, wo ZFS lediglich über Third-Party-Plugins (omv-extras) nachgerüstet wird, ist OpenZFS in **Debian NAS** das fundamentale, native Kernspeichersystem.
  * **Umfassende ZFS-Verwaltung:** Pools, VDEV-Topologien, Datasets, Zvols, Point-in-Time Snapshots, Klone, Scrubbing, TRIM und zerstörungsgeschützte Danger-Modals direkt integriert.
* **Modernes Zero-JavaScript-Build Frontend (Go + HTMX + Alpine.js):**
  * **Kein Virtual-DOM- oder SPA-Ballast:** Saubere, modulare Go-Templates (`html/template`) statt monolithischem JavaScript.
  * **Flackerfreie Live-Telemetrie:** ZFS-Scrub-Fortschritt, Systemmetriken und Standby-Zustände werden live aktualisiert.
  * **Alpine.js:** Leichtgewichtige Client-Zustände für Tabs, Modals, Slide-Over-Drawer und Toasts.
  * **Tailwind CSS v4:** Integriert über die offizielle Standalone CLI (kein Node.js / kein npm nötig).
* **HDD-Standby funktioniert wirklich:**
  * System- und Statusabfragen wecken schlafende Festplatten **niemals** auf (`smartctl -n standby` und non-intrusives `hdparm -C`).
  * Alle ZFS-Datasets werden standardmäßig mit **`atime=off`** angelegt, sodass Lesezugriffe die Festplatten nicht permanent wachhalten.
  * Automatisches Spindown-Management via `hd-idle` und konfigurierbare Timer (Default 30 Minuten).
  * 180s SCSI-Block-Timeout, `allow_restart=1` (für LSI SAS HBAs wie 9500-8i bei `Sense Key: Not Ready`) und `failmode=wait` verhindern ZFS-Pool-Suspends beim 15–20s Anlauf großer Enterprise-HDDs.
* **Notifications, Jobs & System Center (Glocken-Icon Dropdown):**
  * Live-Tracking von ZFS Scrubs (inkl. Fortschrittsbalken), Cloud Sync Tasks, S.M.A.R.T.-Tests, Warnungen und Ereignis-Log.
* **Single-Binary-Architektur (Go):**
  * Der gesamte Daemon inklusive Web-UI (HTML-Templates, CSS, Icons, HTMX, Alpine) wird in **eine einzige statische Binärdatei** kompiliert (`nasd`).
  * Startzeit: < 10 Millisekunden, RAM-Verbrauch: **nur ca. 15–25 MB RSS**.
* **Dateifreigaben & Benutzerverwaltung:**
  * **Samba (SMB):** Inklusive nativer Windows-Netzwerkerkennung (`wsdd2`), macOS Time Machine-Unterstützung und Benutzerverwaltung.
  * **NFS Kernel Server:** Hohe I/O-Performance für Linux-Clients, Proxmox VE und Virtualisierung.
* **Cloud Sync Tasks nach TrueNAS-Vorbild (Bare-Metal Rclone Engine):**
  * 100% Linux-nativ & ohne Docker-Overhead.
  * Hetzner Storage Box (SFTP), S3, B2, WebDAV, Google Drive, OneDrive.
  * Zero-Knowledge Client-Side AES-256 Encryption & Filename Encryption.

---

## Schnellstart auf Debian 13 (Trixie)

### 1. Minimales Debian 13 installieren
Installiere Debian 13 auf einer dedizierten Boot-SSD/NVMe.

### 2. Projekt nach `/opt/debian-nas` klonen & Installer ausführen
```bash
sudo git clone https://github.com/MrCoopa/Debian-NAS.git /opt/debian-nas
cd /opt/debian-nas
sudo ./debian/install.sh
```

### 3. Web-Interface aufrufen
Öffne im Browser:
```text
http://<ip-deines-nas>:8080
```

---

## Aktualisierung auf einem bestehenden Server

```bash
cd /opt/debian-nas
git pull
sudo ./debian/install.sh
```

---

## TrueNAS & OpenZFS Pool-Migration (Feature-Flags & Upgrade-Guide)

Wenn ein ZFS-Pool von TrueNAS SCALE (24.10+) importiert werden soll (`com.truenas:block_cloning_endian`):

### Weg A: Upgrade auf OpenZFS 2.4.4 via Sid-Pinning (Vollständige Schreib- & Leserechte)
```bash
echo "deb http://deb.debian.org/debian sid main contrib non-free-firmware" | sudo tee /etc/apt/sources.list.d/sid.list
sudo tee /etc/apt/preferences.d/sid << 'EOF'
Package: *
Pin: release a=sid
Pin-Priority: 100
EOF
sudo apt update
sudo apt install -y linux-headers-$(uname -r)
sudo apt install -t sid -y zfs-dkms zfsutils-linux
sudo rm -f /etc/apt/sources.list.d/sid.list /etc/apt/preferences.d/sid
sudo apt update
sudo reboot
```
Danach normal importieren: `zpool import DATA`

### Weg B: Schreibgeschützter Import & Daten-Migration
```bash
zpool import -f -o readonly=on DATA
zfs mount -a
rsync -avP /mnt/DATA/ /mnt/MEDIA-12TB/data/
zpool destroy DATA
```

---

## HDD-Standby & Power Management Guide

1. **Non-intrusive Statusabfragen:** Nur `smartctl -n standby -i <disk>` und `hdparm -C <disk>`.
2. **S.M.A.R.T.-Daemon Schutz (`smartd`):** Automatisch konfiguriert mit `-n standby`.
3. **ZFS `atime=off`:** Verhindert Metadaten-Schreibzugriffe bei reinem Lesen.
4. **Spindown-Timer via `hd-idle`:** 30 Minuten Standard-Timer (`1800s`), granular konfigurierbar.
5. **Schutz vor ZFS "SUSPENDED" beim Aufwachen (LSI HBA `allow_restart=1` & 180s Timeout):**
   * 180s SCSI-Block-Timeout & `ATTR{device/allow_restart}="1"` (`/etc/udev/rules.d/99-hdd-timeout.rules`): Zwingt den Linux-SCSI-Treiber bei `Sense Key: Not Ready` (während die Spindel am SAS-Controller anläuft), automatisch einen Motor-Startbefehl zu senden und geduldig zu warten, statt einen I/O-Error an ZFS zu melden.
   * SATA ALPM auf `max_performance`.
   * ZFS Pool `failmode=wait`.
6. **Globale APM- & EPC-Deaktivierung:**
   * Deaktiviert per `@reboot`-Root-Cronjob interne WD/Seagate Firmware-Timer (`STANDBY=0`, `apm,off`).

---

## Cloud Sync & Hetzner Storage Box Guide

* **AES-256 Rclone Crypt:** Client-seitige Zero-Knowledge-Verschlüsselung im RAM vor der Übertragung.
* **Filename Encryption:** `Off (Plain Names)` für lesbare Verzeichnisstrukturen bei 100% verschlüsseltem Inhalt, oder `Standard` für vollständige EME-Verschleierung.
* **Dataset-Mapping:** Automatische 1:1 Spiegelung der ZFS-Pfade nach `/backup/<Pool>/<Dataset>`.
* **Disaster Recovery:** Wiederherstellung direkt über WebUI (PULL Task) oder pure Rclone CLI auf jedem beliebigen Rechner.

---

<br/>
<br/>

---

<div id="-english"></div>

# 🇬🇧 English

An uncompromisingly fast, lightweight NAS operating system frontend and daemon built on **Debian 13 (Trixie)**, native **OpenZFS** storage architecture, **Samba**, **NFS**, and **Rclone** — engineered for peak performance, intuitive configuration, and **real, working HDD spindown/standby**.

[⬅️ Zur deutschen Dokumentation wechseln](#-deutsch) | [📄 Open standalone README.en.md](README.en.md)

---

## Highlights (EN)

* **OpenMediaVault 6 / 7 Dark Dashboard Design:**
  * **Authentic OMV Design System:** Distinctive blue topbar (`#008be5`), dark charcoal sidebar (`#181818`), breadcrumb navigation (`🏠 | Dashboard`), and responsive 3-column dashboard grid.
  * **Real Dashboard Widgets:** CPU RRD graph, CPU utilization donut, concentric load average rings (1m, 5m, 15m), memory donut (with ZFS ARC auto-detection), active service pills (SSH, SMB, ZFS), system time, uptime, network interface overview, and S.M.A.R.T. physical disk tiles.
  * **100% English Web Interface:** Clean and consistent English terminology throughout the entire WebUI (Dashboard, tables, configuration modals, and notifications).
* **Native OpenZFS Foundation (First-Class Core Engine):**
  * **No Optional Plugin:** Unlike OpenMediaVault, where ZFS is only retrofitted through third-party plugins (omv-extras), OpenZFS in **Debian NAS** is the fundamental, native core storage system.
  * **Comprehensive ZFS Management:** Pools, VDEV topologies, datasets, zvols, point-in-time snapshots, clones, scrubbing, TRIM, and destructive action confirmation modals are directly built-in.
* **Modern Zero-JavaScript-Build Frontend (Go + HTMX + Alpine.js):**
  * **No Virtual DOM or SPA Overhead:** Clean, modular Go templates (`html/template`) instead of monolithic JavaScript frameworks.
  * **Flicker-Free Live Telemetry:** ZFS scrub progress, system metrics, and disk standby states update seamlessly without jumping or loss of UI focus.
  * **Alpine.js:** Lightweight client state management for tabs, modals, slide-over drawers, dropdowns, and toast notifications.
  * **Tailwind CSS v4:** Integrated via the official standalone CLI (no Node.js or npm required).
* **Real, Working HDD Standby (Power Saving that actually works):**
  * System and status telemetry queries **never** spin up sleeping drives (using non-intrusive `smartctl -n standby` and `hdparm -C`).
  * All ZFS datasets are created with **`atime=off`** by default so read operations do not permanently keep hard drives awake.
  * Automatic spindown management via `hd-idle` with configurable per-disk timers (5m to 2h, or Always On; default 30m).
  * Global firmware APM & EPC disabling on boot to prevent disks from waking themselves up every 30 minutes.
  * 180-second SCSI device timeout, `allow_restart=1` (`udev` fix for LSI SAS HBAs returning `Sense Key: Not Ready` during spin-up), and `failmode=wait` to safely bridge 15–20s spin-up delays of large helium enterprise drives without ZFS I/O freezes.
* **Notifications, Jobs & System Center (Topbar Bell Dropdown):**
  * **Live Status Indicator:** Real-time badge dot (pulsing blue when jobs are active, red for critical alerts, amber for warnings, green for all healthy).
  * **Running Jobs:** Live tracking of ZFS Scrubs (with percentage progress bars), Rclone Cloud Sync tasks, and S.M.A.R.T. self-tests.
  * **Completed Jobs:** History of finished scrubs, completed cloud backups, and periodic snapshot tasks with timestamps.
  * **System Alerts & Errors:** Instant detection of degraded pools, disk I/O errors, high disk temperatures, and high pool capacity.
  * **Event Log:** Chronological activity stream with a 1-click "Clear Log" action.
* **Single-Binary Architecture (Go):**
  * The entire daemon including the WebUI (HTML templates, CSS, icons, HTMX, Alpine) compiles into **a single static binary** (`nasd`).
  * Startup time: < 10 milliseconds.
  * Idle RAM usage: **only ~15–25 MB RSS**.
* **File Shares & User Management:**
  * **Samba (SMB):** Native Windows network discovery (`wsdd2`), macOS Time Machine support, and granular user/guest permissions.
  * **NFS Kernel Server:** Low-latency exports for Linux clients, Proxmox VE, and virtualization clusters.
* **TrueNAS-Style Cloud Sync Tasks (Bare-Metal Rclone Engine):**
  * **100% Linux Native & Container-Free:** Maximum throughput and minimal latency through direct OS process execution on Debian 13.
  * **Multi-Cloud & SFTP Support:** Ready-to-use profiles for **Hetzner Storage Box (SFTP)**, Nextcloud/ownCloud (WebDAV), Backblaze B2, Amazon S3 / S3-compatible object storage (MinIO, Wasabi, Cloudflare R2), Google Drive, and Microsoft OneDrive.
  * **Standard TrueNAS Transfer Modes:** `COPY`, `SYNC`, and `MOVE`.
  * **Zero-Knowledge Client-Side Encryption:** AES-256 Rclone Crypt in memory with independent passphrases and master salts.
  * **Granular Filename Encryption:** `Off (Plain Names)` for readable folders with 100% encrypted contents, or `Standard` for full AES-256-EME obfuscation.
* **Hardware-Agnostic Storage Management:**
  * Disks identified via persistent paths (`/dev/disk/by-id/`).
  * 45Drives Houston chassis & drive-bay identification, S.M.A.R.T. testing, and locate LED support.

---

## Quickstart on Debian 13 (Trixie)

### 1. Install Minimal Debian 13
Install Debian 13 (e.g. via minimal Netinst ISO) on a dedicated boot SSD/NVMe.

### 2. Clone Repository to `/opt/debian-nas` & Run Installer
```bash
sudo git clone https://github.com/MrCoopa/Debian-NAS.git /opt/debian-nas
cd /opt/debian-nas
sudo ./debian/install.sh
```

### 3. Open Web Interface
Open your web browser and navigate to:
```text
http://<nas-ip-address>:8080
```

---

## Updating an Existing Server

```bash
cd /opt/debian-nas
git pull
sudo ./debian/install.sh
```

---

## TrueNAS & OpenZFS Pool Migration (Feature Flags & Upgrade Guide)

When importing a ZFS pool from an external system with newer feature flags (such as `com.truenas:block_cloning_endian` from TrueNAS SCALE 24.10+):

### Option A: Upgrade to OpenZFS 2.4.4 via Sid Pinning (Full Read & Write Access)
```bash
echo "deb http://deb.debian.org/debian sid main contrib non-free-firmware" | sudo tee /etc/apt/sources.list.d/sid.list
sudo tee /etc/apt/preferences.d/sid << 'EOF'
Package: *
Pin: release a=sid
Pin-Priority: 100
EOF
sudo apt update
sudo apt install -y linux-headers-$(uname -r)
sudo apt install -t sid -y zfs-dkms zfsutils-linux
sudo rm -f /etc/apt/sources.list.d/sid.list /etc/apt/preferences.d/sid
sudo apt update
sudo reboot
```
Then import normally: `zpool import DATA`

### Option B: Read-Only Import & Data Migration (Standard Debian)
```bash
zpool import -f -o readonly=on DATA
zfs mount -a
rsync -avP /mnt/DATA/ /mnt/MEDIA-12TB/data/
zpool destroy DATA
```

---

## HDD Standby & Power Management Guide

1. **Non-Intrusive Status Telemetry:** Uses `smartctl -n standby -i <disk>` and `hdparm -C <disk>`. Never wakes sleeping drive spindles.
2. **S.M.A.R.T. Daemon Protection (`smartd`):** Configured with `-n standby` to prevent periodic spin-ups.
3. **ZFS `atime=off`:** Default on all datasets to avoid metadata writes on read operations.
4. **Spindown Timers via `hd-idle`:** Configured to 30 minutes (`1800s`) by default, with per-disk configuration via WebUI.
5. **Protection Against ZFS "SUSPENDED" on Wake-Up (LSI HBA `allow_restart=1` & 180s Timeout):**
   * 180s SCSI device timeout & `ATTR{device/allow_restart}="1"` (`/etc/udev/rules.d/99-hdd-timeout.rules`) instructs the Linux SCSI disk driver to intercept `Sense Key: Not Ready` from SAS HBAs (like LSI 9500-8i), send a motor start command, and wait for full RPM without reporting premature I/O errors to ZFS.
   * SATA ALPM set to `max_performance` prevents link dropouts.
   * Pool `failmode=wait` prevents kernel dropouts during spin-up delays.
6. **Global APM & EPC Disabling:**
   * Automatically disables conflicting internal firmware timers (`STANDBY=0`, `apm,off`) via `@reboot` root cronjob.

---

## Cloud Sync & Hetzner Storage Box Guide

* **TrueNAS-Style Architecture:** Direct Rclone engine integration without Docker overhead.
* **Zero-Knowledge Encryption:** AES-256 Rclone Crypt encrypted in memory before network transmission.
* **Filename Encryption:** Choose between `Off (Plain Names)` for easy directory browsing with encrypted file contents, or `Standard` for full obscurity.
* **Automated Hierarchy:** Auto-maps source datasets to `/backup/<Pool>/<Dataset>`.
* **Disaster Recovery:** Restore via WebUI PULL task or standalone Rclone CLI on any machine.

---

## Running Backend Unit Tests

```bash
cd backend
go test -v ./...
```
