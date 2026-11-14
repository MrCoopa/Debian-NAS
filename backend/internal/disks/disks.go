package disks

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"debian-nas/internal/validate"
)

type DiskState string

const (
	StateActive  DiskState = "ACTIVE"
	StateStandby DiskState = "STANDBY"
	StateSleep   DiskState = "SLEEPING"
	StateUnknown DiskState = "UNKNOWN"
)

type DiskInfo struct {
	Name                   string    `json:"name"`                     // e.g. "sda"
	Device                 string    `json:"device"`                   // alias for name or dev path
	Path                   string    `json:"path"`                     // e.g. "/dev/sda"
	ByIDPath               string    `json:"by_id_path"`               // e.g. "/dev/disk/by-id/ata-WDC_WD40EFRX..."
	Vendor                 string    `json:"vendor"`                   // e.g. "WDC", "Seagate", "Samsung"
	Model                  string    `json:"model"`                    // e.g. "WDC WD40EFRX-68N32N0"
	Serial                 string    `json:"serial"`                   // e.g. "WD-WCC7K1234567"
	SizeBytes              uint64    `json:"size_bytes"`               // e.g. 4000787030016
	SizeHuman              string    `json:"size_human"`               // e.g. "3.64 TB"
	IsRotational           bool      `json:"is_rotational"`            // true = HDD, false = SSD/NVMe
	State                  DiskState `json:"state"`                    // ACTIVE, STANDBY, SLEEPING
	PowerState             string    `json:"power_state"`              // ACTIVE, STANDBY, SLEEP
	TemperatureC           int       `json:"temperature_c"`            // -1 if sleeping
	SpindownSeconds        int       `json:"spindown_seconds"`         // configured timeout
	SpindownTimeoutSeconds int       `json:"spindown_timeout_seconds"` // alias for frontend drawer
	ZfsPool                string    `json:"zfs_pool"`                 // pool name if in use
	Pool                   string    `json:"pool"`                     // alias for zfs_pool
	Slot                   int       `json:"slot"`                     // Physical chassis bay slot (1..X)
	Enclosure              string    `json:"enclosure"`                // e.g. "Bay 01 (Front)"
	BusType                string    `json:"bus_type"`                 // SATA 6Gb/s, SAS 12Gb/s, NVMe PCIe 4.0
	SmartStatus            string    `json:"smart_status"`             // PASSED, WARNING, STANDBY
	SmartLastTest          string    `json:"smart_last_test"`          // Last self-test status
	PowerOnHours           int       `json:"power_on_hours"`           // e.g. 14200
	ReallocatedSectors     int       `json:"reallocated_sectors"`      // 0
	PendingSectors         int       `json:"pending_sectors"`          // 0
	LocateLed              bool      `json:"locate_led"`               // LED blinking
	VdevType               string    `json:"vdev_type"`                // mirror-0, raidz1, single, unassigned
	AdvPowerManagement     string    `json:"adv_power_management"`     // "Disabled", "128", etc.
	HasAPM                 bool      `json:"has_apm"`                  // true if APM supported
	HasEPC                 bool      `json:"has_epc"`                  // true if Extended Power Conditions (EPC) supported
	ExtendedPowerConditions string   `json:"extended_power_conditions"` // "Disabled", "Enabled", "Default"
	EPCStatus              string    `json:"epc_status"`               // "Supported", "Enabled", "Not Supported"
	IsSystemDisk            bool      `json:"is_system_disk"`            // true = contains OS mount (/ or /boot)
	LastIOStats             string    `json:"-"`                         // snapshot of /sys/block/<name>/stat to prevent waking standby drives
	LastWakeTime            time.Time `json:"-"`                         // timestamp when drive transitioned from standby/sleep to active
	pendingHardwareSync     bool      `json:"-"`                         // true if saved APM/EPC/Spindown was deferred while disk was sleeping
	LastStateCheck          time.Time `json:"last_state_check"`
	LastSmartCheck          time.Time `json:"-"`
}

func syncDiskFields(d *DiskInfo) {
	if d == nil {
		return
	}
	if d.Device == "" {
		d.Device = d.Name
	}
	if d.Path == "" && d.Name != "" {
		d.Path = "/dev/" + d.Name
	}
	switch d.State {
	case StateStandby:
		d.PowerState = "STANDBY"
	case StateSleep:
		d.PowerState = "SLEEP"
	case StateActive:
		d.PowerState = "ACTIVE"
	default:
		d.PowerState = string(d.State)
	}
	d.SpindownTimeoutSeconds = d.SpindownSeconds
	if d.ZfsPool != "" && d.Pool == "" {
		d.Pool = d.ZfsPool
	} else if d.Pool != "" && d.ZfsPool == "" {
		d.ZfsPool = d.Pool
	}
	if d.AdvPowerManagement == "" || strings.EqualFold(d.AdvPowerManagement, "255") || strings.EqualFold(d.AdvPowerManagement, "off") {
		d.AdvPowerManagement = "Disabled"
	}
	if d.AdvPowerManagement != "" && !strings.EqualFold(d.AdvPowerManagement, "not supported") {
		d.HasAPM = true
	}
	if d.ExtendedPowerConditions == "" {
		d.ExtendedPowerConditions = "Disabled"
	}
	if d.EPCStatus == "" {
		if d.HasEPC {
			d.EPCStatus = "Supported"
		} else if !d.IsRotational {
			d.EPCStatus = "Not Supported (SSD)"
		} else {
			d.EPCStatus = "Supported"
		}
	}
	if d.IsRotational {
		d.HasEPC = true
	}
	if d.IsSystemDisk && d.ZfsPool == "" {
		d.ZfsPool = "boot-pool"
	}
	if d.Vendor == "" && d.Model != "" {
		parts := strings.Fields(d.Model)
		if len(parts) > 0 {
			d.Vendor = parts[0]
		}
	}
}

// DiskPowerConfig stores persisted per-disk power settings across scans and reboots.
type DiskPowerConfig struct {
	Name                    string `json:"name,omitempty"`
	ByIDPath                string `json:"by_id_path,omitempty"`
	Serial                  string `json:"serial,omitempty"`
	SpindownSeconds         *int   `json:"spindown_seconds,omitempty"`
	AdvPowerManagement      string `json:"adv_power_management,omitempty"`
	ExtendedPowerConditions string `json:"extended_power_conditions,omitempty"`
}

// NormalizeEPCMode validates and normalizes an EPC setting ("Disabled", "Enabled", "Default").
func NormalizeEPCMode(raw string) (string, error) {
	v := strings.TrimSpace(raw)
	switch strings.ToLower(v) {
	case "disabled", "off", "0", "false":
		return "Disabled", nil
	case "enabled", "on", "1", "true":
		return "Enabled", nil
	case "", "default":
		return "Default", nil
	default:
		return "", fmt.Errorf("invalid EPC mode '%s': must be Disabled, Enabled or Default", raw)
	}
}

// NormalizeAPMLevel validates and normalizes an APM level ("Disabled"/"255" or "1".."254").
func NormalizeAPMLevel(raw string) (displayVal string, hdparmArg string, err error) {
	s := strings.TrimSpace(raw)
	if s == "" || strings.EqualFold(s, "disabled") || strings.EqualFold(s, "off") || s == "255" {
		return "Disabled", "255", nil
	}
	n, convErr := strconv.Atoi(s)
	if convErr != nil || n < 1 || n > 254 {
		return "", "", fmt.Errorf("invalid APM level '%s': must be Disabled (255) or 1..254", raw)
	}
	norm := strconv.Itoa(n)
	return norm, norm, nil
}

type Manager struct {
	mu               sync.RWMutex
	pollMu           sync.Mutex
	mockMode         bool
	cachedDisks      map[string]*DiskInfo
	savedConfigs     map[string]DiskPowerConfig
	hdIdleConfig     string
	configPath       string
	customConfigPath bool
}

func NewManager(mockMode bool, hdIdleConfig string) *Manager {
	m := &Manager{
		mockMode:     mockMode,
		cachedDisks:  make(map[string]*DiskInfo),
		savedConfigs: make(map[string]DiskPowerConfig),
		hdIdleConfig: hdIdleConfig,
		configPath:   "/etc/nas/disks.json",
	}

	if m.mockMode {
		m.initMockDisks()
	} else {
		m.mu.Lock()
		m.loadSavedDiskConfigLocked()
		_, _ = m.scanLinuxDisks()
		m.syncHdIdleConfigLocked()
		toApply := m.snapshotSavedApplyTargetsLocked()
		m.mu.Unlock()
		m.applyDiskConfigsToHardware(toApply)
	}

	// Start background non-intrusive status poll loop
	go m.startMonitorLoop()

	return m
}

// SetConfigPath sets a custom persistence file path (primarily for testing).
func (m *Manager) SetConfigPath(path string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.configPath = path
	m.customConfigPath = path != ""
	if path != "" {
		m.loadSavedDiskConfigLocked()
		for _, d := range m.cachedDisks {
			m.applySavedConfigToDiskLocked(d)
			syncDiskFields(d)
		}
	}
}

func (m *Manager) initMockDisks() {
	m.cachedDisks = map[string]*DiskInfo{
		"sda": {
			Name:               "sda",
			Path:               "/dev/sda",
			ByIDPath:           "/dev/disk/by-id/scsi-0QEMU_QEMU_HARDDISK_drive-scsi0",
			Model:              "QEMU HARDDISK (drive-scsi0)",
			Serial:             "drive-scsi0",
			SizeBytes:          17179869184, // 16 GiB
			SizeHuman:          "16 GiB",
			IsRotational:       false,
			IsSystemDisk:       true,
			AdvPowerManagement: "Disabled",
			State:              StateActive,
			TemperatureC:       30,
			SpindownSeconds:    0,
			ZfsPool:            "boot-pool",
			Pool:               "boot-pool",
			Slot:               1,
			Enclosure:          "Slot 01 (Boot)",
			BusType:            "SCSI / QEMU",
			SmartStatus:        "PASSED",
			PowerOnHours:       1200,
			ReallocatedSectors: 0,
			PendingSectors:     0,
			LocateLed:          false,
			VdevType:           "stripe",
			LastStateCheck:     time.Now(),
		},
		"sdb": {
			Name:               "sdb",
			Path:               "/dev/sdb",
			ByIDPath:           "/dev/disk/by-id/ata-WDC_WD20EFAX-68B29N0_WD-WCC4M124",
			Model:              "WDC WD20EFAX-68B29N0 (Red Plus)",
			Serial:             "WD-WCC4M124",
			SizeBytes:          2000398934016,
			SizeHuman:          "1.82 TiB",
			IsRotational:       true,
			AdvPowerManagement: "128",
			State:              StateStandby,
			TemperatureC:       -1, // asleep
			SpindownSeconds:    900,
			ZfsPool:            "DATA",
			Slot:               2,
			Enclosure:          "Bay 02 (Front Hot-Swap)",
			BusType:            "SATA 6.0 Gb/s (Port 1)",
			SmartStatus:        "PASSED (Standby Safe)",
			PowerOnHours:       11240,
			ReallocatedSectors: 0,
			PendingSectors:     0,
			LocateLed:          false,
			VdevType:           "mirror-0",
			LastStateCheck:     time.Now(),
		},
		"sdc": {
			Name:               "sdc",
			Path:               "/dev/sdc",
			ByIDPath:           "/dev/disk/by-id/ata-TOSHIBA_MG08ACA12TE_20E0A099",
			Model:              "TOSHIBA Enterprise MG08ACA12TE",
			Serial:             "20E0A09931AB",
			SizeBytes:          12000138625024,
			SizeHuman:          "10.91 TiB",
			IsRotational:       true,
			AdvPowerManagement: "128",
			State:              StateStandby,
			TemperatureC:       -1, // asleep
			SpindownSeconds:    900,
			ZfsPool:            "MEDIA-12TB",
			Slot:               3,
			Enclosure:          "Bay 03 (Front Hot-Swap)",
			BusType:            "SATA 6.0 Gb/s (Port 2)",
			SmartStatus:        "PASSED (Standby Safe)",
			PowerOnHours:       8420,
			ReallocatedSectors: 0,
			PendingSectors:     0,
			LocateLed:          false,
			VdevType:           "stripe",
			LastStateCheck:     time.Now(),
		},
		"sdd": {
			Name:               "sdd",
			Path:               "/dev/sdd",
			ByIDPath:           "/dev/disk/by-id/ata-Seagate_Exos_X18_16TB_ZR510XYZ",
			Model:              "Seagate Exos X18 16TB Enterprise",
			Serial:             "ZR510XYZ9912",
			SizeBytes:          16000900661248,
			SizeHuman:          "14.55 TiB",
			IsRotational:       true,
			AdvPowerManagement: "128",
			State:              StateActive,
			TemperatureC:       31,
			SpindownSeconds:    1200,
			ZfsPool:            "",
			Slot:               4,
			Enclosure:          "Bay 04 (Front Hot-Swap)",
			BusType:            "SAS 12.0 Gb/s (LSI HBA)",
			SmartStatus:        "PASSED",
			PowerOnHours:       3100,
			ReallocatedSectors: 0,
			PendingSectors:     0,
			LocateLed:          false,
			VdevType:           "unassigned (Ready for Pool)",
			LastStateCheck:     time.Now(),
		},
		"sde": {
			Name:               "sde",
			Path:               "/dev/sde",
			ByIDPath:           "/dev/disk/by-id/ata-WDC_WD20EFAX-68B29N0_Y3J2S4HF5BWJ",
			Model:              "WDC WD20EFAX-68B29N0 (Red Plus)",
			Serial:             "Y3J2S4HF5BWJ",
			SizeBytes:          2000398934016, // 1.82 TiB
			SizeHuman:          "1.82 TiB",
			IsRotational:       true,
			AdvPowerManagement: "128",
			State:              StateActive,
			TemperatureC:       32,
			SpindownSeconds:    0,
			ZfsPool:            "DATA",
			Pool:               "DATA",
			Slot:               5,
			Enclosure:          "Bay 05 (Front Hot-Swap)",
			BusType:            "SATA 6.0 Gb/s",
			SmartStatus:        "PASSED",
			PowerOnHours:       3100,
			ReallocatedSectors: 0,
			PendingSectors:     0,
			LocateLed:          false,
			VdevType:           "mirror-0",
			LastStateCheck:     time.Now(),
		},
		"sdf": {
			Name:               "sdf",
			Path:               "/dev/sdf",
			ByIDPath:           "/dev/disk/by-id/ata-WDC_WD20EFAX-68B29N0_Y3J2S4HHSBWJ",
			Model:              "WDC WD20EFAX-68B29N0 (Red Plus)",
			Serial:             "Y3J2S4HHSBWJ",
			SizeBytes:          2000398934016, // 1.82 TiB
			SizeHuman:          "1.82 TiB",
			IsRotational:       true,
			AdvPowerManagement: "128",
			State:              StateActive,
			TemperatureC:       30,
			SpindownSeconds:    0,
			ZfsPool:            "DATA",
			Pool:               "DATA",
			Slot:               6,
			Enclosure:          "Bay 06 (Front Hot-Swap)",
			BusType:            "SATA 6.0 Gb/s",
			SmartStatus:        "PASSED",
			PowerOnHours:       3100,
			ReallocatedSectors: 0,
			PendingSectors:     0,
			LocateLed:          false,
			VdevType:           "mirror-0",
			LastStateCheck:     time.Now(),
		},
		"nvme0n1": {
			Name:               "nvme0n1",
			Path:               "/dev/nvme0n1",
			ByIDPath:           "/dev/disk/by-id/nvme-Samsung_SSD_980_PRO_1TB_S5GXNF0T111",
			Model:              "Samsung SSD 980 PRO 1TB NVMe",
			Serial:             "S5GXNF0T111",
			SizeBytes:          1000204886016,
			SizeHuman:          "931.51 GiB",
			IsRotational:       false,
			State:              StateActive,
			TemperatureC:       34,
			SpindownSeconds:    0,
			ZfsPool:            "SSD-MIRROR",
			Slot:               7,
			Enclosure:          "M.2 NVMe Slot 1 (PCIe 4.0 x4)",
			BusType:            "PCIe Gen4 x4 NVMe",
			SmartStatus:        "PASSED (100% Health)",
			PowerOnHours:       4200,
			ReallocatedSectors: 0,
			PendingSectors:     0,
			LocateLed:          false,
			VdevType:           "mirror-0",
			LastStateCheck:     time.Now(),
		},
		"nvme1n1": {
			Name:               "nvme1n1",
			Path:               "/dev/nvme1n1",
			ByIDPath:           "/dev/disk/by-id/nvme-Samsung_SSD_980_PRO_1TB_S5GXNF0T112",
			Model:              "Samsung SSD 980 PRO 1TB NVMe",
			Serial:             "S5GXNF0T112",
			SizeBytes:          1000204886016,
			SizeHuman:          "931.51 GiB",
			IsRotational:       false,
			State:              StateActive,
			TemperatureC:       35,
			SpindownSeconds:    0,
			ZfsPool:            "SSD-MIRROR",
			Slot:               8,
			Enclosure:          "M.2 NVMe Slot 2 (PCIe 4.0 x4)",
			BusType:            "PCIe Gen4 x4 NVMe",
			SmartStatus:        "PASSED (100% Health)",
			PowerOnHours:       4200,
			ReallocatedSectors: 0,
			PendingSectors:     0,
			LocateLed:          false,
			VdevType:           "mirror-0",
			LastStateCheck:     time.Now(),
		},
	}
	for _, d := range m.cachedDisks {
		syncDiskFields(d)
	}
}

// SyncPoolMembership updates disk ZFS pool assignments from active pools
func (m *Manager) SyncPoolMembership(poolMap map[string]string, roleMap map[string]string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, d := range m.cachedDisks {
		byIDBase := filepath.Base(d.ByIDPath)
		pool := poolMap[d.Name]
		if pool == "" {
			pool = poolMap[d.Path]
		}
		if pool == "" && byIDBase != "" {
			pool = poolMap[byIDBase]
		}
		if pool == "" && d.ByIDPath != "" {
			pool = poolMap[d.ByIDPath]
		}

		d.ZfsPool = pool
		d.Pool = pool
		if pool != "" {
			if role, ok := roleMap[d.Name]; ok && role != "" {
				d.VdevType = role
			} else if role, ok := roleMap[byIDBase]; ok && role != "" {
				d.VdevType = role
			} else {
				d.VdevType = "assigned (" + pool + ")"
			}
		} else if d.IsSystemDisk {
			d.VdevType = "OS / System Drive"
		} else {
			d.VdevType = "unassigned (Ready for Pool)"
		}
		syncDiskFields(d)
	}
}

func isManagedBlockDevice(name string) bool {
	return strings.HasPrefix(name, "sd") ||
		strings.HasPrefix(name, "nvme") ||
		strings.HasPrefix(name, "vd") ||
		strings.HasPrefix(name, "xvd")
}

func readBlockDevStat(name string) string {
	data, err := os.ReadFile(filepath.Join("/sys/block", name, "stat"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// hasBlockIOInFlight checks whether field 9 (index 8, in_flight I/O requests) in /sys/block/<dev>/stat is > 0.
func hasBlockIOInFlight(stat string) bool {
	fields := strings.Fields(stat)
	if len(fields) < 9 {
		return false
	}
	inFlight, err := strconv.ParseUint(fields[8], 10, 64)
	return err == nil && inFlight > 0
}

// linuxBlockDeviceSetChangedLocked reports whether /sys/block managed devices differ from cachedDisks.
// Caller must hold m.mu.RLock or m.mu.Lock.
func (m *Manager) linuxBlockDeviceSetChangedLocked() bool {
	entries, err := os.ReadDir("/sys/block")
	if err != nil {
		return false
	}
	managedCount := 0
	for _, entry := range entries {
		name := entry.Name()
		if !isManagedBlockDevice(name) {
			continue
		}
		if _, ok := m.cachedDisks[name]; !ok {
			return true
		}
		managedCount++
	}
	cachedCount := 0
	for name := range m.cachedDisks {
		if isManagedBlockDevice(name) {
			cachedCount++
		}
	}
	return managedCount != cachedCount
}

// ListDisks enumerates all block devices without waking up sleeping HDDs.
func (m *Manager) ListDisks() ([]*DiskInfo, error) {
	m.mu.RLock()
	if len(m.cachedDisks) > 0 && (m.mockMode || !m.linuxBlockDeviceSetChangedLocked()) {
		var list []*DiskInfo
		for _, d := range m.cachedDisks {
			list = append(list, d)
		}
		m.mu.RUnlock()
		sort.Slice(list, func(i, j int) bool {
			return list[i].Name < list[j].Name
		})
		return list, nil
	}
	m.mu.RUnlock()

	m.mu.Lock()
	defer m.mu.Unlock()

	if len(m.cachedDisks) > 0 && (m.mockMode || !m.linuxBlockDeviceSetChangedLocked()) {
		var list []*DiskInfo
		for _, d := range m.cachedDisks {
			syncDiskFields(d)
			list = append(list, d)
		}
		sort.Slice(list, func(i, j int) bool {
			return list[i].Name < list[j].Name
		})
		return list, nil
	}

	if m.mockMode {
		var list []*DiskInfo
		for _, d := range m.cachedDisks {
			syncDiskFields(d)
			list = append(list, d)
		}
		sort.Slice(list, func(i, j int) bool {
			return list[i].Name < list[j].Name
		})
		return list, nil
	}

	disks, err := m.scanLinuxDisks()
	if err == nil {
		sort.Slice(disks, func(i, j int) bool {
			return disks[i].Name < disks[j].Name
		})
	}
	return disks, err
}

// RescanDisks forces a hardware rescan of all block devices
func (m *Manager) RescanDisks() ([]*DiskInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.mockMode {
		var list []*DiskInfo
		for _, d := range m.cachedDisks {
			syncDiskFields(d)
			list = append(list, d)
		}
		sort.Slice(list, func(i, j int) bool {
			return list[i].Name < list[j].Name
		})
		return list, nil
	}

	disks, err := m.scanLinuxDisks()
	if err == nil {
		sort.Slice(disks, func(i, j int) bool {
			return disks[i].Name < disks[j].Name
		})
	}
	return disks, err
}

func (m *Manager) scanLinuxDisks() ([]*DiskInfo, error) {
	blockDir := "/sys/block"
	entries, err := os.ReadDir(blockDir)
	if err != nil {
		return nil, fmt.Errorf("reading /sys/block: %w", err)
	}

	byIDMap := m.buildByIDMap()
	poolMap := m.buildDiskPoolMap()
	sysMap := m.buildSystemDiskMap()
	seen := make(map[string]bool)
	var disks []*DiskInfo

	for _, entry := range entries {
		name := entry.Name()
		// Only consider sdX, nvmeXnY, vdX, xvdX
		if !isManagedBlockDevice(name) {
			continue
		}
		seen[name] = true

		sysPath := filepath.Join(blockDir, name)

		// Check if rotational (HDD vs SSD)
		rotational := false
		rotData, err := os.ReadFile(filepath.Join(sysPath, "queue", "rotational"))
		if err == nil && strings.TrimSpace(string(rotData)) == "1" {
			rotational = true
		}

		// Ensure SCSI block timeout is at least 180s and allow_restart=1 for standard block drives (sd[a-z])
		// so SAS HBAs (e.g. LSI 9500-8i) automatically issue START STOP UNIT on 'Sense Key: Not Ready' instead of failing I/O to ZFS
		if strings.HasPrefix(name, "sd") && runtime.GOOS == "linux" {
			timeoutPath := filepath.Join(sysPath, "device", "timeout")
			if tData, err := os.ReadFile(timeoutPath); err == nil {
				curSec, _ := strconv.Atoi(strings.TrimSpace(string(tData)))
				if curSec > 0 && curSec < 180 {
					_ = os.WriteFile(timeoutPath, []byte("180\n"), 0644)
				}
			}
			allowRestartPath := filepath.Join(sysPath, "device", "allow_restart")
			if rData, err := os.ReadFile(allowRestartPath); err == nil {
				if strings.TrimSpace(string(rData)) != "1" {
					_ = os.WriteFile(allowRestartPath, []byte("1\n"), 0644)
				}
			}
		}

		// Read size in 512-byte sectors
		sizeData, err := os.ReadFile(filepath.Join(sysPath, "size"))
		var sizeBytes uint64
		if err == nil {
			sectors, _ := strconv.ParseUint(strings.TrimSpace(string(sizeData)), 10, 64)
			sizeBytes = sectors * 512
		}

		// Read model and vendor
		model := m.readSysfsString(filepath.Join(sysPath, "device", "model"))
		vendor := m.readSysfsString(filepath.Join(sysPath, "device", "vendor"))
		serial := m.readSysfsString(filepath.Join(sysPath, "device", "serial"))
		fullModel := strings.TrimSpace(vendor + " " + model)
		if fullModel == "" {
			fullModel = name
		}

		devPath := "/dev/" + name
		byID := byIDMap[name]
		if byID == "" {
			byID = devPath
		}

		// Safe Standby Check (hdparm -C or smartctl -n standby)
		state := StateUnknown
		temp := -1
		powerHours := 0
		realloc := 0
		pending := 0
		smartStatus := "PASSED (Standby Safe)"
		smartLastTest := ""
		curIOStat := readBlockDevStat(name)
		lastIOStat := ""

		existing, hasExisting := m.cachedDisks[name]
		if hasExisting {
			lastIOStat = existing.LastIOStats
		}

		if hasExisting && (existing.State == StateStandby || existing.State == StateSleep) && curIOStat != "" && lastIOStat != "" && curIOStat == lastIOStat && !hasBlockIOInFlight(curIOStat) {
			// Zero block I/O has occurred since the disk went to standby.
			// Never ping hardware with smartctl or hdparm, preserve sleeping state!
			state = existing.State
			temp = -1
			smartStatus = existing.SmartStatus
			smartLastTest = existing.SmartLastTest
			powerHours = existing.PowerOnHours
		} else if rotational {
			state = m.checkStandbySafe(devPath)
			if state == StateActive {
				temp, powerHours, realloc, pending, smartStatus, smartLastTest = m.readSmartAttributesSafe(devPath)
			}
			if curIOStat != "" {
				lastIOStat = curIOStat
			}
		} else {
			state = StateActive
			temp, powerHours, realloc, pending, smartStatus, smartLastTest = m.readSmartAttributesSafe(devPath)
			if curIOStat != "" {
				lastIOStat = curIOStat
			}
		}

		poolName := poolMap[name]
		if poolName == "" {
			poolName = poolMap[filepath.Base(byID)]
		}

		spindownSec := 0
		if rotational {
			spindownSec = 1800
		}
		advPowerMgmt := "Disabled"
		epcMode := "Disabled"
		locateLed := false
		var lastWakeTime time.Time
		var pendingHwSync bool
		if saved, ok := m.lookupSavedConfigLocked(name, byID, serial); ok {
			if saved.SpindownSeconds != nil && *saved.SpindownSeconds >= 0 {
				spindownSec = *saved.SpindownSeconds
			}
			if saved.AdvPowerManagement != "" {
				advPowerMgmt = saved.AdvPowerManagement
			}
			if saved.ExtendedPowerConditions != "" {
				epcMode = saved.ExtendedPowerConditions
			}
		} else if existing, exists := m.cachedDisks[name]; exists {
			if existing.SpindownSeconds >= 0 {
				spindownSec = existing.SpindownSeconds
			}
			if existing.AdvPowerManagement != "" {
				advPowerMgmt = existing.AdvPowerManagement
			} else if rotational && state == StateActive {
				advPowerMgmt = m.readHardwareAPMSafe(devPath)
			}
			if existing.ExtendedPowerConditions != "" {
				epcMode = existing.ExtendedPowerConditions
			}
			locateLed = existing.LocateLed
			if smartLastTest == "" && existing.SmartLastTest != "" {
				smartLastTest = existing.SmartLastTest
			}
		} else if rotational && state == StateActive {
			advPowerMgmt = m.readHardwareAPMSafe(devPath)
		}
		if existing, exists := m.cachedDisks[name]; exists {
			locateLed = existing.LocateLed
			lastWakeTime = existing.LastWakeTime
			pendingHwSync = existing.pendingHardwareSync
			if smartLastTest == "" && existing.SmartLastTest != "" {
				smartLastTest = existing.SmartLastTest
			}
		}

		slotIdx := len(disks) + 1
		busType := m.detectDiskBusType(name, sysPath, byID)

		isSystem := sysMap[name] || sysMap[devPath] || sysMap[filepath.Base(byID)]
		vdevType := "unassigned (Ready for Pool)"
		if poolName != "" {
			vdevType = "assigned (" + poolName + ")"
		} else if isSystem {
			vdevType = "OS / System Drive"
		}

		disk := &DiskInfo{
			Name:                    name,
			Device:                  name,
			Path:                    devPath,
			ByIDPath:                byID,
			Vendor:                  vendor,
			Model:                   fullModel,
			Serial:                  serial,
			SizeBytes:               sizeBytes,
			SizeHuman:               formatBytes(sizeBytes),
			IsRotational:            rotational,
			State:                   state,
			TemperatureC:            temp,
			SpindownSeconds:         spindownSec,
			AdvPowerManagement:      advPowerMgmt,
			ExtendedPowerConditions: epcMode,
			EPCStatus:               epcMode,
			ZfsPool:                 poolName,
			Pool:                    poolName,
			Slot:                    slotIdx,
			Enclosure:               fmt.Sprintf("Bay %02d (Front Hot-Swap)", slotIdx),
			BusType:                 busType,
			SmartStatus:             smartStatus,
			SmartLastTest:           smartLastTest,
			PowerOnHours:            powerHours,
			ReallocatedSectors:      realloc,
			PendingSectors:          pending,
			LocateLed:               locateLed,
			VdevType:                vdevType,
			IsSystemDisk:            isSystem,
			LastIOStats:             lastIOStat,
			LastWakeTime:            lastWakeTime,
			pendingHardwareSync:     pendingHwSync,
			LastStateCheck:          time.Now(),
		}
		syncDiskFields(disk)

		disks = append(disks, disk)
		m.cachedDisks[name] = disk
	}

	for name := range m.cachedDisks {
		if !seen[name] {
			delete(m.cachedDisks, name)
		}
	}

	return disks, nil
}

// detectDiskBusType identifies whether a block device is connected via NVMe, an LSI/Broadcom SAS HBA (mpt3sas/mpi3mr), or onboard SATA.
func (m *Manager) detectDiskBusType(name, sysPath, byID string) string {
	if strings.HasPrefix(name, "nvme") {
		return "PCIe Gen4 x4 NVMe"
	}
	isSasHba := strings.Contains(byID, "sas-")
	if !isSasHba {
		if sasAddr := m.readSysfsString(filepath.Join(sysPath, "device", "sas_address")); sasAddr != "" {
			isSasHba = true
		} else if resolved, err := filepath.EvalSymlinks(sysPath); err == nil {
			lowerRes := strings.ToLower(resolved)
			if strings.Contains(lowerRes, "end_device-") || strings.Contains(lowerRes, "sas") || strings.Contains(lowerRes, "mpt") || strings.Contains(lowerRes, "mpi3") {
				isSasHba = true
			}
		}
	}
	if isSasHba {
		if strings.Contains(byID, "ata-") {
			return "SATA via LSI SAS HBA (12 Gb/s)"
		}
		return "SAS 12.0 Gb/s (LSI HBA)"
	}
	return "SATA 6.0 Gb/s"
}

// parseSmartctlStandbyOutput determines DiskState from 'smartctl -n standby -i' output and exit code.
func parseSmartctlStandbyOutput(out []byte, exitCode int, runErr error) (DiskState, bool) {
	lower := strings.ToLower(string(out))
	if strings.Contains(lower, "device is in sleep") || strings.Contains(lower, "power mode is:   sleep") {
		return StateSleep, true
	}
	if strings.Contains(lower, "standby") || strings.Contains(lower, "standby_z") ||
		strings.Contains(lower, "standby_y") || strings.Contains(lower, "idle_b") ||
		strings.Contains(lower, "idle_c") || strings.Contains(lower, "low-power mode") {
		return StateStandby, true
	}
	if strings.Contains(lower, "sleep") && !strings.Contains(lower, "never") {
		return StateSleep, true
	}
	openFailed := strings.Contains(lower, "unable to detect device type") ||
		strings.Contains(lower, "open device:") ||
		strings.Contains(lower, "specify device type with the -d option")
	if exitCode == 2 && !openFailed {
		return StateStandby, true
	}
	if runErr == nil && !openFailed && len(out) > 0 {
		return StateActive, true
	}
	// smartctl uses a bitmask exit code (e.g. bit 6 = 64 for historical error log entries, bit 5 = 32 for past threshold).
	// If smartctl successfully identified the drive without reporting standby/sleep, the drive is ACTIVE.
	if !openFailed && (exitCode&2) == 0 && (strings.Contains(lower, "device model:") ||
		strings.Contains(lower, "model number:") ||
		strings.Contains(lower, "product:") ||
		strings.Contains(lower, "serial number:") ||
		strings.Contains(lower, "user capacity:") ||
		strings.Contains(lower, "active") ||
		strings.Contains(lower, "idle")) {
		return StateActive, true
	}
	return StateUnknown, false
}

// checkStandbySafe queries drive status without spinning up sleeping HDDs.
// It prioritizes smartctl with '-n standby' because 'hdparm -C' sends ATA CHECK POWER MODE,
// which causes a mandatory spin-up on Western Digital and SAS drive firmwares.
func (m *Manager) checkStandbySafe(devPath string) DiskState {
	sCtx, sCancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer sCancel()
	smartCmd := exec.CommandContext(sCtx, "smartctl", "-n", "standby", "-i", devPath)
	smartOut, smartErr := smartCmd.CombinedOutput()
	exitCode := 0
	if smartCmd.ProcessState != nil {
		exitCode = smartCmd.ProcessState.ExitCode()
	}
	if state, ok := parseSmartctlStandbyOutput(smartOut, exitCode, smartErr); ok {
		return state
	}

	// If smartctl could not auto-detect the device behind an LSI SAS HBA (mpt3sas), try explicit '-d sat'
	if !errors.Is(smartErr, exec.ErrNotFound) {
		satCtx, satCancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer satCancel()
		satCmd := exec.CommandContext(satCtx, "smartctl", "-d", "sat", "-n", "standby", "-i", devPath)
		satOut, satErr := satCmd.CombinedOutput()
		satExit := 0
		if satCmd.ProcessState != nil {
			satExit = satCmd.ProcessState.ExitCode()
		}
		if state, ok := parseSmartctlStandbyOutput(satOut, satExit, satErr); ok {
			return state
		}
		return StateUnknown
	}

	// Fallback to hdparm -C ONLY if smartctl binary is not installed on the system
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "hdparm", "-C", devPath)
	out, err := cmd.Output()
	if err == nil {
		outStr := strings.ToLower(string(out))
		if strings.Contains(outStr, "standby") {
			return StateStandby
		}
		if strings.Contains(outStr, "sleeping") {
			return StateSleep
		}
		if strings.Contains(outStr, "active") || strings.Contains(outStr, "idle") {
			return StateActive
		}
	}
	return StateUnknown
}

// readSmartAttributesSafe reads SMART attributes (ATA, NVMe, SAS) without spinning up sleeping drives
func (m *Manager) readSmartAttributesSafe(devPath string) (temp, powerOnHours, realloc, pending int, status, lastTest string) {
	temp = -1
	status = "PASSED"
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "smartctl", "-n", "standby", "-a", devPath)
	out, _ := cmd.Output()
	outLower := strings.ToLower(string(out))
	if len(out) == 0 || strings.Contains(outLower, "unable to detect device type") || strings.Contains(outLower, "specify device type") {
		satCtx, satCancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer satCancel()
		satCmd := exec.CommandContext(satCtx, "smartctl", "-d", "sat", "-n", "standby", "-a", devPath)
		if satOut, _ := satCmd.Output(); len(satOut) > 0 {
			out = satOut
		}
	}
	if len(out) == 0 {
		return
	}
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		lower := strings.ToLower(line)

		// Check overall health assessment
		if strings.Contains(lower, "overall-health self-assessment test result:") || strings.Contains(lower, "smart health status:") {
			if strings.Contains(lower, "failed") {
				status = "FAILED"
			} else if strings.Contains(lower, "passed") || strings.Contains(lower, "ok") {
				status = "PASSED"
			}
		}

		// Check NVMe / SAS temperature lines: e.g. "Temperature: 34 Celsius" or "Current Drive Temperature: 31 C"
		if strings.HasPrefix(lower, "temperature:") || strings.HasPrefix(lower, "current drive temperature:") {
			parts := strings.Fields(line)
			for _, p := range parts {
				if v, err := strconv.Atoi(strings.Trim(p, ",")); err == nil && v > 0 && v < 120 {
					temp = v
					break
				}
			}
		}

		// Check NVMe / SAS power on hours: e.g. "Power On Hours: 4,200"
		if strings.HasPrefix(lower, "power on hours:") {
			parts := strings.Fields(line)
			if len(parts) > 0 {
				cleanNum := strings.ReplaceAll(strings.ReplaceAll(parts[len(parts)-1], ",", ""), ".", "")
				if v, err := strconv.Atoi(cleanNum); err == nil && v >= 0 {
					powerOnHours = v
				}
			}
		}

		// Check self-test execution status
		if strings.Contains(lower, "self-test routine in progress") || strings.Contains(lower, "of test remaining") {
			lastTest = "Self-test in progress"
		} else if strings.HasPrefix(line, "# 1") && lastTest == "" {
			lastTest = strings.TrimSpace(strings.TrimPrefix(line, "# 1"))
		}

		// Parse ATA Attribute table (IDs 5, 9, 190/194, 197)
		fields := strings.Fields(line)
		if len(fields) >= 10 {
			id, err1 := strconv.Atoi(fields[0])
			val, err2 := strconv.Atoi(fields[9])
			if err1 != nil || err2 != nil {
				continue
			}
			switch id {
			case 5: // Reallocated_Sector_Ct
				realloc = val
			case 9: // Power_On_Hours
				powerOnHours = val
			case 194, 190: // Temperature_Celsius
				if val > 0 && val < 120 {
					temp = val
				}
			case 197: // Current_Pending_Sector
				pending = val
			}
		}
	}
	if realloc > 0 || pending > 0 {
		status = "WARNING (Sector Errors)"
	}
	return
}

func (m *Manager) findDiskLocked(devPath string) *DiskInfo {
	devPath = strings.TrimSpace(devPath)
	if devPath == "" {
		return nil
	}
	diskName := filepath.Base(devPath)
	for _, d := range m.cachedDisks {
		if d.Path == devPath || d.ByIDPath == devPath || d.Name == devPath || d.Device == devPath || d.Name == diskName {
			return d
		}
	}
	return nil
}

// TriggerSmartTest triggers a short or long SMART self-test
func (m *Manager) TriggerSmartTest(devPath, testType string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if testType != "short" && testType != "long" {
		testType = "short"
	}

	target := m.findDiskLocked(devPath)
	if target == nil {
		return fmt.Errorf("disk %s not found", devPath)
	}
	diskPath := target.Path

	if m.mockMode {
		target.SmartStatus = fmt.Sprintf("TESTING (%s Self-Test running...)", strings.ToUpper(testType))
		go func(d *DiskInfo) {
			time.Sleep(4 * time.Second)
			m.mu.Lock()
			d.SmartStatus = "PASSED (Self-Test Completed 100%)"
			m.mu.Unlock()
		}(target)
		return nil
	}

	if err := validate.DevicePath(diskPath); err != nil {
		return fmt.Errorf("invalid disk device path: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "smartctl", "-t", testType, diskPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		// Fallback for SATA drives behind LSI SAS HBAs (e.g. 9500-8i / mpt3sas) requiring explicit -d sat
		satCtx, satCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer satCancel()
		if satOut, satErr := exec.CommandContext(satCtx, "smartctl", "-d", "sat", "-t", testType, diskPath).CombinedOutput(); satErr == nil {
			out, err = satOut, nil
		}
		if err != nil {
			msg := strings.TrimSpace(string(out))
			if msg != "" {
				return fmt.Errorf("smartctl -t %s %s failed: %s", testType, diskPath, msg)
			}
			return fmt.Errorf("smartctl -t %s %s failed: %w", testType, diskPath, err)
		}
	}

	target.SmartStatus = fmt.Sprintf("TESTING (%s Self-Test running...)", strings.ToUpper(testType))
	target.SmartLastTest = fmt.Sprintf("%s self-test running", testType)
	target.LastStateCheck = time.Now()
	return nil
}

// readTemperatureSafe uses smartctl with '-n standby' so it exits with code 2 if disk is sleeping!
func (m *Manager) readTemperatureSafe(devPath string) int {
	t, _, _, _, _, _ := m.readSmartAttributesSafe(devPath)
	return t
}

func spindownSecondsToHdparmArg(seconds int) int {
	// hdparm standby value formula (-S):
	// 0 = disabled
	// 1-240 = multiples of 5 seconds (5s to 20 mins)
	// 241-251 = 1 to 11 units of 30 minutes (30m to 5.5 hours)
	// 253 = vendor-defined timeout between 8 and 12 hours
	if seconds <= 0 {
		return 0
	}
	if seconds <= 1200 {
		val := seconds / 5
		if val < 1 {
			return 1
		}
		if val > 240 {
			return 240
		}
		return val
	}
	if seconds <= 19800 {
		units := (seconds + 899) / 1800
		if units < 1 {
			units = 1
		} else if units > 11 {
			units = 11
		}
		return 240 + units
	}
	if seconds >= 28800 {
		return 253
	}
	return 251
}

// SetSpindown sets spindown timeout for a drive using hdparm and hd-idle
func (m *Manager) SetSpindown(devPath string, seconds int) error {
	if seconds < 0 {
		return fmt.Errorf("spindown timeout must be >= 0 (got %d)", seconds)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	target := m.findDiskLocked(devPath)
	if target == nil {
		return fmt.Errorf("disk %s not found", devPath)
	}
	diskPath := target.Path

	if !m.mockMode {
		if err := validate.DevicePath(diskPath); err != nil {
			return fmt.Errorf("invalid disk device path: %w", err)
		}
		hdErr := applyHardwareSpindown(diskPath, seconds)
		hasHdIdle := m.hasHdIdleConfig()
		if hdErr != nil && seconds > 0 && !hasHdIdle {
			return hdErr
		}
	}

	target.SpindownSeconds = seconds
	target.SpindownTimeoutSeconds = seconds
	target.LastStateCheck = time.Now()
	m.recordSavedConfigLocked(target)
	if !m.mockMode || m.customConfigPath {
		m.saveDiskConfigLocked()
	}
	if !m.mockMode {
		m.syncHdIdleConfigLocked()
	}
	return nil
}

// SetAPM sets Advanced Power Management level using hdparm -B
func (m *Manager) SetAPM(devPath string, level string) error {
	displayVal, hdparmArg, err := NormalizeAPMLevel(level)
	if err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	target := m.findDiskLocked(devPath)
	if target == nil {
		return fmt.Errorf("disk %s not found", devPath)
	}
	diskPath := target.Path

	if !m.mockMode {
		if err := validate.DevicePath(diskPath); err != nil {
			return fmt.Errorf("invalid disk device path: %w", err)
		}
		if err := applyHardwareAPM(diskPath, hdparmArg); err != nil {
			if displayVal != "Disabled" {
				return err
			}
			// "Disabled" means APM off; virtual/non-APM drives already have APM disabled.
		}
	}

	target.AdvPowerManagement = displayVal
	target.LastStateCheck = time.Now()
	m.recordSavedConfigLocked(target)
	if !m.mockMode || m.customConfigPath {
		m.saveDiskConfigLocked()
	}
	return nil
}

// SetEPC sets Extended Power Conditions (EPC) mode using sdparm
func (m *Manager) SetEPC(devPath string, mode string) error {
	normMode, err := NormalizeEPCMode(mode)
	if err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	target := m.findDiskLocked(devPath)
	if target == nil {
		return fmt.Errorf("disk %s not found", devPath)
	}
	diskPath := target.Path

	if !m.mockMode {
		if err := validate.DevicePath(diskPath); err != nil {
			return fmt.Errorf("invalid disk device path: %w", err)
		}
		if normMode == "Disabled" {
			_ = applyHardwareEPC(diskPath, true)
		} else if normMode == "Enabled" {
			_ = applyHardwareEPC(diskPath, false)
		}
	}

	target.ExtendedPowerConditions = normMode
	target.EPCStatus = normMode
	target.LastStateCheck = time.Now()
	m.recordSavedConfigLocked(target)
	if !m.mockMode || m.customConfigPath {
		m.saveDiskConfigLocked()
	}
	return nil
}

// ForceStandby immediately puts a drive into standby
func (m *Manager) ForceStandby(devPath string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	target := m.findDiskLocked(devPath)
	if target == nil {
		return fmt.Errorf("disk %s not found", devPath)
	}
	diskPath := target.Path

	if !m.mockMode {
		if err := validate.DevicePath(diskPath); err != nil {
			return fmt.Errorf("invalid disk device path: %w", err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "hdparm", "-y", diskPath)
		if out, err := cmd.CombinedOutput(); err != nil {
			// Fallback for LSI HBAs (mpt3sas): smartctl -s standby,now via SCSI-to-ATA Translation (SAT)
			sCtx, sCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer sCancel()
			sCmd := exec.CommandContext(sCtx, "smartctl", "-s", "standby,now", diskPath)
			if sErr := sCmd.Run(); sErr != nil {
				// Further fallback: explicit -d sat flag for LSI controllers
				satCtx, satCancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer satCancel()
				satCmd := exec.CommandContext(satCtx, "smartctl", "-d", "sat", "-s", "standby,now", diskPath)
				if satErr := satCmd.Run(); satErr != nil {
					msg := strings.TrimSpace(string(out))
					if msg != "" {
						return fmt.Errorf("standby for %s failed (hdparm and smartctl SAT failed): %s", diskPath, msg)
					}
					return fmt.Errorf("standby for %s failed: %w", diskPath, err)
				}
			}
		}
	}

	target.State = StateStandby
	target.PowerState = "STANDBY"
	target.TemperatureC = -1
	target.LastIOStats = readBlockDevStat(target.Name)
	target.LastStateCheck = time.Now()
	syncDiskFields(target)
	return nil
}

// ToggleLocate toggles the enclosure identify / locate LED for a disk (45Drives Houston feature)
func (m *Manager) ToggleLocate(devPath string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	target := m.findDiskLocked(devPath)
	if target == nil {
		return false, fmt.Errorf("disk %s not found", devPath)
	}

	nextState := !target.LocateLed

	if !m.mockMode {
		if err := validate.DevicePath(target.Path); err != nil {
			return false, fmt.Errorf("invalid disk device path: %w", err)
		}
		// Real Linux: use ledctl locate/off if available on the host
		if _, lookErr := exec.LookPath("ledctl"); lookErr == nil {
			action := "off"
			if nextState {
				action = "locate"
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "ledctl", fmt.Sprintf("%s=%s", action, target.Path))
			_ = cmd.Run()
		}
	}

	target.LocateLed = nextState
	return target.LocateLed, nil
}

// WipeDisk erases partition tables and ZFS labels from an unallocated non-system disk
func (m *Manager) WipeDisk(devPath, mode string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	target := m.findDiskLocked(devPath)
	if target == nil {
		return fmt.Errorf("disk '%s' not found", devPath)
	}
	if target.IsSystemDisk {
		return fmt.Errorf("cannot wipe system/boot disk '%s'", target.Name)
	}
	if target.ZfsPool != "" || target.Pool != "" {
		return fmt.Errorf("cannot wipe disk '%s' while it is assigned to active ZFS pool '%s'", target.Name, target.ZfsPool)
	}

	if m.mockMode {
		target.VdevType = "unassigned (Wiped Clean)"
		target.State = StateActive
		target.PowerState = "ACTIVE"
		return nil
	}

	if err := validate.DevicePath(target.Path); err != nil {
		return fmt.Errorf("invalid disk device path: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Clear ZFS labels on whole disk and common ZFS partitions (part1, part9)
	_ = exec.CommandContext(ctx, "zpool", "labelclear", "-f", target.Path).Run()
	if strings.HasPrefix(target.Name, "nvme") {
		_ = exec.CommandContext(ctx, "zpool", "labelclear", "-f", target.Path+"p1").Run()
		_ = exec.CommandContext(ctx, "zpool", "labelclear", "-f", target.Path+"p9").Run()
	} else {
		_ = exec.CommandContext(ctx, "zpool", "labelclear", "-f", target.Path+"1").Run()
		_ = exec.CommandContext(ctx, "zpool", "labelclear", "-f", target.Path+"9").Run()
	}

	if out, err := exec.CommandContext(ctx, "wipefs", "-a", "-f", target.Path).CombinedOutput(); err != nil {
		return fmt.Errorf("wipefs failed on %s: %v (%s)", target.Path, err, strings.TrimSpace(string(out)))
	}
	_ = exec.CommandContext(ctx, "sgdisk", "--zap-all", target.Path).Run()

	if mode == "zero" {
		// Zero first 16 MiB
		_ = exec.CommandContext(ctx, "dd", "if=/dev/zero", "of="+target.Path, "bs=1M", "count=16", "conv=notrunc").Run()
	}

	_ = exec.CommandContext(ctx, "partprobe", target.Path).Run()
	_ = exec.CommandContext(ctx, "udevadm", "settle").Run()

	target.VdevType = "unassigned (Wiped Clean)"
	return nil
}

func (m *Manager) startMonitorLoop() {
	ticker := time.NewTicker(4 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		if m.mockMode {
			continue
		}
		m.pollDiskStatesOnce()
	}
}

// PollStatesNow triggers an immediate state check of all disks without waking sleeping drives.
func (m *Manager) PollStatesNow() {
	if m.mockMode {
		return
	}
	m.pollDiskStatesOnce()
}

type diskPollSnapshot struct {
	name                string
	path                string
	byIDPath            string
	serial              string
	isRotational        bool
	prevState           DiskState
	needAPMProbe        bool
	lastIOStats         string
	lastWakeTime        time.Time
	lastSmartCheck      time.Time
	pendingHardwareSync bool
}

type diskApplyTarget struct {
	name         string
	path         string
	state        DiskState
	spindownSec  *int
	advPowerMgmt string
	epcMode      string
	isRotational bool
}

func (m *Manager) pollDiskStatesOnce() {
	m.pollMu.Lock()
	defer m.pollMu.Unlock()

	m.mu.RLock()
	snaps := make([]diskPollSnapshot, 0, len(m.cachedDisks))
	for _, d := range m.cachedDisks {
		_, hasSaved := m.lookupSavedConfigLocked(d.Name, d.ByIDPath, d.Serial)
		snaps = append(snaps, diskPollSnapshot{
			name:                d.Name,
			path:                d.Path,
			byIDPath:            d.ByIDPath,
			serial:              d.Serial,
			isRotational:        d.IsRotational,
			prevState:           d.State,
			needAPMProbe:        d.IsRotational && !hasSaved && d.AdvPowerManagement == "",
			lastIOStats:         d.LastIOStats,
			lastWakeTime:        d.LastWakeTime,
			lastSmartCheck:      d.LastSmartCheck,
			pendingHardwareSync: d.pendingHardwareSync,
		})
	}
	m.mu.RUnlock()

	var wakeApply []diskApplyTarget
	for _, snap := range snaps {
		scanStarted := time.Now()
		var state DiskState = StateActive
		temp := -1
		var hours, realloc, pending int
		var status, lastTest, probedAPM string
		curIOStat := readBlockDevStat(snap.name)
		ioChanged := curIOStat != "" && snap.lastIOStats != "" && curIOStat != snap.lastIOStats
		ioInFlight := hasBlockIOInFlight(curIOStat)

		if snap.prevState == StateStandby || snap.prevState == StateSleep {
			if curIOStat != "" && snap.lastIOStats != "" && !ioChanged && !ioInFlight {
				// Zero block I/O has occurred since the disk went to standby.
				// Never ping hardware with smartctl or hdparm, preserve sleeping state!
				m.mu.Lock()
				if d, exists := m.cachedDisks[snap.name]; exists {
					d.State = snap.prevState
					d.TemperatureC = -1
					d.LastStateCheck = time.Now()
					syncDiskFields(d)
				}
				m.mu.Unlock()
				continue
			}
			if ioChanged || ioInFlight {
				// Block I/O has been issued to a sleeping drive (e.g. ZFS / Jellyfin read).
				// Transition immediately to ACTIVE in the UI without issuing smartctl commands
				// during the ~15s motor spin-up window (avoids HBA queue contention and false STANDBY exit codes).
				m.mu.Lock()
				if d, exists := m.cachedDisks[snap.name]; exists && !d.LastStateCheck.After(scanStarted) {
					d.State = StateActive
					d.LastWakeTime = time.Now()
					d.LastSmartCheck = time.Time{}
					if curIOStat != "" {
						d.LastIOStats = curIOStat
					}
					d.LastStateCheck = time.Now()
					syncDiskFields(d)
				}
				m.mu.Unlock()
				continue
			}
		}

		inSpinupGrace := snap.isRotational && !snap.lastWakeTime.IsZero() && time.Since(snap.lastWakeTime) < 20*time.Second
		if inSpinupGrace {
			m.mu.Lock()
			if d, exists := m.cachedDisks[snap.name]; exists && !d.LastStateCheck.After(scanStarted) {
				d.State = StateActive
				if curIOStat != "" {
					d.LastIOStats = curIOStat
				}
				d.LastStateCheck = time.Now()
				syncDiskFields(d)
			}
			m.mu.Unlock()
			continue
		}

		didReadSmart := false
		if snap.isRotational {
			if ioChanged || ioInFlight {
				// Disk actively served block I/O since the previous poll; it is definitely ACTIVE.
				state = StateActive
			} else {
				state = m.checkStandbySafe(snap.path)
				if state == StateUnknown && snap.prevState != StateUnknown {
					state = snap.prevState
				}
			}
			if state == StateActive {
				needSmart := snap.prevState != StateActive || snap.lastSmartCheck.IsZero() || time.Since(snap.lastSmartCheck) >= 5*time.Minute
				if needSmart {
					temp, hours, realloc, pending, status, lastTest = m.readSmartAttributesSafe(snap.path)
					didReadSmart = true
					if snap.needAPMProbe {
						probedAPM = m.readHardwareAPMSafe(snap.path)
					}
				}
			}
		} else {
			needSmart := snap.lastSmartCheck.IsZero() || time.Since(snap.lastSmartCheck) >= 5*time.Minute
			if needSmart {
				temp, hours, realloc, pending, status, lastTest = m.readSmartAttributesSafe(snap.path)
				didReadSmart = true
			}
		}

		m.mu.Lock()
		d, exists := m.cachedDisks[snap.name]
		if exists {
			if !d.LastStateCheck.After(scanStarted) {
				if snap.isRotational && state != StateUnknown {
					d.State = state
				}
				if curIOStat != "" {
					d.LastIOStats = curIOStat
				}
				if state == StateActive {
					if didReadSmart {
						if temp > 0 {
							d.TemperatureC = temp
						} else if !snap.isRotational && temp >= 0 {
							d.TemperatureC = temp
						}
					}
				} else {
					d.TemperatureC = -1
				}
				if didReadSmart {
					d.LastSmartCheck = time.Now()
					if hours > 0 {
						d.PowerOnHours = hours
					}
					d.ReallocatedSectors = realloc
					d.PendingSectors = pending
					if status != "" && !strings.HasPrefix(d.SmartStatus, "TESTING") {
						d.SmartStatus = status
					}
					if lastTest != "" && !strings.HasPrefix(d.SmartStatus, "TESTING") {
						d.SmartLastTest = lastTest
					}
					if d.AdvPowerManagement == "" && probedAPM != "" {
						d.AdvPowerManagement = probedAPM
					}
				}
				d.LastStateCheck = time.Now()
				syncDiskFields(d)
			}

			// Apply deferred hardware config once if the drive was asleep at daemon startup and has now finished spinning up
			if d.pendingHardwareSync && d.State == StateActive && (d.LastWakeTime.IsZero() || time.Since(d.LastWakeTime) >= 20*time.Second) {
				d.pendingHardwareSync = false
				if saved, ok := m.lookupSavedConfigLocked(d.Name, d.ByIDPath, d.Serial); ok {
					wakeApply = append(wakeApply, diskApplyTarget{
						name:         d.Name,
						path:         d.Path,
						state:        StateActive,
						spindownSec:  saved.SpindownSeconds,
						advPowerMgmt: saved.AdvPowerManagement,
						epcMode:      saved.ExtendedPowerConditions,
						isRotational: d.IsRotational,
					})
				}
			}
		}
		m.mu.Unlock()
	}

	if len(wakeApply) > 0 {
		m.applyDiskConfigsToHardware(wakeApply)
	}
}

func (m *Manager) buildDiskPoolMap() map[string]string {
	res := make(map[string]string)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "zpool", "status", "-P")
	out, err := cmd.Output()
	if err != nil {
		return res
	}

	var currentPool string
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "pool:") {
			currentPool = strings.TrimSpace(strings.TrimPrefix(line, "pool:"))
		} else if currentPool != "" && strings.HasPrefix(line, "/dev/") {
			parts := strings.Fields(line)
			if len(parts) >= 1 {
				devName := filepath.Base(parts[0])
				cleanDev := stripPartition(devName)
				res[cleanDev] = currentPool
				res[parts[0]] = currentPool
				if resolved, err := filepath.EvalSymlinks(parts[0]); err == nil {
					res[stripPartition(filepath.Base(resolved))] = currentPool
				}
			}
		}
	}
	return res
}

func stripPartition(name string) string {
	if idx := strings.Index(name, "-part"); idx > 0 {
		return name[:idx]
	}
	if strings.HasPrefix(name, "nvme") {
		idx := strings.LastIndex(name, "p")
		if idx > 4 {
			return name[:idx]
		}
	} else if strings.HasPrefix(name, "sd") || strings.HasPrefix(name, "vd") || strings.HasPrefix(name, "xvd") {
		return strings.TrimRight(name, "0123456789")
	}
	return name
}

func (m *Manager) buildByIDMap() map[string]string {
	res := make(map[string]string)
	byIDDir := "/dev/disk/by-id"
	entries, err := os.ReadDir(byIDDir)
	if err != nil {
		return res
	}

	for _, entry := range entries {
		if strings.Contains(entry.Name(), "-part") {
			continue
		}
		fullPath := filepath.Join(byIDDir, entry.Name())
		target, err := os.Readlink(fullPath)
		if err == nil {
			devName := filepath.Base(target)
			if _, exists := res[devName]; !exists || !strings.HasPrefix(entry.Name(), "wwn-") {
				res[devName] = fullPath
			}
		}
	}
	return res
}

func (m *Manager) buildSystemDiskMap() map[string]bool {
	res := make(map[string]bool)
	files := []string{"/proc/mounts", "/proc/swaps"}
	for _, fPath := range files {
		data, err := os.ReadFile(fPath)
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(bytes.NewReader(data))
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if !strings.HasPrefix(line, "/dev/") {
				continue
			}
			parts := strings.Fields(line)
			if len(parts) >= 1 {
				dev := filepath.Base(parts[0])
				cleanDev := stripPartition(dev)
				// If mounted as root, boot, or swap, it's a critical system drive
				if fPath == "/proc/swaps" || (len(parts) >= 2 && (parts[1] == "/" || strings.HasPrefix(parts[1], "/boot"))) {
					res[cleanDev] = true
					res[dev] = true
					res[parts[0]] = true
				}
			}
		}
	}
	return res
}

func (m *Manager) readSysfsString(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func diskConfigKey(name, byIDPath, serial string) string {
	if byIDPath != "" && !strings.HasPrefix(byIDPath, "/dev/sd") && !strings.HasPrefix(byIDPath, "/dev/nvme") && !strings.HasPrefix(byIDPath, "/dev/vd") && !strings.HasPrefix(byIDPath, "/dev/xvd") {
		return byIDPath
	}
	if serial != "" {
		return "serial:" + serial
	}
	return name
}

func (m *Manager) lookupSavedConfigLocked(name, byIDPath, serial string) (DiskPowerConfig, bool) {
	if len(m.savedConfigs) == 0 {
		return DiskPowerConfig{}, false
	}
	key := diskConfigKey(name, byIDPath, serial)
	if cfg, ok := m.savedConfigs[key]; ok {
		return cfg, true
	}
	if byIDPath != "" {
		for _, cfg := range m.savedConfigs {
			if cfg.ByIDPath == byIDPath {
				return cfg, true
			}
		}
	}
	if serial != "" {
		for _, cfg := range m.savedConfigs {
			if cfg.Serial != "" && cfg.Serial == serial {
				return cfg, true
			}
		}
	}
	if cfg, ok := m.savedConfigs[name]; ok {
		return cfg, true
	}
	for _, cfg := range m.savedConfigs {
		if cfg.Name != "" && cfg.Name == name {
			return cfg, true
		}
	}
	return DiskPowerConfig{}, false
}

func (m *Manager) recordSavedConfigLocked(d *DiskInfo) {
	if d == nil {
		return
	}
	if m.savedConfigs == nil {
		m.savedConfigs = make(map[string]DiskPowerConfig)
	}
	key := diskConfigKey(d.Name, d.ByIDPath, d.Serial)
	spSec := d.SpindownSeconds
	apm := d.AdvPowerManagement
	epc := d.ExtendedPowerConditions
	if epc == "" {
		epc = d.EPCStatus
	}
	// Remove stale aliases pointing to the same disk under older keys
	for k, existing := range m.savedConfigs {
		if k == key {
			continue
		}
		if (d.ByIDPath != "" && existing.ByIDPath == d.ByIDPath) ||
			(d.Serial != "" && existing.Serial == d.Serial) ||
			existing.Name == d.Name {
			if apm == "" {
				apm = existing.AdvPowerManagement
			}
			if epc == "" {
				epc = existing.ExtendedPowerConditions
			}
			delete(m.savedConfigs, k)
		}
	}
	m.savedConfigs[key] = DiskPowerConfig{
		Name:                    d.Name,
		ByIDPath:                d.ByIDPath,
		Serial:                  d.Serial,
		SpindownSeconds:         &spSec,
		AdvPowerManagement:      apm,
		ExtendedPowerConditions: epc,
	}
}

func (m *Manager) applySavedConfigToDiskLocked(d *DiskInfo) {
	if d == nil {
		return
	}
	cfg, ok := m.lookupSavedConfigLocked(d.Name, d.ByIDPath, d.Serial)
	if !ok {
		return
	}
	if cfg.SpindownSeconds != nil && *cfg.SpindownSeconds >= 0 {
		d.SpindownSeconds = *cfg.SpindownSeconds
	}
	if cfg.AdvPowerManagement != "" {
		if norm, _, err := NormalizeAPMLevel(cfg.AdvPowerManagement); err == nil {
			d.AdvPowerManagement = norm
		}
	}
	if cfg.ExtendedPowerConditions != "" {
		if norm, err := NormalizeEPCMode(cfg.ExtendedPowerConditions); err == nil {
			d.ExtendedPowerConditions = norm
			d.EPCStatus = norm
		}
	}
}

func (m *Manager) saveDiskConfigLocked() {
	if m.configPath == "" {
		return
	}
	_ = os.MkdirAll(filepath.Dir(m.configPath), 0755)
	data, err := json.MarshalIndent(m.savedConfigs, "", "  ")
	if err == nil {
		tmpPath := m.configPath + ".tmp"
		if writeErr := os.WriteFile(tmpPath, data, 0644); writeErr == nil {
			_ = os.Rename(tmpPath, m.configPath)
		} else {
			_ = os.WriteFile(m.configPath, data, 0644)
		}
	}
}

func (m *Manager) loadSavedDiskConfigLocked() {
	if m.configPath == "" {
		return
	}
	data, err := os.ReadFile(m.configPath)
	if err != nil {
		return
	}

	var structured map[string]DiskPowerConfig
	if err := json.Unmarshal(data, &structured); err == nil && len(structured) > 0 {
		loadedAny := false
		for _, cfg := range structured {
			if cfg.SpindownSeconds != nil || cfg.AdvPowerManagement != "" || cfg.ExtendedPowerConditions != "" || cfg.Name != "" || cfg.ByIDPath != "" || cfg.Serial != "" {
				loadedAny = true
				break
			}
		}
		if loadedAny {
			if m.savedConfigs == nil {
				m.savedConfigs = make(map[string]DiskPowerConfig)
			}
			for k, cfg := range structured {
				if cfg.AdvPowerManagement != "" {
					if norm, _, normErr := NormalizeAPMLevel(cfg.AdvPowerManagement); normErr == nil {
						cfg.AdvPowerManagement = norm
					}
				}
				if cfg.ExtendedPowerConditions != "" {
					if norm, normErr := NormalizeEPCMode(cfg.ExtendedPowerConditions); normErr == nil {
						cfg.ExtendedPowerConditions = norm
					}
				}
				m.savedConfigs[k] = cfg
			}
			return
		}
	}

	// Backward compatibility with legacy map[string]int format
	var legacy map[string]int
	if err := json.Unmarshal(data, &legacy); err == nil {
		if m.savedConfigs == nil {
			m.savedConfigs = make(map[string]DiskPowerConfig)
		}
		for name, sec := range legacy {
			v := sec
			m.savedConfigs[name] = DiskPowerConfig{
				Name:            name,
				SpindownSeconds: &v,
			}
		}
	}
}

func (m *Manager) snapshotSavedApplyTargetsLocked() []diskApplyTarget {
	var out []diskApplyTarget
	for _, d := range m.cachedDisks {
		cfg, ok := m.lookupSavedConfigLocked(d.Name, d.ByIDPath, d.Serial)
		if !ok {
			continue
		}
		out = append(out, diskApplyTarget{
			name:         d.Name,
			path:         d.Path,
			state:        d.State,
			spindownSec:  cfg.SpindownSeconds,
			advPowerMgmt: cfg.AdvPowerManagement,
			epcMode:      cfg.ExtendedPowerConditions,
			isRotational: d.IsRotational,
		})
	}
	return out
}

func (m *Manager) applyDiskConfigsToHardware(targets []diskApplyTarget) {
	for _, t := range targets {
		if t.state == StateStandby || t.state == StateSleep {
			if t.name != "" {
				m.mu.Lock()
				if d, ok := m.cachedDisks[t.name]; ok {
					d.pendingHardwareSync = true
				}
				m.mu.Unlock()
			}
			continue
		}
		if t.advPowerMgmt != "" {
			if _, arg, err := NormalizeAPMLevel(t.advPowerMgmt); err == nil {
				_ = applyHardwareAPM(t.path, arg)
			}
		}
		if t.epcMode != "" {
			if normEPC, err := NormalizeEPCMode(t.epcMode); err == nil && normEPC != "Default" {
				_ = applyHardwareEPC(t.path, normEPC == "Disabled")
			}
		} else if strings.EqualFold(t.advPowerMgmt, "Disabled") || t.advPowerMgmt == "255" {
			_ = applyHardwareEPC(t.path, true)
		}
		if t.spindownSec != nil && *t.spindownSec >= 0 {
			_ = applyHardwareSpindown(t.path, *t.spindownSec)
		}
	}
	m.mu.Lock()
	m.syncHdIdleConfigLocked()
	m.mu.Unlock()
}

func applyHardwareSpindown(devPath string, seconds int) error {
	val := spindownSecondsToHdparmArg(seconds)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "hdparm", "-S", fmt.Sprintf("%d", val), devPath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		lower := strings.ToLower(msg)
		// On LSI HBAs (mpt3sas), hardware spindown ioctl may not be supported directly;
		// hd-idle handles software spindown in Debian NAS, so treat this as non-fatal.
		if strings.Contains(lower, "inappropriate ioctl") || strings.Contains(lower, "not supported") {
			return nil
		}
		if msg != "" {
			return fmt.Errorf("hdparm -S %d %s failed: %s", val, devPath, msg)
		}
		return fmt.Errorf("hdparm -S %d %s failed: %w", val, devPath, err)
	}
	return nil
}

func applyHardwareAPM(devPath, hdparmArg string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "hdparm", "-B", hdparmArg, devPath)
	out, err := cmd.CombinedOutput()
	outStr := strings.TrimSpace(string(out))
	lower := strings.ToLower(outStr)

	// If APM is being disabled (255), also apply smartctl -s apm,off (vital for Western Digital and enterprise SATA/SAS drives on LSI HBAs)
	if hdparmArg == "255" {
		sCtx, sCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer sCancel()
		if sErr := exec.CommandContext(sCtx, "smartctl", "-s", "apm,off", devPath).Run(); sErr != nil {
			satCtx, satCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer satCancel()
			_ = exec.CommandContext(satCtx, "smartctl", "-d", "sat", "-s", "apm,off", devPath).Run()
		}
	}

	if err != nil || strings.Contains(lower, "not supported") || strings.Contains(lower, "failed") || strings.Contains(lower, "inappropriate ioctl") {
		// Fallback for LSI SAS HBAs (9500-8i / mpt3sas): apply APM via smartctl SAT pass-through
		smartArg := "apm," + hdparmArg
		if hdparmArg == "255" {
			smartArg = "apm,off"
		}
		sCtx, sCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer sCancel()
		if sErr := exec.CommandContext(sCtx, "smartctl", "-s", smartArg, devPath).Run(); sErr == nil {
			return nil
		}
		satCtx, satCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer satCancel()
		if satErr := exec.CommandContext(satCtx, "smartctl", "-d", "sat", "-s", smartArg, devPath).Run(); satErr == nil {
			return nil
		}
		if outStr != "" {
			return fmt.Errorf("APM level %s rejected by %s: %s", hdparmArg, devPath, outStr)
		}
		if err != nil {
			return fmt.Errorf("APM level %s rejected by %s: %w", hdparmArg, devPath, err)
		}
		return fmt.Errorf("APM level %s not supported by %s", hdparmArg, devPath)
	}
	return nil
}

// applyHardwareEPC configures or clears EPC Standby timers using sdparm
func applyHardwareEPC(devPath string, disabled bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	val := "0"
	if !disabled {
		val = "1"
	}
	// Use sdparm on Power Condition (po) mode page to set STANDBY timer switch
	cmd := exec.CommandContext(ctx, "sdparm", "--flexible", "--set=STANDBY="+val, devPath)
	_ = cmd.Run()
	if disabled {
		cCtx, cCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cCancel()
		_ = exec.CommandContext(cCtx, "sdparm", "--flexible", "--clear=SCT", devPath).Run()
	}
	return nil
}

func (m *Manager) readHardwareAPMSafe(devPath string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "hdparm", "-B", devPath)
	out, err := cmd.CombinedOutput()
	if err != nil && len(out) == 0 {
		return "Disabled"
	}
	return parseHdparmAPMOutput(string(out))
}

func parseHdparmAPMOutput(output string) string {
	lower := strings.ToLower(output)
	if strings.Contains(lower, "not supported") || strings.Contains(lower, "inappropriate ioctl") {
		return "Disabled"
	}
	for _, line := range strings.Split(output, "\n") {
		l := strings.ToLower(strings.TrimSpace(line))
		if strings.Contains(l, "apm_level") {
			if idx := strings.LastIndex(l, "="); idx >= 0 {
				val := strings.TrimSpace(l[idx+1:])
				if val == "off" || val == "255" || val == "not supported" {
					return "Disabled"
				}
				if norm, _, err := NormalizeAPMLevel(val); err == nil {
					return norm
				}
			}
		}
	}
	return "Disabled"
}

func (m *Manager) hasHdIdleConfig() bool {
	if m.hdIdleConfig == "" {
		return false
	}
	_, err := os.Stat(m.hdIdleConfig)
	return err == nil
}

func (m *Manager) syncHdIdleConfigLocked() {
	if !m.hasHdIdleConfig() {
		return
	}
	var names []string
	for name := range m.cachedDisks {
		names = append(names, name)
	}
	sort.Strings(names)

	// Use '-c ata' so hd-idle sends ATA STANDBY IMMEDIATE (0xE0 via SAT pass-through) instead of
	// SCSI START STOP UNIT (STOP). On Broadcom/LSI SAS HBAs (e.g. 9500-8i / mpt3sas), SCSI STOP puts
	// the SATL into a stopped state that immediately rejects ZFS Read(16)/Write(16) with Sense 04h/00h
	// ('Logical unit not ready, cause not reportable', cmd_age=0s), whereas ATA Standby wakes transparently on I/O.
	opts := []string{"-i", "0", "-c", "ata"}
	for _, name := range names {
		d := m.cachedDisks[name]
		if d == nil || !d.IsRotational || d.SpindownSeconds <= 0 {
			continue
		}
		targetDev := d.Name
		if targetDev == "" {
			targetDev = strings.TrimPrefix(d.Path, "/dev/")
		}
		opts = append(opts, "-a", targetDev, "-i", strconv.Itoa(d.SpindownSeconds))
	}
	opts = append(opts, "-l", "/var/log/hd-idle.log")

	content := fmt.Sprintf("# Managed by nasd\nSTART_HD_IDLE=true\nHD_IDLE_OPTS=%q\n", strings.Join(opts, " "))
	if existing, err := os.ReadFile(m.hdIdleConfig); err == nil && string(existing) == content {
		return
	}
	if err := os.WriteFile(m.hdIdleConfig, []byte(content), 0644); err == nil {
		log.Printf("[INFO] Synchronized hd-idle config (%s): %s", m.hdIdleConfig, strings.Join(opts, " "))
		if !m.mockMode {
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = exec.CommandContext(ctx, "systemctl", "try-restart", "hd-idle").Run()
			}()
		}
	} else if err != nil {
		log.Printf("[WARN] Failed to write hd-idle config (%s): %v", m.hdIdleConfig, err)
	}
}

func formatBytes(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := uint64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.2f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}
