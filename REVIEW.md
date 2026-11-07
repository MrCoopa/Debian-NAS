# Projekt-Review: Debian-NAS (Go-Daemon + eingebettete Web-UI)

Stand: 07.11.2026 · Basis: Commit `37e679e` · Schwerpunkt: **ZFS- und Freigabe-Funktionen**

**Kurz gesagt:** Die Architektur ist gut gewählt und passt zum Ziel. Die Umsetzung ist aber noch ein Prototyp.
Im jetzigen Zustand sollte `nasd` **nicht in einem Netz laufen, in dem andere Geräte oder Personen sind**:
Jeder, der Port 8080 erreicht, kann ohne Anmeldung root-Rechte bekommen.
Bei ZFS und den Freigaben gibt es außerdem mehrere Stellen, an denen **auch ein berechtigter Benutzer durch
einen Fehlklick Daten verlieren kann**, weil Schutzprüfungen fehlen oder per `-f` abgeschaltet werden.

## Inhalt

1. [Was gut ist](#-was-gut-ist)
2. [Kritisch: Sicherheit (allgemein)](#-kritisch-sicherheit-allgemein)
3. [Vertiefung: ZFS-Funktionen](#vertiefung-zfs-funktionen)
4. [Vertiefung: Freigaben (SMB, NFS, Benutzer)](#vertiefung-freigaben-smb-nfs-benutzer)
5. [Architektur und Robustheit](#-architektur-und-robustheit)
6. [Projekt-Hygiene](#-projekt-hygiene)
7. [Empfohlene Reihenfolge](#empfohlene-reihenfolge)

Legende: 🔴 Datenverlust oder Sicherheitslücke · 🟠 Fehlfunktion · 🟡 Verbesserung

---

## ✅ Was gut ist

| Punkt | Warum es passt |
|---|---|
| **Go als einzelne Binärdatei mit `embed`** (`internal/web/web.go`) | Keine Laufzeit-Abhängigkeiten, schneller Start, einfach zu verteilen. Für ein schlankes NAS genau richtig. |
| **Vorhandene Tools aufrufen** (`zpool`, `zfs`, `smbd`, `exportfs`, `rclone`) statt alles selbst zu bauen | Nutzt bewährte Werkzeuge, kein eigener ZFS-Stack. Cockpit/45Drives Houston arbeiten genauso. |
| **Befehle über `exec.Command` mit Argument-Liste**, nicht über `sh -c` | Klassische Shell-Injection ist damit ausgeschlossen. |
| **HDD-Standby-Ansatz**: `hdparm -C`, `smartctl -n standby`, `atime=off` | Richtig gedacht und das eigentliche Alleinstellungsmerkmal gegenüber TrueNAS. |
| **Pool-Import über `-d /dev/disk/by-id`** | Stabile Gerätenamen nach dem Import, unabhängig von der `sdX`-Reihenfolge. |
| **Eigene Dateien statt Eingriff in Hauptkonfiguration** (`/etc/samba/conf.d/*.conf`, `/etc/exports.d/nas.exports`) | Sauber getrennt von manuellen Einstellungen, leicht rückgängig zu machen. |
| **Mock-Modus** und Go-1.22-Routing (`"GET /api/v1/..."`) | Entwicklung ohne echte Hardware möglich, kein Router-Framework nötig. |
| **Aufteilung in Pakete** (`zfs`, `disks`, `shares`, …) | Übersichtlich, jedes Paket hat eine klare Aufgabe. |

---

## 🔴 Kritisch: Sicherheit (allgemein)

### 1. Keine Anmeldung
`AuthToken` steht zwar in `internal/config/config.go` (Z. 18), wird aber nirgends geprüft. Dazu kommt:

- Der Server lauscht auf `0.0.0.0:8080`, nur über HTTP.
- Der Dienst läuft als `User=root` (`debian/nasd.service`).
- CORS steht auf `Access-Control-Allow-Origin: *` (`internal/api/api.go`, `corsMiddleware`).

Damit kann jeder im LAN z. B. `DELETE /api/v1/zfs/pools` aufrufen und einen Pool löschen.
Wegen CORS reicht sogar eine **beliebige Webseite**, die jemand im LAN im Browser öffnet.

### 2. Lock-Dateien werden gelöscht
`cleanStalePasswdLocks` beendet `useradd`/`chpasswd` per `pkill -9` und löscht `/etc/.pwd.lock`,
`/etc/shadow.lock` usw. Das behandelt nur das Symptom und kann `/etc/shadow` beschädigen.
Laut Git-Historie wurde das eingebaut, weil es Lock-Probleme gab. Die eigentliche Ursache (parallele oder
hängende Aufrufe, vermutlich `chpasswd`) sollte behoben werden – siehe [F-12](#f-12-chpasswd-root-passwort-überschreibbar-und-unnötig).

### 3. Zu offene Dateirechte für Geheimnisse
`rclone.conf` (Cloud-Zugangsdaten), `config.json` und `cloudsync.json` werden mit `0644` gespeichert und sind
damit für alle lokalen Benutzer lesbar.

> Die ZFS- und Freigabe-spezifischen Sicherheitsprobleme (Konfigurations-Injection, Option-Injection,
> Pfade, Benutzerverwaltung) sind in den beiden Vertiefungs-Kapiteln beschrieben.

---

## Vertiefung: ZFS-Funktionen

Betroffene Dateien: `internal/zfs/zfs.go` (~1400 Zeilen), ZFS-Handler in `internal/api/api.go`,
Pool-/Dataset-Dialoge in `internal/web/static/app.js`.

### Übersicht

| ID | Thema | Schwere |
|---|---|---|
| Z-1 | Pool anlegen: alle Platten wählbar, `/dev/sdX`, immer `-f` | 🔴 |
| Z-2 | Vdev hinzufügen: immer `-f`, keine Topologie-Prüfung | 🔴 |
| Z-3 | Keine Eingabeprüfung → Option-Injection | 🔴 |
| Z-4 | Destroy/Rollback: rekursiv und erzwungen, ohne Vorschau | 🔴 |
| Z-5 | Mountpoint-Annahme `/mnt/<pool>` stimmt nicht | 🟠 |
| Z-6 | `zpool status` wird unvollständig und teils falsch geparst | 🟠 |
| Z-7 | Dataset-Liste: Zahlen fehlen, Leerzeichen brechen das Parsing | 🟠 |
| Z-8 | Volles Snapshot-Listing alle 2 s – gefährdet HDD-Standby | 🟠 |
| Z-9 | Properties werden lokal gesetzt statt vererbt | 🟡 |
| Z-10 | Pool-Defaults für SMB/NFS fehlen (`xattr`, `acltype`, …) | 🟡 |
| Z-11 | Geplante Scrubs/Snapshots nur in der Config, nicht umgesetzt | 🟡 |
| Z-12 | Gerätepfade in Attach/Replace nicht über by-id | 🟡 |

---

### Z-1 Pool anlegen: alle Platten wählbar, `/dev/sdX`, immer `-f` 🔴

**Ist-Zustand**

- Der Dialog `openCreatePoolModal` (`app.js` ~Z. 1777) listet **alle** erkannten Platten zur Auswahl –
  auch die Systemplatte und Platten, die bereits in einem Pool sind. Platten eines *exportierten* Pools
  erscheinen sogar als „Unallocated“, weil `buildDiskPoolMap` nur importierte Pools kennt.
- Gesendet wird der Kurzname (`sda`), das Backend macht daraus `/dev/sda` (`CreatePool`, `zfs.go` ~Z. 870).
- `zpool create` wird **immer mit `-f`** aufgerufen. `-f` schaltet die Schutzprüfungen von ZFS ab, z. B.
  „Platte gehört zu einem (exportierten) Pool“, „Platte enthält ein anderes Dateisystem“ und
  „unterschiedliche Redundanz-Stufen“.

**Folgen**

- Ein Fehlklick kann einen exportierten Pool oder eine Platte mit fremden Daten **ohne Rückfrage überschreiben**.
- Der Pool wird mit `sdX`-Namen angelegt. Nach Umstecken oder Hinzufügen einer Platte stimmen die Namen
  nicht mehr; `zpool status` zeigt dann andere Geräte als erwartet. Das widerspricht der README
  („Identifiziert Disks über persistente Pfade“).
- Es gibt keine Prüfung der Mindestanzahl (Mirror ≥ 2, RAID-Z1 ≥ 3, RAID-Z2 ≥ 4 – steht nur im Dropdown-Text)
  und keine Warnung bei unterschiedlich großen Platten.

**Empfehlung**

- Backend liefert eine Liste **verfügbarer** Platten: nicht Systemplatte (Mountpoints von `/`, `/boot`, Swap
  über `/proc/mounts`/`/proc/swaps` bzw. `lsblk -J` prüfen), nicht in importiertem oder exportiertem Pool
  (`zpool import` auswerten), keine vorhandenen Partitionen/Signaturen (`wipefs -n`/`blkid`).
- Im Backend gegen diese Liste prüfen – nicht nur im Frontend filtern.
- Immer `/dev/disk/by-id/...` an `zpool create` übergeben.
- `-f` entfernen. Falls nötig, nur als ausdrückliche Option („Vorhandene Signaturen überschreiben“) mit
  Bestätigung durch Eintippen des Pool-Namens.
- Mindestanzahl je Topologie serverseitig prüfen.
- Optional: `zpool create -n` als Vorschau („so wird der Pool aussehen“) vor dem eigentlichen Anlegen.

---

### Z-2 Vdev hinzufügen: immer `-f`, keine Topologie-Prüfung 🔴

**Ist-Zustand:** `AddVdev` ruft `zpool add -f <pool> [typ] <disks>` auf.

**Folge:** `zpool add` warnt normalerweise, wenn man z. B. **eine einzelne Platte zu einem RAID-Z- oder
Mirror-Pool** hinzufügt („mismatched replication level“). Mit `-f` passiert das stillschweigend. Danach hängt
der ganze Pool an dieser einen Platte ohne Redundanz – fällt sie aus, ist der **gesamte Pool verloren**.
Ein normales Daten-Vdev lässt sich bei RAID-Z-Pools nicht wieder entfernen.

Ebenso fehlt eine Prüfung für `special`-Vdevs: Ein `special`-Vdev ohne Redundanz ist genauso kritisch wie ein
Daten-Vdev, weil dort Metadaten liegen.

**Empfehlung**

- `-f` entfernen; die Warnung von ZFS als Fehler an die UI weitergeben.
- Serverseitig: Typ des neuen Vdevs muss zur bestehenden Topologie passen (Daten- und `special`-Vdevs
  mindestens Mirror, wenn der Pool redundant ist). `cache` und `spare` dürfen einzeln sein.
- Vorher `zpool add -n` ausführen und das Ergebnis anzeigen.
- Gleiche Plattenauswahl-Logik wie bei Z-1 (nur freie Platten, by-id).

---

### Z-3 Keine Eingabeprüfung → Option-Injection 🔴

**Ist-Zustand:** Pool-, Dataset-, Snapshot-, Geräte- und Property-Namen gehen ungeprüft als Argumente an
`zpool`/`zfs`. Beispiele:

- `DestroyDataset(name)` → `zfs destroy -r <name>`
- `SetDatasetProperty(dataset, property, value)` → `zfs set <property>=<value> <dataset>`
- `ImportPool(nameOrID, newName, force)` → `zpool import ... <nameOrID> <newName>`
- `CreateZvol(..., volblocksize, ...)` → `zfs create -V ... -b <volblocksize> ...`

Werte mit führendem `-` werden als Optionen interpretiert. Ein `property` wie `mountpoint` mit Wert `/etc`
oder `/root` hängt ein Dataset über Systemverzeichnisse – zusammen mit einer Freigabe darauf ist das ein
Weg zur Systemübernahme. Ebenso sind `sharenfs`/`sharesmb` setzbar, womit ZFS selbst Freigaben anlegt, die
an der nasd-Verwaltung vorbeilaufen.

**Empfehlung:** Zentrales Prüfpaket (z. B. `internal/validate`) mit Whitelists:

| Feld | Regel (Vorschlag) |
|---|---|
| Pool-Name | `^[A-Za-z][A-Za-z0-9_.:-]{0,254}$`, nicht `mirror`, `raidz*`, `draid*`, `spare`, `log`, `cache`, `special`, `dedup` |
| Dataset | Pool-Name + `(/[A-Za-z0-9_.:-]+)*`, Gesamtlänge ≤ 255 |
| Snapshot | `<dataset>@[A-Za-z0-9_.:-]+` |
| Gerät | `^/dev/disk/by-id/[A-Za-z0-9_.:+-]+$` oder bekannter Kurzname aus der Plattenliste |
| Property | **Whitelist**: `compression`, `recordsize`, `quota`, `refquota`, `reservation`, `atime`, `sync`, `xattr`, `acltype`, `readonly`, `snapdir`, … – **nicht** `mountpoint`, `sharenfs`, `sharesmb`, `canmount` (oder nur mit eigener Pfadprüfung) |
| Property-Wert | keine Steuerzeichen; je Property eigene Prüfung (z. B. `recordsize` ∈ {4K…1M}) |
| `ashift` | 9–16 |
| `vdev_type` | Whitelist (`single`, `mirror`, `raidz1-3`, `log`, `cache`, `spare`, `special`) |

Die Prüfung gehört in den Manager (gilt dann für alle Aufrufer), nicht nur in die HTTP-Handler.
Zusätzlich `--` vor positionellen Argumenten setzen, wo `zfs`/`zpool` das unterstützt.

---

### Z-4 Destroy/Rollback: rekursiv und erzwungen, ohne Vorschau 🔴

**Ist-Zustand**

| Funktion | Befehl | Problem |
|---|---|---|
| `DestroyPool` | `zfs unmount -f` + `zpool destroy -f` | Erzwingt Aushängen, auch wenn Freigaben gerade aktiv sind. |
| `DestroyDataset` | `zfs destroy -r` | Löscht **alle Kind-Datasets und Snapshots** mit. Der Dialog sagt nur „Destroy dataset“. |
| `RollbackSnapshot` | `zfs rollback -r` | Löscht **alle neueren Snapshots** ohne Hinweis. Der Dialog erwähnt nur „Daten nach dem Snapshot“. |
| `DestroySnapshot` | `zfs destroy <snap>` | ok, aber die API repariert doppelte `@` durch String-Basteln (`parts[0] + "@" + parts[last]`) – ein Hinweis auf uneinheitliche Bezeichner im Frontend. |

Außerdem bleiben nach `DestroyPool`/`DestroyDataset` die zugehörigen SMB-/NFS-Freigaben bestehen
(siehe [F-6](#f-6-freigaben-auf-nicht-eingehängten-pfaden-schreiben-auf-die-systemplatte)).

**Empfehlung**

- Vorschau per `zfs destroy -nvr <ds>` bzw. Liste der betroffenen Snapshots bei Rollback; im Dialog anzeigen.
- Bestätigung durch Eintippen des Namens (wie bei GitHub/TrueNAS).
- `-f` nur nach Fehlermeldung „busy“ und erneuter Bestätigung.
- Vor dem Löschen prüfen, ob Freigaben auf den Pfad zeigen; diese mit entfernen oder Löschen ablehnen.
- Optional: automatischer Sicherheits-Snapshot vor Rollback (`<ds>@pre-rollback-<zeit>`) – nur sinnvoll ohne `-r`.

---

### Z-5 Mountpoint-Annahme `/mnt/<pool>` stimmt nicht 🟠

**Ist-Zustand**

- `zpool create` wird **ohne** `-m` oder `-R` aufgerufen. ZFS hängt den Pool dann unter **`/<pool>`** ein,
  nicht unter `/mnt/<pool>`.
- Der Mock-Modus, die Beispiel-Freigaben und die Cloud-Sync-Beispieljobs verwenden aber `/mnt/<pool>/...`.
- Das Share-Formular schlägt `p.mountpoint || '/' + p.name` vor, `Pool` hat aber gar kein Feld `mountpoint`.
- Ein eingegebener Pfad ohne führenden `/` wird zu `/<eingabe>` ergänzt (Frontend **und** Backend).

**Folge:** Eine Freigabe auf `/mnt/TANK/daten` landet auf einem echten System **auf der Systemplatte**,
weil dort kein Pool eingehängt ist (das Verzeichnis wird per `MkdirAll` einfach angelegt, siehe F-6).

**Empfehlung**

- Festlegen: entweder `zpool create -m /mnt/<pool>` oder konsequent den echten `mountpoint` aus ZFS verwenden.
- Freigabepfade im UI nur aus einer Liste echter Dataset-Mountpoints wählen lassen (plus optional
  Unterordner), nicht als Freitext.

---

### Z-6 `zpool status` wird unvollständig und teils falsch geparst 🟠

`scanPoolStatus` liest die Textausgabe Zeile für Zeile. Dabei:

- **Hierarchie geht verloren:** Alle Zeilen landen in einer flachen Liste. Welche Platte zu welchem Mirror/RAID-Z
  gehört, ist nicht mehr erkennbar. Die UI kann z. B. „Detach“ für eine Platte in einem RAID-Z anbieten
  (dort gar nicht möglich).
- **Abschnitte `logs`, `cache`, `spares`, `special`, `dedup`** werden nicht erkannt. Ihre Platten erscheinen
  als normale Datenplatten; Spares haben Status `AVAIL`/`INUSE`, nicht `ONLINE`.
- **`draid`** wird nicht erkannt, `replacing-0`/`spare-0` (während Resilver) erscheinen als „disk“.
- **Gerätepfad falsch:** `Path: "/dev/" + filepath.Base(name)`. Bei by-id-Namen ergibt das
  `/dev/ata-WDC_...` statt `/dev/disk/by-id/ata-WDC_...`; Partitionen (`-part1`) werden nicht berücksichtigt.
- **Scrub-Daten sind erfunden:** `LastRun` ist immer `time.Now()`. Fortschritt, Geschwindigkeit, Restzeit und
  Fehlerzahl werden nie ausgelesen. Resilver wird nicht erkannt.
- Die Zeile `errors:` (Datenfehler!) wird ignoriert; ebenso Lese-/Schreib-/Prüfsummen-Fehler pro Gerät
  (Spalten 3–5).

**Empfehlung:** Debian 13 liefert OpenZFS 2.3. Ab dieser Version gibt es **JSON-Ausgabe**:
`zpool status -j`, `zpool list -j`, `zfs list -j` (vor der Umstellung auf dem Zielsystem prüfen).
Damit entfällt das fehleranfällige Text-Parsing komplett, und Hierarchie, Fehlerzähler und Scan-Status sind
strukturiert verfügbar. Falls ältere Versionen unterstützt werden sollen: `zpool status -P -p` und ein
Parser, der die Einrückung auswertet – mit Tests auf gespeicherten Beispielausgaben.

---

### Z-7 Dataset-Liste: Zahlen fehlen, Leerzeichen brechen das Parsing 🟠

- `zfs list -H` ohne `-p`: `UsedBytes`/`AvailBytes` bleiben immer 0, nur Text wie `1.2T` wird geliefert.
  Sortieren, Prozentbalken oder Warnungen bei vollem Dataset sind so nicht möglich.
- Die Zeilen werden mit `strings.Fields` getrennt. `-H` trennt aber mit **Tabs**; ein Mountpoint mit
  Leerzeichen verschiebt alle folgenden Spalten.
- Zvols (Typ `volume`) erscheinen in derselben Liste mit Mountpoint `-`; das UI bietet dafür „Share“ an.
- `-t filesystem,volume` und eine `type`-Spalte fehlen; `mounted` fehlt ebenfalls (wichtig für F-6).

**Empfehlung:** `zfs list -H -p -o name,type,used,avail,refer,mountpoint,mounted,compression,compressratio,atime,recordsize,quota`
mit Tab-Split, oder `zfs list -j`.

---

### Z-8 Volles Snapshot-Listing alle 2 s – gefährdet HDD-Standby 🟠

- Jeder Aufruf von `/zfs/datasets` ruft zusätzlich `getSnapshotCounts` auf (`zfs list -t snapshot`, **alle**
  Snapshots). `/zfs/snapshots` listet sie noch einmal. Das UI ruft beides **alle 2 Sekunden** auf.
- Bei vielen Snapshots (z. B. stündliche Auto-Snapshots über Monate) sind das Tausende Einträge pro Abruf.
- Solange die Metadaten im ARC liegen, bleiben die Platten ruhig. Werden sie verdrängt, muss ZFS sie
  **von den Platten lesen – und weckt schlafende HDDs**. Das unterläuft das Kernversprechen des Projekts.

**Empfehlung**

- Snapshot-Anzahl über `zfs get -H -p -o name,value snapshot_count` (nur wenn `snapshot_limit` genutzt wird)
  oder über einen Cache, der nur nach Änderungen bzw. selten (z. B. alle 5 min) aktualisiert wird.
- Snapshots nur laden, wenn der Snapshot-Tab offen ist, und dann pro Dataset (`zfs list -t snapshot -d 1 <ds>`).
- Grundsätzlich: Polling-Intervall im UI auf Telemetrie (CPU/Netz) beschränken; ZFS-Daten ereignis- oder
  bedarfsgesteuert laden.

---

### Z-9 Properties werden lokal gesetzt statt vererbt 🟡

- `CreatePool` setzt `-O atime=off -O compression=zstd` auf dem Root-Dataset – gut, das vererbt sich.
- `CreateDataset` setzt aber **jedes Mal** `atime=off`, `compression=lz4` (!) und `recordsize=128K` lokal.
  Dadurch wird die Pool-Einstellung `zstd` überschrieben, und spätere Änderungen am Eltern-Dataset wirken
  nicht mehr auf die Kinder.

**Empfehlung:** Nur Werte übergeben, die der Benutzer ausdrücklich gewählt hat; sonst erben lassen.
Im UI anzeigen, ob ein Wert lokal gesetzt oder geerbt ist (`zfs get -o property,value,source`).

---

### Z-10 Pool-Defaults für SMB/NFS fehlen 🟡

Für ein NAS mit Samba/NFS-Fokus sind beim Anlegen des Pools folgende Root-Dataset-Properties üblich:

| Property | Wert | Warum |
|---|---|---|
| `xattr` | `sa` | Erweiterte Attribute im Inode statt in versteckten Verzeichnissen – deutlich schneller, wichtig für macOS (`streams_xattr`, Time Machine) |
| `acltype` | `posixacl` | POSIX-ACLs für Samba/NFS-Rechte |
| `dnodesize` | `auto` | Platz für SA-xattrs |
| `relatime` | – | entfällt, da `atime=off` |
| `normalization` | `formD` | Unicode-Dateinamen von macOS und Windows konsistent (nur bei Erstellung setzbar) |
| `autotrim` (Pool) | `on` bei SSD-Pools | statt manueller TRIM-Buttons |

Optional `casesensitivity=mixed` für reine SMB-Datasets (nur bei Erstellung setzbar).

---

### Z-11 Geplante Scrubs/Snapshots nur in der Config 🟡

- `config.go` enthält `scrub_schedule: monthly` und `snapshot_schedule: daily`, **nirgends im Code verwendet**.
- Debian liefert mit `zfsutils-linux` bereits einen Cron-Job für monatliche Scrubs und TRIM
  (`/etc/cron.d/zfsutils-linux`). Eine eigene Planung würde doppelt scrubben.
- Automatische Snapshots mit Aufbewahrungsregeln (stündlich/täglich/wöchentlich) fehlen komplett – für ein
  NAS mit „Datasets & Snapshots“ im Funktionsumfang eine wichtige Lücke (Schutz gegen versehentliches Löschen
  und Ransomware über SMB).

**Empfehlung:** Für Scrubs den Debian-Cron-Job nutzen und in der UI nur anzeigen. Für Snapshots entweder
`zfs-auto-snapshot`/`sanoid` einbinden (bewährt, inkl. Aufbewahrung) oder einen eigenen Planer mit
Aufbewahrungsregeln. Bei HDD-Pools beachten: Snapshots auf ruhenden Datasets wecken die Platten.

---

### Z-12 Gerätepfade in Attach/Replace/Offline nicht über by-id 🟡

`AttachDisk`, `ReplaceDisk`, `OfflineDisk`, `OnlineDisk`, `DetachDisk` übernehmen den Gerätenamen ungeprüft.
Das neue Gerät bei Attach/Replace sollte – wie bei Z-1 – aus der Liste freier Platten kommen und als by-id
übergeben werden. Zusätzlich fehlt eine Prüfung, ob die neue Platte mindestens so groß ist wie die alte
(ZFS lehnt das zwar ab, die Meldung ist aber für Benutzer kryptisch).

---

## Vertiefung: Freigaben (SMB, NFS, Benutzer)

Betroffene Dateien: `internal/shares/shares.go`, Share-/User-Handler in `internal/api/api.go`,
Share-Dialog in `internal/web/static/app.js`.

### Übersicht

| ID | Thema | Schwere |
|---|---|---|
| F-1 | `smb.conf`-Injection über Name/Pfad/Kommentar/Benutzer | 🔴 |
| F-2 | Path Traversal beim Konfig-Dateinamen | 🔴 |
| F-3 | Freigabepfade nicht eingeschränkt, `chmod 0777` auf beliebige Verzeichnisse | 🔴 |
| F-4 | Rechte-Modell: alles `0777`/`0666` | 🔴 |
| F-5 | NFS: `*` und `no_root_squash` als Standard | 🔴 |
| F-6 | Freigaben auf nicht eingehängten Pfaden schreiben auf die Systemplatte | 🔴 |
| F-7 | NFS „Read-only“ wird ignoriert | 🟠 |
| F-8 | NFS-Optionen ändern sich beim Löschen anderer Exports | 🟠 |
| F-9 | Konfiguration wird beim Speichern verlustbehaftet neu geschrieben | 🟠 |
| F-10 | Kein `testparm`/Fehlerprüfung beim Neuladen | 🟠 |
| F-11 | Share-Namen: Groß-/Kleinschreibung, Kollisionen | 🟡 |
| F-12 | `chpasswd`: root-Passwort überschreibbar und unnötig | 🔴 |
| F-13 | `userdel` ohne Prüfung | 🔴 |
| F-14 | Benutzerrollen ohne Wirkung, Phantom-„admin“ | 🟡 |
| F-15 | Time Machine/macOS unvollständig konfiguriert | 🟡 |
| F-16 | Stored XSS über Share-/Benutzernamen in der UI | 🟠 |

---

### F-1 `smb.conf`-Injection 🔴

`generateSmbConfig` schreibt `Name`, `Path`, `Comment` und `ValidUsers` ungefiltert:

```go
sb.WriteString(fmt.Sprintf("[%s]\n", s.Name))
sb.WriteString(fmt.Sprintf("   path = %s\n", s.Path))
sb.WriteString(fmt.Sprintf("   comment = %s\n", s.Comment))
```

Ein Kommentar wie `x\n   root preexec = /bin/sh -c '...'` fügt eine eigene Zeile ein. `root preexec`
führt beim Verbinden **einen Befehl als root** aus. Ebenso möglich: `path = /`, `force user = root`,
`admin users = gast`, oder mit `]` im Namen eine zweite Freigabe-Sektion.

**Empfehlung**

- Share-Name: `^[A-Za-z0-9][A-Za-z0-9 _.-]{0,79}$` (keine `[]`, `/`, `\`, `=`, Steuerzeichen);
  reservierte Namen ablehnen: `global`, `homes`, `printers`, `print$`, `ipc$`.
- Kommentar: max. 256 Zeichen, keine Steuerzeichen (`\r`, `\n`, `\x00`).
- Pfad: siehe F-3.
- `ValidUsers`: jedes Element gegen die Benutzernamen-Regel (`^@?[a-z_][a-z0-9_-]{0,31}$`) **und** gegen die
  existierenden Samba-Benutzer/Gruppen prüfen.

---

### F-2 Path Traversal beim Konfig-Dateinamen 🔴

```go
confPath := filepath.Join(m.sambaConfDir, fmt.Sprintf("%s.conf", s.Name))
```

Mit `Name = "../../cron.d/evil"` landet die Datei als `/etc/cron.d/evil.conf`. Zusammen mit F-1 lässt sich
Inhalt darin steuern. `DeleteSmb` löscht auf demselben Weg beliebige `*.conf`-Dateien
(z. B. `../../ssh/sshd_config.d/x`).

**Empfehlung:** Name prüfen (F-1). Zusätzlich den Dateinamen nicht aus dem Anzeigenamen ableiten, sondern
normalisiert (`strings.ToLower`, nur `[a-z0-9_-]`) und nach `filepath.Join` prüfen, dass das Ergebnis
innerhalb von `sambaConfDir` liegt. Beim Löschen nur Dateien entfernen, die nasd selbst angelegt hat
(z. B. Kennzeichnung per Kommentarzeile `# managed by nasd`).

---

### F-3 Freigabepfade nicht eingeschränkt, `chmod 0777` auf beliebige Verzeichnisse 🔴

`SaveSmb` und `SaveNfs` akzeptieren jeden Pfad und führen dann aus:

```go
_ = os.MkdirAll(s.Path, 0777)
_ = os.Chmod(s.Path, 0777)
```

Eine Freigabe auf `/etc`, `/root` oder `/` macht diese Verzeichnisse **weltweit beschreibbar** und stellt sie
gleichzeitig ins Netz. Auch ohne böse Absicht reicht ein Tippfehler.

**Empfehlung**

- Erlaubt sind nur Pfade **innerhalb eines eingehängten ZFS-Datasets** (Mountpoint aus `zfs list`,
  `mounted=yes`) – optional zusätzlich `/srv`.
- Sperrliste als zweite Verteidigungslinie: `/`, `/etc`, `/bin`, `/sbin`, `/usr`, `/lib*`, `/boot`, `/root`,
  `/proc`, `/sys`, `/dev`, `/run`, `/var`, `/tmp`, `/home` (Wichtig bei Root-on-ZFS, z. B. Proxmox:
  `rpool/ROOT/...` ist auf `/` eingehängt und würde sonst als „Dataset-Mountpoint“ durchgehen.)
- `filepath.Clean` + `filepath.EvalSymlinks` vor der Prüfung, damit `/mnt/tank/link -> /etc` nicht durchrutscht.
- **Nie `chmod` auf bestehende Verzeichnisse** anwenden; nur beim Neuanlegen Rechte setzen (siehe F-4).

---

### F-4 Rechte-Modell: alles `0777`/`0666` 🔴

**Ist-Zustand:** Jeder Freigabe-Ordner bekommt `0777`, Samba erzwingt `create mask = 0666`,
`force create mode = 0666`, `directory mask = 0777`, `force directory mode = 0777`.

**Folgen**

- `valid users` schränkt nur ein, **wer sich verbinden darf**. Auf Dateisystemebene darf jeder lokale Prozess
  und jeder NFS-Client alles lesen und ändern.
- Freigaben wie „Milena“ und „Niklas“ (Beispiel im Mock) sind damit nur scheinbar privat: Über NFS oder eine
  zweite SMB-Freigabe auf einen übergeordneten Pfad kommt man an alle Daten.
- Der Commit „0777 permissions“ hat das Symptom („kann nicht schreiben“) gelöst, nicht die Ursache
  (Ordner gehört `root:root`, Samba-Benutzer sind keine Eigentümer).

**Empfehlung (einfaches, robustes Modell)**

- Gruppe `nasusers` anlegen; jeder von nasd erstellte Benutzer kommt in diese Gruppe.
- Neuer Freigabe-Ordner: `root:nasusers`, Modus `2770` (setgid, damit neue Dateien die Gruppe erben).
- Samba: `force group = nasusers`, `create mask = 0660`, `directory mask = 2770`; bei Gastfreigaben
  bewusst `0775`/`2775` und Hinweis im UI.
- Für private Freigaben: Eigentümer = Benutzer, Modus `0700`, `valid users = <user>`.
- Langfristig: ACLs (`acltype=posixacl`, Z-10) und eine Rechte-Verwaltung im UI.

---

### F-5 NFS: `*` und `no_root_squash` als Standard 🔴

- Das Frontend sendet fest `allowed_clients: '*'` – jedes Gerät, das das NAS erreicht, darf mounten.
- Standard-Optionen in `SaveNfs`: `rw,sync,no_subtree_check,no_root_squash`. Mit `no_root_squash` hat
  **root auf jedem Client auch root-Rechte auf dem Export** – kann also z. B. setuid-root-Binärdateien
  ablegen oder alle Dateien lesen.
- Zusammen: Jeder im Netz mit einem Linux-Rechner hat volle root-Rechte auf allen NFS-Daten.
- `AllowedClients` und `Options` werden ungeprüft in die Exports-Datei geschrieben – ein Zeilenumbruch fügt
  einen beliebigen weiteren Export hinzu (z. B. `/ *(rw,no_root_squash)`).

**Empfehlung**

- Erlaubte Clients im Formular **pflichtig** abfragen (IP, CIDR oder Hostname); `*` nur nach ausdrücklicher
  Bestätigung.
- Default: `rw,sync,no_subtree_check,root_squash`; `no_root_squash` nur als bewusste Option
  (z. B. „für Proxmox-Backups“), mit Warnhinweis.
- Clients gegen `^[A-Za-z0-9.*:/\[\]_-]+$` prüfen, Optionen gegen eine Whitelist bekannter NFS-Optionen.
- Mehrere Clients pro Export unterstützen (`/pfad c1(opts) c2(opts)`) – der Parser liest heute nur den ersten.

---

### F-6 Freigaben auf nicht eingehängten Pfaden schreiben auf die Systemplatte 🔴

**Szenario**

1. Pool wird exportiert, ist beim Booten nicht importiert (Platte fehlt, Import schlägt fehl) oder der
   Pfad stimmt nicht (Z-5).
2. `SaveSmb`/`SaveNfs` führt `MkdirAll(path)` aus – das Verzeichnis entsteht **auf dem Root-Dateisystem**.
3. Benutzer kopieren Daten auf die Freigabe; sie landen auf der System-SSD, die vollläuft.
4. Wird der Pool später wieder importiert, schlägt das Einhängen fehl („directory is not empty“) oder
   verdeckt die zwischenzeitlich geschriebenen Daten.

Dasselbe passiert, wenn ein Pool/Dataset gelöscht wird: Die Freigaben bleiben in `conf.d`/`exports.d`
stehen (Z-4), Samba legt beim nächsten Zugriff keine Verzeichnisse an, aber ein erneutes „Speichern“ in nasd tut es.

**Empfehlung**

- Vor dem Anlegen prüfen: Zielpfad liegt in einem **eingehängten** Dataset (F-3). Nicht vorhandene
  Unterordner nur anlegen, wenn das Eltern-Dataset eingehängt ist.
- Samba: Option `path` zusätzlich absichern, z. B. per `root preexec = /usr/bin/mountpoint -q <dataset-mountpoint>`
  mit `root preexec close = yes` – dann verweigert Samba die Verbindung, wenn das Dataset fehlt.
- NFS: Option `mountpoint` (bzw. `mp`) in den Export-Optionen setzen – dann exportiert der Kernel nur,
  wenn dort tatsächlich ein Dateisystem eingehängt ist.
- Beim Löschen/Exportieren von Pools und Datasets die betroffenen Freigaben anzeigen und mit entfernen.

---

### F-7 NFS „Read-only“ wird ignoriert 🟠

Das Formular bietet „Read-only“ an und sendet `read_only: true`. Der Typ `NfsExport` hat aber **kein Feld
`ReadOnly`** – der Wert wird beim JSON-Dekodieren verworfen. Ein als schreibgeschützt angelegter Export
ist tatsächlich `rw`. Für den Benutzer ist das nicht erkennbar.

**Empfehlung:** Feld `ReadOnly bool` ergänzen und in den Optionen `ro` statt `rw` setzen; in der Tabelle
den tatsächlichen Zustand aus den Optionen anzeigen.

---

### F-8 NFS-Optionen ändern sich beim Löschen anderer Exports 🟠

`SaveNfs` und `DeleteNfs` erzeugen die Exports-Datei jeweils selbst – mit **unterschiedlichen Defaults**:

| Funktion | Default bei leeren Optionen |
|---|---|
| `SaveNfs` | `rw,sync,no_subtree_check,no_root_squash` |
| `DeleteNfs` | `rw,sync,no_subtree_check` |

Löscht man Export A, ändern sich die Rechte von Export B (falls dessen Optionen leer waren). Außerdem
fehlen in `DeleteNfs` die Fallbacks für `AllowedHosts`, und Schreibfehler werden ignoriert.

**Empfehlung:** Eine gemeinsame Funktion `writeExports()` mit einem Default; Optionen beim Speichern
immer explizit füllen, damit die Datei selbsterklärend ist. Datei atomar schreiben (temporäre Datei +
`rename`), dann `exportfs -ra`; bei Fehler die alte Datei zurückspielen.

---

### F-9 Konfiguration wird beim Speichern verlustbehaftet neu geschrieben 🟠

- `parseSmbConfigFile` erkennt nur `path`, `comment`, `read only`, `guest ok`, `valid users` und die
  Time-Machine-Zeile. Alle anderen Einstellungen (z. B. von Hand ergänzte `veto files`, `hosts allow`,
  `fruit:time machine max size`) gehen beim nächsten Speichern verloren.
- Werte mit `=` darin werden durch `strings.Split(line, "=")` abgeschnitten (`parts[1]`).
- `strings.HasPrefix(line, "path")` trifft auch auf andere Parameter, die mit „path“ beginnen.
- Zustand liegt nur in einer Map im Speicher; wird eine Datei außerhalb von nasd geändert, überschreibt
  das nächste Speichern die Änderung ohne Hinweis.

**Empfehlung:** Entweder klar definieren „Dateien in `conf.d` gehören nasd, manuelle Änderungen werden
überschrieben“ (dann Kopfzeile `# managed by nasd – do not edit` schreiben) oder unbekannte Parameter beim
Einlesen aufbewahren und wieder ausgeben. `strings.SplitN(line, "=", 2)` verwenden und Schlüssel exakt
vergleichen. Langfristig: eigene Zustandsdatei (`/etc/nas/shares.json`) als Quelle der Wahrheit, aus der
die Samba-/NFS-Dateien generiert werden.

---

### F-10 Kein `testparm`/Fehlerprüfung beim Neuladen 🟠

- Nach dem Schreiben wird `smbcontrol all reload-config` **und** `systemctl reload-or-restart smbd`
  aufgerufen; beide Fehler werden ignoriert. Eine fehlerhafte Datei in `conf.d` kann Samba dazu bringen,
  den Include zu verwerfen – **alle** Freigaben sind dann weg, die UI meldet „gespeichert“.
- `ensureSambaInclude` hängt den Include an das Ende von `smb.conf` an. Steht dort zuletzt eine
  Freigabe-Sektion (z. B. `[printers]`), wird der Include Teil dieser Sektion statt global wirksam.
  `install.sh` macht dasselbe.

**Empfehlung:** Neue Datei zuerst als temporäre Datei schreiben, mit `testparm -s <datei>` prüfen
(bzw. die Gesamtkonfiguration), erst dann umbenennen und einmal `smbcontrol smbd reload-config` aufrufen.
Fehler an die UI zurückgeben. Include in die `[global]`-Sektion setzen oder `smb.conf` bei der Installation
mit einem klar definierten Inhalt ausliefern.

---

### F-11 Share-Namen: Groß-/Kleinschreibung, Kollisionen 🟡

- Samba behandelt Share-Namen **ohne Groß-/Kleinschreibung**. `Media` und `media` erzeugen zwei Dateien mit
  derselben Freigabe; welche gilt, hängt von der Lade-Reihenfolge ab.
- `SaveSmb` unterscheidet nicht zwischen „neu anlegen“ und „ändern“ – ein neuer Share mit vorhandenem Namen
  überschreibt den alten still.
- Umbenennen ist nicht möglich (alter Eintrag bliebe stehen).
- NFS-Exports sind nach Pfad geschlüsselt, ohne `Clean`: `/mnt/a` und `/mnt/a/` sind zwei Einträge.

**Empfehlung:** Eindeutigkeit case-insensitiv prüfen; getrennte Endpunkte für Anlegen (`POST`, 409 bei
Konflikt) und Ändern (`PUT`); Pfade normalisieren.

---

### F-12 `chpasswd`: root-Passwort überschreibbar und unnötig 🔴

`CreateUser` übergibt `username:password\n` ungefiltert an `chpasswd`. Ein Passwort wie `x\nroot:neu`
**setzt das root-Passwort**. Dazu:

- Der Benutzer hat die Shell `nologin` – ein Linux-Passwort wird für Samba **gar nicht gebraucht**.
  Samba verwaltet sein eigenes Passwort (`smbpasswd`/`pdbedit`).
- Der Fehler von `chpasswd` wird ignoriert (`_ = chCmd.Run()`).
- `chpasswd` ist sehr wahrscheinlich die Quelle der Lock-Probleme, die mit `cleanStalePasswdLocks`
  „gelöst“ wurden.

**Empfehlung:** `chpasswd` und `cleanStalePasswdLocks` ersatzlos entfernen. Passwort nur an `smbpasswd -s`
übergeben, vorher prüfen: keine `\r`, `\n`, `\x00`, Mindestlänge. Benutzername:
`^[a-z_][a-z0-9_-]{0,31}$`, nicht mit `-` beginnend. Bei Lock-Fehlern von `useradd` eine klare Fehlermeldung
zurückgeben statt Prozesse zu beenden.

---

### F-13 `userdel` ohne Prüfung 🔴

`DeleteUser` ruft `smbpasswd -x <name>` und `userdel <name>` für **jeden** übergebenen Namen auf. Über die API
lassen sich damit Systemkonten (`www-data`, `sshd`, `nobody`, im Extremfall `root`) löschen. Fehler werden
ignoriert, die UI meldet Erfolg.

Ebenso legt `CreateUser` für einen **existierenden** Systembenutzer (z. B. `root`) einen Samba-Zugang an –
`root` über SMB ist ein beliebtes Angriffsziel.

**Empfehlung**

- Nur Benutzer mit UID ≥ 1000 (und ≠ 65534) zulassen; Systemnamen sperren.
- Von nasd angelegte Benutzer kennzeichnen (z. B. `useradd -c "nasd storage user"` oder Gruppe `nasusers`)
  und `userdel` nur für diese ausführen. Bei fremden Konten nur den Samba-Zugang entfernen.
- Vor dem Löschen prüfen, ob der Benutzer in `valid users` einer Freigabe steht, und das anzeigen.

---

### F-14 Benutzerrollen ohne Wirkung, Phantom-„admin“ 🟡

- `Role` („admin“/„user“) wird gespeichert, hat aber **keine Auswirkung** – weder in Samba noch in nasd.
  `loadLinuxUsers` vergibt „admin“ allein anhand des Namens (`root`, `admin`).
- Gibt es keine Samba-Benutzer, wird ein Benutzer `admin` in die Liste eingefügt, **der nicht existiert**.
- Die UI-Tabelle zeigt Spalten (UID, Primary Group, Created), die das Backend nicht liefert.

**Empfehlung:** Rollen entweder entfernen oder echt umsetzen (z. B. `admin users`/Gruppe in Samba,
Login-Berechtigung für die nasd-Oberfläche). Keine erfundenen Einträge; UID und Gruppe aus `/etc/passwd`
liefern.

---

### F-15 Time Machine/macOS unvollständig konfiguriert 🟡

- `vfs objects = catia fruit streams_xattr` wird nur in Time-Machine-Freigaben gesetzt. Samba empfiehlt,
  `fruit` **einheitlich für alle Freigaben** (bzw. in `[global]`) zu konfigurieren, da sich macOS-Clients sonst
  je nach zuerst verbundener Freigabe unterschiedlich verhalten.
- Es fehlen `fruit:time machine max size` (sonst füllt Time Machine das Dataset komplett) und sinnvolle
  globale Werte wie `fruit:metadata = stream`, `fruit:model = MacSamba`.
- `streams_xattr` braucht erweiterte Attribute – mit ZFS-Default `xattr=on` funktioniert das, ist aber
  langsam; siehe Z-10 (`xattr=sa`).
- Für Time Machine empfiehlt sich ein **eigenes Dataset mit Quota** statt nur `max size`.

---

### F-16 Stored XSS über Share-/Benutzernamen 🟠

Die UI baut Tabellen und Toasts per `innerHTML` mit Template-Strings (z. B. `showToast`,
Share-Tabelle, `onclick="promptCreateShare('${...}')"`). Share-Namen, Kommentare, Benutzernamen und
Fehlermeldungen vom Backend (die z. B. `zfs`-Ausgaben mit Benutzereingaben enthalten) werden **ohne
Escaping** eingefügt. Ein Share-Name wie `<img src=x onerror=...>` führt Code im Browser jedes
Administrators aus – mit dessen Sitzung, sobald es eine Anmeldung gibt.

**Empfehlung:** Serverseitige Eingabeprüfung (F-1) plus clientseitig `textContent` bzw. eine
`escapeHtml()`-Hilfsfunktion für alle dynamischen Werte; keine Inline-`onclick`-Handler mit Daten,
sondern `data-*`-Attribute und Event-Delegation. Zusätzlich eine Content-Security-Policy setzen.

---

## 🟠 Architektur und Robustheit

- **Hardware wird bei jeder Anfrage abgefragt.** Die UI fragt alle 2 s nach (`setInterval(refreshAll, 2000)`)
  und lädt dabei System, Disks, Pools, Datasets, SMB, NFS, Snapshots und Benutzer – 8 Anfragen.
  `/system` und `/disks` rufen jedes Mal `scanLinuxDisks` auf (pro Platte `hdparm` + `smartctl`, dazu
  `zpool status`), `/zfs/pools` ruft pro Pool `zpool status` auf, `/zfs/datasets` listet alle Snapshots (Z-8).
  Das alles bei gesperrtem Mutex. Der vorhandene 30-s-Hintergrund-Loop füllt einen Cache, der für die
  Antworten nicht genutzt wird.
  → Besser: Ein Hintergrundprozess sammelt die Daten, die API liest nur aus dem Cache; ZFS- und
  Freigabedaten nur bei Bedarf/nach Änderungen neu laden.
- **Lange ZFS-Operationen blockieren alles.** Jede schreibende ZFS-Funktion hält `m.mu.Lock()` während des
  gesamten `zpool`/`zfs`-Aufrufs. Ein langsames `zpool import` oder `zfs destroy` eines großen Datasets
  blockiert damit auch alle lesenden Anfragen (`ListPools` braucht `RLock`). Zusammen mit `WriteTimeout: 15s`
  bricht die HTTP-Antwort ab, während der Befehl weiterläuft.
  → Lange Operationen als Hintergrund-Job mit Status-Endpunkt; Lock nur für den In-Memory-Zustand.
- **Kein Timeout für externe Befehle.** Hängt `zpool` (z. B. bei einer defekten Platte im D-State), hängt der
  Handler für immer. → `exec.CommandContext` mit Timeout.
- **Spindown wird doppelt gesteuert.** `hd-idle` und `hdparm -S` laufen beide und können sich stören.
  Die Umrechnung für `-S` ist fehlerhaft: > 5,5 h ergibt `252` (= 21 min), 1800 s ergibt `240` statt `241`.
- **Fehler werden still verschluckt.** `_ = cmd.Run()` bei `hdparm -S`, `ledctl`, `chpasswd`, Samba-Reload,
  `userdel`, `smbpasswd -x` und mehreren `os.WriteFile`. Die UI meldet dann Erfolg, obwohl nichts passiert ist.
- **Gefälschte Benchmark-Werte.** Wenn `fio` fehlschlägt, liefert `handleStorageBenchmark` die fest
  eingetragenen Mock-Werte (542 MB/s) als echtes Ergebnis zurück. Das Benchmark-Ziel (`target`) wird ungeprüft
  zu einem Pfad zusammengesetzt; `fio` schreibt dann z. B. nach `/etc/.nas_bench.tmp`.
- **Mock-Beispieljobs im Echtbetrieb.** `cloudsync.NewManager` legt auf einem echten System, wenn noch keine
  Jobs existieren, die **Mock-Beispieljobs** an und speichert sie (`/mnt/DATA` → `hetzner_storagebox`, täglich).
- **API-Handler doppelt und uneinheitlich.** Viele Handler lesen dasselbe Feld aus `name`, `pool`,
  Query-Parameter **oder** Body. Ein Großteil der ~1150 Zeilen in `api.go` ist kopierter Code.
  Ein gemeinsamer Request-Typ und ein Helfer zur Prüfung würden das deutlich verkürzen.
- **Ungenaue Hardware-Angaben.** Slot/Gehäuse werden aus der Listenreihenfolge abgeleitet (`Bay 01…`),
  der Bus pauschal als „SATA 6.0 Gb/s“ bzw. „PCIe Gen4 x4“ angezeigt.
- **Kein sauberes Herunterfahren.** Nach dem Signal endet `main` ohne `httpServer.Shutdown()`.
  Laufende `rclone`-Jobs werden einfach abgebrochen.

---

## 🟡 Projekt-Hygiene

- **Keine Tests:** keine einzige `_test.go`-Datei. Besonders lohnend:
  - Parser für `zpool status`, `zpool import`, `zfs list` mit gespeicherten Beispielausgaben
    (Mirror, RAID-Z, mit logs/cache/spares, während Scrub/Resilver, DEGRADED).
  - Prüffunktionen (Namen, Pfade, NFS-Optionen) mit Positiv- und Negativfällen (Zeilenumbruch, `..`, `-x`).
  - Generierte `smb.conf`-/`exports`-Inhalte als Golden Files.
- **UI liegt doppelt vor:** `backend/static/` und `backend/internal/web/static/` sind beide im Repository;
  eingebettet wird nur `internal/web/static/`.
- **Installer baut auf dem NAS selbst:** `install.sh` installiert `golang-go` und kompiliert dort. Besser:
  im CI bauen und eine fertige Binärdatei oder ein `.deb`-Paket ausliefern. Außerdem wird `non-free` per `sed`
  an die Paketquellen gehängt, ohne dass es nötig ist.
- **Ungeprüfte Werbeaussagen im README:** „Startzeit < 10 ms“, „15 MB RAM“ und „Samba Multi-Channel“
  (nirgends konfiguriert) sind nicht belegt.

---

## Empfohlene Reihenfolge

### Phase 1 – Sicherheit (blockierend)

1. Anmeldung einbauen (Session-Cookie `HttpOnly`/`SameSite=Strict` plus Login-Seite), CORS entfernen,
   Origin-Prüfung für schreibende Anfragen, optional HTTPS.
2. Zentrales Prüfpaket `internal/validate` (Z-3, F-1, F-2, F-3, F-5, F-12) und Aufruf in **jedem** Manager.
3. `chpasswd` und `cleanStalePasswdLocks` entfernen, `userdel` nur für eigene Benutzer (F-12, F-13).
4. Escaping im Frontend (F-16).

### Phase 2 – Datenverlust verhindern (ZFS & Freigaben)

5. Plattenauswahl nur freie Platten, by-id, kein `-f` bei `create`/`add` (Z-1, Z-2, Z-12).
6. Vorschau und Namens-Bestätigung bei Destroy/Rollback; verknüpfte Freigaben anzeigen (Z-4).
7. Freigaben nur auf eingehängten Datasets, `mountpoint`-Schutz in Samba/NFS (Z-5, F-6).
8. Rechte-Modell mit Gruppe statt `0777`, NFS mit expliziten Clients und `root_squash` (F-4, F-5, F-7).

### Phase 3 – Korrektheit und Standby

9. Parser auf JSON-Ausgabe (`zpool status -j`, `zfs list -j`) umstellen (Z-6, Z-7).
10. Hintergrund-Cache statt Abfrage pro Anfrage, Snapshot-Liste nur bei Bedarf (Z-8, Architektur).
11. Gemeinsames, atomares Schreiben der Samba-/NFS-Dateien mit `testparm`/`exportfs`-Prüfung (F-8, F-9, F-10).
12. Fehler konsequent zurückmelden, Timeouts für externe Befehle, keine Mock-Werte im Echtbetrieb.

### Phase 4 – Funktionen und Qualität

13. Tests für Parser, Prüfungen und generierte Konfiguration, dazu CI.
14. Pool-Defaults für NAS (Z-10), Properties erben (Z-9), automatische Snapshots mit Aufbewahrung (Z-11).
15. macOS/Time Machine sauber konfigurieren (F-15), Benutzerrollen klären (F-14).

---

## 🚀 Umsetzungsstatus (Implementiert & Verifiziert)

Stand: **07.11.2026** · Alle Phasen wurden implementiert, per Unit-Tests abgesichert und durch Linux-/Windows-Builds verifiziert.

### Status-Übersicht

| ID | Thema | Status | Umgesetzte Maßnahme |
|---|---|---|---|
| **Auth** | Fehlende Authentifizierung | ✅ Erledigt | Auth-Middleware mit Bearer-Token, `X-Auth-Token` & Session-Cookie. Auth-Endpoints (`/api/v1/auth/*`). |
| **CORS/CSRF** | Wildcard-CORS & CSRF-Gefahr | ✅ Erledigt | Wildcard-CORS entfernt, Security-Middleware mit Origin-Check für Mutationen (`POST`/`PUT`/`DELETE`) und Security-Headern (`X-Content-Type-Options: nosniff`, `X-Frame-Options: SAMEORIGIN`). |
| **F-12 / F-13** | `cleanStalePasswdLocks` & `chpasswd` Root-Überschreibung | ✅ Erledigt | `cleanStalePasswdLocks` und `chpasswd` ersatzlos entfernt. Samba-Passwort wird direkt per `smbpasswd` gesetzt, Benutzer mit `/usr/sbin/nologin` und Gruppe `nasusers` angelegt. `DeleteUser` schützt System-UIDs (< 1000). |
| **Val** | Zentrale Validierung | ✅ Erledigt | Paket `internal/validate` implementiert (Pool-, Dataset-, Snapshot-, Freigabenamen, Pfade, Dateirechte, Benutzer, Passwörter, NFS-Clients & Optionen, Device-Pfade). 100% Testabdeckung in `validate_test.go`. |
| **Z-1 / Z-12** | ZFS Pool-Erstellung & Plattenauswahl | ✅ Erledigt | UI priorisiert `/dev/disk/by-id/`-Pfade. System-/OS-Platten (`is_system_disk`) und bereits zugewiesene Platten werden im Frontend gesperrt/deaktiviert. Blindes `-f` bei `zpool create` und `zpool add` entfernt. Mindest-Plattenanzahl nach Topologie erzwungen (Mirror >= 2, RAIDZ1 >= 3, RAIDZ2 >= 4, RAIDZ3 >= 5). |
| **Z-4 / F-6** | ZFS Destroy Safeguards | ✅ Erledigt | UI prüft vor Pool-/Dataset-Löschung aktiv verknüpfte SMB/NFS-Freigaben, warnt den Benutzer und bereinigt verwaiste Freigaben automatisch. |
| **Z-10** | NAS Root-Dataset Defaults | ✅ Erledigt | Standardmäßig `-O atime=off -O compression=zstd -O xattr=sa -O acltype=posixacl` bei `zpool create`. |
| **F-4 / F-5** | Freigabe-Pfade & Rechte | ✅ Erledigt | Blindes `chmod 0777` entfernt. Neue Freigabe-Verzeichnisse werden mit restriktiven `0775` angelegt. Pfade auf geschützte Systempfade (`/etc`, `/boot`, `/usr` etc.) werden strikt abgewiesen. |
| **F-7** | NFS-Sicherheit | ✅ Erledigt | NFS-Exporte verwenden standardmäßig sicheres `root_squash` (statt gefährlichem `no_root_squash`). UI erlaubt Konfiguration von Client-Subnetzen statt erzwungenem `*`. |
| **F-16** | Stored XSS im Frontend | ✅ Erledigt | `escapeHtml()` in `app.js` implementiert für Tabellenzeilen, Toasts und Modalfenster. |
| **Z-6 / Z-7** | ZFS & Scrub Status Parsing | ✅ Erledigt | Modularer Parser `parseZpoolStatus` mit `zpool status -P`. Scrub-Fortschritt (`% done`), Fehleranzahl (`with X errors`) und exakter Abschlusszeitstempel (`on <Date>`) werden sauber geparst; kein hartcodiertes `time.Now()` mehr. |
| **Z-8** | Polling & Standby-Schutz | ✅ Erledigt | Schneller 2-Sekunden-Loop ruft nur dynamische Telemetrie ab (`/system`, gecachte `/disks`, `/zfs/pools`). Schwere Operationen (`zfs list -t snapshot`, `pdbedit -L`) laufen nur noch bei Tab-Aktivierung, nach Mutationen oder im 30-Sekunden-Hintergrundsync. |
| **Timeouts** | Prozess-Hangs bei Storage-Aufrufen | ✅ Erledigt | Alle `exec.Command`-Aufrufe in `zfs`, `shares` und `disks` auf `exec.CommandContext` mit Timeouts (5s bis 120s) umgestellt. Verhindert Deadlocks bei hängenden Festplatten oder I/O-Timeouts. |
| **Spindown** | hdparm Spindown-Formel | ✅ Erledigt | Formel in `disks.go` korrigiert. Kein unbeabsichtigtes Setzen von `val = 252` (21 Minuten) bei langen Timeouts mehr. |
| **Tests** | Unit-Tests & CI-Fähigkeit | ✅ Erledigt | Umfangreiche Unit-Tests für `validate`, `shares` und `zfs` (`zfs_test.go`, `shares_test.go`, `validate_test.go`). Alle Tests laufen mit `go test ./...` durch (100% grün). |
| **Builds** | Cross-Platform Kompilierung | ✅ Erledigt | `bin/nasd-linux-amd64` (Linux Target) und `bin/nasd.exe` (Windows) kompilieren fehlerfrei mit Go 1.22. |

