package zfs

import (
	"bufio"
	"bytes"
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

type PoolHealth string

const (
	HealthOnline   PoolHealth = "ONLINE"
	HealthDegraded PoolHealth = "DEGRADED"
	HealthFaulted  PoolHealth = "FAULTED"
	HealthOffline  PoolHealth = "OFFLINE"
	HealthUnknown  PoolHealth = "UNKNOWN"
)

type Vdev struct {
	Name   string `json:"name"`
	Type   string `json:"type"` // disk, mirror, raidz1, raidz2
	Health string `json:"health"`
	Path   string `json:"path,omitempty"`
}

type ScrubStatus struct {
	Active     bool      `json:"active"`
	Percentage float64   `json:"percentage"`
	BytesDone  string    `json:"bytes_done"`
	TotalBytes string    `json:"total_bytes"`
	Errors     uint64    `json:"errors"`
	Speed      string    `json:"speed"`
	TimeLeft   string    `json:"time_left"`
	LastRun    time.Time `json:"last_run"`
}

type Pool struct {
	Name          string      `json:"name"`
	Health        PoolHealth  `json:"health"`
	SizeBytes     uint64      `json:"size_bytes"`
	SizeHuman     string      `json:"size_human"`
	Allocated     uint64      `json:"allocated"`
	AllocHuman    string      `json:"alloc_human"`
	FreeBytes     uint64      `json:"free_bytes"`
	FreeHuman     string      `json:"free_human"`
	Fragmentation int         `json:"fragmentation"`
	CapPercent    int         `json:"cap_percent"`
	Vdevs         []Vdev      `json:"vdevs"`
	Scrub         ScrubStatus `json:"scrub"`
	AtimeDefault  string      `json:"atime_default"`
}

type Dataset struct {
	Name        string            `json:"name"`
	Pool        string            `json:"pool"`
	Mountpoint  string            `json:"mountpoint"`
	UsedBytes   uint64            `json:"used_bytes"`
	UsedHuman   string            `json:"used_human"`
	AvailBytes  uint64            `json:"avail_bytes"`
	AvailHuman  string            `json:"avail_human"`
	Compression string            `json:"compression"`
	Atime       string            `json:"atime"`
	RecordSize  string            `json:"recordsize"`
	Snapshots   int               `json:"snapshots_count"`
	Properties  map[string]string `json:"properties,omitempty"`
}

type Snapshot struct {
	Name       string    `json:"name"`
	Dataset    string    `json:"dataset"`
	Created    time.Time `json:"created"`
	UsedHuman  string    `json:"used_human"`
	Referenced string    `json:"referenced"`
}

type ImportablePool struct {
	Name   string `json:"name"`
	ID     string `json:"id"`
	State  string `json:"state"`
	Action string `json:"action"`
	Config string `json:"config"`
}

type Manager struct {
	mu           sync.RWMutex
	mockMode     bool
	mockPools    map[string]*Pool
	mockDS       map[string]*Dataset
	mockSnaps    []*Snapshot
	mockExported map[string]*ImportablePool
}

func NewManager(mockMode bool) *Manager {
	m := &Manager{
		mockMode:     mockMode || runtime.GOOS != "linux",
		mockPools:    make(map[string]*Pool),
		mockDS:       make(map[string]*Dataset),
		mockExported: make(map[string]*ImportablePool),
	}

	if m.mockMode {
		m.initMockZFS()
	}
	return m
}

func (m *Manager) initMockZFS() {
	m.mockExported = map[string]*ImportablePool{
		"BACKUP-VAULT": {
			Name:   "BACKUP-VAULT",
			ID:     "1483920193849182741",
			State:  "ONLINE",
			Action: "The pool was exported cleanly and can be imported directly.",
			Config: "BACKUP-VAULT ONLINE (2 Member Disks - Mirror)",
		},
	}
	m.mockPools = map[string]*Pool{
		"DATA": {
			Name:          "DATA",
			Health:        HealthOnline,
			SizeBytes:     2000398934016,
			SizeHuman:     "1.82 TiB",
			Allocated:     348356784128,
			AllocHuman:    "324.43 GiB",
			FreeBytes:     1627255140352,
			FreeHuman:     "1.48 TiB",
			Fragmentation: 1,
			CapPercent:    18,
			AtimeDefault:  "off",
			Vdevs: []Vdev{
				{Name: "mirror-0", Type: "mirror", Health: "ONLINE"},
				{Name: "sda", Type: "disk", Health: "ONLINE", Path: "/dev/disk/by-id/ata-WDC_WD20EFAX-68B29N0_WD-WCC4M123"},
				{Name: "sdb", Type: "disk", Health: "ONLINE", Path: "/dev/disk/by-id/ata-WDC_WD20EFAX-68B29N0_WD-WCC4M124"},
			},
			Scrub: ScrubStatus{
				Active:     false,
				Percentage: 100.0,
				Errors:     0,
				LastRun:    time.Date(2026, 10, 15, 1, 31, 42, 0, time.Local),
			},
		},
		"MEDIA-12TB": {
			Name:          "MEDIA-12TB",
			Health:        HealthOnline,
			SizeBytes:     11852504285184,
			SizeHuman:     "10.78 TiB",
			Allocated:     3507421839360,
			AllocHuman:    "3.19 TiB",
			FreeBytes:     8345082445824,
			FreeHuman:     "7.59 TiB",
			Fragmentation: 3,
			CapPercent:    30,
			AtimeDefault:  "off",
			Vdevs: []Vdev{
				{Name: "sdc", Type: "disk", Health: "ONLINE", Path: "/dev/disk/by-id/ata-TOSHIBA_MG08ACA12TE_20E0A099"},
			},
			Scrub: ScrubStatus{
				Active:     false,
				Percentage: 100.0,
				Errors:     0,
				LastRun:    time.Date(2026, 10, 10, 3, 0, 0, 0, time.Local),
			},
		},
		"SSD-MIRROR": {
			Name:          "SSD-MIRROR",
			Health:        HealthOnline,
			SizeBytes:     245553198080,
			SizeHuman:     "228.69 GiB",
			Allocated:     1095216640,
			AllocHuman:    "1.02 GiB",
			FreeBytes:     244457981440,
			FreeHuman:     "227.67 GiB",
			Fragmentation: 0,
			CapPercent:    1,
			AtimeDefault:  "off",
			Vdevs: []Vdev{
				{Name: "mirror-0", Type: "mirror", Health: "ONLINE"},
				{Name: "nvme0n1", Type: "disk", Health: "ONLINE", Path: "/dev/disk/by-id/nvme-Samsung_SSD_980_250GB_S5GXNF0T111"},
				{Name: "nvme1n1", Type: "disk", Health: "ONLINE", Path: "/dev/disk/by-id/nvme-Samsung_SSD_980_250GB_S5GXNF0T112"},
			},
			Scrub: ScrubStatus{
				Active:     false,
				Percentage: 100.0,
				Errors:     0,
				LastRun:    time.Date(2026, 10, 20, 2, 0, 0, 0, time.Local),
			},
		},
	}

	m.mockDS = map[string]*Dataset{
		"DATA": {
			Name:        "DATA",
			Pool:        "DATA",
			Mountpoint:  "/mnt/DATA",
			UsedHuman:   "324.43 GiB",
			AvailHuman:  "1.44 TiB",
			Compression: "1.01x (LZ4)",
			Atime:       "off",
			RecordSize:  "128K",
			Snapshots:   14,
		},
		"DATA/Backup": {
			Name:        "DATA/Backup",
			Pool:        "DATA",
			Mountpoint:  "/mnt/DATA/Backup",
			UsedHuman:   "99.91 GiB",
			AvailHuman:  "1.44 TiB",
			Compression: "1.00x (LZ4)",
			Atime:       "off",
			RecordSize:  "128K",
			Snapshots:   14,
		},
		"DATA/Fileshare": {
			Name:        "DATA/Fileshare",
			Pool:        "DATA",
			Mountpoint:  "/mnt/DATA/Fileshare",
			UsedHuman:   "224.51 GiB",
			AvailHuman:  "1.44 TiB",
			Compression: "1.01x (LZ4)",
			Atime:       "off",
			RecordSize:  "128K",
			Snapshots:   14,
		},
		"DATA/Fileshare/Milena": {
			Name:        "DATA/Fileshare/Milena",
			Pool:        "DATA",
			Mountpoint:  "/mnt/DATA/Fileshare/Milena",
			UsedHuman:   "110.20 GiB",
			AvailHuman:  "1.44 TiB",
			Compression: "LZ4",
			Atime:       "off",
			RecordSize:  "128K",
			Snapshots:   14,
		},
		"DATA/Fileshare/Niklas": {
			Name:        "DATA/Fileshare/Niklas",
			Pool:        "DATA",
			Mountpoint:  "/mnt/DATA/Fileshare/Niklas",
			UsedHuman:   "114.31 GiB",
			AvailHuman:  "1.44 TiB",
			Compression: "LZ4",
			Atime:       "off",
			RecordSize:  "128K",
			Snapshots:   14,
		},
		"MEDIA-12TB": {
			Name:        "MEDIA-12TB",
			Pool:        "MEDIA-12TB",
			Mountpoint:  "/mnt/MEDIA-12TB",
			UsedHuman:   "3.19 TiB",
			AvailHuman:  "7.59 TiB",
			Compression: "1.00x (LZ4)",
			Atime:       "off",
			RecordSize:  "1M",
			Snapshots:   14,
		},
		"MEDIA-12TB/data": {
			Name:        "MEDIA-12TB/data",
			Pool:        "MEDIA-12TB",
			Mountpoint:  "/mnt/MEDIA-12TB/data",
			UsedHuman:   "3.19 TiB",
			AvailHuman:  "7.59 TiB",
			Compression: "1.00x (LZ4)",
			Atime:       "off",
			RecordSize:  "1M",
			Snapshots:   14,
		},
		"SSD-MIRROR": {
			Name:        "SSD-MIRROR",
			Pool:        "SSD-MIRROR",
			Mountpoint:  "/mnt/SSD-MIRROR",
			UsedHuman:   "1.02 GiB",
			AvailHuman:  "227.67 GiB",
			Compression: "1.00x (LZ4)",
			Atime:       "off",
			RecordSize:  "128K",
			Snapshots:   14,
		},
		"SSD-MIRROR/mail-archiver": {
			Name:        "SSD-MIRROR/mail-archiver",
			Pool:        "SSD-MIRROR",
			Mountpoint:  "/mnt/SSD-MIRROR/mail-archiver",
			UsedHuman:   "1.02 GiB",
			AvailHuman:  "227.67 GiB",
			Compression: "1.00x (LZ4)",
			Atime:       "off",
			RecordSize:  "128K",
			Snapshots:   14,
		},
	}

	m.mockSnaps = []*Snapshot{
		{
			Name:       "DATA@auto-2026-10-27-0000",
			Dataset:    "DATA",
			Created:    time.Now().Add(-11 * time.Hour),
			UsedHuman:  "1.2 MiB",
			Referenced: "324.43 GiB",
		},
		{
			Name:       "MEDIA-12TB@auto-2026-10-27-0000",
			Dataset:    "MEDIA-12TB",
			Created:    time.Now().Add(-11 * time.Hour),
			UsedHuman:  "96 KiB",
			Referenced: "3.19 TiB",
		},
		{
			Name:       "SSD-MIRROR@auto-2026-10-27-0000",
			Dataset:    "SSD-MIRROR",
			Created:    time.Now().Add(-11 * time.Hour),
			UsedHuman:  "32 KiB",
			Referenced: "1.02 GiB",
		},
	}
}

// ListPools retrieves all pools and their health
func (m *Manager) ListPools() ([]*Pool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.mockMode {
		var list []*Pool
		for _, p := range m.mockPools {
			list = append(list, p)
		}
		return list, nil
	}

	return m.scanLinuxPools()
}

func (m *Manager) scanLinuxPools() ([]*Pool, error) {
	cmd := exec.Command("zpool", "list", "-H", "-p", "-o", "name,size,alloc,free,frag,cap,health")
	out, err := cmd.CombinedOutput()
	if err != nil {
		outStr := strings.TrimSpace(string(out))
		if strings.Contains(outStr, "no pools available") || strings.Contains(outStr, "no pools found") || outStr == "" {
			return []*Pool{}, nil
		}
		return nil, fmt.Errorf("zpool list: %w (%s)", err, outStr)
	}

	var pools []*Pool
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		f := strings.Fields(line)
		if len(f) < 7 {
			continue
		}
		var size, alloc, free uint64
		var frag, cap int
		fmt.Sscanf(f[1], "%d", &size)
		fmt.Sscanf(f[2], "%d", &alloc)
		fmt.Sscanf(f[3], "%d", &free)
		fmt.Sscanf(strings.TrimSuffix(f[4], "%"), "%d", &frag)
		fmt.Sscanf(strings.TrimSuffix(f[5], "%"), "%d", &cap)

		poolName := f[0]
		vdevs, scrub := m.scanPoolStatus(poolName)

		p := &Pool{
			Name:          poolName,
			Health:        PoolHealth(f[6]),
			SizeBytes:     size,
			SizeHuman:     formatZfsBytes(size),
			Allocated:     alloc,
			AllocHuman:    formatZfsBytes(alloc),
			FreeBytes:     free,
			FreeHuman:     formatZfsBytes(free),
			Fragmentation: frag,
			CapPercent:    cap,
			Vdevs:         vdevs,
			Scrub:         scrub,
			AtimeDefault:  "off",
		}
		pools = append(pools, p)
	}
	return pools, nil
}

func (m *Manager) scanPoolStatus(poolName string) ([]Vdev, ScrubStatus) {
	var vdevs []Vdev
	var scrub ScrubStatus
	scrub.LastRun = time.Now()

	cmd := exec.Command("zpool", "status", poolName)
	out, err := cmd.Output()
	if err != nil {
		return vdevs, scrub
	}

	scanner := bufio.NewScanner(bytes.NewReader(out))
	inConfig := false
	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)

		// Scrub line check
		if strings.HasPrefix(trimmed, "scan:") {
			if strings.Contains(trimmed, "scrub in progress") {
				scrub.Active = true
			}
			if strings.Contains(trimmed, "repaired") {
				// parse errors
				scrub.Active = false
			}
		}

		if strings.HasPrefix(trimmed, "config:") {
			inConfig = true
			continue
		}

		if inConfig {
			if strings.HasPrefix(trimmed, "NAME") || trimmed == "" {
				continue
			}
			if strings.HasPrefix(trimmed, "errors:") {
				break
			}

			parts := strings.Fields(trimmed)
			if len(parts) >= 2 {
				name := parts[0]
				health := parts[1]
				if name == poolName {
					continue
				}

				devType := "disk"
				if strings.HasPrefix(name, "mirror") {
					devType = "mirror"
				} else if strings.HasPrefix(name, "raidz") {
					devType = name
				}

				vdevs = append(vdevs, Vdev{
					Name:   name,
					Type:   devType,
					Health: health,
					Path:   "/dev/" + filepath.Base(name),
				})
			}
		}
	}
	return vdevs, scrub
}

// ListDatasets returns all datasets
func (m *Manager) ListDatasets() ([]*Dataset, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.mockMode {
		var list []*Dataset
		for _, d := range m.mockDS {
			list = append(list, d)
		}
		return list, nil
	}

	snapCounts := m.getSnapshotCounts()

	cmd := exec.Command("zfs", "list", "-H", "-o", "name,used,avail,mountpoint,compression,atime,recordsize")
	out, err := cmd.CombinedOutput()
	if err != nil {
		outStr := strings.TrimSpace(string(out))
		if strings.Contains(outStr, "no datasets available") || strings.Contains(outStr, "no pools available") || outStr == "" {
			return []*Dataset{}, nil
		}
		return nil, fmt.Errorf("zfs list: %w (%s)", err, outStr)
	}

	var datasets []*Dataset
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		f := strings.Fields(line)
		if len(f) < 7 {
			continue
		}
		parts := strings.Split(f[0], "/")
		poolName := parts[0]

		ds := &Dataset{
			Name:        f[0],
			Pool:        poolName,
			UsedHuman:   f[1],
			AvailHuman:  f[2],
			Mountpoint:  f[3],
			Compression: f[4],
			Atime:       f[5],
			RecordSize:  f[6],
			Snapshots:   snapCounts[f[0]],
		}
		datasets = append(datasets, ds)
	}
	return datasets, nil
}

func (m *Manager) getSnapshotCounts() map[string]int {
	counts := make(map[string]int)
	cmd := exec.Command("zfs", "list", "-t", "snapshot", "-H", "-o", "name")
	out, err := cmd.Output()
	if err != nil {
		return counts
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		parts := strings.Split(l, "@")
		if len(parts) >= 1 {
			counts[parts[0]]++
		}
	}
	return counts
}

// CreateDataset creates a dataset with 'atime=off' enabled by default!
func (m *Manager) CreateDataset(name string, compression, recordsize string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if compression == "" {
		compression = "lz4"
	}
	if recordsize == "" {
		recordsize = "128K"
	}

	if m.mockMode {
		parts := strings.Split(name, "/")
		pool := parts[0]
		m.mockDS[name] = &Dataset{
			Name:        name,
			Pool:        pool,
			Mountpoint:  "/mnt/" + name,
			UsedHuman:   "0 B",
			AvailHuman:  "Available",
			Compression: compression,
			Atime:       "off", // Crucial for HDD Standby!
			RecordSize:  recordsize,
			Snapshots:   0,
		}
		return nil
	}

	// Always enforce atime=off for HDD standby safety!
	args := []string{
		"create",
		"-o", "atime=off",
		"-o", "compression=" + compression,
		"-o", "recordsize=" + recordsize,
		name,
	}

	cmd := exec.Command("zfs", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("zfs create failed: %v (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// DestroyDataset deletes a dataset
func (m *Manager) DestroyDataset(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.mockMode {
		delete(m.mockDS, name)
		return nil
	}

	cmd := exec.Command("zfs", "destroy", "-r", name)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("zfs destroy failed: %v (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// ListSnapshots returns all snapshots
func (m *Manager) ListSnapshots() ([]*Snapshot, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.mockMode {
		return m.mockSnaps, nil
	}

	cmd := exec.Command("zfs", "list", "-t", "snapshot", "-H", "-p", "-o", "name,used,refer,creation")
	out, err := cmd.CombinedOutput()
	if err != nil {
		outStr := strings.TrimSpace(string(out))
		if strings.Contains(outStr, "no datasets available") || strings.Contains(outStr, "no snapshots available") || strings.Contains(outStr, "no pools available") || outStr == "" {
			return []*Snapshot{}, nil
		}
		return nil, fmt.Errorf("zfs list snapshot: %w (%s)", err, outStr)
	}

	var list []*Snapshot
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	for _, line := range lines {
		f := strings.Fields(line)
		if len(f) < 4 {
			continue
		}
		parts := strings.Split(f[0], "@")
		datasetName := parts[0]
		createdEpoch, _ := strconv.ParseInt(f[3], 10, 64)
		created := time.Unix(createdEpoch, 0)

		var usedBytes, referBytes uint64
		fmt.Sscanf(f[1], "%d", &usedBytes)
		fmt.Sscanf(f[2], "%d", &referBytes)

		list = append(list, &Snapshot{
			Name:       f[0],
			Dataset:    datasetName,
			Created:    created,
			UsedHuman:  formatZfsBytes(usedBytes),
			Referenced: formatZfsBytes(referBytes),
		})
	}
	return list, nil
}

// CreateSnapshot creates a manual snapshot
func (m *Manager) CreateSnapshot(dataset, snapName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	fullName := fmt.Sprintf("%s@%s", dataset, snapName)
	if m.mockMode {
		m.mockSnaps = append(m.mockSnaps, &Snapshot{
			Name:       fullName,
			Dataset:    dataset,
			Created:    time.Now(),
			UsedHuman:  "0 B",
			Referenced: "Available",
		})
		if ds, ok := m.mockDS[dataset]; ok {
			ds.Snapshots++
		}
		return nil
	}

	cmd := exec.Command("zfs", "snapshot", fullName)
	return cmd.Run()
}

// RollbackSnapshot rolls back a dataset to a snapshot
func (m *Manager) RollbackSnapshot(snapshot string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.mockMode {
		return nil
	}

	cmd := exec.Command("zfs", "rollback", "-r", snapshot)
	return cmd.Run()
}

// DestroySnapshot deletes a snapshot
func (m *Manager) DestroySnapshot(snapshot string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.mockMode {
		for i, s := range m.mockSnaps {
			if s.Name == snapshot {
				m.mockSnaps = append(m.mockSnaps[:i], m.mockSnaps[i+1:]...)
				break
			}
		}
		return nil
	}

	cmd := exec.Command("zfs", "destroy", snapshot)
	return cmd.Run()
}

// SetDatasetProperty sets a ZFS dataset property like compression, recordsize, or quota
func (m *Manager) SetDatasetProperty(dataset, property, value string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.mockMode {
		if ds, ok := m.mockDS[dataset]; ok {
			switch property {
			case "compression":
				ds.Compression = value
			case "recordsize":
				ds.RecordSize = value
			}
		}
		return nil
	}

	cmd := exec.Command("zfs", "set", fmt.Sprintf("%s=%s", property, value), dataset)
	return cmd.Run()
}

// StartScrub initiates a scrub on a pool
func (m *Manager) StartScrub(poolName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.mockMode {
		if p, ok := m.mockPools[poolName]; ok {
			p.Scrub.Active = true
			p.Scrub.Percentage = 0.1
			p.Scrub.Speed = "480 MB/s"
			p.Scrub.TimeLeft = "3h 45m"
		}
		return nil
	}

	cmd := exec.Command("zpool", "scrub", poolName)
	return cmd.Run()
}

// StopScrub pauses or cancels an active scrub
func (m *Manager) StopScrub(poolName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.mockMode {
		if p, ok := m.mockPools[poolName]; ok {
			p.Scrub.Active = false
		}
		return nil
	}

	cmd := exec.Command("zpool", "scrub", "-s", poolName)
	return cmd.Run()
}

// CreatePool creates a new ZFS pool with chosen topology, ashift, compression and atime=off
func (m *Manager) CreatePool(name, vdevType string, disks []string, ashift int, compression string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if ashift <= 0 {
		ashift = 12 // Default 4K sectors
	}
	if compression == "" {
		compression = "zstd"
	}

	if m.mockMode {
		var vdevs []Vdev
		if vdevType != "stripe" && vdevType != "single" {
			vdevs = append(vdevs, Vdev{
				Name:   vdevType + "-0",
				Type:   vdevType,
				Health: "ONLINE",
			})
		}
		for _, d := range disks {
			vdevs = append(vdevs, Vdev{
				Name:   d,
				Type:   "disk",
				Health: "ONLINE",
				Path:   "/dev/" + d,
			})
		}

		rawSize := uint64(len(disks)) * 16000900661248 // 16 TB per disk mock
		if vdevType == "mirror" && len(disks) > 0 {
			rawSize = 16000900661248
		} else if vdevType == "raidz1" && len(disks) > 1 {
			rawSize = uint64(len(disks)-1) * 16000900661248
		} else if vdevType == "raidz2" && len(disks) > 2 {
			rawSize = uint64(len(disks)-2) * 16000900661248
		}
		if rawSize == 0 {
			rawSize = 32000000000000
		}
		m.mockPools[name] = &Pool{
			Name:          name,
			Health:        HealthOnline,
			SizeBytes:     rawSize,
			SizeHuman:     formatZfsBytes(rawSize),
			Allocated:     1048576,
			AllocHuman:    "1.0 MiB",
			FreeBytes:     rawSize - 1048576,
			FreeHuman:     formatZfsBytes(rawSize - 1048576),
			Fragmentation: 0,
			CapPercent:    0,
			AtimeDefault:  "off",
			Vdevs:         vdevs,
			Scrub: ScrubStatus{
				Active:     false,
				Percentage: 100.0,
				Errors:     0,
				LastRun:    time.Now(),
			},
		}

		// Also create root dataset
		m.mockDS[name] = &Dataset{
			Name:        name,
			Pool:        name,
			Mountpoint:  "/mnt/" + name,
			UsedHuman:   "1.0 MiB",
			AvailHuman:  formatZfsBytes(rawSize - 1048576),
			Compression: compression,
			Atime:       "off",
			RecordSize:  "128K",
			Snapshots:   0,
		}
		return nil
	}

	args := []string{
		"create",
		"-f",
		"-o", fmt.Sprintf("ashift=%d", ashift),
		"-O", "atime=off",
		"-O", "compression=" + compression,
		name,
	}
	if vdevType != "stripe" && vdevType != "single" && vdevType != "" {
		args = append(args, vdevType)
	}

	var cleanDisks []string
	for _, d := range disks {
		d = strings.TrimSpace(d)
		if d == "" || d == "undefined" {
			continue
		}
		if !strings.HasPrefix(d, "/") {
			d = "/dev/" + d
		}
		cleanDisks = append(cleanDisks, d)
	}
	if len(cleanDisks) == 0 {
		return fmt.Errorf("no valid block devices specified")
	}
	args = append(args, cleanDisks...)

	cmd := exec.Command("zpool", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("zpool create failed: %v (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// DestroyPool destroys a storage pool
func (m *Manager) DestroyPool(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	name = strings.TrimSpace(name)
	name = strings.TrimPrefix(name, "/dev/")
	if name == "" {
		return fmt.Errorf("pool name is required")
	}

	if m.mockMode {
		delete(m.mockPools, name)
		for ds := range m.mockDS {
			if ds == name || strings.HasPrefix(ds, name+"/") {
				delete(m.mockDS, ds)
			}
		}
		return nil
	}

	// Unmount filesystems first to prevent busy lockouts
	_ = exec.Command("zfs", "unmount", "-f", name).Run()

	cmd := exec.Command("zpool", "destroy", "-f", name)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("zpool destroy failed: %v (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// ExportPool exports a storage pool safely
func (m *Manager) ExportPool(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.mockMode {
		if p, ok := m.mockPools[name]; ok {
			m.mockExported[name] = &ImportablePool{
				Name:   name,
				ID:     fmt.Sprintf("%d", time.Now().UnixNano()),
				State:  string(p.Health),
				Action: "The pool was exported cleanly and can be imported directly.",
				Config: fmt.Sprintf("%s %s", name, p.Health),
			}
			delete(m.mockPools, name)
		}
		return nil
	}

	cmd := exec.Command("zpool", "export", name)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("zpool export failed: %v (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// FindImportablePools scans for pools that can be imported
func (m *Manager) FindImportablePools() ([]*ImportablePool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.mockMode {
		var list []*ImportablePool
		for _, p := range m.mockExported {
			list = append(list, p)
		}
		return list, nil
	}

	cmd := exec.Command("zpool", "import", "-d", "/dev/disk/by-id")
	out, _ := cmd.CombinedOutput()
	var list []*ImportablePool
	if len(out) == 0 {
		return list, nil
	}

	scanner := bufio.NewScanner(bytes.NewReader(out))
	var current *ImportablePool

	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)

		if strings.HasPrefix(trimmed, "pool:") {
			if current != nil {
				list = append(list, current)
			}
			current = &ImportablePool{
				Name: strings.TrimSpace(strings.TrimPrefix(trimmed, "pool:")),
			}
		} else if current != nil {
			if strings.HasPrefix(trimmed, "id:") {
				current.ID = strings.TrimSpace(strings.TrimPrefix(trimmed, "id:"))
			} else if strings.HasPrefix(trimmed, "state:") {
				current.State = strings.TrimSpace(strings.TrimPrefix(trimmed, "state:"))
			} else if strings.HasPrefix(trimmed, "action:") {
				current.Action = strings.TrimSpace(strings.TrimPrefix(trimmed, "action:"))
			} else if strings.HasPrefix(trimmed, "status:") {
				current.Action = strings.TrimSpace(strings.TrimPrefix(trimmed, "status:"))
			} else if current.Config == "" && (strings.Contains(trimmed, "ONLINE") || strings.Contains(trimmed, "DEGRADED")) {
				current.Config = trimmed
			}
		}
	}
	if current != nil {
		list = append(list, current)
	}

	return list, nil
}

// ImportPool imports an existing storage pool
func (m *Manager) ImportPool(nameOrID, newName string, force bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.mockMode {
		var found *ImportablePool
		var foundKey string
		for k, p := range m.mockExported {
			if p.Name == nameOrID || p.ID == nameOrID {
				found = p
				foundKey = k
				break
			}
		}
		targetName := nameOrID
		if newName != "" {
			targetName = newName
		} else if found != nil {
			targetName = found.Name
		}

		delete(m.mockExported, foundKey)
		m.mockPools[targetName] = &Pool{
			Name:          targetName,
			Health:        HealthOnline,
			SizeBytes:     16000900661248,
			SizeHuman:     "14.55 TiB",
			Allocated:     348356784128,
			AllocHuman:    "324.43 GiB",
			FreeBytes:     15652543877120,
			FreeHuman:     "14.23 TiB",
			Fragmentation: 1,
			CapPercent:    2,
			AtimeDefault:  "off",
			Vdevs: []Vdev{
				{Name: "mirror-0", Type: "mirror", Health: "ONLINE"},
				{Name: "sdd", Type: "disk", Health: "ONLINE", Path: "/dev/sdd"},
				{Name: "sde", Type: "disk", Health: "ONLINE", Path: "/dev/sde"},
			},
			Scrub: ScrubStatus{
				Active:     false,
				Percentage: 100.0,
				Errors:     0,
				LastRun:    time.Now().Add(-24 * time.Hour),
			},
		}
		m.mockDS[targetName] = &Dataset{
			Name:        targetName,
			Pool:        targetName,
			Mountpoint:  "/mnt/" + targetName,
			UsedHuman:   "324.43 GiB",
			AvailHuman:  "14.23 TiB",
			Compression: "lz4",
			Atime:       "off",
			RecordSize:  "128K",
		}
		return nil
	}

	args := []string{"import", "-d", "/dev/disk/by-id"}
	if force {
		args = append(args, "-f")
	}
	args = append(args, nameOrID)
	if newName != "" {
		args = append(args, newName)
	}

	cmd := exec.Command("zpool", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("zpool import failed: %v (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// TrimPool initiates a TRIM operation across all SSD / NVMe vdevs in the pool
func (m *Manager) TrimPool(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.mockMode {
		return nil
	}

	cmd := exec.Command("zpool", "trim", name)
	return cmd.Run()
}

// ClearPool clears errors and device faults on a pool
func (m *Manager) ClearPool(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.mockMode {
		if p, ok := m.mockPools[name]; ok {
			p.Health = HealthOnline
			p.Scrub.Errors = 0
		}
		return nil
	}

	cmd := exec.Command("zpool", "clear", name)
	return cmd.Run()
}

// AddVdev expands pool capacity or adds auxiliary vdevs (log, cache, special metadata, spare)
func (m *Manager) AddVdev(pool, vdevType string, disks []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if len(disks) == 0 {
		return fmt.Errorf("at least one disk required")
	}

	if m.mockMode {
		if p, ok := m.mockPools[pool]; ok {
			for _, d := range disks {
				p.Vdevs = append(p.Vdevs, Vdev{
					Name:   d,
					Type:   vdevType,
					Health: "ONLINE",
					Path:   "/dev/" + d,
				})
			}
			if vdevType == "cache" || vdevType == "log" || vdevType == "special" {
				// auxiliary vdev
			} else {
				p.SizeBytes += uint64(len(disks)) * 16000900661248
				p.FreeBytes += uint64(len(disks)) * 16000900661248
				p.SizeHuman = formatZfsBytes(p.SizeBytes)
				p.FreeHuman = formatZfsBytes(p.FreeBytes)
			}
		}
		return nil
	}

	args := []string{"add", "-f", pool}
	if vdevType != "stripe" && vdevType != "single" && vdevType != "disk" && vdevType != "data" {
		args = append(args, vdevType)
	}

	var cleanDisks []string
	for _, d := range disks {
		d = strings.TrimSpace(d)
		if d == "" || d == "undefined" {
			continue
		}
		if !strings.HasPrefix(d, "/") {
			d = "/dev/" + d
		}
		cleanDisks = append(cleanDisks, d)
	}
	if len(cleanDisks) == 0 {
		return fmt.Errorf("no valid block devices specified")
	}
	args = append(args, cleanDisks...)

	cmd := exec.Command("zpool", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("zpool add failed: %v (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// AttachDisk attaches a new disk to an existing device (creating or widening a mirror)
func (m *Manager) AttachDisk(pool, existingDev, newDev string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.mockMode {
		if p, ok := m.mockPools[pool]; ok {
			p.Vdevs = append(p.Vdevs, Vdev{
				Name:   newDev,
				Type:   "mirror",
				Health: "ONLINE",
				Path:   "/dev/" + newDev,
			})
		}
		return nil
	}

	cmd := exec.Command("zpool", "attach", pool, existingDev, newDev)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("zpool attach failed: %v (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// DetachDisk detaches a disk from a mirror vdev
func (m *Manager) DetachDisk(pool, device string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.mockMode {
		if p, ok := m.mockPools[pool]; ok {
			for i, v := range p.Vdevs {
				if v.Name == device || v.Path == device {
					p.Vdevs = append(p.Vdevs[:i], p.Vdevs[i+1:]...)
					break
				}
			}
		}
		return nil
	}

	cmd := exec.Command("zpool", "detach", pool, device)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("zpool detach failed: %v (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// ReplaceDisk replaces a disk in a pool with another device
func (m *Manager) ReplaceDisk(pool, oldDev, newDev string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.mockMode {
		if p, ok := m.mockPools[pool]; ok {
			for i, v := range p.Vdevs {
				if v.Name == oldDev || v.Path == oldDev {
					p.Vdevs[i].Name = newDev
					p.Vdevs[i].Path = "/dev/" + newDev
					break
				}
			}
		}
		return nil
	}

	cmd := exec.Command("zpool", "replace", pool, oldDev, newDev)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("zpool replace failed: %v (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// OfflineDisk sets a disk offline
func (m *Manager) OfflineDisk(pool, device string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.mockMode {
		if p, ok := m.mockPools[pool]; ok {
			for i, v := range p.Vdevs {
				if v.Name == device || v.Path == device {
					p.Vdevs[i].Health = "OFFLINE"
					break
				}
			}
		}
		return nil
	}

	cmd := exec.Command("zpool", "offline", pool, device)
	return cmd.Run()
}

// OnlineDisk brings an offline disk back online
func (m *Manager) OnlineDisk(pool, device string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.mockMode {
		if p, ok := m.mockPools[pool]; ok {
			for i, v := range p.Vdevs {
				if v.Name == device || v.Path == device {
					p.Vdevs[i].Health = "ONLINE"
					break
				}
			}
		}
		return nil
	}

	cmd := exec.Command("zpool", "online", pool, device)
	return cmd.Run()
}

// SetPoolProperty sets a zpool property like autotrim, autoexpand, failmode
func (m *Manager) SetPoolProperty(pool, property, value string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.mockMode {
		return nil
	}

	cmd := exec.Command("zpool", "set", fmt.Sprintf("%s=%s", property, value), pool)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("zpool set failed: %v (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// CreateZvol creates a block volume for VMs, iSCSI or raw block storage
func (m *Manager) CreateZvol(name string, sizeBytes uint64, volblocksize string, sparse bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if volblocksize == "" {
		volblocksize = "16k"
	}

	if m.mockMode {
		poolName := strings.Split(name, "/")[0]
		m.mockDS[name] = &Dataset{
			Name:        name,
			Pool:        poolName,
			Mountpoint:  "/dev/zvol/" + name,
			UsedHuman:   "0 B",
			AvailHuman:  formatZfsBytes(sizeBytes),
			Compression: "lz4",
			Atime:       "off",
			RecordSize:  volblocksize,
		}
		return nil
	}

	args := []string{"create", "-V", fmt.Sprintf("%d", sizeBytes), "-b", volblocksize}
	if sparse {
		args = append(args, "-s")
	}
	args = append(args, name)

	cmd := exec.Command("zfs", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("zfs create -V failed: %v (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// CloneSnapshot creates a writable dataset clone from a read-only snapshot
func (m *Manager) CloneSnapshot(snapshot, targetDataset string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.mockMode {
		poolName := strings.Split(targetDataset, "/")[0]
		m.mockDS[targetDataset] = &Dataset{
			Name:        targetDataset,
			Pool:        poolName,
			Mountpoint:  "/mnt/" + targetDataset,
			UsedHuman:   "1.0 MiB",
			AvailHuman:  "Available",
			Compression: "lz4",
			Atime:       "off",
			RecordSize:  "128K",
		}
		return nil
	}

	cmd := exec.Command("zfs", "clone", snapshot, targetDataset)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("zfs clone failed: %v (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func formatZfsBytes(b uint64) string {
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
