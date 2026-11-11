# OpenZFS Feature-Audit & Architektur-Review (Debian 13 „Trixie“)

Dieses Dokument fasst die vollständige Überprüfung der gesamten **OpenZFS-Engine** (`backend/internal/zfs/zfs.go`, `backend/internal/validate/validate.go`, `backend/internal/api/ui_handlers.go` sowie aller Web-UI-Templates) zusammen – inklusive aller im Rahmen dieses Reviews identifizierten und direkt behobenen Bugs.

---

## 1. Übersicht der geprüften ZFS-Subsysteme

| Subsystem | Geprüfte Funktionen | Status |
| :--- | :--- | :---: |
| **Pool-Lifecycle** | `CreatePool`, `DestroyPool`, `ExportPool`, `FindImportablePools`, `ImportPool` | ✅ Geprüft & gehärtet |
| **VDEV- & Disk-Management** | `AddVdev` (Mirror, Stripe, Log/SLOG, Cache/L2ARC, Special, Hot Spare), `ReplaceDisk`, `AttachDisk`, `DetachDisk`, `OfflineDisk`, `OnlineDisk` | ✅ Geprüft & gehärtet |
| **Wartung & Selbstheilung** | `StartScrub`, `StopScrub`, `TrimPool`, `ClearPool`, `SetPoolProperty` (`autotrim`, `autoexpand`) | ✅ Geprüft & gehärtet |
| **Datasets & Zvols** | `ListDatasets`, `CreateDataset`, `DestroyDataset`, `SetDatasetProperty` (`compression`, `recordsize`, `quota`, `atime`, `readonly`), `CreateZvol` (Sparse/Thick, `volblocksize`) | ✅ Geprüft & gehärtet |
| **Snapshots & Clones** | `ListSnapshots`, `CreateSnapshot`, `RollbackSnapshot`, `CloneSnapshot`, `DestroySnapshot` | ✅ Geprüft & gehärtet |
| **Periodic Snapshot Tasks** | `ListSnapshotTasks`, `SaveSnapshotTask`, `ToggleSnapshotTask`, `RunSnapshotTask`, `DeleteSnapshotTask`, Retention-Pruning (`Keep for`), Cron-Scheduler & `/etc/nas/snapshot_tasks.json` | ✅ Implementiert & verifiziert |
| **Sicherheit & Validierung** | Schutz vor Command-Injection, Argument-Injection (`-`), reservierten Pool-Namen, Partition-Suffix-Auflösung (`-part1`, `nvme0n1p1`, `sda1`) | ✅ Geprüft & verifiziert |

---

## 2. Im Review gefundene & direkt behobene Fehler (Fixes)

### Fix 1: Snapshot- & Clone-Modal Namenskollision in `layout.html`
- **Ursache:** Im zentralen Alpine.js `x-data`-Objekt hießen die booleschen Zustandsvariablen `openCreateSnapModal: false` und `openCloneSnapModal: false` exakt genauso wie die Methoden `openCreateSnapModal(dataset)` und `openCloneSnapModal(snapshot)`. Nach dem ersten Öffnen und Schließen wurde die Funktion durch `false` überschrieben (`TypeError: openCreateSnapModal is not a function`).
- **Lösung:** Die Öffnungs-Methoden heißen nun sauber `openCreateSnap(dataset)` und `openCloneSnap(snapshot)` (analog zu `openCreateDataset(parent)`), während die Modal-Booleans `openCreateSnapModal` und `openCloneSnapModal` unverändert bleiben. Alle Aufrufe in `datasets_list.html` und `snapshots_list.html` wurden aktualisiert.

### Fix 2: Disk-Replace & Mirror-Attach Modal (`disk_action.html` & `ui_handlers.go`)
- **Ursache:**
  1. Das Formular nutzte `:hx-post="..."` (dynamisches Alpine-Binding). Da HTMX Attribute nur beim initialen Laden bindet, führte ein Submit zu einem Seiten-Reload statt zu einem HTMX-Request.
  2. Das Formular sendete `existing_device` und `old_device`, während `handleUIActionDiskAction` nur `target_device` und `device` auslas.
- **Lösung:** Statisches `hx-post="/ui/actions/zfs/pools/disk/action"` mit `<input type="hidden" name="action_type" :value="diskAction.type">` und `<input type="hidden" name="target_device" :value="diskAction.targetDevice">` hinterlegt; `handleUIActionDiskAction` akzeptiert zusätzlich `existing_device` und `old_device`.

### Fix 3: Einheitlicher Mountpoint `-m /mnt/<pool>` bei `zpool create` (`zfs.go`)
- **Ursache:** Auf Debian Linux mountet `zpool create <POOL>` einen neuen Pool standardmäßig unter `/<POOL>`, während die gesamte Share-Verwaltung (SMB/NFS) und Dataset-Pfade `/mnt/<POOL>` erwarten.
- **Lösung:** `CreatePool` übergibt nun explizit `-m /mnt/<name>` an `zpool create`, sodass alle neuen Pools auf Debian 13 automatisch sauber unter `/mnt/<POOL>` eingehängt werden.

### Fix 4: Schutz vor versehentlichem `zfs destroy` auf Root-Pool-Datasets (`zfs.go` & `datasets_list.html`)
- **Ursache:** In der Dataset-Baumansicht wurde der `Destroy`-Button auch auf der obersten Pool-Ebene (`$depth == 0`, z. B. `DATA`) eingeblendet. Ein Root-Dataset kann in OpenZFS nicht per `zfs destroy` gelöscht werden (dafür ist `zpool destroy` im Storage-Tab vorgesehen).
- **Lösung:**
  1. In `datasets_list.html` wird `Destroy` nur für Kind-Datasets/Zvols (`$depth > 0`) angezeigt; außerdem wurde ein direkter `+ Dataset`-Button pro Zeile ergänzt, der das Eltern-Dataset im Modal vorauswählt.
  2. `DestroyDataset` blockiert Aufrufe ohne `/` im Namen mit einer klaren Fehlermeldung.

### Fix 5: Quota-Normalisierung & Fehlerprüfung bei Dataset-/Pool-Properties (`zfs.go`, `validate.go`, `ui_handlers.go`)
- **Ursache:**
  1. `zfs list -p` liefert Quotas in Bytes. Wurde daraus `"500.00 GB"` mit Leerzeichen formatiert und im Properties-Modal erneut gespeichert, schlug `zfs set quota=...` fehl.
  2. `handleUIActionSetDatasetProperty` und `handleUIActionSetPoolProperty` verwarfen Fehler (`_ = s.zfs.SetDatasetProperty(...)`).
- **Lösung:**
  1. Neue Hilfsfunktionen `formatZfsQuota` und `normalizeZfsSize` wandeln Eingaben wie `"500 GiB"`, `"500 GB"` oder `"0"` automatisch in ZFS-konforme Werte (`"500G"`, `"none"`) um.
  2. `validate.go` prüft nun auch `readonly`, `quota`/`refquota` sowie `autotrim`/`autoexpand`.
  3. Fehler beim Setzen von Properties werden im UI als Toast-Fehlermeldung ausgegeben; bei Zvols (`volume`) werden Dateisystem-Properties (`recordsize`, `quota`, `atime`) übersprungen.

### Fix 6: Vollständige SMB- und NFS-Share-Bereinigung beim Löschen von Pools/Datasets (`ui_handlers.go`)
- **Ursache:** Bei aktivierter Option *„Automatically remove all associated SMB and NFS shares“* im Destroy-Bestätigungsdialog wurden bisher nur SMB-Shares gelöscht, nicht jedoch zugehörige NFS-Exports.
- **Lösung:** `handleUIActionDangerDestroy` bereinigt nun sowohl SMB-Shares als auch NFS-Exports für den betroffenen Pool bzw. das Dataset und seine Unterpfade.

### Fix 7: Erweiterte Pool-Erstellungsoptionen (`RAID-Z3`, `ashift`, `compression`) im UI (`create_pool.html`)
- **Ursache:** Das Backend unterstützte bereits `raidz3`, `ashift` (9–16) und `compression`, das Modal bot jedoch keine Auswahlfelder für `RAID-Z3`, `ashift` und `compression`.
- **Lösung:** Auswahlfelder für `RAID-Z3 (5+ Disks)`, `ashift` (`12` = 4 KiB Standard, `13` = 8 KiB Enterprise NVMe, `9` = 512B Legacy) und `Default Compression` (`lz4`, `zstd`, `gzip`, `off`) in `create_pool.html` ergänzt.

### Fix 8: `+ Create Snapshot` Button im Data Protection Tab & Toast-Event-Delivery (`layout.html`)
- **Ursache:** Der `+ Create Snapshot`-Button in `layout.html` rief noch die alte Boolean-Property `openCreateSnapModal('')` statt der Methode `openCreateSnap('')` auf. Zusätzlich sendet HTMX `showToast` als camelCase-Event, wofür `@show-toast.camel.window` nötig ist.
- **Lösung:** Button auf `openCreateSnap('')` umgestellt und `@show-toast.camel.window` auf `<body>` ergänzt.

### Fix 9: Snapshot-Erstellungs-Modal (`create_snap.html`) — Rekursive Snapshots, Live-Dropdown & Reset
- **Ursache:** Root-Pools waren im Dropdown doppelt vorhanden, neu erstellte Pools/Datasets fehlten vor einem Reload im Dropdown, der alte Snapshot-Name blieb beim Wiederöffnen stehen und rekursive Snapshots (`zfs snapshot -r`) fehlten.
- **Lösung:** Live-Partial `partials/snap_datasets_options.html` (`/ui/partials/modals/snap_datasets` auf `poolUpdate`/`datasetUpdate`), Ausfilterung doppelter Root-Pools, automatischer Feld-Reset beim Öffnen und Option **Recursive Snapshot (`-r`)** (`CreateSnapshotRecursive` in `zfs.go`) ergänzt.

### Fix 10: `CloneSnapshot` bei Zvols & Pool-/Pfad-Schutz (`zfs.go`, `clone_snap.html`)
- **Ursache:** `zfs clone -o atime=off` schlägt auf echtem OpenZFS bei Block-Volumes (`Zvol`) fehl, weil Volumes keine `atime`-Property unterstützen. Außerdem fehlten Vorabprüfungen für Pool-Präfix / Same-Pool-Clone sowie ein Default-Zielpfad im Modal.
- **Lösung:** Typ-Check vor `zfs clone` (`atime=off` nur für `filesystem`), Validierung auf `<pool>/<name>` im selben Pool und automatische Pfadvorschläge (`DATA/DATA-clone` bzw. `DATA/Backup-clone`) in `openCloneSnap()`.

### Fix 11: Mock-Lifecycle & `snapshots_list.html` UI (`zfs.go`, `snapshots_list.html`, `web.go`)
- **Lösung:** Duplikat-Schutz bei `CreateSnapshotRecursive`, Clone-`Origin`-Schutz bei `DestroySnapshot`, Entfernen neuerer Snapshots bei `RollbackSnapshot` (analog `zfs rollback -r`), `Created`-Spalte via `formatTime`, Live-Filterleiste in der Snapshot-Tabelle sowie `urlquery` beim `Destroy`-Link ergänzt.

### Feature 12: Periodic Snapshot Tasks (`snapshot_tasks.go`, `snapshot_tasks.html`, `create_snapshot_task.html`)
- **Umsetzung:** Vollständige TrueNAS-SCALE-Kachel **Periodic Snapshot Tasks** unter *Data Protection* mit Seed-Einträgen (`DATA`, `MEDIA-12TB`, `SSD-MIRROR/mail-archiver`), *Keep for*-Retention (`hour(s)`, `day(s)`, `week(s)`, `month(s)`, `year(s)`), Cron-Scheduler (inkl. manuellem *Run Now* + automatischer Bereinigung abgelaufener Snapshots nach Naming-Schema), *Enabled*-Toggle-Switch, Status-Pill (`Completed`/`Running`/`Failed`/`Idle`), *Add/Edit*-Modal sowie JSON-Persistenz unter `/etc/nas/snapshot_tasks.json`.

---

## 3. Testergebnisse (`go test -v -count=1 ./...`)

- `debian-nas/internal/zfs`: **PASS** (inkl. Periodic Snapshot Tasks CRUD, Validierung, JSON-Persistenz, *Run Now* + Retention-Pruning sowie Rekursiv-Snapshot, Duplikat-Schutz, Zvol-Clone, Cross-Pool-Schutz, Clone-Origin-Schutz, Rollback)
- `debian-nas/internal/api`: **PASS** (`TestSnapshotTasks_UIAndAPIRoutes`, `TestUI_RootAndPartials`, `TestUI_Actions`)
- `debian-nas/internal/web`: **PASS** (`TestRender_PeriodicSnapshotTasksPartialAndModal`, `TestEngine_RenderAll`)
- `debian-nas/internal/validate`, `disks`, `shares`: **PASS**


