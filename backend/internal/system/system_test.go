package system

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSystem_ServicesProbeAndStateMapping(t *testing.T) {
	services := buildServicesFromStates([]string{"active", "inactive", "failed", "activating", "unknown"})
	if len(services) != len(coreServiceCatalog) {
		t.Fatalf("expected %d services, got %d", len(coreServiceCatalog), len(services))
	}
	if !services[0].Active || services[0].BadgeClass != "green" {
		t.Fatalf("expected active service green, got %+v", services[0])
	}
	if services[1].Active || services[1].BadgeClass != "slate" || services[1].StateLabel != "Inactive (stopped)" {
		t.Fatalf("expected inactive service slate, got %+v", services[1])
	}
	if services[2].BadgeClass != "red" || services[2].StateLabel != "Failed" {
		t.Fatalf("expected failed service red, got %+v", services[2])
	}
	if services[3].BadgeClass != "amber" || services[3].StateLabel != "Activating" {
		t.Fatalf("expected activating service amber, got %+v", services[3])
	}
	if services[4].BadgeClass != "slate" {
		t.Fatalf("expected unknown service slate, got %+v", services[4])
	}

	status := NewCollector(true).GetStatus()
	if len(status.Services) != len(coreServiceCatalog) {
		t.Fatalf("expected %d services in mock SystemStatus, got %d", len(coreServiceCatalog), len(status.Services))
	}
}

func TestSystem_ParseArcSizeInputAndValidation(t *testing.T) {
	cases := []struct {
		raw     string
		unit    string
		want    uint64
		wantErr bool
	}{
		{"", "GiB", 0, false},
		{"0", "GiB", 0, false},
		{"auto", "GiB", 0, false},
		{"4", "GiB", 4 * 1024 * 1024 * 1024, false},
		{"2.5", "GiB", uint64(2.5 * 1024 * 1024 * 1024), false},
		{"512MiB", "GiB", 512 * 1024 * 1024, false},
		{"2,5G", "MiB", uint64(2.5 * 1024 * 1024 * 1024), false},
		{"-1", "GiB", 0, true},
		{"abc", "GiB", 0, true},
	}
	for _, tc := range cases {
		got, err := ParseArcSizeInput(tc.raw, tc.unit)
		if (err != nil) != tc.wantErr {
			t.Fatalf("ParseArcSizeInput(%q, %q) err=%v wantErr=%v", tc.raw, tc.unit, err, tc.wantErr)
		}
		if !tc.wantErr && got != tc.want {
			t.Fatalf("ParseArcSizeInput(%q, %q) = %d, want %d", tc.raw, tc.unit, got, tc.want)
		}
	}

	c := NewCollector(true)
	// Below 64 MiB floor
	if _, err := c.SetArcConfig(16*1024*1024, 0); err == nil {
		t.Fatalf("expected error for max < 64 MiB")
	}
	// Above 90% RAM
	if _, err := c.SetArcConfig(15*1024*1024*1024+512*1024*1024, 0); err == nil {
		t.Fatalf("expected error for max > 90%% RAM")
	}
	// min > max
	if _, err := c.SetArcConfig(2*1024*1024*1024, 4*1024*1024*1024); err == nil {
		t.Fatalf("expected error when min > max")
	}

	// Valid mock update clamps active ARC & updates SystemStatus
	updated, err := c.SetArcConfig(4*1024*1024*1024, 512*1024*1024)
	if err != nil {
		t.Fatalf("valid SetArcConfig failed: %v", err)
	}
	if updated.MaxBytes != 4*1024*1024*1024 || updated.MinBytes != 512*1024*1024 || !updated.Persisted || updated.IsAutoMax {
		t.Fatalf("unexpected updated ARC config: %+v", updated)
	}
	if st := c.GetStatus(); st.Memory.ZfsArcBytes > 4*1024*1024*1024 || st.ZfsArc.MaxBytes != 4*1024*1024*1024 {
		t.Fatalf("GetStatus did not reflect new ARC limit: memArc=%d zfsArc=%+v", st.Memory.ZfsArcBytes, st.ZfsArc)
	}
}

func TestSystem_RealArcKernelAndModprobePersistence(t *testing.T) {
	dir := t.TempDir()
	paramsDir := filepath.Join(dir, "params")
	if err := os.MkdirAll(paramsDir, 0755); err != nil {
		t.Fatal(err)
	}
	maxParam := filepath.Join(paramsDir, "zfs_arc_max")
	minParam := filepath.Join(paramsDir, "zfs_arc_min")
	// Seed high min & max to ensure shrink path writes min before max safely.
	_ = os.WriteFile(maxParam, []byte("8589934592"), 0644)
	_ = os.WriteFile(minParam, []byte("4294967296"), 0644)

	statsPath := filepath.Join(dir, "arcstats")
	_ = os.WriteFile(statsPath, []byte("name type data\nsize 4 1073741824\nc 4 4294967296\nc_max 4 4294967296\nc_min 4 536870912\nhits 4 950\nmisses 4 50\n"), 0644)

	memPath := filepath.Join(dir, "meminfo")
	_ = os.WriteFile(memPath, []byte("MemTotal:        8388608 kB\nMemAvailable:    4194304 kB\n"), 0644)

	modPath := filepath.Join(dir, "modprobe.d", "zfs.conf")
	_ = os.MkdirAll(filepath.Dir(modPath), 0755)
	_ = os.WriteFile(modPath, []byte("# existing ZFS config\noptions zfs zfs_prefetch_disable=1 zfs_arc_max=8589934592\n"), 0644)

	c := &Collector{mockMode: false}
	c.SetArcPaths(statsPath, paramsDir, modPath, memPath)

	// Shrink max below previous kernel min (4 GiB -> 2 GiB max, 256 MiB min)
	cfg, err := c.SetArcConfig(2*1024*1024*1024, 256*1024*1024)
	if err != nil {
		t.Fatalf("SetArcConfig real path failed: %v", err)
	}
	if !cfg.Persisted || !cfg.KernelModuleLoaded || cfg.MaxInputValue != "2" || cfg.MaxInputUnit != "GiB" {
		t.Fatalf("unexpected real ARC config: %+v", cfg)
	}

	gotMax, _ := os.ReadFile(maxParam)
	gotMin, _ := os.ReadFile(minParam)
	if string(gotMax) != "2147483648" || string(gotMin) != "268435456" {
		t.Fatalf("kernel params mismatch: max=%s min=%s", gotMax, gotMin)
	}

	modRaw, _ := os.ReadFile(modPath)
	modStr := string(modRaw)
	if !strings.Contains(modStr, "# existing ZFS config") || !strings.Contains(modStr, "zfs_prefetch_disable=1") || !strings.Contains(modStr, "zfs_arc_max=2147483648") || !strings.Contains(modStr, "zfs_arc_min=268435456") {
		t.Fatalf("modprobe file did not preserve existing options or write new limits:\n%s", modStr)
	}

	// Simulate kernel reset before startup, then verify ApplyPersistedArcOnStartup restores from zfs.conf
	_ = os.WriteFile(maxParam, []byte("0"), 0644)
	_ = os.WriteFile(minParam, []byte("0"), 0644)
	if err := c.ApplyPersistedArcOnStartup(); err != nil {
		t.Fatalf("ApplyPersistedArcOnStartup failed: %v", err)
	}

	// Verify reset to Auto removes zfs_arc_* while keeping unrelated options
	if _, err := c.SetArcConfig(0, 0); err != nil {
		t.Fatalf("reset SetArcConfig(0,0) failed: %v", err)
	}
	modAfterReset, _ := os.ReadFile(modPath)
	if strings.Contains(string(modAfterReset), "zfs_arc_max") || !strings.Contains(string(modAfterReset), "zfs_prefetch_disable=1") {
		t.Fatalf("reset to Auto did not clean zfs_arc_* or dropped unrelated option:\n%s", modAfterReset)
	}
}


