package disks

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
	IsSystemDisk           bool      `json:"is_system_disk"`           // true = contains OS mount (/ or /boot)
	LastStateCheck         time.Time `json:"last_state_check"`
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
	if d.AdvPowerManagement == "" {
		if d.IsRotational {
			d.AdvPowerManagement = "128"
		} else {
			d.AdvPowerManagement = "Disabled"
		}
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

type Manager struct {
	mu           sync.RWMutex
	mockMode     bool
	cachedDisks  map[string]*DiskInfo
	hdIdleConfig string
	configPath   string
}

func NewManager(mockMode bool, hdIdleConfig string) *Manager {
	m := &Manager{
		mockMode:     mockMode,
		cachedDisks:  make(map[string]*DiskInfo),
		hdIdleConfig: hdIdleConfig,
		configPath:   "/etc/nas/disks.json",
	}

	if m.mockMode {
		m.initMockDisks()
	} else {
		// Scan linux disks on startup
		_, _ = m.scanLinuxDisks()
		m.loadSavedSpindownConfig()
	}

	// Start background non-intrusive status poll loop
	go m.startMonitorLoop()

	return m
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

// ListDisks enumerates all block devices without waking up sleeping HDDs.
func (m *Manager) ListDisks() ([]*DiskInfo, error) {
	m.mu.RLock()
	if len(m.cachedDisks) > 0 {
		var list []*DiskInfo
		for _, d := range m.cachedDisks {
			syncDiskFields(d)
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

	if len(m.cachedDisks) > 0 {
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
	var disks []*DiskInfo

	for _, entry := range entries {
		name := entry.Name()
		// Only consider sdX, nvmeXnY, vdX, xvdX
		if !strings.HasPrefix(name, "sd") && !strings.HasPrefix(name, "nvme") && !strings.HasPrefix(name, "vd") && !strings.HasPrefix(name, "xvd") {
			continue
		}

		sysPath := filepath.Join(blockDir, name)

		// Check if rotational (HDD vs SSD)
		rotational := false
		rotData, err := os.ReadFile(filepath.Join(sysPath, "queue", "rotational"))
		if err == nil && strings.TrimSpace(string(rotData)) == "1" {
			rotational = true
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

		if rotational {
			state = m.checkStandbySafe(devPath)
			if state == StateActive {
				temp, powerHours, realloc, pending, smartStatus, smartLastTest = m.readSmartAttributesSafe(devPath)
			}
		} else {
			state = StateActive
			temp, powerHours, realloc, pending, smartStatus, smartLastTest = m.readSmartAttributesSafe(devPath)
		}

		poolName := poolMap[name]
		if poolName == "" {
			poolName = poolMap[filepath.Base(byID)]
		}

		spindownSec := 900
		locateLed := false
		if existing, exists := m.cachedDisks[name]; exists {
			if existing.SpindownSeconds >= 0 {
				spindownSec = existing.SpindownSeconds
			}
			locateLed = existing.LocateLed
			if smartLastTest == "" && existing.SmartLastTest != "" {
				smartLastTest = existing.SmartLastTest
			}
		}

		slotIdx := len(disks) + 1
		busType := "SATA 6.0 Gb/s"
		if strings.HasPrefix(name, "nvme") {
			busType = "PCIe Gen4 x4 NVMe"
		} else if strings.Contains(byID, "sas-") {
			busType = "SAS 12.0 Gb/s (LSI HBA)"
		}

		isSystem := sysMap[name] || sysMap[devPath] || sysMap[filepath.Base(byID)]
		vdevType := "unassigned (Ready for Pool)"
		if poolName != "" {
			vdevType = "assigned (" + poolName + ")"
		} else if isSystem {
			vdevType = "OS / System Drive"
		}

		disk := &DiskInfo{
			Name:               name,
			Device:             name,
			Path:               devPath,
			ByIDPath:           byID,
			Vendor:             vendor,
			Model:              fullModel,
			Serial:             serial,
			SizeBytes:          sizeBytes,
			SizeHuman:          formatBytes(sizeBytes),
			IsRotational:       rotational,
			State:              state,
			TemperatureC:       temp,
			SpindownSeconds:    spindownSec,
			ZfsPool:            poolName,
			Pool:               poolName,
			Slot:               slotIdx,
			Enclosure:          fmt.Sprintf("Bay %02d (Front Hot-Swap)", slotIdx),
			BusType:            busType,
			SmartStatus:        smartStatus,
			SmartLastTest:      smartLastTest,
			PowerOnHours:       powerHours,
			ReallocatedSectors: realloc,
			PendingSectors:     pending,
			LocateLed:          locateLed,
			VdevType:           vdevType,
			IsSystemDisk:       isSystem,
			LastStateCheck:     time.Now(),
		}
		syncDiskFields(disk)

		disks = append(disks, disk)
		m.cachedDisks[name] = disk
	}

	return disks, nil
}

// checkStandbySafe runs 'hdparm -C' to query drive status without spinning it up
func (m *Manager) checkStandbySafe(devPath string) DiskState {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "hdparm", "-C", devPath)
	out, err := cmd.Output()
	if err != nil {
		// Fallback check: smartctl with -n standby
		sCtx, sCancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer sCancel()
		smartCmd := exec.CommandContext(sCtx, "smartctl", "-n", "standby", "-i", devPath)
		_ = smartCmd.Run()
		if smartCmd.ProcessState != nil && smartCmd.ProcessState.ExitCode() == 2 {
			return StateStandby
		}
		return StateUnknown
	}
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
	if target != nil {
		devPath = target.Path
		target.SmartStatus = fmt.Sprintf("TESTING (%s Self-Test running...)", strings.ToUpper(testType))
		if m.mockMode {
			go func(d *DiskInfo) {
				time.Sleep(4 * time.Second)
				m.mu.Lock()
				d.SmartStatus = "PASSED (Self-Test Completed 100%)"
				m.mu.Unlock()
			}(target)
			return nil
		}
	}

	if m.mockMode {
		if target == nil {
			return fmt.Errorf("disk %s not found", devPath)
		}
		return nil
	}

	if err := validate.DevicePath(devPath); err != nil {
		return fmt.Errorf("invalid disk device path: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "smartctl", "-t", testType, devPath)
	return cmd.Run()
}

// readTemperatureSafe uses smartctl with '-n standby' so it exits with code 2 if disk is sleeping!
func (m *Manager) readTemperatureSafe(devPath string) int {
	t, _, _, _, _, _ := m.readSmartAttributesSafe(devPath)
	return t
}

// SetSpindown sets spindown timeout for a drive using hdparm and hd-idle
func (m *Manager) SetSpindown(devPath string, seconds int) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	target := m.findDiskLocked(devPath)
	if target != nil {
		target.SpindownSeconds = seconds
		target.SpindownTimeoutSeconds = seconds
		devPath = target.Path
	}

	if m.mockMode {
		if target == nil {
			return fmt.Errorf("disk %s not found", devPath)
		}
		return nil
	}

	if err := validate.DevicePath(devPath); err != nil {
		return fmt.Errorf("invalid disk device path: %w", err)
	}

	// hdparm standby value formula (-S):
	// 0 = disabled
	// 1-240 = multiples of 5 seconds (5s to 20 mins)
	// 241-251 = 1 to 11 units of 30 minutes (30m to 5.5 hours)
	// 252 = 21 minutes (vendor specific)
	// 253 = vendor-defined timeout between 8 and 12 hours
	// 255 = 21 minutes plus 15 seconds
	var val int
	if seconds <= 0 {
		val = 0
	} else if seconds <= 1200 {
		val = seconds / 5
		if val < 1 {
			val = 1
		} else if val > 240 {
			val = 240
		}
	} else if seconds <= 19800 {
		// 1800s (30m) -> 241, 3600s (1h) -> 242, ..., 19800s (5.5h) -> 251
		units := (seconds + 899) / 1800
		if units < 1 {
			units = 1
		} else if units > 11 {
			units = 11
		}
		val = 240 + units
	} else if seconds >= 28800 {
		// Vendor-defined 8-12 hours
		val = 253
	} else {
		// Between 5.5h and 8h: cap at 251 (5.5h) instead of 252 (which means 21 minutes!)
		val = 251
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "hdparm", "-S", fmt.Sprintf("%d", val), devPath)
	_ = cmd.Run()

	// Persist settings
	m.saveSpindownConfig()
	return nil
}

// SetAPM sets Advanced Power Management level using hdparm -B
func (m *Manager) SetAPM(devPath string, level string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	target := m.findDiskLocked(devPath)
	if target != nil {
		target.AdvPowerManagement = level
		devPath = target.Path
	}

	if m.mockMode {
		if target == nil {
			return fmt.Errorf("disk %s not found", devPath)
		}
		return nil
	}

	if err := validate.DevicePath(devPath); err != nil {
		return fmt.Errorf("invalid disk device path: %w", err)
	}

	val := "255"
	switch level {
	case "Disabled", "255", "off", "Off":
		val = "255"
	case "1":
		val = "1"
	case "64":
		val = "64"
	case "127":
		val = "127"
	case "128":
		val = "128"
	case "192":
		val = "192"
	case "254":
		val = "254"
	default:
		val = level
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "hdparm", "-B", val, devPath)
	_ = cmd.Run()

	return nil
}

// ForceStandby immediately puts a drive into standby
func (m *Manager) ForceStandby(devPath string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	target := m.findDiskLocked(devPath)
	if target != nil {
		target.State = StateStandby
		target.PowerState = "STANDBY"
		target.TemperatureC = -1
		devPath = target.Path
	}

	if m.mockMode {
		if target == nil {
			return fmt.Errorf("disk %s not found", devPath)
		}
		return nil
	}

	if err := validate.DevicePath(devPath); err != nil {
		return fmt.Errorf("invalid disk device path: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "hdparm", "-y", devPath)
	return cmd.Run()
}

// ToggleLocate toggles the enclosure identify / locate LED for a disk (45Drives Houston feature)
func (m *Manager) ToggleLocate(devPath string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	target := m.findDiskLocked(devPath)
	if target == nil {
		return false, fmt.Errorf("disk %s not found", devPath)
	}

	target.LocateLed = !target.LocateLed

	if m.mockMode {
		return target.LocateLed, nil
	}

	if err := validate.DevicePath(target.Path); err != nil {
		return false, fmt.Errorf("invalid disk device path: %w", err)
	}

	// Real Linux: use ledctl locate or sas2ircu / smartctl activity
	action := "off"
	if target.LocateLed {
		action = "locate"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ledctl", fmt.Sprintf("%s=%s", action, target.Path))
	_ = cmd.Run()

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
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		if m.mockMode {
			continue
		}
		m.mu.Lock()
		for _, d := range m.cachedDisks {
			if d.IsRotational {
				// Query non-intrusively
				state := m.checkStandbySafe(d.Path)
				d.State = state
				if state == StateActive {
					temp, hours, realloc, pending, status, lastTest := m.readSmartAttributesSafe(d.Path)
					d.TemperatureC = temp
					if hours > 0 {
						d.PowerOnHours = hours
					}
					d.ReallocatedSectors = realloc
					d.PendingSectors = pending
					if status != "" {
						d.SmartStatus = status
					}
					if lastTest != "" {
						d.SmartLastTest = lastTest
					}
				} else {
					d.TemperatureC = -1
				}
				d.LastStateCheck = time.Now()
				syncDiskFields(d)
			} else {
				temp, hours, realloc, pending, status, lastTest := m.readSmartAttributesSafe(d.Path)
				if temp > 0 {
					d.TemperatureC = temp
				}
				if hours > 0 {
					d.PowerOnHours = hours
				}
				d.ReallocatedSectors = realloc
				d.PendingSectors = pending
				if status != "" {
					d.SmartStatus = status
				}
				if lastTest != "" {
					d.SmartLastTest = lastTest
				}
				d.LastStateCheck = time.Now()
				syncDiskFields(d)
			}
		}
		m.mu.Unlock()
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

func (m *Manager) saveSpindownConfig() {
	timeouts := make(map[string]int)
	for name, d := range m.cachedDisks {
		timeouts[name] = d.SpindownSeconds
	}
	_ = os.MkdirAll(filepath.Dir(m.configPath), 0755)
	data, err := json.MarshalIndent(timeouts, "", "  ")
	if err == nil {
		_ = os.WriteFile(m.configPath, data, 0644)
	}
}

func (m *Manager) loadSavedSpindownConfig() {
	data, err := os.ReadFile(m.configPath)
	if err != nil {
		return
	}
	var timeouts map[string]int
	if err := json.Unmarshal(data, &timeouts); err == nil {
		for name, sec := range timeouts {
			if d, ok := m.cachedDisks[name]; ok {
				d.SpindownSeconds = sec
			}
		}
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
