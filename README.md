# Debian NAS (Debian 13 "Trixie" + Go)

Ein kompromisslos schnelles, leichtgewichtiges NAS-Betriebssystem-Frontend und Daemon auf Basis von **Debian 13 (Trixie)**, **OpenZFS**, **Samba**, **NFS** und **Rclone** – entwickelt für maximale Performance, intuitive Konfiguration und **funktionierenden HDD-Standby**.

---

## Highlights

* **Kein Virtualisierungs-Ballast:** Keine KVM-, Docker- oder Kubernetes-Overheads wie bei TrueNAS.
* **HDD-Standby funktioniert wirklich:**
  * System- und Statusabfragen wecken schlafende Festplatten **niemals** auf (`smartctl -n standby` und `hdparm -C`).
  * Alle ZFS-Datasets werden standardmäßig mit **`atime=off`** angelegt, sodass Lesezugriffe die Festplatten nicht permanent wachhalten.
  * Automatisches Spindown-Management via `hd-idle` und `hdparm`.
* **Single-Binary-Architektur (Go):**
  * Der gesamte Daemon inklusive Web-UI (HTML5/CSS/JS) wird in **eine einzige Binärdatei** kompiliert (`nasd`).
  * Startzeit: < 10 Millisekunden.
  * RAM-Verbrauch im Leerlauf: **nur ca. 15 MB**.
* **Volle TrueNAS-Storage-Features:**
  * **ZFS-Pools:** Status, Scrub-Steuerung, Kapazitätsanzeige, Topologie.
  * **Datasets & Snapshots:** Dateisysteme mit `zstd`-Kompression und flexiblen Recordgrößen.
  * **Samba (SMB):** Inklusive nativer macOS Time Machine-Unterstützung und Multi-Channel.
  * **NFS Kernel Server:** Hohe I/O-Performance für Linux-Clients und Proxmox.
  * **Cloud Sync (Rclone Engine):** Zeitgesteuerte oder manuelle Synchronisation zu Backblaze B2, Google Drive, OneDrive, S3, WebDAV und Nextcloud.
* **Hardware-Agnostisch:**
  * Identifiziert Disks über persistente Pfade (`/dev/disk/by-id/`).
  * Volle Unterstützung aller SAS-HBAs (LSI IT-Mode `mpt3sas`), Onboard-SATA, NVMe und 2.5G/10G-Netzwerkkarten.

---

## Projekt-Struktur

```text
Debian-Nas/
├── backend/
│   ├── cmd/nasd/main.go            # Daemon-Einstiegspunkt (Embedded UI)
│   ├── internal/
│   │   ├── zfs/                    # ZFS Pools, Datasets, Scrubs & atime=off
│   │   ├── disks/                  # HDD-Erkennung, Standby-Check, smartctl
│   │   ├── shares/                 # Samba (smb.conf) & NFS (/etc/exports.d)
│   │   ├── cloudsync/              # Rclone-Integration (B2, Drive, S3, etc.)
│   │   ├── config/                 # Konfigurationsmanager
│   │   └── api/                    # REST API Handler
│   ├── static/                     # Modernes Web-Dashboard (Embedded)
│   │   ├── index.html
│   │   ├── style.css
│   │   └── app.js
│   ├── go.mod
│   └── go.sum
├── debian/
│   ├── install.sh                  # 1-Klick Installer für Debian 13
│   └── nasd.service                # Systemd Service Unit
├── Makefile                        # Cross-Compile & Build-Skripte
└── README.md
```

---

## Schnellstart auf Debian 13 (Trixie)

### 1. Minimales Debian 13 installieren
Installiere Debian 13 (z. B. via Minimal-Netinst-ISO). Stelle sicher, dass das OS auf einer separaten SSD oder NVMe installiert ist.

### 2. Projekt klonen & Installer ausführen
```bash
git clone <dein-repo-url> /opt/debian-nas
cd /opt/debian-nas/debian
sudo chmod +x install.sh
sudo ./install.sh
```

Das Skript:
1. Aktiviert `contrib` und `non-free-firmware` in den Paketquellen.
2. Installiert `zfs-dkms`, `zfsutils-linux`, `samba`, `nfs-kernel-server`, `hd-idle`, `smartmontools` und `rclone`.
3. Kompiliert die Go-Binärdatei nach `/usr/local/bin/nasd`.
4. Richtet den Systemd-Service `nasd.service` ein und startet die Web-GUI.

### 3. Web-Interface aufrufen
Öffne im Browser deines PCs oder Laptops:
```text
http://<ip-deines-nas>:8080
```

---

## Lokales Testen (Mock-Modus)

Du kannst den Daemon auch auf jedem Entwicklungsrechner (auch ohne ZFS oder Linux) im **Mock-Modus** starten, um das Dashboard und die API zu testen:

```bash
cd backend
go run cmd/nasd/main.go -mock -port 8080
```
Rufe anschließend `http://localhost:8080` auf.
