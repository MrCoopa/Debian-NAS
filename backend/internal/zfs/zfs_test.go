package zfs

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
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

func TestExecuteZfsRollback_BusyMountsAndRemount(t *testing.T) {
	t.Run("fast path succeeds without touching mounts", func(t *testing.T) {
		var cmds []string
		releaseCalls := 0
		runner := rollbackRunner{
			run: func(timeout time.Duration, name string, args ...string) ([]byte, error) {
				cmds = append(cmds, name+" "+strings.Join(args, " "))
				return nil, nil
			},
			collectMounts: func(target string) []zfsMountEntry {
				t.Fatalf("collectMounts should not be called on fast path")
				return nil
			},
			releaseMounts: func(target string, forceKill bool) {
				releaseCalls++
			},
		}

		if err := executeZfsRollback(runner, "Any/data@s1", "Any/data"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if releaseCalls != 0 {
			t.Fatalf("expected 0 releaseMounts calls, got %d", releaseCalls)
		}
		want := []string{"zfs rollback -r Any/data@s1"}
		if !reflect.DeepEqual(cmds, want) {
			t.Fatalf("cmds = %v, want %v", cmds, want)
		}
	})

	t.Run("graceful unmount recovers busy child mount and remounts parent-first", func(t *testing.T) {
		var events []string
		rollbackAttempt := 0
		runner := rollbackRunner{
			run: func(timeout time.Duration, name string, args ...string) ([]byte, error) {
				cmdStr := name + " " + strings.Join(args, " ")
				events = append(events, cmdStr)
				if name == "zfs" && len(args) >= 1 && args[0] == "rollback" {
					rollbackAttempt++
					if rollbackAttempt == 1 {
						return []byte("cannot unmount '/mnt/Any': pool or dataset is busy"), fmt.Errorf("exit status 1")
					}
					return nil, nil
				}
				if name == "zfs" && len(args) >= 2 && args[0] == "mount" && args[1] == "Any" {
					// Simulate parent already auto-mounted while child still needs zfs mount.
					return []byte("filesystem 'Any' is already mounted"), fmt.Errorf("exit status 1")
				}
				return nil, nil
			},
			collectMounts: func(target string) []zfsMountEntry {
				return []zfsMountEntry{
					{name: "Any/sub", mountpoint: "/mnt/Any/sub", mounted: true},
					{name: "Any/zvol1", mountpoint: "-", mounted: false},
					{name: "Any", mountpoint: "/mnt/Any", mounted: true},
				}
			},
			releaseMounts: func(target string, forceKill bool) {
				events = append(events, fmt.Sprintf("release:%s:force=%v", target, forceKill))
			},
		}

		if err := executeZfsRollback(runner, "Any@s1", "Any"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := []string{
			"zfs rollback -r Any@s1",
			"release:Any:force=false",
			"zfs rollback -r Any@s1",
			"zfs mount Any",
			"zfs mount Any/sub",
		}
		if !reflect.DeepEqual(events, want) {
			t.Fatalf("events = %#v, want %#v", events, want)
		}
	})

	t.Run("force recovery unexports matching NFS shares kills busy handles and restores exports", func(t *testing.T) {
		var events []string
		rollbackAttempt := 0
		runner := rollbackRunner{
			run: func(timeout time.Duration, name string, args ...string) ([]byte, error) {
				cmdStr := name + " " + strings.Join(args, " ")
				events = append(events, cmdStr)
				if name == "zfs" && len(args) >= 1 && args[0] == "rollback" {
					rollbackAttempt++
					if rollbackAttempt <= 2 {
						return []byte("cannot unmount '/mnt/Any/share': pool or dataset is busy"), fmt.Errorf("exit status 1")
					}
					return nil, nil
				}
				if name == "exportfs" && len(args) == 1 && args[0] == "-s" {
					return []byte("/mnt/Any/share  <world>(rw,sync)\n/mnt/Any/share/sub  192.168.2.0/24(rw,sync)\n/mnt/OtherPool  *(rw,sync)\n"), nil
				}
				return nil, nil
			},
			collectMounts: func(target string) []zfsMountEntry {
				return []zfsMountEntry{
					{name: "Any/share", mountpoint: "/mnt/Any/share", mounted: true},
				}
			},
			releaseMounts: func(target string, forceKill bool) {
				events = append(events, fmt.Sprintf("release:%s:force=%v", target, forceKill))
			},
		}

		if err := executeZfsRollback(runner, "Any/share@s1", "Any/share"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := []string{
			"zfs rollback -r Any/share@s1",
			"release:Any/share:force=false",
			"zfs rollback -r Any/share@s1",
			"exportfs -s",
			"exportfs -u *:/mnt/Any/share",
			"exportfs -u 192.168.2.0/24:/mnt/Any/share/sub",
			"release:Any/share:force=true",
			"zfs rollback -r Any/share@s1",
			"zfs mount Any/share",
			"exportfs -ra",
		}
		if !reflect.DeepEqual(events, want) {
			t.Fatalf("events = %#v, want %#v", events, want)
		}
	})

	t.Run("dependent clones preserve clear error without unmounting", func(t *testing.T) {
		releaseCalls := 0
		runner := rollbackRunner{
			run: func(timeout time.Duration, name string, args ...string) ([]byte, error) {
				return []byte("cannot rollback to 'Any@s1': clones of previous snapshots exist\nuse '-R' to force deletion of all clones"), fmt.Errorf("exit status 1")
			},
			collectMounts: func(target string) []zfsMountEntry {
				t.Fatalf("collectMounts should not run on clone error")
				return nil
			},
			releaseMounts: func(target string, forceKill bool) {
				releaseCalls++
			},
		}

		err := executeZfsRollback(runner, "Any@s1", "Any")
		if err == nil || !strings.Contains(err.Error(), "dependent clones") {
			t.Fatalf("expected dependent clones error, got: %v", err)
		}
		if releaseCalls != 0 {
			t.Fatalf("expected 0 releaseMounts calls, got %d", releaseCalls)
		}
	})

	t.Run("remounts and restores NFS exports even if forced rollback still fails", func(t *testing.T) {
		var events []string
		runner := rollbackRunner{
			run: func(timeout time.Duration, name string, args ...string) ([]byte, error) {
				events = append(events, name+" "+strings.Join(args, " "))
				if name == "zfs" && len(args) >= 1 && args[0] == "rollback" {
					return []byte("cannot unmount '/mnt/Any': pool or dataset is busy"), fmt.Errorf("exit status 1")
				}
				if name == "exportfs" && len(args) == 1 && args[0] == "-s" {
					return []byte("/mnt/Any  *(rw,sync)\n"), nil
				}
				return nil, nil
			},
			collectMounts: func(target string) []zfsMountEntry {
				return []zfsMountEntry{{name: "Any", mountpoint: "/mnt/Any", mounted: true}}
			},
			releaseMounts: func(target string, forceKill bool) {
				events = append(events, fmt.Sprintf("release:%s:force=%v", target, forceKill))
			},
		}

		err := executeZfsRollback(runner, "Any@s1", "Any")
		if err == nil {
			t.Fatalf("expected error when rollback fails all retries")
		}
		want := []string{
			"zfs rollback -r Any@s1",
			"release:Any:force=false",
			"zfs rollback -r Any@s1",
			"exportfs -s",
			"exportfs -u *:/mnt/Any",
			"release:Any:force=true",
			"zfs rollback -r Any@s1",
			"zfs mount Any",
			"exportfs -ra",
		}
		if !reflect.DeepEqual(events, want) {
			t.Fatalf("events = %#v, want %#v", events, want)
		}
	})
}

func TestEnsureDatasetTreeMounted_ParentFirstAndCanmount(t *testing.T) {
	var mountedCmds []string
	collect := func(target string) []zfsMountEntry {
		return []zfsMountEntry{
			{name: "Docker/Share/deep", mountpoint: "/mnt/Docker/Share/deep", mounted: false, canmount: true},
			{name: "Docker/Share", mountpoint: "/mnt/Docker/Share", mounted: true, canmount: true},
			{name: "Docker/NoAuto", mountpoint: "/mnt/Docker/NoAuto", mounted: false, canmount: false},
			{name: "Docker", mountpoint: "/mnt/Docker", mounted: false, canmount: true},
		}
	}
	run := func(timeout time.Duration, name string, args ...string) ([]byte, error) {
		mountedCmds = append(mountedCmds, name+" "+strings.Join(args, " "))
		return nil, nil
	}

	if err := ensureDatasetTreeMounted(run, collect, "Docker"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{
		"zfs mount Docker",
		"zfs mount Docker/Share/deep",
	}
	if !reflect.DeepEqual(mountedCmds, want) {
		t.Fatalf("mountedCmds = %#v, want %#v", mountedCmds, want)
	}

	// Verify real mount error (e.g. non-empty directory) propagates cleanly while ignoring "already mounted".
	errRun := func(timeout time.Duration, name string, args ...string) ([]byte, error) {
		if len(args) >= 2 && args[1] == "Docker" {
			return []byte("filesystem 'Docker' is already mounted"), fmt.Errorf("exit status 1")
		}
		return []byte("cannot mount '/mnt/Docker/Share/deep': directory is not empty"), fmt.Errorf("exit status 1")
	}
	err := ensureDatasetTreeMounted(errRun, collect, "Docker")
	if err == nil || !strings.Contains(err.Error(), "directory is not empty") {
		t.Fatalf("expected non-empty directory mount error, got: %v", err)
	}
}

func TestBuildRollbackPlan_RecursiveAndSkippedChildren(t *testing.T) {
	allSnaps := []*Snapshot{
		{Name: "Docker@s1", Dataset: "Docker"},
		{Name: "Docker/Share@s1", Dataset: "Docker/Share"},
		{Name: "Docker/Share/Sub@s1", Dataset: "Docker/Share/Sub"},
	}
	children := []string{"Docker/Share", "Docker/Share/Sub", "Docker/Extra"}

	// Recursive plan rolls back deepest children first, then parent, and reports missing tag on Docker/Extra.
	targetsRec, skippedRec := buildRollbackPlan("Docker", "s1", children, allSnaps, true)
	wantTargetsRec := []string{"Docker/Share/Sub@s1", "Docker/Share@s1", "Docker@s1"}
	if !reflect.DeepEqual(targetsRec, wantTargetsRec) {
		t.Fatalf("recursive targets = %#v, want %#v", targetsRec, wantTargetsRec)
	}
	wantSkippedRec := []string{"Docker/Extra"}
	if !reflect.DeepEqual(skippedRec, wantSkippedRec) {
		t.Fatalf("recursive skipped = %#v, want %#v", skippedRec, wantSkippedRec)
	}

	// Non-recursive rolls back only target snapshot and reports all descendants as unchanged.
	targetsSingle, skippedSingle := buildRollbackPlan("Docker", "s1", children, allSnaps, false)
	if !reflect.DeepEqual(targetsSingle, []string{"Docker@s1"}) {
		t.Fatalf("single targets = %#v, want [Docker@s1]", targetsSingle)
	}
	wantSkippedSingle := []string{"Docker/Extra", "Docker/Share", "Docker/Share/Sub"}
	if !reflect.DeepEqual(skippedSingle, wantSkippedSingle) {
		t.Fatalf("single skipped = %#v, want %#v", skippedSingle, wantSkippedSingle)
	}
}

func TestParseSambaSharePaths_AndProcMountHolders(t *testing.T) {
	smbConf := `
[global]
   workgroup = WORKGROUP

[homes]
   path = /home/%S

[DockerShare]
   path = /mnt/Docker/Share/
   read only = no

[Backup]
   path = /mnt/DATA/Backup
`
	got := parseSambaSharePaths(smbConf)
	want := map[string]string{
		"DockerShare": "/mnt/Docker/Share",
		"Backup":      "/mnt/DATA/Backup",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseSambaSharePaths = %#v, want %#v", got, want)
	}

	// Test fake /proc layout cross-platform via injected readlink reader.
	procDir := t.TempDir()
	for _, rel := range []string{"1/fd", "42/fd", "105/fd", "200/fd", "999/fd"} {
		if err := os.MkdirAll(filepath.Join(procDir, rel), 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(procDir, "105/fd/3"), nil, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(procDir, "200/fd/7"), nil, 0644); err != nil {
		t.Fatal(err)
	}

	links := map[string]string{
		filepath.Join(procDir, "42", "cwd"):       "/mnt/Docker/Share/subdir",
		filepath.Join(procDir, "105", "fd", "3"):  "/mnt/Docker/Share/file.txt",
		filepath.Join(procDir, "200", "fd", "7"):  "/var/log/syslog",
		filepath.Join(procDir, "999", "cwd"):      "/mnt/Docker/Share",
	}
	pids := findMountHoldingPIDsWithReader(procDir, "/mnt/Docker/Share", 999, func(p string) (string, error) {
		if v, ok := links[p]; ok {
			return v, nil
		}
		return "", os.ErrNotExist
	})
	if !reflect.DeepEqual(pids, []int{42, 105}) {
		t.Fatalf("findMountHoldingPIDsWithReader = %v, want [42 105]", pids)
	}
	if nonMnt := findMountHoldingPIDsWithReader(procDir, "/etc", 999, os.Readlink); nonMnt != nil {
		t.Fatalf("expected non-/mnt path scan to refuse and return nil, got %v", nonMnt)
	}
}

func TestRollbackSnapshotWithOptions_RecursiveMock(t *testing.T) {
	m := NewManager(true)

	// Create recursive snapshot on DATA (which has DATA/Fileshare, DATA/Fileshare/Milena, etc.)
	if err := m.CreateSnapshotRecursive("DATA", "tree-snap", true); err != nil {
		t.Fatalf("CreateSnapshotRecursive failed: %v", err)
	}
	if err := m.CreateSnapshotRecursive("DATA", "newer-tree-snap", true); err != nil {
		t.Fatalf("second CreateSnapshotRecursive failed: %v", err)
	}

	snaps, err := m.ListSnapshots()
	if err != nil {
		t.Fatalf("ListSnapshots failed: %v", err)
	}
	var rootDescCount int
	for _, s := range snaps {
		if s.Name == "DATA@tree-snap" {
			rootDescCount = s.DescendantCount
		}
	}
	if rootDescCount == 0 {
		t.Fatalf("expected DescendantCount > 0 on recursive DATA@tree-snap")
	}

	rolledBack, _, err := m.RollbackSnapshotWithOptions("DATA@tree-snap", true)
	if err != nil {
		t.Fatalf("recursive RollbackSnapshotWithOptions failed: %v", err)
	}
	if len(rolledBack) < 2 {
		t.Fatalf("expected parent and child snapshots to roll back, got %v", rolledBack)
	}

	// Newer snapshots on both parent and child should be destroyed by recursive -r rollback.
	afterSnaps, _ := m.ListSnapshots()
	for _, s := range afterSnaps {
		if strings.HasSuffix(s.Name, "@newer-tree-snap") {
			t.Fatalf("expected newer snapshot %q to be destroyed after recursive rollback", s.Name)
		}
	}
}

func TestPlanImportMountpointUpdates(t *testing.T) {
	t.Run("TrueNAS_To_Mnt", func(t *testing.T) {
		raw := strings.Join([]string{
			"tank\t/tank\tlocal",
			"tank/media\t/tank/media\tinherited from tank",
			"tank/apps\t/tank/apps\tlocal",
			"tank/ix-apps\tnone\tlocal",
			"tank/legacyfs\tlegacy\tlocal",
			"tank/ok\t/mnt/tank/ok\tlocal",
			"otherpool/ds\t/otherpool/ds\tlocal",
		}, "\n")
		got := planImportMountpointUpdates("tank", "", parseZfsMountpointProps([]byte(raw)))
		want := []zfsMountpointUpdate{
			{dataset: "tank", mountpoint: "/mnt/tank"},
			{dataset: "tank/apps", mountpoint: "/mnt/tank/apps"},
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("updates = %#v, want %#v", got, want)
		}
	})

	t.Run("Native_And_Dangerous_Paths", func(t *testing.T) {
		raw := strings.Join([]string{
			"DATA\t/mnt/DATA\tlocal",
			"DATA/Share\t/mnt/DATA/Share\tinherited from DATA",
			"DATA/Srv\t/srv/DATA/Srv\tlocal",
			"DATA/RootBug\t/\tlocal",
			"DATA/MntRoot\t/mnt\tlocal",
			"DATA/EtcBug\t/etc/custom\treceived",
		}, "\n")
		got := planImportMountpointUpdates("DATA", "", parseZfsMountpointProps([]byte(raw)))
		want := []zfsMountpointUpdate{
			{dataset: "DATA/RootBug", mountpoint: "/mnt/DATA/RootBug"},
			{dataset: "DATA/MntRoot", mountpoint: "/mnt/DATA/MntRoot"},
			{dataset: "DATA/EtcBug", mountpoint: "/mnt/DATA/EtcBug"},
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("updates = %#v, want %#v", got, want)
		}
	})

	t.Run("Renamed_Import_Relocates_Old_Mnt", func(t *testing.T) {
		raw := strings.Join([]string{
			"archiv\t/mnt/tank\tlocal",
			"archiv/docs\t/mnt/tank/docs\tinherited from archiv",
			"archiv/custom\t/mnt/tank/custom\tlocal",
		}, "\n")
		got := planImportMountpointUpdates("archiv", "tank", parseZfsMountpointProps([]byte(raw)))
		want := []zfsMountpointUpdate{
			{dataset: "archiv", mountpoint: "/mnt/archiv"},
			{dataset: "archiv/custom", mountpoint: "/mnt/archiv/custom"},
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("updates = %#v, want %#v", got, want)
		}
	})
}

func TestExecuteZfsPoolImport_NormalizesAndMounts(t *testing.T) {
	t.Run("TrueNAS_Pool_Import_Uses_NoMount_Normalizes_Then_Mounts", func(t *testing.T) {
		var cmds []string
		run := func(timeout time.Duration, name string, args ...string) ([]byte, error) {
			cmdLine := name + " " + strings.Join(args, " ")
			cmds = append(cmds, cmdLine)
			if name == "zfs" && len(args) > 0 && args[0] == "get" {
				return []byte("tank\t/tank\tlocal\ntank/media\t/tank/media\tinherited from tank\ntank/apps\t/tank/apps\tlocal\n"), nil
			}
			return nil, nil
		}
		collect := func(target string) []zfsMountEntry {
			return []zfsMountEntry{
				{name: "tank/media", mountpoint: "/mnt/tank/media", mounted: false, canmount: true},
				{name: "tank/apps", mountpoint: "/mnt/tank/apps", mounted: false, canmount: true},
				{name: "tank", mountpoint: "/mnt/tank", mounted: false, canmount: true},
			}
		}

		if err := executeZfsPoolImport(run, collect, "tank", "", true); err != nil {
			t.Fatalf("executeZfsPoolImport failed: %v", err)
		}
		want := []string{
			"zpool import -N -f tank",
			"zfs get -H -t filesystem -o name,value,source -r mountpoint tank",
			"zfs set mountpoint=/mnt/tank tank",
			"zfs set mountpoint=/mnt/tank/apps tank/apps",
			"zfs mount tank",
			"zfs mount tank/apps",
			"zfs mount tank/media",
		}
		if !reflect.DeepEqual(cmds, want) {
			t.Fatalf("cmds = %#v, want %#v", cmds, want)
		}
	})

	t.Run("Import_By_Numeric_GUID_Resolves_Name_And_Normalizes", func(t *testing.T) {
		var cmds []string
		guid := "1483920193849182741"
		run := func(timeout time.Duration, name string, args ...string) ([]byte, error) {
			cmds = append(cmds, name+" "+strings.Join(args, " "))
			if name == "zpool" && len(args) > 0 && args[0] == "list" {
				return []byte("other\t999999999\ntruenas\t" + guid + "\n"), nil
			}
			if name == "zfs" && len(args) > 0 && args[0] == "get" {
				return []byte("truenas\t/truenas\tlocal\n"), nil
			}
			return nil, nil
		}
		collect := func(target string) []zfsMountEntry {
			return []zfsMountEntry{
				{name: "truenas", mountpoint: "/mnt/truenas", mounted: false, canmount: true},
			}
		}

		if err := executeZfsPoolImport(run, collect, guid, "", false); err != nil {
			t.Fatalf("executeZfsPoolImport by GUID failed: %v", err)
		}
		want := []string{
			"zpool import -N " + guid,
			"zpool list -H -o name,guid",
			"zfs get -H -t filesystem -o name,value,source -r mountpoint truenas",
			"zfs set mountpoint=/mnt/truenas truenas",
			"zfs mount truenas",
		}
		if !reflect.DeepEqual(cmds, want) {
			t.Fatalf("cmds = %#v, want %#v", cmds, want)
		}
	})

	t.Run("Native_Mnt_Pool_Skips_Redundant_Set_And_Avoids_Double_Mnt", func(t *testing.T) {
		var cmds []string
		run := func(timeout time.Duration, name string, args ...string) ([]byte, error) {
			cmds = append(cmds, name+" "+strings.Join(args, " "))
			if name == "zfs" && len(args) > 0 && args[0] == "get" {
				return []byte("DATA\t/mnt/DATA\tlocal\nDATA/Share\t/mnt/DATA/Share\tinherited from DATA\n"), nil
			}
			return nil, nil
		}
		collect := func(target string) []zfsMountEntry {
			return []zfsMountEntry{
				{name: "DATA/Share", mountpoint: "/mnt/DATA/Share", mounted: false, canmount: true},
				{name: "DATA", mountpoint: "/mnt/DATA", mounted: false, canmount: true},
			}
		}

		if err := executeZfsPoolImport(run, collect, "DATA", "", false); err != nil {
			t.Fatalf("executeZfsPoolImport native pool failed: %v", err)
		}
		want := []string{
			"zpool import -N DATA",
			"zfs get -H -t filesystem -o name,value,source -r mountpoint DATA",
			"zfs mount DATA",
			"zfs mount DATA/Share",
		}
		if !reflect.DeepEqual(cmds, want) {
			t.Fatalf("cmds = %#v, want %#v", cmds, want)
		}
	})

	t.Run("Mountpoint_Set_Error_Aborts_Before_Mounting", func(t *testing.T) {
		var cmds []string
		run := func(timeout time.Duration, name string, args ...string) ([]byte, error) {
			cmds = append(cmds, name+" "+strings.Join(args, " "))
			if name == "zfs" && len(args) > 0 && args[0] == "get" {
				return []byte("evil\t/\tlocal\n"), nil
			}
			if name == "zfs" && len(args) > 0 && args[0] == "set" {
				return []byte("permission denied"), fmt.Errorf("exit status 1")
			}
			return nil, nil
		}
		collect := func(target string) []zfsMountEntry {
			t.Fatalf("collectMounts should not be called when mountpoint normalization fails")
			return nil
		}

		err := executeZfsPoolImport(run, collect, "evil", "", false)
		if err == nil || !strings.Contains(err.Error(), "imported without mounting") {
			t.Fatalf("expected abort error mentioning imported without mounting, got: %v", err)
		}
		for _, c := range cmds {
			if strings.HasPrefix(c, "zfs mount") {
				t.Fatalf("must not attempt mount after set failure, cmds=%v", cmds)
			}
		}
	})
}




