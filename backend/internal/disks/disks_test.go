package disks

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiskManager_OperationsAndSync(t *testing.T) {
	m := NewManager(true, "")

	disks, err := m.ListDisks()
	if err != nil || len(disks) == 0 {
		t.Fatalf("expected mock disks to be initialized: %v", err)
	}

	// Verify fields are synchronized for frontend contract
	for _, d := range disks {
		if d.Device == "" {
			t.Errorf("expected Device field to be populated for %s", d.Name)
		}
		if d.PowerState == "" {
			t.Errorf("expected PowerState field to be populated for %s", d.Name)
		}
		if d.Vendor == "" {
			t.Errorf("expected Vendor field to be populated for %s", d.Name)
		}
	}

	// Test SyncPoolMembership
	m.SyncPoolMembership(
		map[string]string{"sda": "TANK", "sdb": "TANK", "sdc": "NEWPOOL"},
		map[string]string{"sda": "mirror-0", "sdb": "mirror-0", "sdc": "stripe"},
	)

	disks, _ = m.ListDisks()
	var sdc, sdd, nvme0 *DiskInfo
	for _, d := range disks {
		switch d.Name {
		case "sdc":
			sdc = d
		case "sdd":
			sdd = d
		case "nvme0n1":
			nvme0 = d
		}
	}
	if sdc == nil || sdc.ZfsPool != "NEWPOOL" || sdc.Pool != "NEWPOOL" {
		t.Errorf("expected sdc pool to be synced to NEWPOOL, got %+v", sdc)
	}

	// Test Spindown, Standby, Locate, and SMART Test using short device name (as sent by app.js)
	if err := m.SetSpindown("sdd", 300); err != nil {
		t.Fatalf("SetSpindown failed: %v", err)
	}
	if err := m.ForceStandby("sdd"); err != nil {
		t.Fatalf("ForceStandby failed: %v", err)
	}
	if _, err := m.ToggleLocate("sdd"); err != nil {
		t.Fatalf("ToggleLocate failed: %v", err)
	}
	if err := m.TriggerSmartTest("sdd", "short"); err != nil {
		t.Fatalf("TriggerSmartTest failed: %v", err)
	}

	disks, _ = m.ListDisks()
	for _, d := range disks {
		if d.Name == "sdd" {
			sdd = d
			break
		}
	}
	if sdd.SpindownTimeoutSeconds != 300 {
		t.Errorf("expected SpindownTimeoutSeconds=300, got %d", sdd.SpindownTimeoutSeconds)
	}
	if !sdd.LocateLed {
		t.Errorf("expected LocateLed=true on sdd")
	}

	// Test WipeDisk protections
	if nvme0 != nil {
		nvme0.IsSystemDisk = true
		if err := m.WipeDisk("nvme0n1", "quick"); err == nil {
			t.Errorf("expected WipeDisk on system disk nvme0n1 to be blocked")
		}
	}
	if err := m.WipeDisk("sda", "quick"); err == nil {
		t.Errorf("expected WipeDisk on pool member sda to be blocked")
	}
	if err := m.WipeDisk("sdd", "quick"); err != nil {
		t.Errorf("expected WipeDisk on unallocated disk sdd to succeed, got: %v", err)
	}
}

func TestDiskPowerSettings_NormalizationPersistenceAndHdIdle(t *testing.T) {
	// 1. APM validation & hdparm output parser
	for _, tc := range []struct {
		raw     string
		wantDsp string
		wantArg string
		wantErr bool
	}{
		{"Disabled", "Disabled", "255", false},
		{"255", "Disabled", "255", false},
		{"off", "Disabled", "255", false},
		{"1", "1", "1", false},
		{"64", "64", "64", false},
		{"128", "128", "128", false},
		{"254", "254", "254", false},
		{"0", "", "", true},
		{"256", "", "", true},
		{"bogus", "", "", true},
	} {
		dsp, arg, err := NormalizeAPMLevel(tc.raw)
		if tc.wantErr {
			if err == nil {
				t.Errorf("expected error for NormalizeAPMLevel(%q)", tc.raw)
			}
			continue
		}
		if err != nil || dsp != tc.wantDsp || arg != tc.wantArg {
			t.Errorf("NormalizeAPMLevel(%q) = (%q, %q, %v), want (%q, %q, nil)", tc.raw, dsp, arg, err, tc.wantDsp, tc.wantArg)
		}
	}

	if got := parseHdparmAPMOutput("\n/dev/sdc:\n APM_level\t= not supported\n"); got != "Disabled" {
		t.Errorf("expected not supported APM output to map to Disabled, got %q", got)
	}
	if got := parseHdparmAPMOutput("\n/dev/sdc:\n APM_level\t= 192\n"); got != "192" {
		t.Errorf("expected APM_level=192, got %q", got)
	}
	if got := parseHdparmAPMOutput("\n/dev/sdc:\n APM_level\t= off\n"); got != "Disabled" {
		t.Errorf("expected APM_level=off to map to Disabled, got %q", got)
	}

	// 2. Spindown + APM persistence across manager reload (fixing reset-to-128 & Always On drift)
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "disks.json")
	hdIdlePath := filepath.Join(tmpDir, "hd-idle")
	if err := os.WriteFile(hdIdlePath, []byte("START_HD_IDLE=true\nHD_IDLE_OPTS=\"-i 900\"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	m := NewManager(true, hdIdlePath)
	m.SetConfigPath(cfgPath)

	if err := m.SetAPM("sdc", "Disabled"); err != nil {
		t.Fatalf("SetAPM(Disabled) failed: %v", err)
	}
	if err := m.SetEPC("sdc", "Disabled"); err != nil {
		t.Fatalf("SetEPC(Disabled) failed: %v", err)
	}
	if err := m.SetEPC("sdb", "Enabled"); err != nil {
		t.Fatalf("SetEPC(Enabled) failed: %v", err)
	}
	if err := m.SetSpindown("sdc", 0); err != nil {
		t.Fatalf("SetSpindown(0) failed: %v", err)
	}
	if err := m.SetSpindown("sdb", 600); err != nil {
		t.Fatalf("SetSpindown(600) failed: %v", err)
	}
	if err := m.SetSpindown("sdd", -5); err == nil {
		t.Errorf("expected negative spindown to return error")
	}

	// Sync hd-idle directly (mockMode SetSpindown skips real systemctl restart)
	m.mu.Lock()
	m.syncHdIdleConfigLocked()
	m.mu.Unlock()
	hdData, err := os.ReadFile(hdIdlePath)
	if err != nil {
		t.Fatal(err)
	}
	hdStr := string(hdData)
	if !strings.Contains(hdStr, "-i 0 -c ata") || !strings.Contains(hdStr, "-a sdb -i 600") {
		t.Errorf("unexpected hd-idle config: %s", hdStr)
	}

	// Disks without /dev/disk/by-id/... symlink (e.g. VM/QEMU disks) must emit bare kernel name ("sde", not "/dev/sde")
	m.mu.Lock()
	m.cachedDisks["sde"] = &DiskInfo{
		Name:            "sde",
		Path:            "/dev/sde",
		ByIDPath:        "/dev/sde",
		IsRotational:    true,
		SpindownSeconds: 1200,
	}
	m.syncHdIdleConfigLocked()
	m.mu.Unlock()
	hdDataFallback, err := os.ReadFile(hdIdlePath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(hdDataFallback), "-a sde -i 1200") || strings.Contains(string(hdDataFallback), "-a /dev/sde") {
		t.Errorf("expected bare kernel device fallback '-a sde -i 1200' without '/dev/', got: %s", string(hdDataFallback))
	}

	// Reload in a brand new Manager (simulates nasd restart / scan)
	m2 := NewManager(true, hdIdlePath)
	m2.SetConfigPath(cfgPath)
	disks2, err := m2.ListDisks()
	if err != nil {
		t.Fatal(err)
	}
	var sdc2, sdb2 *DiskInfo
	for _, d := range disks2 {
		if d.Name == "sdc" {
			sdc2 = d
		} else if d.Name == "sdb" {
			sdb2 = d
		}
	}
	if sdc2 == nil || sdc2.AdvPowerManagement != "Disabled" || sdc2.SpindownSeconds != 0 || sdc2.SpindownTimeoutSeconds != 0 {
		t.Fatalf("expected sdc persisted settings (Disabled, 0s) after reload, got %+v", sdc2)
	}
	if sdc2.ExtendedPowerConditions != "Disabled" {
		t.Fatalf("expected sdc2 persisted EPC=Disabled after reload, got %q", sdc2.ExtendedPowerConditions)
	}
	if sdb2 == nil || sdb2.SpindownSeconds != 600 || sdb2.SpindownTimeoutSeconds != 600 {
		t.Fatalf("expected sdb persisted spindown=600 after reload, got %+v", sdb2)
	}
	if sdb2.ExtendedPowerConditions != "Enabled" {
		t.Fatalf("expected sdb2 persisted EPC=Enabled after reload, got %q", sdb2.ExtendedPowerConditions)
	}

	// 3. Legacy map[string]int disks.json backward compatibility
	legacyPath := filepath.Join(tmpDir, "legacy-disks.json")
	if err := os.WriteFile(legacyPath, []byte(`{"sdc": 300}`), 0644); err != nil {
		t.Fatal(err)
	}
	m3 := NewManager(true, "")
	m3.SetConfigPath(legacyPath)
	for _, d := range func() []*DiskInfo { l, _ := m3.ListDisks(); return l }() {
		if d.Name == "sdc" && d.SpindownSeconds != 300 {
			t.Fatalf("expected legacy spindown 300 for sdc, got %d", d.SpindownSeconds)
		}
	}
}

func TestDiskStandbyZeroIOPreservation(t *testing.T) {
	m := NewManager(true, "")
	if err := m.ForceStandby("sdc"); err != nil {
		t.Fatalf("ForceStandby failed: %v", err)
	}

	m.mu.RLock()
	sdc := m.cachedDisks["sdc"]
	m.mu.RUnlock()

	if sdc.State != StateStandby || sdc.PowerState != "STANDBY" || sdc.TemperatureC != -1 {
		t.Fatalf("expected sdc in standby, got: %+v", sdc)
	}

	// Simulate mock/readBlockDevStat matching lastIOStats
	sdc.LastIOStats = "100 200 400"
	// In mock mode pollDiskStatesOnce skips, but we can verify field synchronization
	syncDiskFields(sdc)
	if sdc.PowerState != "STANDBY" {
		t.Errorf("expected PowerState to stay STANDBY, got %s", sdc.PowerState)
	}
}

func TestBlockIOInFlightAndSmartctlStandbyParser(t *testing.T) {
	if hasBlockIOInFlight("100 0 800 50 20 0 160 10 0 40 60") {
		t.Errorf("expected in_flight=0 to return false")
	}
	if !hasBlockIOInFlight("100 0 800 50 20 0 160 10 2 40 60") {
		t.Errorf("expected in_flight=2 to return true")
	}

	// 1. Standby exit code 2
	if st, ok := parseSmartctlStandbyOutput([]byte("Device is in STANDBY mode, exit(2)\n"), 2, errors.New("exit status 2")); !ok || st != StateStandby {
		t.Errorf("expected StateStandby for exit 2 standby output, got (%v, %v)", st, ok)
	}
	// 2. Active drive with historical error log entry (bitmask exit code 64) must be recognized as ACTIVE without falling back to hdparm -C
	activeBitmaskOut := []byte("Device Model:     WDC WD120EFBX-68B0EN0\nSerial Number:    WD-12345\nPower mode is:    ACTIVE or IDLE\n")
	if st, ok := parseSmartctlStandbyOutput(activeBitmaskOut, 64, errors.New("exit status 64")); !ok || st != StateActive {
		t.Errorf("expected StateActive for bitmask 64 active output, got (%v, %v)", st, ok)
	}
	// 3. LSI HBA requiring -d sat (exit code 2 with open failure) must return ok=false so caller retries with -d sat
	hbaOpenErrOut := []byte("Smartctl open device: /dev/sdb failed: Unable to detect device type\nPlease specify device type with the -d option.\n")
	if _, ok := parseSmartctlStandbyOutput(hbaOpenErrOut, 2, errors.New("exit status 2")); ok {
		t.Errorf("expected ok=false when smartctl fails to detect device type on HBA")
	}
}
