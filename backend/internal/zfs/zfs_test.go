package zfs

import (
	"strings"
	"testing"
	"time"
)

func TestParseZpoolStatus_ScrubCompleted(t *testing.T) {
	output := `  pool: TANK
 state: ONLINE
  scan: scrub repaired 0B in 04:12:33 with 0 errors on Sun Oct 15 01:31:42 2026
config:

	NAME                                                 STATE     READ WRITE CKSUM
	TANK                                                 ONLINE       0     0     0
	  mirror-0                                           ONLINE       0     0     0
	    /dev/disk/by-id/ata-WDC_WD20EFAX_WD-123          ONLINE       0     0     0
	    /dev/disk/by-id/ata-WDC_WD20EFAX_WD-124          ONLINE       0     0     0

errors: No known data errors
`
	vdevs, scrub := parseZpoolStatus(strings.NewReader(output), "TANK")

	if len(vdevs) != 3 {
		t.Fatalf("expected 3 vdev entries, got %d", len(vdevs))
	}

	if vdevs[0].Type != "mirror" || vdevs[0].Name != "mirror-0" {
		t.Errorf("unexpected mirror vdev: %+v", vdevs[0])
	}

	if vdevs[1].Type != "disk" || vdevs[1].Name != "ata-WDC_WD20EFAX_WD-123" || vdevs[1].Path != "/dev/disk/by-id/ata-WDC_WD20EFAX_WD-123" {
		t.Errorf("unexpected disk 1 vdev: %+v", vdevs[1])
	}

	if scrub.Active {
		t.Errorf("expected scrub inactive, got active")
	}
	if scrub.Percentage != 100.0 {
		t.Errorf("expected 100%% scrub, got %f", scrub.Percentage)
	}
	if scrub.Errors != 0 {
		t.Errorf("expected 0 scrub errors, got %d", scrub.Errors)
	}
	expectedDate := time.Date(2026, 10, 15, 1, 31, 42, 0, time.UTC)
	if scrub.LastRun.Day() != expectedDate.Day() || scrub.LastRun.Month() != expectedDate.Month() || scrub.LastRun.Year() != expectedDate.Year() {
		t.Errorf("expected scrub date %v, got %v", expectedDate, scrub.LastRun)
	}
}

func TestParseZpoolStatus_ScrubInProgress(t *testing.T) {
	output := `  pool: BACKUP
 state: ONLINE
  scan: scrub in progress since Sun Nov  8 14:02:10 2026
	12.4G scanned at 1.38G/s, 2.10G issued at 238M/s, 84.5G total
	0B repaired, 24.8% done, 00:05:32 to go
config:

	NAME                        STATE     READ WRITE CKSUM
	BACKUP                      ONLINE       0     0     0
	  raidz1-0                  ONLINE       0     0     0
	    /dev/sda                ONLINE       0     0     0
	    /dev/sdb                ONLINE       0     0     0
	    /dev/sdc                ONLINE       0     0     0

errors: No known data errors
`
	vdevs, scrub := parseZpoolStatus(strings.NewReader(output), "BACKUP")

	if len(vdevs) != 4 {
		t.Fatalf("expected 4 vdev entries, got %d", len(vdevs))
	}
	if vdevs[0].Type != "raidz1-0" {
		t.Errorf("expected raidz1-0 type, got %s", vdevs[0].Type)
	}
	if !scrub.Active {
		t.Errorf("expected scrub active, got inactive")
	}
	if scrub.Percentage < 24.7 || scrub.Percentage > 24.9 {
		t.Errorf("expected ~24.8%% progress, got %f", scrub.Percentage)
	}
}

func TestParseZpoolStatus_ErrorsAndNoneRequested(t *testing.T) {
	outputErrors := `  pool: TANK
 state: DEGRADED
  scan: scrub repaired 4K in 00:01:20 with 3 errors on Sun Oct 15 01:31:42 2026
config:
	NAME        STATE     READ WRITE CKSUM
	TANK        DEGRADED     0     0     0
	  sda       DEGRADED     0     0     3
`
	_, scrubErr := parseZpoolStatus(strings.NewReader(outputErrors), "TANK")
	if scrubErr.Errors != 3 {
		t.Errorf("expected 3 scrub errors, got %d", scrubErr.Errors)
	}

	outputNone := `  pool: FRESH
 state: ONLINE
  scan: none requested
config:
	NAME        STATE     READ WRITE CKSUM
	FRESH       ONLINE       0     0     0
	  sda       ONLINE       0     0     0
`
	_, scrubNone := parseZpoolStatus(strings.NewReader(outputNone), "FRESH")
	if scrubNone.Active {
		t.Errorf("expected inactive scrub")
	}
	if !scrubNone.LastRun.IsZero() {
		t.Errorf("expected zero LastRun for none requested, got %v", scrubNone.LastRun)
	}
}

func TestZfsManager_ValidationEnforcement(t *testing.T) {
	m := NewManager(true)

	// Pool creation validations
	if err := m.CreatePool("; rm -rf /", "mirror", []string{"sda", "sdb"}, 12, "lz4"); err == nil {
		t.Error("expected invalid pool name to fail")
	}
	if err := m.CreatePool("tank", "mirror", []string{"sda"}, 12, "lz4"); err == nil {
		t.Error("expected mirror with 1 disk to fail")
	}
	if err := m.CreatePool("tank", "raidz1", []string{"sda", "sdb"}, 12, "lz4"); err == nil {
		t.Error("expected raidz1 with 2 disks to fail")
	}
	if err := m.CreatePool("tank", "mirror", []string{"sda", "sdb"}, 12, "invalidcomp"); err == nil {
		t.Error("expected invalid compression to fail")
	}

	// Dataset validations
	if err := m.CreateDataset("../escape", "lz4", "128K"); err == nil {
		t.Error("expected traversal dataset to fail")
	}
	if err := m.CreateDataset("tank/test", "lz4", "999K"); err == nil {
		t.Error("expected invalid recordsize to fail")
	}

	// Snapshot validations
	if err := m.CreateSnapshot("tank/test", "../evil"); err == nil {
		t.Error("expected invalid snapshot name to fail")
	}
	if err := m.RollbackSnapshot("tank/test-no-at"); err == nil {
		t.Error("expected rollback without @ to fail")
	}
}

func TestZfsManager_FullLifecycle(t *testing.T) {
	m := NewManager(true)

	// 1. Offline a disk in DATA -> pool health must become DEGRADED
	if err := m.OfflineDisk("DATA", "sdb"); err != nil {
		t.Fatalf("OfflineDisk failed: %v", err)
	}
	pools, err := m.ListPools()
	if err != nil {
		t.Fatalf("ListPools failed: %v", err)
	}
	var dataPool *Pool
	for _, p := range pools {
		if p.Name == "DATA" {
			dataPool = p
			break
		}
	}
	if dataPool == nil {
		t.Fatal("expected DATA pool to exist")
	}
	if dataPool.Health != HealthDegraded {
		t.Errorf("expected DATA health DEGRADED after offlining sdb, got %s", dataPool.Health)
	}

	// 2. Bring sdb back ONLINE -> pool health must return to ONLINE
	if err := m.OnlineDisk("DATA", "sdb"); err != nil {
		t.Fatalf("OnlineDisk failed: %v", err)
	}
	pools, _ = m.ListPools()
	for _, p := range pools {
		if p.Name == "DATA" {
			dataPool = p
			break
		}
	}
	if dataPool.Health != HealthOnline {
		t.Errorf("expected DATA health ONLINE after onlining sdb, got %s", dataPool.Health)
	}

	// 3. Attach sdd to sdb, Replace sdd with sde, Detach sde
	if err := m.AttachDisk("DATA", "sdb", "sdd"); err != nil {
		t.Fatalf("AttachDisk failed: %v", err)
	}
	if err := m.ReplaceDisk("DATA", "sdd", "sde"); err != nil {
		t.Fatalf("ReplaceDisk failed: %v", err)
	}
	if err := m.DetachDisk("DATA", "sde"); err != nil {
		t.Fatalf("DetachDisk failed: %v", err)
	}

	// 4. Add VDEV (Hot Spare) and update Pool Properties
	if err := m.AddVdev("DATA", "spare", []string{"sdf"}); err != nil {
		t.Fatalf("AddVdev failed: %v", err)
	}
	if err := m.SetPoolProperty("DATA", "autotrim", "on"); err != nil {
		t.Fatalf("SetPoolProperty autotrim failed: %v", err)
	}
	if err := m.SetPoolProperty("DATA", "autoexpand", "off"); err != nil {
		t.Fatalf("SetPoolProperty autoexpand failed: %v", err)
	}

	poolMap, roleMap := m.GetDiskPoolMap()
	if poolMap["sdf"] != "DATA" || roleMap["sdf"] != "spare" {
		t.Errorf("expected sdf mapped to DATA/spare, got %s/%s", poolMap["sdf"], roleMap["sdf"])
	}

	// 5. Dataset & Zvol lifecycle + properties + snapshots + clone
	if err := m.CreateDataset("DATA/vms", "zstd", "64K"); err != nil {
		t.Fatalf("CreateDataset failed: %v", err)
	}
	if err := m.SetDatasetProperty("DATA/vms", "quota", "500G"); err != nil {
		t.Fatalf("SetDatasetProperty quota failed: %v", err)
	}
	if err := m.SetDatasetProperty("DATA/vms", "readonly", "on"); err != nil {
		t.Fatalf("SetDatasetProperty readonly failed: %v", err)
	}
	if err := m.CreateZvol("DATA/vm-100-disk", 10*1024*1024*1024, "16k", true); err != nil {
		t.Fatalf("CreateZvol failed: %v", err)
	}
	if err := m.CreateSnapshot("DATA/vms", "snap1"); err != nil {
		t.Fatalf("CreateSnapshot failed: %v", err)
	}
	if err := m.CreateSnapshot("DATA/vms", "snap1"); err == nil {
		t.Errorf("expected duplicate snapshot creation to fail")
	}
	if err := m.CloneSnapshot("DATA/vms@snap1", "DATA/vms-clone"); err != nil {
		t.Fatalf("CloneSnapshot failed: %v", err)
	}
	if err := m.CloneSnapshot("DATA/vms@snap1", "onlyname"); err == nil {
		t.Errorf("expected clone without pool prefix to fail")
	}
	if err := m.CloneSnapshot("DATA/vms@snap1", "SSD-MIRROR/cross-pool"); err == nil {
		t.Errorf("expected cross-pool clone to fail")
	}
	if err := m.DestroySnapshot("DATA/vms@snap1"); err == nil {
		t.Errorf("expected destroying snapshot with dependent clone to fail")
	}
	// Zvol snapshot & clone preserves volume type
	if err := m.CreateSnapshot("DATA/vm-100-disk", "zsnap1"); err != nil {
		t.Fatalf("CreateSnapshot on zvol failed: %v", err)
	}
	if err := m.CloneSnapshot("DATA/vm-100-disk@zsnap1", "DATA/vm-100-clone"); err != nil {
		t.Fatalf("CloneSnapshot on zvol failed: %v", err)
	}
	dsAfterClone, _ := m.ListDatasets()
	for _, ds := range dsAfterClone {
		if ds.Name == "DATA/vm-100-clone" && ds.Type != "volume" {
			t.Errorf("expected zvol clone to keep Type='volume', got %q", ds.Type)
		}
	}
	// Recursive snapshot & rollback removing newer snapshots
	if err := m.CreateSnapshotRecursive("DATA/Fileshare", "rec1", true); err != nil {
		t.Fatalf("CreateSnapshotRecursive failed: %v", err)
	}
	time.Sleep(2 * time.Millisecond)
	if err := m.CreateSnapshot("DATA/Fileshare", "rec2"); err != nil {
		t.Fatalf("CreateSnapshot rec2 failed: %v", err)
	}
	if err := m.RollbackSnapshot("DATA/Fileshare@rec1"); err != nil {
		t.Fatalf("RollbackSnapshot failed: %v", err)
	}
	snaps, err := m.ListSnapshots()
	if err != nil {
		t.Fatalf("ListSnapshots failed: %v", err)
	}
	foundSnap := false
	foundChildRec := false
	for _, s := range snaps {
		if s.Name == "DATA/vms@snap1" {
			foundSnap = true
			if s.ReferencedBytes == 0 {
				t.Errorf("expected non-zero ReferencedBytes on snapshot")
			}
		}
		if s.Name == "DATA/Fileshare/Milena@rec1" {
			foundChildRec = true
		}
		if s.Name == "DATA/Fileshare@rec2" {
			t.Errorf("expected newer snapshot DATA/Fileshare@rec2 to be removed on rollback")
		}
	}
	if !foundSnap {
		t.Errorf("expected snapshot DATA/vms@snap1 in ListSnapshots")
	}
	if !foundChildRec {
		t.Errorf("expected recursive child snapshot DATA/Fileshare/Milena@rec1")
	}

	// 6. Export and re-import pool (verifying dataset preservation)
	if err := m.ExportPool("MEDIA-12TB"); err != nil {
		t.Fatalf("ExportPool failed: %v", err)
	}
	importable, err := m.FindImportablePools()
	if err != nil || len(importable) == 0 {
		t.Fatalf("expected importable pools after export, got %d (%v)", len(importable), err)
	}
	if err := m.ImportPool("MEDIA-12TB", "", false); err != nil {
		t.Fatalf("ImportPool failed: %v", err)
	}
	dsAfterImport, _ := m.ListDatasets()
	foundMediaData := false
	for _, ds := range dsAfterImport {
		if ds.Name == "MEDIA-12TB/data" {
			foundMediaData = true
		}
	}
	if !foundMediaData {
		t.Errorf("expected child dataset MEDIA-12TB/data to be restored after ImportPool")
	}

	// 7. Prevent destroying root pool dataset directly via DestroyDataset
	if err := m.DestroyDataset("DATA"); err == nil {
		t.Errorf("expected DestroyDataset('DATA') on root pool to fail")
	}

	// 8. Quota normalization ("500 GiB" -> "500G", "0" -> "none")
	if err := m.SetDatasetProperty("DATA/Backup", "quota", "500 GiB"); err != nil {
		t.Fatalf("SetDatasetProperty quota '500 GiB' failed: %v", err)
	}
	if err := m.SetDatasetProperty("DATA/Backup", "quota", "0"); err != nil {
		t.Fatalf("SetDatasetProperty quota '0' failed: %v", err)
	}
}

func TestStripZfsPartition_DebianLinux(t *testing.T) {
	cases := map[string]string{
		"sda1":                                    "sda",
		"sda9":                                    "sda",
		"sda":                                     "sda",
		"/dev/sdb1":                               "/dev/sdb",
		"nvme0n1p1":                               "nvme0n1",
		"/dev/nvme0n1p9":                          "/dev/nvme0n1",
		"ata-WDC_WD160EDGZ-11B2DA0_2LG9X1AA-part1": "ata-WDC_WD160EDGZ-11B2DA0_2LG9X1AA",
		"/dev/disk/by-id/ata-WDC_WD160EDGZ-part1":  "/dev/disk/by-id/ata-WDC_WD160EDGZ",
	}
	for input, expected := range cases {
		got := stripZfsPartition(input)
		if got != expected {
			t.Errorf("stripZfsPartition(%q) = %q, want %q", input, got, expected)
		}
	}
}



