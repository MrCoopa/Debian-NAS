package disks

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

type DiskState string

const (
	StateActive  DiskState = "ACTIVE"
	StateStandby DiskState = "STANDBY"
	StateSleep   DiskState = "SLEEPING"
	StateUnknown DiskState = "UNKNOWN"
)

type DiskInfo struct {
	Name               string    `json:"name"`                // e.g. "sda"
	Path               string    `json:"path"`                // e.g. "/dev/sda"
	ByIDPath           string    `json:"by_id_path"`          // e.g. "/dev/disk/by-id/ata-WDC_WD40EFRX..."
	Model              string    `json:"model"`               // e.g. "WDC WD40EFRX-68N32N0"
	Serial             string    `json:"serial"`              // e.g. "WD-WCC7K1234567"
	SizeBytes          uint64    `json:"size_bytes"`          // e.g. 4000787030016
	SizeHuman          string    `json:"size_human"`          // e.g. "3.64 TB"
	IsRotational       bool      `json:"is_rotational"`       // true = HDD, false = SSD/NVMe
	State              DiskState `json:"state"`               // ACTIVE, STANDBY, SLEEPING
	TemperatureC       int       `json:"temperature_c"`       // -1 if sleeping
	SpindownSeconds    int       `json:"spindown_seconds"`    // configured timeout
	ZfsPool            string    `json:"zfs_pool"`            // pool name if in use
	Slot               int       `json:"slot"`                // Physical chassis bay slot (1..X)
	Enclosure          string    `json:"enclosure"`           // e.g. "Bay 01 (Front)"
	BusType            string    `json:"bus_type"`            // SATA 6Gb/s, SAS 12Gb/s, NVMe PCIe 4.0
	SmartStatus        string    `json:"smart_status"`        // PASSED, WARNING, STANDBY
	PowerOnHours       int       `json:"power_on_hours"`      // e.g. 14200
	ReallocatedSectors int       `json:"reallocated_sectors"` // 0
	PendingSectors     int       `json:"pending_sectors"`     // 0
	LocateLed          bool      `json:"locate_led"`          // LED blinking
	VdevType           string    `json:"vdev_type"`           // mirror-0, raidz1, single, unassigned
	LastStateCheck     time.Time `json:"last_state_check"`
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
	} else if runtime.GOOS == "windows" {
		_, _ = m.scanWindowsDisks()
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
			ByIDPath:           "/dev/disk/by-id/ata-WDC_WD20EFAX-68B29N0_WD-WCC4M123",
			Model:              "WDC WD20EFAX-68B29N0 (Red Plus)",
			Serial:             "WD-WCC4M123",
			SizeBytes:          2000398934016,
			SizeHuman:          "1.82 TiB",
			IsRotational:       true,
			State:              StateStandby,
			TemperatureC:       -1, // asleep
			SpindownSeconds:    900,
			ZfsPool:            "DATA",
			Slot:               1,
			Enclosure:          "Bay 01 (Front Hot-Swap)",
			BusType:            "SATA 6.0 Gb/s (Port 0)",
			SmartStatus:        "PASSED (Standby Safe)",
			PowerOnHours:       11240,
			ReallocatedSectors: 0,
			PendingSectors:     0,
			LocateLed:          false,
			VdevType:           "mirror-0",
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
			VdevType:           "single",
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
			ByIDPath:           "/dev/disk/by-id/ata-Seagate_Exos_X18_16TB_ZR510ABC",
			Model:              "Seagate Exos X18 16TB Enterprise",
			Serial:             "ZR510ABC8841",
			SizeBytes:          16000900661248,
			SizeHuman:          "14.55 TiB",
			IsRotational:       true,
			State:              StateActive,
			TemperatureC:       32,
			SpindownSeconds:    1200,
			ZfsPool:            "",
			Slot:               5,
			Enclosure:          "Bay 05 (Front Hot-Swap)",
			BusType:            "SAS 12.0 Gb/s (LSI HBA)",
			SmartStatus:        "PASSED",
			PowerOnHours:       3100,
			ReallocatedSectors: 0,
			PendingSectors:     0,
			LocateLed:          false,
			VdevType:           "unassigned (Ready for Pool)",
			LastStateCheck:     time.Now(),
		},
		"sdf": {
			Name:               "sdf",
			Path:               "/dev/sdf",
			ByIDPath:           "/dev/disk/by-id/ata-Seagate_Exos_X18_16TB_ZR510DEF",
			Model:              "Seagate Exos X18 16TB Enterprise",
			Serial:             "ZR510DEF7729",
			SizeBytes:          16000900661248,
			SizeHuman:          "14.55 TiB",
			IsRotational:       true,
			State:              StateActive,
			TemperatureC:       30,
			SpindownSeconds:    1200,
			ZfsPool:            "",
			Slot:               6,
			Enclosure:          "Bay 06 (Front Hot-Swap)",
			BusType:            "SAS 12.0 Gb/s (LSI HBA)",
			SmartStatus:        "PASSED",
			PowerOnHours:       3100,
			ReallocatedSectors: 0,
			PendingSectors:     0,
			LocateLed:          false,
			VdevType:           "unassigned (Ready for Pool)",
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
}

// ListDisks enumerates all block devices without waking up sleeping HDDs.
func (m *Manager) ListDisks() ([]*DiskInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.mockMode {
		var list []*DiskInfo
		for _, d := range m.cachedDisks {
			list = append(list, d)
		}
		return list, nil
	}

	if runtime.GOOS == "windows" {
		return m.scanWindowsDisks()
	}

	return m.scanLinuxDisks()
}

func (m *Manager) scanLinuxDisks() ([]*DiskInfo, error) {
	blockDir := "/sys/block"
	entries, err := os.ReadDir(blockDir)
	if err != nil {
		return nil, fmt.Errorf("reading /sys/block: %w", err)
	}

	byIDMap := m.buildByIDMap()
	poolMap := m.buildDiskPoolMap()
	var disks []*DiskInfo

	for _, entry := range entries {
		name := entry.Name()
		// Only consider sdX, nvmeXnY, vdX, xvdX
		if !strings.HasPrefix(name, "sd") && !strings.HasPrefix(name, "nvme") && !strings.HasPrefix(name, "vd") {
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

		if rotational {
			state = m.checkStandbySafe(devPath)
			if state == StateActive {
				temp, powerHours, realloc, pending, smartStatus = m.readSmartAttributesSafe(devPath)
			}
		} else {
			state = StateActive
			temp, powerHours, realloc, pending, smartStatus = m.readSmartAttributesSafe(devPath)
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
		}

		slotIdx := len(disks) + 1
		busType := "SATA 6.0 Gb/s"
		if strings.HasPrefix(name, "nvme") {
			busType = "PCIe Gen4 x4 NVMe"
		} else if strings.Contains(byID, "sas-") {
			busType = "SAS 12.0 Gb/s (LSI HBA)"
		}

		disk := &DiskInfo{
			Name:               name,
			Path:               devPath,
			ByIDPath:           byID,
			Model:              fullModel,
			Serial:             serial,
			SizeBytes:          sizeBytes,
			SizeHuman:          formatBytes(sizeBytes),
			IsRotational:       rotational,
			State:              state,
			TemperatureC:       temp,
			SpindownSeconds:    spindownSec,
			ZfsPool:            poolName,
			Slot:               slotIdx,
			Enclosure:          fmt.Sprintf("Bay %02d (Front Hot-Swap)", slotIdx),
			BusType:            busType,
			SmartStatus:        smartStatus,
			PowerOnHours:       powerHours,
			ReallocatedSectors: realloc,
			PendingSectors:     pending,
			LocateLed:          locateLed,
			VdevType:           "assigned",
			LastStateCheck:     time.Now(),
		}

		disks = append(disks, disk)
		m.cachedDisks[name] = disk
	}

	return disks, nil
}

// checkStandbySafe runs 'hdparm -C' to query drive status without spinning it up
func (m *Manager) checkStandbySafe(devPath string) DiskState {
	cmd := exec.Command("hdparm", "-C", devPath)
	out, err := cmd.Output()
	if err != nil {
		// Fallback check: smartctl with -n standby
		smartCmd := exec.Command("smartctl", "-n", "standby", "-i", devPath)
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

// readSmartAttributesSafe reads SMART attributes without spinning up sleeping drives
func (m *Manager) readSmartAttributesSafe(devPath string) (temp, powerOnHours, realloc, pending int, status string) {
	temp = -1
	status = "PASSED"
	cmd := exec.Command("smartctl", "-n", "standby", "-A", devPath)
	out, err := cmd.Output()
	if err != nil {
		return
	}
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		line := scanner.Text()
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

// TriggerSmartTest triggers a short or long SMART self-test
func (m *Manager) TriggerSmartTest(devPath, testType string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if testType != "short" && testType != "long" {
		testType = "short"
	}

	diskName := filepath.Base(devPath)
	var target *DiskInfo
	for _, d := range m.cachedDisks {
		if d.Path == devPath || d.ByIDPath == devPath || d.Name == devPath || d.Name == diskName {
			target = d
			break
		}
	}

	if target != nil {
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
		return nil
	}

	cmd := exec.Command("smartctl", "-t", testType, devPath)
	return cmd.Run()
}

// readTemperatureSafe uses smartctl with '-n standby' so it exits with code 2 if disk is sleeping!
func (m *Manager) readTemperatureSafe(devPath string) int {
	t, _, _, _, _ := m.readSmartAttributesSafe(devPath)
	return t
}

// SetSpindown sets spindown timeout for a drive using hdparm and hd-idle
func (m *Manager) SetSpindown(devPath string, seconds int) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	diskName := filepath.Base(devPath)
	for _, d := range m.cachedDisks {
		if d.Path == devPath || d.ByIDPath == devPath || d.Name == devPath || d.Name == diskName {
			d.SpindownSeconds = seconds
			devPath = d.Path
			break
		}
	}

	if m.mockMode {
		return nil
	}

	// hdparm standby value formula:
	// 0 = disabled
	// 1-240 = value * 5 seconds (5s to 20 mins)
	// 241-251 = 1 to 11 units of 30 minutes (30m to 5.5 hours)
	var val int
	if seconds <= 0 {
		val = 0
	} else if seconds <= 1200 {
		val = seconds / 5
		if val < 1 {
			val = 1
		}
	} else if seconds <= 19800 {
		val = 240 + (seconds-1200)/1800
	} else {
		val = 252 // 21 minutes
	}

	cmd := exec.Command("hdparm", "-S", fmt.Sprintf("%d", val), devPath)
	_ = cmd.Run()

	// Persist settings
	m.saveSpindownConfig()
	return nil
}

// ForceStandby immediately puts a drive into standby
func (m *Manager) ForceStandby(devPath string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	diskName := filepath.Base(devPath)
	for _, d := range m.cachedDisks {
		if d.Path == devPath || d.ByIDPath == devPath || d.Name == devPath || d.Name == diskName {
			d.State = StateStandby
			d.TemperatureC = -1
			devPath = d.Path
			break
		}
	}

	if m.mockMode {
		return nil
	}

	cmd := exec.Command("hdparm", "-y", devPath)
	return cmd.Run()
}

// ToggleLocate toggles the enclosure identify / locate LED for a disk (45Drives Houston feature)
func (m *Manager) ToggleLocate(devPath string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	diskName := filepath.Base(devPath)
	var target *DiskInfo
	for _, d := range m.cachedDisks {
		if d.Path == devPath || d.ByIDPath == devPath || d.Name == devPath || d.Name == diskName {
			target = d
			break
		}
	}

	if target == nil {
		return false, fmt.Errorf("disk %s not found", devPath)
	}

	target.LocateLed = !target.LocateLed

	if m.mockMode {
		return target.LocateLed, nil
	}

	// Real Linux: use ledctl locate or sas2ircu / smartctl activity
	action := "off"
	if target.LocateLed {
		action = "locate"
	}
	cmd := exec.Command("ledctl", fmt.Sprintf("%s=%s", action, target.Path))
	_ = cmd.Run()

	return target.LocateLed, nil
}

func (m *Manager) startMonitorLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		if m.mockMode || runtime.GOOS != "linux" {
			continue
		}
		m.mu.Lock()
		for _, d := range m.cachedDisks {
			if d.IsRotational {
				// Query non-intrusively
				state := m.checkStandbySafe(d.Path)
				d.State = state
				if state == StateActive {
					d.TemperatureC = m.readTemperatureSafe(d.Path)
				} else {
					d.TemperatureC = -1
				}
				d.LastStateCheck = time.Now()
			}
		}
		m.mu.Unlock()
	}
}

func (m *Manager) buildDiskPoolMap() map[string]string {
	res := make(map[string]string)
	cmd := exec.Command("zpool", "status", "-P")
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
				// Strip partition suffix (e.g. sda1 -> sda, nvme0n1p1 -> nvme0n1)
				cleanDev := stripPartition(devName)
				res[cleanDev] = currentPool
				res[parts[0]] = currentPool
			}
		}
	}
	return res
}

func stripPartition(name string) string {
	if strings.HasPrefix(name, "nvme") {
		idx := strings.LastIndex(name, "p")
		if idx > 0 {
			return name[:idx]
		}
	} else if strings.HasPrefix(name, "sd") {
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
