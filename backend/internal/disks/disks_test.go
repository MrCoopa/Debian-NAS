package disks

import (
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
