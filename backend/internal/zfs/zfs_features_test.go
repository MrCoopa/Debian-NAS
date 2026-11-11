package zfs

import (
	"strings"
	"testing"
)

func TestZfsPools_CreateTopologiesAndDestroy(t *testing.T) {
	m := NewManager(true)

	const mockDiskBytes = uint64(16000900661248)
	topologies := []struct {
		name     string
		vdevType string
		disks    []string
		wantSize uint64
	}{
		{name: "pstripe", vdevType: "stripe", disks: []string{"sdf", "sdg"}, wantSize: 2 * mockDiskBytes},
		{name: "pmirror", vdevType: "mirror", disks: []string{"sdh", "sdi"}, wantSize: mockDiskBytes},
		{name: "praidz1", vdevType: "raidz1", disks: []string{"sdj", "sdk", "sdl"}, wantSize: 2 * mockDiskBytes},
		{name: "praidz2", vdevType: "raidz2", disks: []string{"sdm", "sdn", "sdo", "sdp"}, wantSize: 2 * mockDiskBytes},
		{name: "praidz3", vdevType: "raidz3", disks: []string{"sdq", "sdr", "sds", "sdt", "sdu"}, wantSize: 2 * mockDiskBytes},
	}

	for _, tc := range topologies {
		if err := m.CreatePool(tc.name, tc.vdevType, tc.disks, 0, ""); err != nil {
			t.Fatalf("CreatePool(%s, %s) failed: %v", tc.name, tc.vdevType, err)
		}
		if err := m.CreatePool(tc.name, tc.vdevType, tc.disks, 12, "lz4"); err == nil {
			t.Errorf("CreatePool(%s) duplicate should fail", tc.name)
		}
	}

	if err := m.CreatePool("badraidz2", "raidz2", []string{"sda", "sdb", "sdc"}, 12, "lz4"); err == nil {
		t.Error("expected raidz2 with 3 disks to fail")
	}
	if err := m.CreatePool("badraidz3", "raidz3", []string{"sda", "sdb", "sdc", "sdd"}, 12, "lz4"); err == nil {
		t.Error("expected raidz3 with 4 disks to fail")
	}
	if err := m.CreatePool("badtopo", "raid5", []string{"sda", "sdb"}, 12, "lz4"); err == nil {
		t.Error("expected unsupported topology to fail")
	}

	pools, err := m.ListPools()
	if err != nil {
		t.Fatalf("ListPools failed: %v", err)
	}
	poolByName := make(map[string]*Pool, len(pools))
	for _, p := range pools {
		poolByName[p.Name] = p
	}
	for _, tc := range topologies {
		p := poolByName[tc.name]
		if p == nil {
			t.Fatalf("created pool %s missing from ListPools", tc.name)
		}
		if p.SizeBytes != tc.wantSize {
			t.Errorf("pool %s SizeBytes = %d, want %d", tc.name, p.SizeBytes, tc.wantSize)
		}
		if p.Compression != "zstd" || p.AtimeDefault != "off" {
			t.Errorf("pool %s defaults unexpected: comp=%s atime=%s", tc.name, p.Compression, p.AtimeDefault)
		}
	}

	// Child dataset + snapshot cascade deletion via DestroyPool
	if err := m.CreateDataset("pmirror/media", "lz4", "128K"); err != nil {
		t.Fatalf("CreateDataset failed: %v", err)
	}
	if err := m.CreateSnapshotRecursive("pmirror", "before-destroy", true); err != nil {
		t.Fatalf("CreateSnapshotRecursive failed: %v", err)
	}
	if err := m.DestroyPool("pmirror"); err != nil {
		t.Fatalf("DestroyPool failed: %v", err)
	}

	datasets, _ := m.ListDatasets()
	for _, ds := range datasets {
		if ds.Pool == "pmirror" {
			t.Errorf("dataset %s remained after DestroyPool('pmirror')", ds.Name)
		}
	}
	snaps, _ := m.ListSnapshots()
	for _, s := range snaps {
		if strings.HasPrefix(s.Dataset, "pmirror") {
			t.Errorf("snapshot %s remained after DestroyPool('pmirror')", s.Name)
		}
	}
}

func TestZfsParsers_PoolListDatasetsAndImportable(t *testing.T) {
	// 1. zpool list parser
	zpoolOut := "DATA\t16000900661248\t4000225165312\t12000675495936\t4%\t25%\tONLINE\ton\toff\n" +
		"FAST\t1000000000000\t100000000000\t900000000000\t1%\t10%\tDEGRADED\n"
	pools := parseZpoolList([]byte(zpoolOut), func(name string) ([]Vdev, ScrubStatus) {
		if name == "FAST" {
			return []Vdev{
				{Name: "mirror-0", Type: "mirror", Health: "DEGRADED", IsDisk: false},
				{Name: "sda", Type: "disk", Role: "mirror-0", Health: "ONLINE", IsDisk: true},
				{Name: "sdb", Type: "disk", Role: "mirror-0", Health: "DEGRADED", IsDisk: true},
			}, ScrubStatus{Errors: 0}
		}
		return []Vdev{{Name: name + "-vdev", Health: "ONLINE"}}, ScrubStatus{Errors: 0}
	})
	if len(pools) != 2 {
		t.Fatalf("parseZpoolList len = %d, want 2", len(pools))
	}
	if pools[0].Name != "DATA" || pools[0].Autotrim != "on" || pools[0].Autoexpand != "off" || pools[0].CapPercent != 25 {
		t.Errorf("unexpected parsed pool[0]: %+v", pools[0])
	}
	if pools[1].Name != "FAST" || pools[1].Health != HealthDegraded || pools[1].Autotrim != "off" || pools[1].Autoexpand != "on" {
		t.Errorf("unexpected parsed pool[1]: %+v", pools[1])
	}

	// 2. zpool status extended (aux vdevs + resilver in progress)
	statusOut := `  pool: TANK
 state: DEGRADED
  scan: resilver in progress since Mon Nov 11 10:00:00 2026
        2.50T scanned at 450M/s, 1.25T issued at 225M/s, 2.50T total
        125G resilvered, 50.00% done, 01:30:00 to go
config:

	NAME                                   STATE     READ WRITE CKSUM
	TANK                                   DEGRADED     0     0     0
	  raidz1-0                             DEGRADED     0     0     0
	    /dev/sda1                          ONLINE       0     0     0
	    /dev/sdb1                          DEGRADED     2     1     0
	    /dev/sdc1                          ONLINE       0     0     0
	logs
	  /dev/nvme0n1p1                       ONLINE       0     0     0
	cache
	  /dev/nvme1n1                         ONLINE       0     0     0
	spares
	  /dev/sdd                             AVAIL
`
	vdevs, scrub := parseZpoolStatus(strings.NewReader(statusOut), "TANK")
	if !scrub.Active || scrub.Percentage != 50.0 {
		t.Errorf("expected active resilver at 50.0%%, got active=%v pct=%v", scrub.Active, scrub.Percentage)
	}
	roleMap := make(map[string]string)
	typeMap := make(map[string]string)
	for _, v := range vdevs {
		typeMap[v.Name] = v.Type
		roleMap[v.Name] = v.Role
	}
	if typeMap["nvme0n1"] != "log" || typeMap["nvme1n1"] != "cache" || typeMap["sdd"] != "spare" {
		t.Errorf("unexpected aux vdev types: %#v", typeMap)
	}
	if roleMap["sdb"] != "raidz1-0" {
		t.Errorf("expected sdb role raidz1-0, got %q", roleMap["sdb"])
	}
	tank := &Pool{Name: "TANK", Vdevs: vdevs, Scrub: scrub}
	syncPoolFields(tank)
	if tank.Health != HealthDegraded {
		t.Errorf("expected TANK health DEGRADED (not FAULTED), got %s", tank.Health)
	}

	// Healthy mirror + AVAIL hot spare must stay ONLINE
	healthySpareOut := `  pool: HEALTHY
 state: ONLINE
  scan: none requested
config:

	NAME        STATE     READ WRITE CKSUM
	HEALTHY     ONLINE       0     0     0
	  mirror-0  ONLINE       0     0     0
	    /dev/sda ONLINE      0     0     0
	    /dev/sdb ONLINE      0     0     0
	spares
	  /dev/sdc  AVAIL
`
	hv, _ := parseZpoolStatus(strings.NewReader(healthySpareOut), "HEALTHY")
	hp := &Pool{Name: "HEALTHY", Health: HealthOnline, Vdevs: hv}
	syncPoolFields(hp)
	if hp.Health != HealthOnline {
		t.Errorf("expected healthy mirror + AVAIL spare to stay ONLINE, got %s", hp.Health)
	}

	// 3. zfs list datasets & zvols parser
	zfsListOut := "DATA/media\t1073741824\t5368709120\t/mnt/DATA/media\tzstd\toff\t131072\t10737418240\toff\tfilesystem\t-\n" +
		"DATA/vm0\t2097152\t500000000\t-\tlz4\t-\t-\t-\toff\tvolume\t10737418240\n"
	datasets := parseZfsDatasets([]byte(zfsListOut), map[string]int{"DATA/media": 4, "DATA/vm0": 1})
	if len(datasets) != 2 {
		t.Fatalf("parseZfsDatasets len = %d, want 2", len(datasets))
	}
	if datasets[0].RecordSize != "128K" || datasets[0].Quota != "10G" || datasets[0].Snapshots != 4 {
		t.Errorf("unexpected filesystem parse: %+v", datasets[0])
	}
	if datasets[1].Type != "volume" || datasets[1].Mountpoint != "/dev/zvol/DATA/vm0" || datasets[1].AvailBytes != 10737418240 {
		t.Errorf("unexpected zvol parse: %+v", datasets[1])
	}

	// 4. zpool import parser
	importOut := `   pool: BACKUP
     id: 1234567890123456789
  state: ONLINE
 action: The pool can be imported using its name or numeric identifier.
 config:

	BACKUP      ONLINE
	  mirror-0  ONLINE
	    sda     ONLINE
	    sdb     ONLINE

   pool: ARCHIVE
     id: 9876543210987654321
  state: DEGRADED
 status: One or more devices are faulted.
 config:

	ARCHIVE     DEGRADED
	  sdc       ONLINE
`
	importable := parseImportablePools([]byte(importOut))
	if len(importable) != 2 {
		t.Fatalf("parseImportablePools len = %d, want 2", len(importable))
	}
	if importable[0].Name != "BACKUP" || importable[0].ID != "1234567890123456789" || importable[0].State != "ONLINE" {
		t.Errorf("unexpected importable[0]: %+v", importable[0])
	}
	if importable[1].Name != "ARCHIVE" || importable[1].State != "DEGRADED" || !strings.Contains(importable[1].Action, "faulted") {
		t.Errorf("unexpected importable[1]: %+v", importable[1])
	}
}

func TestZfsMaintenance_ScrubTrimClearAndPoolProperties(t *testing.T) {
	m := NewManager(true)

	if err := m.StartScrub("DATA"); err != nil {
		t.Fatalf("StartScrub failed: %v", err)
	}
	pools, _ := m.ListPools()
	var dataPool *Pool
	for _, p := range pools {
		if p.Name == "DATA" {
			dataPool = p
			break
		}
	}
	if dataPool == nil || !dataPool.Scrub.Active {
		t.Fatalf("expected active scrub on DATA")
	}

	if err := m.StopScrub("DATA"); err != nil {
		t.Fatalf("StopScrub failed: %v", err)
	}
	if dataPool.Scrub.Active {
		t.Errorf("expected scrub stopped on DATA")
	}

	if err := m.TrimPool("DATA"); err != nil {
		t.Fatalf("TrimPool failed: %v", err)
	}
	if err := m.TrimPool("../bad"); err == nil {
		t.Error("expected TrimPool invalid name error")
	}

	// Simulate degraded pool with vdev & scrub errors and clear it via ClearPool.
	m.mu.Lock()
	dataPool.Health = HealthDegraded
	dataPool.Scrub.Errors = 3
	if len(dataPool.Vdevs) > 1 {
		dataPool.Vdevs[1].ReadErrors = 4
		dataPool.Vdevs[1].WriteErrors = 2
		dataPool.Vdevs[1].CksumErrors = 5
	}
	m.mu.Unlock()

	if err := m.ClearPool("DATA"); err != nil {
		t.Fatalf("ClearPool failed: %v", err)
	}
	if dataPool.Health != HealthOnline || dataPool.Scrub.Errors != 0 {
		t.Errorf("expected ONLINE with 0 scrub errors after ClearPool, got %s errors=%d", dataPool.Health, dataPool.Scrub.Errors)
	}
	for _, v := range dataPool.Vdevs {
		if v.ReadErrors != 0 || v.WriteErrors != 0 || v.CksumErrors != 0 {
			t.Errorf("vdev %s errors not cleared: R=%d W=%d C=%d", v.Name, v.ReadErrors, v.WriteErrors, v.CksumErrors)
		}
	}

	if err := m.SetPoolProperty("DATA", "failmode", "continue"); err != nil {
		t.Fatalf("SetPoolProperty failmode failed: %v", err)
	}
	if err := m.SetPoolProperty("DATA", "autotrim", "invalid"); err == nil {
		t.Error("expected invalid autotrim property value to fail")
	}
	if err := m.SetPoolProperty("DATA", "unknownprop", "on"); err == nil {
		t.Error("expected unsupported pool property to fail")
	}
}

func TestZfsVdevs_AddAuxAndDataVdevs(t *testing.T) {
	m := NewManager(true)
	if err := m.CreatePool("EXP", "stripe", []string{"sdb"}, 12, "lz4"); err != nil {
		t.Fatalf("CreatePool failed: %v", err)
	}

	getPool := func() *Pool {
		pools, _ := m.ListPools()
		for _, p := range pools {
			if p.Name == "EXP" {
				return p
			}
		}
		return nil
	}

	baseSize := getPool().SizeBytes

	// Mirror data vdev adds 1x disk usable size; stripe adds 1x disk usable size.
	if err := m.AddVdev("EXP", "mirror", []string{"sdc", "sdd"}); err != nil {
		t.Fatalf("AddVdev mirror failed: %v", err)
	}
	afterMirrorSize := getPool().SizeBytes
	if afterMirrorSize != 2*baseSize {
		t.Errorf("after mirror vdev size = %d, want %d", afterMirrorSize, 2*baseSize)
	}

	// Auxiliary vdevs (log, cache, special, spare) must NOT add data capacity.
	for _, aux := range []struct {
		vdevType string
		disk     string
	}{
		{"log", "nvme0n1"},
		{"cache", "nvme1n1"},
		{"special", "nvme2n1"},
		{"spare", "sde"},
	} {
		if err := m.AddVdev("EXP", aux.vdevType, []string{aux.disk}); err != nil {
			t.Fatalf("AddVdev(%s) failed: %v", aux.vdevType, err)
		}
		if got := getPool().SizeBytes; got != afterMirrorSize {
			t.Errorf("AddVdev(%s) altered data capacity: got %d want %d", aux.vdevType, got, afterMirrorSize)
		}
	}

	// Guard against <2 disks in mirror vdev & empty disk list
	if err := m.AddVdev("EXP", "mirror", []string{"sdf"}); err == nil {
		t.Error("expected 1-disk mirror AddVdev to fail")
	}
	if err := m.AddVdev("EXP", "stripe", []string{}); err == nil {
		t.Error("expected empty disk list AddVdev to fail")
	}
}

func TestZfsDiskOps_DetachGuardAndPoolMapKeys(t *testing.T) {
	m := NewManager(true)
	if err := m.CreatePool("SOLO", "stripe", []string{"sde"}, 12, "lz4"); err != nil {
		t.Fatalf("CreatePool failed: %v", err)
	}

	// Cannot detach last remaining disk
	if err := m.DetachDisk("SOLO", "sde"); err == nil {
		t.Fatal("expected detaching only remaining disk to fail")
	}

	// Attaching second disk allows detaching afterwards
	if err := m.AttachDisk("SOLO", "sde", "sdf"); err != nil {
		t.Fatalf("AttachDisk failed: %v", err)
	}
	if err := m.DetachDisk("SOLO", "sdf"); err != nil {
		t.Fatalf("DetachDisk failed after attach: %v", err)
	}

	poolMap, roleMap := BuildDiskPoolMap([]*Pool{
		{
			Name: "BYIDPOOL",
			Vdevs: []Vdev{
				{Name: "ata-WDC_TEST-part1", Path: "/dev/disk/by-id/ata-WDC_TEST-part1", Role: "mirror-0", IsDisk: true},
				{Name: "nvme0n1p1", Path: "/dev/nvme0n1p1", Role: "log", IsDisk: true},
			},
		},
	})
	if poolMap["ata-WDC_TEST"] != "BYIDPOOL" || roleMap["/dev/disk/by-id/ata-WDC_TEST"] != "mirror-0" {
		t.Errorf("unexpected by-id disk pool mapping: pool=%#v role=%#v", poolMap["ata-WDC_TEST"], roleMap["/dev/disk/by-id/ata-WDC_TEST"])
	}
	if poolMap["nvme0n1"] != "BYIDPOOL" || roleMap["nvme0n1"] != "log" {
		t.Errorf("unexpected nvme partition disk pool mapping: pool=%q role=%q", poolMap["nvme0n1"], roleMap["nvme0n1"])
	}
}

func TestZfsDatasetsAndZvols_FullFeatureSet(t *testing.T) {
	if got := formatRecordSize("131072"); got != "128K" {
		t.Errorf("formatRecordSize(131072) = %q, want 128K", got)
	}
	if got := formatRecordSize("1048576"); got != "1M" {
		t.Errorf("formatRecordSize(1048576) = %q, want 1M", got)
	}
	if got := formatZfsQuota(500 * 1024 * 1024 * 1024); got != "500G" {
		t.Errorf("formatZfsQuota(500GiB) = %q, want 500G", got)
	}
	if got := normalizeZfsSize("500 GiB"); got != "500G" {
		t.Errorf("normalizeZfsSize('500 GiB') = %q, want 500G", got)
	}
	if got := normalizeZfsSize("0"); got != "none" {
		t.Errorf("normalizeZfsSize('0') = %q, want none", got)
	}

	m := NewManager(true)

	if err := m.CreateDataset("DATA/apps", "zstd", "128K"); err != nil {
		t.Fatalf("CreateDataset failed: %v", err)
	}
	if err := m.CreateDataset("DATA/apps/db", "lz4", "16K"); err != nil {
		t.Fatalf("CreateDataset nested failed: %v", err)
	}

	props := []struct{ prop, val string }{
		{"compression", "lz4"},
		{"recordsize", "64K"},
		{"atime", "on"},
		{"quota", "250 GiB"},
		{"refquota", "200G"},
		{"reservation", "50G"},
		{"refreservation", "10G"},
		{"readonly", "on"},
		{"sync", "disabled"},
	}
	for _, tc := range props {
		if err := m.SetDatasetProperty("DATA/apps", tc.prop, tc.val); err != nil {
			t.Errorf("SetDatasetProperty(%s=%s) failed: %v", tc.prop, tc.val, err)
		}
	}
	if err := m.SetDatasetProperty("DATA/apps", "exec", "off"); err == nil {
		t.Error("expected disallowed dataset property to fail")
	}

	// Zvol validations and creation
	if err := m.CreateZvol("nozvolpool", 1024*1024*1024, "16k", true); err == nil {
		t.Error("expected Zvol without pool prefix to fail")
	}
	if err := m.CreateZvol("MISSINGPOOL/vol1", 1024*1024*1024, "16k", true); err == nil {
		t.Error("expected Zvol on missing pool to fail")
	}
	if err := m.CreateZvol("DATA/badblock", 1024*1024*1024, "99k", true); err == nil {
		t.Error("expected invalid volblocksize to fail")
	}
	if err := m.CreateZvol("DATA/zero", 0, "16k", false); err == nil {
		t.Error("expected zero-size Zvol to fail")
	}
	if err := m.CreateZvol("DATA/iscsi0", 20*1024*1024*1024, "32k", false); err != nil {
		t.Fatalf("CreateZvol thick failed: %v", err)
	}
	if err := m.CreateZvol("DATA/iscsi0", 20*1024*1024*1024, "32k", false); err == nil {
		t.Error("expected duplicate Zvol to fail")
	}

	// Cascade DestroyDataset deletes children & snapshots cleanly
	if err := m.CreateSnapshotRecursive("DATA/apps", "pre-del", true); err != nil {
		t.Fatalf("CreateSnapshotRecursive failed: %v", err)
	}
	if err := m.DestroyDataset("DATA/apps"); err != nil {
		t.Fatalf("DestroyDataset failed: %v", err)
	}

	datasets, _ := m.ListDatasets()
	for _, ds := range datasets {
		if ds.Name == "DATA/apps" || ds.Name == "DATA/apps/db" {
			t.Errorf("expected %s to be deleted by cascade DestroyDataset", ds.Name)
		}
	}
	snaps, _ := m.ListSnapshots()
	for _, s := range snaps {
		if s.Dataset == "DATA/apps" || s.Dataset == "DATA/apps/db" {
			t.Errorf("expected snapshot %s to be deleted by cascade DestroyDataset", s.Name)
		}
	}
}

func TestZfsPoolExportImport_RenameAndByID(t *testing.T) {
	m := NewManager(true)
	if err := m.CreatePool("OLDPOOL", "mirror", []string{"sdc", "sdd"}, 12, "zstd"); err != nil {
		t.Fatalf("CreatePool failed: %v", err)
	}
	if err := m.CreateDataset("OLDPOOL/projects", "lz4", "128K"); err != nil {
		t.Fatalf("CreateDataset failed: %v", err)
	}
	if err := m.CreateZvol("OLDPOOL/vmdisk", 5*1024*1024*1024, "16k", true); err != nil {
		t.Fatalf("CreateZvol failed: %v", err)
	}

	if err := m.ExportPool("OLDPOOL"); err != nil {
		t.Fatalf("ExportPool failed: %v", err)
	}
	importable, err := m.FindImportablePools()
	if err != nil {
		t.Fatalf("FindImportablePools failed: %v", err)
	}
	var exportedID string
	for _, p := range importable {
		if p.Name == "OLDPOOL" {
			exportedID = p.ID
			break
		}
	}
	if exportedID == "" {
		t.Fatalf("expected OLDPOOL in FindImportablePools with numeric ID")
	}

	if err := m.ImportPool(exportedID, "NEWPOOL", true); err != nil {
		t.Fatalf("ImportPool by ID with rename failed: %v", err)
	}

	datasets, _ := m.ListDatasets()
	foundFS := false
	foundZvol := false
	for _, ds := range datasets {
		if ds.Name == "NEWPOOL/projects" && ds.Mountpoint == "/mnt/NEWPOOL/projects" {
			foundFS = true
		}
		if ds.Name == "NEWPOOL/vmdisk" && ds.Mountpoint == "/dev/zvol/NEWPOOL/vmdisk" {
			foundZvol = true
		}
		if ds.Pool == "OLDPOOL" {
			t.Errorf("stale OLDPOOL dataset %s still present after renamed import", ds.Name)
		}
	}
	if !foundFS || !foundZvol {
		t.Errorf("renamed imported datasets missing: fs=%v zvol=%v", foundFS, foundZvol)
	}
}
