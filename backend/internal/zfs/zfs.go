package zfs

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
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

const (
	fastZfsTimeout    = 10 * time.Second
	defaultZfsTimeout = 25 * time.Second
	longZfsTimeout    = 120 * time.Second
)

func runCommand(timeout time.Duration, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return nil, fmt.Errorf("command '%s %s' timed out after %v", name, strings.Join(args, " "), timeout)
	}
	return out, err
}

func runCommandOutput(timeout time.Duration, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.Output()
	if ctx.Err() == context.DeadlineExceeded {
		return nil, fmt.Errorf("command '%s %s' timed out after %v", name, strings.Join(args, " "), timeout)
	}
	return out, err
}

type PoolHealth string

const (
	HealthOnline   PoolHealth = "ONLINE"
	HealthDegraded PoolHealth = "DEGRADED"
	HealthFaulted  PoolHealth = "FAULTED"
	HealthOffline  PoolHealth = "OFFLINE"
	HealthUnknown  PoolHealth = "UNKNOWN"
)

type Vdev struct {
	Name        string `json:"name"`
	Type        string `json:"type"` // disk, mirror, raidz1, raidz2, log, cache, special, spare
	Role        string `json:"role"` // e.g. "mirror-0", "raidz1-0", "stripe", "log", "cache", "special", "spare"
	Health      string `json:"health"`
	State       string `json:"state"`
	Path        string `json:"path,omitempty"`
	SizeBytes   uint64 `json:"size_bytes"`
	ReadErrors  uint64 `json:"read_errors"`
	WriteErrors uint64 `json:"write_errors"`
	CksumErrors uint64 `json:"cksum_errors"`
	IsDisk      bool   `json:"is_disk"`
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
	Topology      string      `json:"topology"`
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
	ScrubStatus   string      `json:"scrub_status"`
	AtimeDefault  string      `json:"atime_default"`
	Autotrim      string      `json:"autotrim"`
	Autoexpand    string      `json:"autoexpand"`
	Compression   string      `json:"compression"`
}

type Dataset struct {
	Name           string            `json:"name"`
	Pool           string            `json:"pool"`
	Type           string            `json:"type"` // filesystem or volume
	Mountpoint     string            `json:"mountpoint"`
	UsedBytes      uint64            `json:"used_bytes"`
	UsedHuman      string            `json:"used_human"`
	AvailBytes     uint64            `json:"avail_bytes"`
	AvailableBytes uint64            `json:"available_bytes"`
	AvailHuman     string            `json:"avail_human"`
	Quota          string            `json:"quota"`
	ReadOnly       string            `json:"readonly"`
	Compression    string            `json:"compression"`
	Atime          string            `json:"atime"`
	RecordSize     string            `json:"recordsize"`
	Sync           string            `json:"sync"`
	Snapshots      int               `json:"snapshots_count"`
	Encryption     string            `json:"encryption"`
	Origin         string            `json:"origin,omitempty"`
	Properties     map[string]string `json:"properties,omitempty"`
}

type Snapshot struct {
	Name            string    `json:"name"`
	Dataset         string    `json:"dataset"`
	Created         time.Time `json:"created"`
	UsedBytes       uint64    `json:"used_bytes"`
	UsedHuman       string    `json:"used_human"`
	ReferencedBytes uint64    `json:"referenced_bytes"`
	Referenced      string    `json:"referenced"`
}

func syncPoolFields(p *Pool) {
	if p == nil {
		return
	}
	if p.Autotrim == "" {
		p.Autotrim = "off"
	}
	if p.Autoexpand == "" {
		p.Autoexpand = "on"
	}
	if p.AtimeDefault == "" {
		p.AtimeDefault = "off"
	}

	var currentGroup string
	hasMirror := false
	hasRaidz1 := false
	hasRaidz2 := false
	hasRaidz3 := false
	diskCount := 0
	offlineCount := 0

	for i := range p.Vdevs {
		v := &p.Vdevs[i]
		if v.Health == "" && v.State != "" {
			v.Health = v.State
		}
		if v.Health == "" {
			v.Health = "ONLINE"
		}
		v.State = v.Health

		isGroup := strings.HasPrefix(v.Name, "mirror") || strings.HasPrefix(v.Name, "raidz") ||
			v.Name == "logs" || v.Name == "cache" || v.Name == "special" || v.Name == "spares" || v.Name == "dedup"
		v.IsDisk = !isGroup

		if isGroup {
			currentGroup = v.Name
			if strings.HasPrefix(v.Name, "mirror") {
				hasMirror = true
			} else if strings.HasPrefix(v.Name, "raidz1") {
				hasRaidz1 = true
			} else if strings.HasPrefix(v.Name, "raidz2") {
				hasRaidz2 = true
			} else if strings.HasPrefix(v.Name, "raidz3") {
				hasRaidz3 = true
			}
		} else {
			diskCount++
			if v.Health != "ONLINE" && v.Type != "spare" {
				offlineCount++
			}
			if v.Role == "" {
				if v.Type == "log" || v.Type == "cache" || v.Type == "special" || v.Type == "spare" {
					v.Role = v.Type
				} else if currentGroup != "" {
					v.Role = currentGroup
				} else if v.Type != "" && v.Type != "disk" {
					v.Role = v.Type
				} else {
					v.Role = "stripe"
				}
			}
			if v.SizeBytes == 0 && p.SizeBytes > 0 {
				v.SizeBytes = p.SizeBytes
			}
			if v.Path == "" {
				v.Path = "/dev/" + v.Name
			}
		}
	}

	if p.Topology == "" {
		switch {
		case hasRaidz3:
			p.Topology = fmt.Sprintf("RAID-Z3 (%d Disks)", diskCount)
		case hasRaidz2:
			p.Topology = fmt.Sprintf("RAID-Z2 (%d Disks)", diskCount)
		case hasRaidz1:
			p.Topology = fmt.Sprintf("RAID-Z1 (%d Disks)", diskCount)
		case hasMirror:
			p.Topology = fmt.Sprintf("MIRROR (%d Disks)", diskCount)
		case diskCount == 1:
			p.Topology = "SINGLE DISK"
		default:
			p.Topology = fmt.Sprintf("STRIPE (%d Disks)", diskCount)
		}
	}

	if offlineCount > 0 {
		if hasMirror || hasRaidz1 || hasRaidz2 || hasRaidz3 {
			if offlineCount >= diskCount {
				p.Health = HealthFaulted
			} else {
				p.Health = HealthDegraded
			}
		} else {
			p.Health = HealthFaulted
		}
	} else if p.Health == HealthDegraded || p.Health == HealthOffline {
		p.Health = HealthOnline
	}

	if p.Scrub.Active {
		speed := p.Scrub.Speed
		if speed == "" {
			speed = "480 MB/s"
		}
		p.ScrubStatus = fmt.Sprintf("IN PROGRESS (%.1f%% done, %s)", p.Scrub.Percentage, speed)
	} else if !p.Scrub.LastRun.IsZero() {
		p.ScrubStatus = fmt.Sprintf("COMPLETED (%d errors on %s)", p.Scrub.Errors, p.Scrub.LastRun.Format("2006-01-02 15:04"))
	} else {
		p.ScrubStatus = "IDLE (0 errors)"
	}
}

func syncDatasetFields(ds *Dataset) {
	if ds == nil {
		return
	}
	if ds.Type == "" {
		if strings.HasPrefix(ds.Mountpoint, "/dev/zvol") || ds.Mountpoint == "-" {
			ds.Type = "volume"
		} else {
			ds.Type = "filesystem"
		}
	}
	if ds.AvailableBytes == 0 && ds.AvailBytes > 0 {
		ds.AvailableBytes = ds.AvailBytes
	} else if ds.AvailBytes == 0 && ds.AvailableBytes > 0 {
		ds.AvailBytes = ds.AvailableBytes
	}
	if ds.Quota == "" {
		ds.Quota = "none"
	}
	if ds.ReadOnly == "" {
		ds.ReadOnly = "off"
	}
	if ds.Atime == "" {
		ds.Atime = "off"
	}
	if ds.Sync == "" {
		ds.Sync = "standard"
	}
	if ds.Encryption == "" {
		ds.Encryption = "Unencrypted"
	}
}

type ImportablePool struct {
	Name   string `json:"name"`
	ID     string `json:"id"`
	State  string `json:"state"`
	Action string `json:"action"`
	Config string `json:"config"`
}

type Manager struct {
	mu                 sync.RWMutex
	mockMode           bool
	mockPools          map[string]*Pool
	mockDS             map[string]*Dataset
	mockSnaps          []*Snapshot
	mockExported       map[string]*ImportablePool
	mockExportedPools  map[string]*Pool
	mockExportedDS     map[string][]*Dataset
	snapTasks          map[string]*SnapshotTask
	snapTaskConfigPath string
}

func NewManager(mockMode bool) *Manager {
	m := &Manager{
		mockMode:           mockMode || runtime.GOOS != "linux",
		mockPools:          make(map[string]*Pool),
		mockDS:             make(map[string]*Dataset),
		mockExported:       make(map[string]*ImportablePool),
		mockExportedPools:  make(map[string]*Pool),
		mockExportedDS:     make(map[string][]*Dataset),
		snapTasks:          make(map[string]*SnapshotTask),
		snapTaskConfigPath: defaultSnapshotTasksPath,
	}

	if m.mockMode {
		m.initMockZFS()
	}
	m.mu.Lock()
	m.initSnapshotTasksLocked()
	m.mu.Unlock()
	go m.startSnapshotScheduler()
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
			Topology:      "MIRROR (2 Disks)",
			SizeBytes:     1936505501450,
			SizeHuman:     "1.76 TiB",
			Allocated:     353208757453,
			AllocHuman:    "328.95 GiB",
			FreeBytes:     1583296743997,
			FreeHuman:     "1.44 TiB",
			Fragmentation: 1,
			CapPercent:    18,
			AtimeDefault:  "off",
			Autotrim:      "off",
			Autoexpand:    "on",
			Compression:   "lz4",
			Vdevs: []Vdev{
				{Name: "mirror-0", Type: "mirror", Health: "ONLINE", State: "ONLINE", IsDisk: false},
				{Name: "sda", Type: "disk", Role: "mirror-0", Health: "ONLINE", State: "ONLINE", SizeBytes: 2000398934016, Path: "/dev/disk/by-id/ata-WDC_WD20EFAX-68B29N0_WD-WCC4M123", IsDisk: true},
				{Name: "sdb", Type: "disk", Role: "mirror-0", Health: "ONLINE", State: "ONLINE", SizeBytes: 2000398934016, Path: "/dev/disk/by-id/ata-WDC_WD20EFAX-68B29N0_WD-WCC4M124", IsDisk: true},
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
			Topology:      "SINGLE DISK",
			SizeBytes:     11852611517808,
			SizeHuman:     "10.78 TiB",
			Allocated:     4024227712860,
			AllocHuman:    "3.66 TiB",
			FreeBytes:     7828383804948,
			FreeHuman:     "7.12 TiB",
			Fragmentation: 3,
			CapPercent:    34,
			AtimeDefault:  "off",
			Autotrim:      "off",
			Autoexpand:    "on",
			Compression:   "lz4",
			Vdevs: []Vdev{
				{Name: "sdc", Type: "disk", Role: "stripe", Health: "ONLINE", State: "ONLINE", SizeBytes: 12000138625024, Path: "/dev/disk/by-id/ata-TOSHIBA_MG08ACA12TE_20E0A099", IsDisk: true},
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
			Topology:      "MIRROR (2 Disks)",
			SizeBytes:     245542460129,
			SizeHuman:     "228.68 GiB",
			Allocated:     1127428915,
			AllocHuman:    "1.05 GiB",
			FreeBytes:     244415031214,
			FreeHuman:     "227.63 GiB",
			Fragmentation: 0,
			CapPercent:    1,
			AtimeDefault:  "off",
			Autotrim:      "on",
			Autoexpand:    "on",
			Compression:   "lz4",
			Vdevs: []Vdev{
				{Name: "mirror-0", Type: "mirror", Health: "ONLINE", State: "ONLINE", IsDisk: false},
				{Name: "nvme0n1", Type: "disk", Role: "mirror-0", Health: "ONLINE", State: "ONLINE", SizeBytes: 1000204886016, Path: "/dev/disk/by-id/nvme-Samsung_SSD_980_250GB_S5GXNF0T111", IsDisk: true},
				{Name: "nvme1n1", Type: "disk", Role: "mirror-0", Health: "ONLINE", State: "ONLINE", SizeBytes: 1000204886016, Path: "/dev/disk/by-id/nvme-Samsung_SSD_980_250GB_S5GXNF0T112", IsDisk: true},
			},
			Scrub: ScrubStatus{
				Active:     false,
				Percentage: 100.0,
				Errors:     0,
				LastRun:    time.Date(2026, 10, 20, 2, 0, 0, 0, time.Local),
			},
		},
	}
	for _, p := range m.mockPools {
		syncPoolFields(p)
	}

	m.mockDS = map[string]*Dataset{
		"DATA": {
			Name:        "DATA",
			Pool:        "DATA",
			Mountpoint:  "/mnt/DATA",
			UsedBytes:   353208757453,
			UsedHuman:   "328.95 GiB",
			AvailBytes:  1583296743997,
			AvailHuman:  "1.44 TiB",
			Compression: "lz4",
			Atime:       "off",
			RecordSize:  "128K",
			Snapshots:   14,
			Encryption:  "Unencrypted",
		},
		"DATA/Backup": {
			Name:        "DATA/Backup",
			Pool:        "DATA",
			Mountpoint:  "/mnt/DATA/Backup",
			UsedBytes:   112023594598,
			UsedHuman:   "104.33 GiB",
			AvailBytes:  1583296743997,
			AvailHuman:  "1.44 TiB",
			Compression: "lz4",
			Atime:       "off",
			RecordSize:  "128K",
			Snapshots:   14,
			Encryption:  "Unencrypted",
		},
		"DATA/Fileshare": {
			Name:        "DATA/Fileshare",
			Pool:        "DATA",
			Mountpoint:  "/mnt/DATA/Fileshare",
			UsedBytes:   241164999066,
			UsedHuman:   "224.6 GiB",
			AvailBytes:  1583296743997,
			AvailHuman:  "1.44 TiB",
			Compression: "lz4",
			Atime:       "off",
			RecordSize:  "128K",
			Snapshots:   14,
			Encryption:  "Unencrypted",
		},
		"DATA/Fileshare/Milena": {
			Name:        "DATA/Fileshare/Milena",
			Pool:        "DATA",
			Mountpoint:  "/mnt/DATA/Fileshare/Milena",
			UsedBytes:   179475477300,
			UsedHuman:   "167.15 GiB",
			AvailBytes:  1583296743997,
			AvailHuman:  "1.44 TiB",
			Compression: "lz4",
			Atime:       "off",
			RecordSize:  "128K",
			Snapshots:   14,
			Encryption:  "Unencrypted",
		},
		"DATA/Fileshare/Niklas": {
			Name:        "DATA/Fileshare/Niklas",
			Pool:        "DATA",
			Mountpoint:  "/mnt/DATA/Fileshare/Niklas",
			UsedBytes:   61697495859,
			UsedHuman:   "57.46 GiB",
			AvailBytes:  1583296743997,
			AvailHuman:  "1.44 TiB",
			Compression: "lz4",
			Atime:       "off",
			RecordSize:  "128K",
			Snapshots:   14,
			Encryption:  "Unencrypted",
		},
		"MEDIA-12TB": {
			Name:        "MEDIA-12TB",
			Pool:        "MEDIA-12TB",
			Mountpoint:  "/mnt/MEDIA-12TB",
			UsedBytes:   4024227712860,
			UsedHuman:   "3.66 TiB",
			AvailBytes:  7828383804948,
			AvailHuman:  "7.12 TiB",
			Compression: "lz4",
			Atime:       "off",
			RecordSize:  "1M",
			Snapshots:   14,
			Encryption:  "Unencrypted",
		},
		"MEDIA-12TB/data": {
			Name:        "MEDIA-12TB/data",
			Pool:        "MEDIA-12TB",
			Mountpoint:  "/mnt/MEDIA-12TB/data",
			UsedBytes:   4024227712860,
			UsedHuman:   "3.66 TiB",
			AvailBytes:  7828383804948,
			AvailHuman:  "7.12 TiB",
			Compression: "lz4",
			Atime:       "off",
			RecordSize:  "1M",
			Snapshots:   14,
			Encryption:  "Unencrypted",
		},
		"MEDIA-12TB/MEDIA-12TB": {
			Name:        "MEDIA-12TB/MEDIA-12TB",
			Pool:        "MEDIA-12TB",
			Mountpoint:  "/mnt/MEDIA-12TB/MEDIA-12TB",
			UsedBytes:   106496,
			UsedHuman:   "104 KiB",
			AvailBytes:  7828383804948,
			AvailHuman:  "7.12 TiB",
			Compression: "lz4",
			Atime:       "off",
			RecordSize:  "128K",
			Snapshots:   0,
			Encryption:  "Unencrypted",
		},
		"SSD-MIRROR": {
			Name:        "SSD-MIRROR",
			Pool:        "SSD-MIRROR",
			Mountpoint:  "/mnt/SSD-MIRROR",
			UsedBytes:   1127428915,
			UsedHuman:   "1.05 GiB",
			AvailBytes:  244415031214,
			AvailHuman:  "227.63 GiB",
			Compression: "lz4",
			Atime:       "off",
			RecordSize:  "128K",
			Snapshots:   14,
			Encryption:  "Unencrypted",
		},
		"SSD-MIRROR/mail-archiver": {
			Name:        "SSD-MIRROR/mail-archiver",
			Pool:        "SSD-MIRROR",
			Mountpoint:  "/mnt/SSD-MIRROR/mail-archiver",
			UsedBytes:   1127428915,
			UsedHuman:   "1.05 GiB",
			AvailBytes:  244415031214,
			AvailHuman:  "227.63 GiB",
			Compression: "lz4",
			Atime:       "off",
			RecordSize:  "128K",
			Snapshots:   14,
			Encryption:  "Unencrypted",
		},
	}
	for _, ds := range m.mockDS {
		syncDatasetFields(ds)
	}

	m.mockSnaps = []*Snapshot{
		{
			Name:            "DATA@auto-2026-10-27-0000",
			Dataset:         "DATA",
			Created:         time.Now().Add(-11 * time.Hour),
			UsedBytes:       1258291,
			UsedHuman:       "1.2 MiB",
			ReferencedBytes: 348356784128,
			Referenced:      "324.43 GiB",
		},
		{
			Name:            "MEDIA-12TB@auto-2026-10-27-0000",
			Dataset:         "MEDIA-12TB",
			Created:         time.Now().Add(-11 * time.Hour),
			UsedBytes:       98304,
			UsedHuman:       "96 KiB",
			ReferencedBytes: 3507421839360,
			Referenced:      "3.19 TiB",
		},
		{
			Name:            "SSD-MIRROR@auto-2026-10-27-0000",
			Dataset:         "SSD-MIRROR",
			Created:         time.Now().Add(-11 * time.Hour),
			UsedBytes:       32768,
			UsedHuman:       "32 KiB",
			ReferencedBytes: 1095216640,
			Referenced:      "1.02 GiB",
		},
	}
}

// stripZfsPartition strips ZFS auto-created partition suffixes (-part1, p1, 1) from device names
func stripZfsPartition(name string) string {
	if idx := strings.LastIndex(name, "-part"); idx > 0 {
		suffix := name[idx+5:]
		allDigits := len(suffix) > 0
		for _, c := range suffix {
			if c < '0' || c > '9' {
				allDigits = false
				break
			}
		}
		if allDigits {
			return name[:idx]
		}
	}
	base := filepath.Base(name)
	if idx := strings.LastIndexByte(name, '/'); idx >= 0 {
		base = name[idx+1:]
	}
	strippedBase := base
	if strings.HasPrefix(base, "nvme") || strings.HasPrefix(base, "loop") || strings.HasPrefix(base, "mmcblk") {
		if idx := strings.LastIndexByte(base, 'p'); idx > 3 {
			suffix := base[idx+1:]
			allDigits := len(suffix) > 0
			for _, c := range suffix {
				if c < '0' || c > '9' {
					allDigits = false
					break
				}
			}
			if allDigits {
				strippedBase = base[:idx]
			}
		}
	} else if strings.HasPrefix(base, "sd") || strings.HasPrefix(base, "hd") || strings.HasPrefix(base, "vd") || strings.HasPrefix(base, "xvd") {
		trimmed := strings.TrimRight(base, "0123456789")
		if len(trimmed) >= 3 && len(trimmed) < len(base) {
			strippedBase = trimmed
		}
	}
	if idx := strings.LastIndexByte(name, '/'); idx >= 0 {
		return name[:idx+1] + strippedBase
	}
	return strippedBase
}

// resolvePoolMember resolves a disk identifier (e.g. "sda", "/dev/sda", or by-id path)
// to the exact vdev path/name registered in the pool on Debian Linux (e.g. "/dev/sda1" or "/dev/disk/by-id/...-part1").
func (m *Manager) resolvePoolMember(pool, dev string) string {
	dev = strings.TrimSpace(dev)
	devBase := stripZfsPartition(filepath.Base(dev))
	vdevs, _ := m.scanPoolStatus(pool)
	for _, v := range vdevs {
		if !v.IsDisk {
			continue
		}
		if v.Path == dev || v.Name == dev || filepath.Base(v.Path) == dev {
			if v.Path != "" {
				return v.Path
			}
			return v.Name
		}
		vBase := stripZfsPartition(filepath.Base(v.Path))
		vNameStripped := stripZfsPartition(v.Name)
		if vBase == devBase || vNameStripped == devBase {
			if v.Path != "" {
				return v.Path
			}
			return v.Name
		}
		if v.Path != "" {
			if resolved, err := filepath.EvalSymlinks(v.Path); err == nil {
				resBase := stripZfsPartition(filepath.Base(resolved))
				if resolved == dev || resBase == devBase {
					return v.Path
				}
			}
		}
	}
	if !strings.HasPrefix(dev, "/") {
		return "/dev/" + dev
	}
	return dev
}

// GetDiskPoolMap returns maps of disk -> poolName and disk -> role for all active pools
func (m *Manager) GetDiskPoolMap() (map[string]string, map[string]string) {
	pools, err := m.ListPools()
	if err != nil {
		return make(map[string]string), make(map[string]string)
	}
	return BuildDiskPoolMap(pools)
}

// BuildDiskPoolMap builds disk -> poolName and disk -> role lookup maps from already-listed pools.
func BuildDiskPoolMap(pools []*Pool) (map[string]string, map[string]string) {
	poolMap := make(map[string]string)
	roleMap := make(map[string]string)

	for _, p := range pools {
		if p == nil {
			continue
		}
		for _, v := range p.Vdevs {
			if !v.IsDisk {
				continue
			}
			for _, key := range []string{v.Name, stripZfsPartition(v.Name)} {
				if key != "" {
					poolMap[key] = p.Name
					roleMap[key] = v.Role
				}
			}
			if v.Path != "" {
				base := filepath.Base(v.Path)
				for _, key := range []string{v.Path, stripZfsPartition(v.Path), base, stripZfsPartition(base)} {
					if key != "" {
						poolMap[key] = p.Name
						roleMap[key] = v.Role
					}
				}
				if resolved, err := filepath.EvalSymlinks(v.Path); err == nil && resolved != "" {
					resBase := filepath.Base(resolved)
					for _, key := range []string{resolved, stripZfsPartition(resolved), resBase, stripZfsPartition(resBase)} {
						if key != "" {
							poolMap[key] = p.Name
							roleMap[key] = v.Role
						}
					}
				}
			}
		}
	}
	return poolMap, roleMap
}

// ListPools retrieves all pools and their health
func (m *Manager) ListPools() ([]*Pool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.mockMode {
		var list []*Pool
		for _, p := range m.mockPools {
			syncPoolFields(p)
			list = append(list, p)
		}
		sort.Slice(list, func(i, j int) bool {
			return strings.ToLower(list[i].Name) < strings.ToLower(list[j].Name)
		})
		return list, nil
	}

	pools, err := m.scanLinuxPools()
	if err == nil {
		for _, p := range pools {
			syncPoolFields(p)
		}
		sort.Slice(pools, func(i, j int) bool {
			return pools[i].Name < pools[j].Name
		})
	}
	return pools, err
}

func (m *Manager) scanLinuxPools() ([]*Pool, error) {
	out, err := runCommand(fastZfsTimeout, "zpool", "list", "-H", "-p", "-o", "name,size,alloc,free,frag,cap,health,autotrim,autoexpand")
	if err != nil {
		// Fallback without autotrim/autoexpand if older zpool
		out, err = runCommand(fastZfsTimeout, "zpool", "list", "-H", "-p", "-o", "name,size,alloc,free,frag,cap,health")
		if err != nil {
			outStr := strings.TrimSpace(string(out))
			if strings.Contains(outStr, "no pools available") || strings.Contains(outStr, "no pools found") || outStr == "" {
				return []*Pool{}, nil
			}
			return nil, fmt.Errorf("zpool list: %w (%s)", err, outStr)
		}
	}

	var pools []*Pool
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) < 7 {
			f = strings.Fields(line)
			if len(f) < 7 {
				continue
			}
		}
		var size, alloc, free uint64
		var frag, cap int
		fmt.Sscanf(f[1], "%d", &size)
		fmt.Sscanf(f[2], "%d", &alloc)
		fmt.Sscanf(f[3], "%d", &free)
		fmt.Sscanf(strings.TrimSuffix(f[4], "%"), "%d", &frag)
		fmt.Sscanf(strings.TrimSuffix(f[5], "%"), "%d", &cap)

		autotrim := "off"
		autoexpand := "on"
		if len(f) >= 8 {
			autotrim = strings.TrimSpace(f[7])
		}
		if len(f) >= 9 {
			autoexpand = strings.TrimSpace(f[8])
		}

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
			Autotrim:      autotrim,
			Autoexpand:    autoexpand,
		}
		syncPoolFields(p)
		pools = append(pools, p)
	}
	return pools, nil
}

func (m *Manager) scanPoolStatus(poolName string) ([]Vdev, ScrubStatus) {
	out, err := runCommandOutput(fastZfsTimeout, "zpool", "status", "-P", poolName)
	if err != nil {
		return nil, ScrubStatus{}
	}
	return parseZpoolStatus(bytes.NewReader(out), poolName)
}

func parseZpoolStatus(r io.Reader, poolName string) ([]Vdev, ScrubStatus) {
	var vdevs []Vdev
	var scrub ScrubStatus

	scanner := bufio.NewScanner(r)
	inScan := false
	inConfig := false
	currentGroup := ""

	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)

		if strings.HasPrefix(trimmed, "scan:") {
			inScan = true
			scanContent := strings.TrimPrefix(trimmed, "scan:")
			parseScanLine(scanContent, &scrub)
			continue
		}

		if inScan {
			if strings.HasPrefix(trimmed, "config:") {
				inScan = false
				inConfig = true
				continue
			}
			parseScanLine(trimmed, &scrub)
			continue
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

				var readErr, writeErr, cksumErr uint64
				if len(parts) >= 5 {
					readErr, _ = strconv.ParseUint(parts[2], 10, 64)
					writeErr, _ = strconv.ParseUint(parts[3], 10, 64)
					cksumErr, _ = strconv.ParseUint(parts[4], 10, 64)
				}

				devType := "disk"
				devPath := ""
				isDisk := true
				if strings.HasPrefix(name, "mirror") {
					devType = "mirror"
					currentGroup = name
					isDisk = false
				} else if strings.HasPrefix(name, "raidz") {
					devType = name
					currentGroup = name
					isDisk = false
				} else if name == "logs" || name == "spares" || name == "cache" || name == "special" || name == "dedup" {
					devType = name
					currentGroup = name
					isDisk = false
				} else {
					if strings.HasPrefix(name, "/") {
						devPath = name
					} else {
						devPath = "/dev/" + name
					}
					if currentGroup == "logs" {
						devType = "log"
					} else if currentGroup == "cache" {
						devType = "cache"
					} else if currentGroup == "special" {
						devType = "special"
					} else if currentGroup == "spares" {
						devType = "spare"
					}
				}

				displayName := name
				if devPath != "" {
					displayName = stripZfsPartition(filepath.Base(name))
				}

				role := currentGroup
				if role == "" && isDisk {
					role = "stripe"
				}

				vdevs = append(vdevs, Vdev{
					Name:        displayName,
					Type:        devType,
					Role:        role,
					Health:      health,
					State:       health,
					Path:        devPath,
					ReadErrors:  readErr,
					WriteErrors: writeErr,
					CksumErrors: cksumErr,
					IsDisk:      isDisk,
				})
			}
		}
	}
	return vdevs, scrub
}

func parseScanLine(text string, scrub *ScrubStatus) {
	lower := strings.ToLower(text)
	if strings.Contains(lower, "scrub in progress") || strings.Contains(lower, "resilver in progress") {
		scrub.Active = true
	}
	if strings.Contains(lower, "scrub repaired") || strings.Contains(lower, "resilver repaired") {
		scrub.Active = false
		scrub.Percentage = 100.0
	}
	if strings.Contains(lower, "scrub canceled") || strings.Contains(lower, "none requested") {
		scrub.Active = false
	}

	// Parse errors: e.g. "with 0 errors" or "with 2 errors"
	if idxWith := strings.Index(lower, "with "); idxWith != -1 {
		rest := text[idxWith+5:]
		if idxErr := strings.Index(strings.ToLower(rest), " error"); idxErr != -1 {
			errStr := strings.TrimSpace(rest[:idxErr])
			if e, err := strconv.ParseUint(errStr, 10, 64); err == nil {
				scrub.Errors = e
			}
		}
	}

	// Parse percentage: e.g. "24.8% done" or "24.80% done"
	if idxDone := strings.Index(lower, "% done"); idxDone != -1 {
		prefix := strings.TrimSpace(text[:idxDone])
		parts := strings.Fields(prefix)
		if len(parts) > 0 {
			numStr := strings.Trim(parts[len(parts)-1], ", ")
			if p, err := strconv.ParseFloat(numStr, 64); err == nil {
				scrub.Percentage = p
			}
		}
	}

	// Parse completion/progress timestamp: e.g. "on Sun Oct 15 01:31:42 2026"
	if idxOn := strings.Index(lower, " on "); idxOn != -1 {
		dateStr := strings.TrimSpace(text[idxOn+4:])
		formats := []string{
			time.ANSIC,
			"Mon Jan 2 15:04:05 2006",
			"Mon Jan _2 15:04:05 MST 2006",
			"2006-01-02 15:04:05",
		}
		for _, f := range formats {
			if t, err := time.Parse(f, dateStr); err == nil {
				scrub.LastRun = t
				break
			}
		}
	}
}

// ListDatasets returns all datasets
func (m *Manager) ListDatasets() ([]*Dataset, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.mockMode {
		var list []*Dataset
		for _, d := range m.mockDS {
			syncDatasetFields(d)
			list = append(list, d)
		}
		sort.Slice(list, func(i, j int) bool {
			return strings.ToLower(list[i].Name) < strings.ToLower(list[j].Name)
		})
		return list, nil
	}

	snapCounts := m.getSnapshotCounts()

	out, err := runCommand(fastZfsTimeout, "zfs", "list", "-H", "-p", "-o", "name,used,avail,mountpoint,compression,atime,recordsize,quota,readonly,type,volsize")
	if err != nil {
		// Fallback to basic columns
		out, err = runCommand(fastZfsTimeout, "zfs", "list", "-H", "-p", "-o", "name,used,avail,mountpoint,compression,atime,recordsize")
		if err != nil {
			outStr := strings.TrimSpace(string(out))
			if strings.Contains(outStr, "no datasets available") || strings.Contains(outStr, "no pools available") || outStr == "" {
				return []*Dataset{}, nil
			}
			return nil, fmt.Errorf("zfs list: %w (%s)", err, outStr)
		}
	}

	var datasets []*Dataset
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) < 7 {
			f = strings.Fields(line)
			if len(f) < 7 {
				continue
			}
		}
		parts := strings.Split(f[0], "/")
		poolName := parts[0]

		usedBytes, _ := strconv.ParseUint(strings.TrimSpace(f[1]), 10, 64)
		availBytes, _ := strconv.ParseUint(strings.TrimSpace(f[2]), 10, 64)
		mountpoint := strings.TrimSpace(f[3])

		quota := "none"
		readOnly := "off"
		dsType := "filesystem"
		if len(f) >= 8 && f[7] != "0" && f[7] != "-" && f[7] != "none" {
			if qBytes, err := strconv.ParseUint(strings.TrimSpace(f[7]), 10, 64); err == nil && qBytes > 0 {
				quota = formatZfsQuota(qBytes)
			} else {
				quota = strings.TrimSpace(f[7])
			}
		}
		if len(f) >= 9 {
			readOnly = strings.TrimSpace(f[8])
		}
		if len(f) >= 10 {
			dsType = strings.TrimSpace(f[9])
		}
		if dsType == "volume" {
			mountpoint = "/dev/zvol/" + f[0]
			if len(f) >= 11 {
				if volBytes, err := strconv.ParseUint(strings.TrimSpace(f[10]), 10, 64); err == nil && volBytes > 0 {
					availBytes = volBytes
				}
			}
		}

		ds := &Dataset{
			Name:           f[0],
			Pool:           poolName,
			Type:           dsType,
			Mountpoint:     mountpoint,
			UsedBytes:      usedBytes,
			UsedHuman:      formatZfsBytes(usedBytes),
			AvailBytes:     availBytes,
			AvailableBytes: availBytes,
			AvailHuman:     formatZfsBytes(availBytes),
			Quota:          quota,
			ReadOnly:       readOnly,
			Compression:    f[4],
			Atime:          f[5],
			RecordSize:     formatRecordSize(f[6]),
			Snapshots:      snapCounts[f[0]],
		}
		syncDatasetFields(ds)
		datasets = append(datasets, ds)
	}
	sort.Slice(datasets, func(i, j int) bool {
		return datasets[i].Name < datasets[j].Name
	})
	return datasets, nil
}

func formatRecordSize(raw string) string {
	raw = strings.TrimSpace(raw)
	if b, err := strconv.ParseUint(raw, 10, 64); err == nil && b >= 1024 {
		if b >= 1024*1024 {
			return fmt.Sprintf("%dM", b/(1024*1024))
		}
		return fmt.Sprintf("%dK", b/1024)
	}
	return raw
}

func formatZfsQuota(b uint64) string {
	if b == 0 {
		return "none"
	}
	const (
		kib = 1024
		mib = 1024 * kib
		gib = 1024 * mib
		tib = 1024 * gib
	)
	if b >= tib && b%tib == 0 {
		return fmt.Sprintf("%dT", b/tib)
	}
	if b >= gib && b%gib == 0 {
		return fmt.Sprintf("%dG", b/gib)
	}
	if b >= mib && b%mib == 0 {
		return fmt.Sprintf("%dM", b/mib)
	}
	if b >= kib && b%kib == 0 {
		return fmt.Sprintf("%dK", b/kib)
	}
	if b >= tib {
		return fmt.Sprintf("%.2fT", float64(b)/float64(tib))
	}
	if b >= gib {
		return fmt.Sprintf("%.2fG", float64(b)/float64(gib))
	}
	if b >= mib {
		return fmt.Sprintf("%.2fM", float64(b)/float64(mib))
	}
	return fmt.Sprintf("%d", b)
}

func normalizeZfsSize(val string) string {
	v := strings.TrimSpace(val)
	lower := strings.ToLower(v)
	if lower == "" || lower == "none" || lower == "0" {
		return "none"
	}
	v = strings.ReplaceAll(v, " ", "")
	upper := strings.ToUpper(v)
	for _, suf := range []string{"IB", "B"} {
		if strings.HasSuffix(upper, suf) && len(upper) > len(suf) {
			v = v[:len(v)-len(suf)]
			break
		}
	}
	return v
}

func (m *Manager) getSnapshotCounts() map[string]int {
	counts := make(map[string]int)
	out, err := runCommandOutput(fastZfsTimeout, "zfs", "list", "-t", "snapshot", "-H", "-o", "name")
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

	name = strings.TrimSpace(name)
	if err := validate.DatasetName(name); err != nil {
		return fmt.Errorf("invalid dataset name: %w", err)
	}

	if compression == "" {
		compression = "lz4"
	}
	if recordsize == "" {
		recordsize = "128K"
	}
	if err := validate.DatasetProperty("compression", compression); err != nil {
		return err
	}
	if err := validate.DatasetProperty("recordsize", recordsize); err != nil {
		return err
	}

	if m.mockMode {
		parts := strings.Split(name, "/")
		pool := parts[0]
		var avail uint64 = 1546188226560
		if p, ok := m.mockPools[pool]; ok && p.FreeBytes > 0 {
			avail = p.FreeBytes
		}
		ds := &Dataset{
			Name:           name,
			Pool:           pool,
			Type:           "filesystem",
			Mountpoint:     "/mnt/" + name,
			UsedBytes:      98304,
			UsedHuman:      "96 KiB",
			AvailBytes:     avail,
			AvailableBytes: avail,
			AvailHuman:     formatZfsBytes(avail),
			Quota:          "none",
			ReadOnly:       "off",
			Compression:    compression,
			Atime:          "off", // Crucial for HDD Standby!
			RecordSize:     recordsize,
			Snapshots:      0,
		}
		syncDatasetFields(ds)
		m.mockDS[name] = ds
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

	out, err := runCommand(defaultZfsTimeout, "zfs", args...)
	if err != nil {
		return fmt.Errorf("zfs create failed: %v (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// DestroyDataset deletes a dataset and its child datasets/snapshots
func (m *Manager) DestroyDataset(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	name = strings.TrimSpace(name)
	if err := validate.DatasetName(name); err != nil {
		return fmt.Errorf("invalid dataset name: %w", err)
	}
	if !strings.Contains(name, "/") {
		return fmt.Errorf("cannot destroy root pool dataset '%s' directly; destroy the storage pool instead", name)
	}

	if m.mockMode {
		for k := range m.mockDS {
			if k == name || strings.HasPrefix(k, name+"/") {
				delete(m.mockDS, k)
			}
		}
		var kept []*Snapshot
		for _, s := range m.mockSnaps {
			if s.Dataset != name && !strings.HasPrefix(s.Dataset, name+"/") {
				kept = append(kept, s)
			}
		}
		m.mockSnaps = kept
		return nil
	}

	out, err := runCommand(defaultZfsTimeout, "zfs", "destroy", "-r", name)
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
		list := make([]*Snapshot, len(m.mockSnaps))
		copy(list, m.mockSnaps)
		sort.Slice(list, func(i, j int) bool {
			return strings.ToLower(list[i].Name) < strings.ToLower(list[j].Name)
		})
		return list, nil
	}

	out, err := runCommand(fastZfsTimeout, "zfs", "list", "-t", "snapshot", "-H", "-p", "-o", "name,used,refer,creation")
	if err != nil {
		outStr := strings.TrimSpace(string(out))
		if strings.Contains(outStr, "no datasets available") || strings.Contains(outStr, "no snapshots available") || strings.Contains(outStr, "no pools available") || outStr == "" {
			return []*Snapshot{}, nil
		}
		return nil, fmt.Errorf("zfs list snapshot: %w (%s)", err, outStr)
	}

	list := []*Snapshot{}
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
			Name:            f[0],
			Dataset:         datasetName,
			Created:         created,
			UsedBytes:       usedBytes,
			UsedHuman:       formatZfsBytes(usedBytes),
			ReferencedBytes: referBytes,
			Referenced:      formatZfsBytes(referBytes),
		})
	}
	sort.Slice(list, func(i, j int) bool {
		return list[i].Name < list[j].Name
	})
	return list, nil
}

// CreateSnapshot creates a manual snapshot on a single dataset or Zvol.
func (m *Manager) CreateSnapshot(dataset, snapName string) error {
	return m.CreateSnapshotRecursive(dataset, snapName, false)
}

// CreateSnapshotRecursive creates a manual snapshot, optionally recursively (-r) across all child datasets.
func (m *Manager) CreateSnapshotRecursive(dataset, snapName string, recursive bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	dataset = strings.TrimSpace(dataset)
	snapName = strings.TrimSpace(snapName)
	if err := validate.SnapshotName(dataset, snapName); err != nil {
		return fmt.Errorf("invalid snapshot target: %w", err)
	}

	fullName := fmt.Sprintf("%s@%s", dataset, snapName)
	if m.mockMode {
		targetDS, exists := m.mockDS[dataset]
		if !exists {
			return fmt.Errorf("dataset '%s' does not exist", dataset)
		}
		for _, s := range m.mockSnaps {
			if s.Name == fullName {
				return fmt.Errorf("snapshot '%s' already exists", fullName)
			}
		}

		now := time.Now()
		addMockSnap := func(dsName string, ds *Dataset) {
			sFull := fmt.Sprintf("%s@%s", dsName, snapName)
			for _, existing := range m.mockSnaps {
				if existing.Name == sFull {
					return
				}
			}
			var referBytes uint64 = 1073741824
			if ds != nil && ds.UsedBytes > 0 {
				referBytes = ds.UsedBytes
			}
			if ds != nil {
				ds.Snapshots++
			}
			m.mockSnaps = append(m.mockSnaps, &Snapshot{
				Name:            sFull,
				Dataset:         dsName,
				Created:         now,
				UsedBytes:       0,
				UsedHuman:       "0 B",
				ReferencedBytes: referBytes,
				Referenced:      formatZfsBytes(referBytes),
			})
		}

		addMockSnap(dataset, targetDS)
		if recursive {
			prefix := dataset + "/"
			for dsName, ds := range m.mockDS {
				if strings.HasPrefix(dsName, prefix) {
					addMockSnap(dsName, ds)
				}
			}
		}
		return nil
	}

	args := []string{"snapshot"}
	if recursive {
		args = append(args, "-r")
	}
	args = append(args, fullName)

	out, err := runCommand(defaultZfsTimeout, "zfs", args...)
	if err != nil {
		return fmt.Errorf("zfs snapshot failed: %v (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// RollbackSnapshot rolls back a dataset to a snapshot (destroying newer snapshots with -r).
func (m *Manager) RollbackSnapshot(snapshot string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	snapshot = strings.TrimSpace(snapshot)
	parts := strings.Split(snapshot, "@")
	if len(parts) != 2 {
		return fmt.Errorf("invalid snapshot target '%s': must contain exactly one '@'", snapshot)
	}
	if err := validate.SnapshotName(parts[0], parts[1]); err != nil {
		return fmt.Errorf("invalid snapshot name: %w", err)
	}

	if m.mockMode {
		targetIdx := -1
		var targetSnap *Snapshot
		for i, s := range m.mockSnaps {
			if s.Name == snapshot {
				targetIdx = i
				targetSnap = s
				break
			}
		}
		if targetIdx == -1 || targetSnap == nil {
			return fmt.Errorf("snapshot '%s' does not exist", snapshot)
		}

		// Collect newer snapshots of the same dataset to be destroyed (mirroring zfs rollback -r)
		dsName := parts[0]
		newerSnaps := make(map[string]bool)
		for i, s := range m.mockSnaps {
			if s.Dataset == dsName && (s.Created.After(targetSnap.Created) || (s.Created.Equal(targetSnap.Created) && i > targetIdx)) {
				newerSnaps[s.Name] = true
			}
		}
		// Prevent rollback if a newer snapshot has dependent clones
		for _, ds := range m.mockDS {
			if newerSnaps[ds.Origin] {
				return fmt.Errorf("cannot rollback to '%s': newer snapshot '%s' has dependent clone '%s'", snapshot, ds.Origin, ds.Name)
			}
		}
		if len(newerSnaps) > 0 {
			var kept []*Snapshot
			for _, s := range m.mockSnaps {
				if !newerSnaps[s.Name] {
					kept = append(kept, s)
				}
			}
			m.mockSnaps = kept
			if ds, ok := m.mockDS[dsName]; ok {
				ds.Snapshots = max(0, ds.Snapshots-len(newerSnaps))
			}
		}
		return nil
	}

	out, err := runCommand(defaultZfsTimeout, "zfs", "rollback", "-r", snapshot)
	if err != nil {
		return fmt.Errorf("zfs rollback failed: %v (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// DestroySnapshot deletes a snapshot
func (m *Manager) DestroySnapshot(snapshot string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	snapshot = strings.TrimSpace(snapshot)
	parts := strings.Split(snapshot, "@")
	if len(parts) != 2 {
		return fmt.Errorf("invalid snapshot target '%s': must contain exactly one '@'", snapshot)
	}
	if err := validate.SnapshotName(parts[0], parts[1]); err != nil {
		return fmt.Errorf("invalid snapshot name: %w", err)
	}

	if m.mockMode {
		for _, ds := range m.mockDS {
			if ds.Origin == snapshot {
				return fmt.Errorf("cannot destroy '%s': snapshot has dependent clone '%s'", snapshot, ds.Name)
			}
		}
		found := false
		for i, s := range m.mockSnaps {
			if s.Name == snapshot {
				m.mockSnaps = append(m.mockSnaps[:i], m.mockSnaps[i+1:]...)
				if ds, ok := m.mockDS[parts[0]]; ok && ds.Snapshots > 0 {
					ds.Snapshots--
				}
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("could not find any snapshots to destroy for '%s'", snapshot)
		}
		return nil
	}

	out, err := runCommand(defaultZfsTimeout, "zfs", "destroy", snapshot)
	if err != nil {
		return fmt.Errorf("zfs destroy failed: %v (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// SetDatasetProperty sets a ZFS dataset property like compression, recordsize, or quota
func (m *Manager) SetDatasetProperty(dataset, property, value string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	dataset = strings.TrimSpace(dataset)
	property = strings.TrimSpace(strings.ToLower(property))
	value = strings.TrimSpace(value)
	if property == "quota" || property == "refquota" || property == "reservation" || property == "refreservation" {
		value = normalizeZfsSize(value)
	}
	if err := validate.DatasetName(dataset); err != nil {
		return fmt.Errorf("invalid dataset name: %w", err)
	}
	if err := validate.DatasetProperty(property, value); err != nil {
		return fmt.Errorf("invalid dataset property: %w", err)
	}

	if m.mockMode {
		if ds, ok := m.mockDS[dataset]; ok {
			switch property {
			case "compression":
				ds.Compression = value
			case "recordsize":
				ds.RecordSize = value
			case "atime":
				ds.Atime = value
			case "quota":
				ds.Quota = value
			case "readonly":
				ds.ReadOnly = value
			case "sync":
				ds.Sync = value
			}
			syncDatasetFields(ds)
		}
		return nil
	}

	out, err := runCommand(defaultZfsTimeout, "zfs", "set", fmt.Sprintf("%s=%s", property, value), dataset)
	if err != nil {
		return fmt.Errorf("zfs set failed: %v (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// StartScrub initiates a scrub on a pool
func (m *Manager) StartScrub(poolName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	poolName = strings.TrimSpace(poolName)
	if err := validate.PoolName(poolName); err != nil {
		return fmt.Errorf("invalid pool name: %w", err)
	}

	if m.mockMode {
		if p, ok := m.mockPools[poolName]; ok {
			p.Scrub.Active = true
			p.Scrub.Percentage = 14.5
			p.Scrub.Speed = "512 MB/s"
			p.Scrub.TimeLeft = "1h 12m"
			syncPoolFields(p)
		}
		return nil
	}

	out, err := runCommand(defaultZfsTimeout, "zpool", "scrub", poolName)
	if err != nil {
		return fmt.Errorf("zpool scrub failed: %v (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// StopScrub pauses or cancels an active scrub
func (m *Manager) StopScrub(poolName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	poolName = strings.TrimSpace(poolName)
	if err := validate.PoolName(poolName); err != nil {
		return fmt.Errorf("invalid pool name: %w", err)
	}

	if m.mockMode {
		if p, ok := m.mockPools[poolName]; ok {
			p.Scrub.Active = false
			p.Scrub.LastRun = time.Now()
			syncPoolFields(p)
		}
		return nil
	}

	out, err := runCommand(defaultZfsTimeout, "zpool", "scrub", "-s", poolName)
	if err != nil {
		return fmt.Errorf("zpool scrub -s failed: %v (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// CreatePool creates a new ZFS pool with chosen topology, ashift, compression and atime=off
func (m *Manager) CreatePool(name, vdevType string, disks []string, ashift int, compression string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	name = strings.TrimSpace(name)
	if err := validate.PoolName(name); err != nil {
		return fmt.Errorf("invalid pool name: %w", err)
	}

	if ashift < 9 || ashift > 16 {
		ashift = 12 // Default 4K sectors
	}
	if compression == "" {
		compression = "zstd"
	}
	if err := validate.DatasetProperty("compression", compression); err != nil {
		return err
	}

	var cleanDisks []string
	for _, d := range disks {
		d = strings.TrimSpace(d)
		if d == "" || d == "undefined" {
			continue
		}
		if err := validate.DevicePath(d); err != nil {
			return fmt.Errorf("invalid member device: %w", err)
		}
		if !strings.HasPrefix(d, "/") {
			d = "/dev/" + d
		}
		cleanDisks = append(cleanDisks, d)
	}
	if len(cleanDisks) == 0 {
		return fmt.Errorf("no valid block devices specified")
	}

	// Topology validation
	vdevType = strings.ToLower(strings.TrimSpace(vdevType))
	switch vdevType {
	case "mirror":
		if len(cleanDisks) < 2 {
			return fmt.Errorf("mirror topology requires at least 2 member disks (got %d)", len(cleanDisks))
		}
	case "raidz1":
		if len(cleanDisks) < 3 {
			return fmt.Errorf("raidz1 topology requires at least 3 member disks (got %d)", len(cleanDisks))
		}
	case "raidz2":
		if len(cleanDisks) < 4 {
			return fmt.Errorf("raidz2 topology requires at least 4 member disks (got %d)", len(cleanDisks))
		}
	case "raidz3":
		if len(cleanDisks) < 5 {
			return fmt.Errorf("raidz3 topology requires at least 5 member disks (got %d)", len(cleanDisks))
		}
	case "single", "stripe", "":
		vdevType = ""
	default:
		return fmt.Errorf("unsupported or invalid vdev topology: %s", vdevType)
	}

	if m.mockMode {
		var vdevs []Vdev
		role := "stripe"
		if vdevType != "" {
			role = vdevType + "-0"
			vdevs = append(vdevs, Vdev{
				Name:   role,
				Type:   vdevType,
				Health: "ONLINE",
				State:  "ONLINE",
				IsDisk: false,
			})
		}
		for _, d := range cleanDisks {
			vdevs = append(vdevs, Vdev{
				Name:      filepath.Base(d),
				Type:      "disk",
				Role:      role,
				Health:    "ONLINE",
				State:     "ONLINE",
				SizeBytes: 16000900661248,
				Path:      d,
				IsDisk:    true,
			})
		}

		rawSize := uint64(len(cleanDisks)) * 16000900661248 // 16 TB per disk mock
		if vdevType == "mirror" && len(cleanDisks) > 0 {
			rawSize = 16000900661248
		} else if vdevType == "raidz1" && len(cleanDisks) > 1 {
			rawSize = uint64(len(cleanDisks)-1) * 16000900661248
		} else if vdevType == "raidz2" && len(cleanDisks) > 2 {
			rawSize = uint64(len(cleanDisks)-2) * 16000900661248
		} else if vdevType == "raidz3" && len(cleanDisks) > 3 {
			rawSize = uint64(len(cleanDisks)-3) * 16000900661248
		}
		if rawSize == 0 {
			rawSize = 16000900661248
		}
		p := &Pool{
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
			Autotrim:      "off",
			Autoexpand:    "on",
			Compression:   compression,
			Vdevs:         vdevs,
			Scrub: ScrubStatus{
				Active:     false,
				Percentage: 100.0,
				Errors:     0,
				LastRun:    time.Now(),
			},
		}
		syncPoolFields(p)
		m.mockPools[name] = p

		// Also create root dataset
		ds := &Dataset{
			Name:           name,
			Pool:           name,
			Type:           "filesystem",
			Mountpoint:     "/mnt/" + name,
			UsedBytes:      1048576,
			UsedHuman:      "1.0 MiB",
			AvailBytes:     rawSize - 1048576,
			AvailableBytes: rawSize - 1048576,
			AvailHuman:     formatZfsBytes(rawSize - 1048576),
			Compression:    compression,
			Atime:          "off",
			RecordSize:     "128K",
			Snapshots:      0,
		}
		syncDatasetFields(ds)
		m.mockDS[name] = ds
		return nil
	}

	args := []string{
		"create", "-f",
		"-m", "/mnt/" + name,
		"-o", fmt.Sprintf("ashift=%d", ashift),
		"-O", "atime=off",
		"-O", "compression=" + compression,
		"-O", "xattr=sa",
		"-O", "acltype=posixacl",
		name,
	}
	if vdevType != "" {
		args = append(args, vdevType)
	}
	args = append(args, cleanDisks...)

	out, err := runCommand(longZfsTimeout, "zpool", args...)
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
	if err := validate.PoolName(name); err != nil {
		return fmt.Errorf("invalid pool name: %w", err)
	}

	if m.mockMode {
		delete(m.mockPools, name)
		for ds := range m.mockDS {
			if ds == name || strings.HasPrefix(ds, name+"/") {
				delete(m.mockDS, ds)
			}
		}
		var kept []*Snapshot
		for _, s := range m.mockSnaps {
			if s.Dataset != name && !strings.HasPrefix(s.Dataset, name+"/") {
				kept = append(kept, s)
			}
		}
		m.mockSnaps = kept
		return nil
	}

	// Unmount filesystems first to prevent busy lockouts
	_, _ = runCommand(fastZfsTimeout, "zfs", "unmount", "-f", name)

	out, err := runCommand(defaultZfsTimeout, "zpool", "destroy", "-f", name)
	if err != nil {
		return fmt.Errorf("zpool destroy failed: %v (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// ExportPool exports a storage pool safely
func (m *Manager) ExportPool(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	name = strings.TrimSpace(name)
	if err := validate.PoolName(name); err != nil {
		return fmt.Errorf("invalid pool name: %w", err)
	}

	if m.mockMode {
		if p, ok := m.mockPools[name]; ok {
			m.mockExported[name] = &ImportablePool{
				Name:   name,
				ID:     fmt.Sprintf("%d", time.Now().UnixNano()),
				State:  string(p.Health),
				Action: "The pool was exported cleanly and can be imported directly.",
				Config: fmt.Sprintf("%s %s (%s)", name, p.Health, p.Topology),
			}
			m.mockExportedPools[name] = p
			var savedDS []*Dataset
			for dsName, ds := range m.mockDS {
				if dsName == name || strings.HasPrefix(dsName, name+"/") {
					savedDS = append(savedDS, ds)
					delete(m.mockDS, dsName)
				}
			}
			m.mockExportedDS[name] = savedDS
			delete(m.mockPools, name)
		}
		return nil
	}

	out, err := runCommand(defaultZfsTimeout, "zpool", "export", name)
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
		sort.Slice(list, func(i, j int) bool {
			return strings.ToLower(list[i].Name) < strings.ToLower(list[j].Name)
		})
		return list, nil
	}

	out, _ := runCommandOutput(fastZfsTimeout, "zpool", "import")
	if len(out) == 0 {
		out, _ = runCommandOutput(fastZfsTimeout, "zpool", "import", "-d", "/dev/disk/by-id")
	}
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

	nameOrID = strings.TrimSpace(nameOrID)
	newName = strings.TrimSpace(newName)
	if nameOrID == "" {
		return fmt.Errorf("pool name or ID is required")
	}
	if newName != "" {
		if err := validate.PoolName(newName); err != nil {
			return fmt.Errorf("invalid new pool name: %w", err)
		}
	}

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
		if savedPool, ok := m.mockExportedPools[foundKey]; ok {
			oldName := savedPool.Name
			savedPool.Name = targetName
			syncPoolFields(savedPool)
			m.mockPools[targetName] = savedPool
			delete(m.mockExportedPools, foundKey)

			if savedDS, ok := m.mockExportedDS[foundKey]; ok && len(savedDS) > 0 {
				for _, ds := range savedDS {
					if ds.Name == oldName {
						ds.Name = targetName
					} else if strings.HasPrefix(ds.Name, oldName+"/") {
						ds.Name = targetName + strings.TrimPrefix(ds.Name, oldName)
					}
					ds.Pool = targetName
					if ds.Type != "volume" {
						ds.Mountpoint = "/mnt/" + ds.Name
					} else {
						ds.Mountpoint = "/dev/zvol/" + ds.Name
					}
					syncDatasetFields(ds)
					m.mockDS[ds.Name] = ds
				}
				delete(m.mockExportedDS, foundKey)
				return nil
			}
		}

		p := &Pool{
			Name:          targetName,
			Health:        HealthOnline,
			Topology:      "MIRROR (2 Disks)",
			SizeBytes:     16000900661248,
			SizeHuman:     "14.55 TiB",
			Allocated:     348356784128,
			AllocHuman:    "324.43 GiB",
			FreeBytes:     15652543877120,
			FreeHuman:     "14.23 TiB",
			Fragmentation: 1,
			CapPercent:    2,
			AtimeDefault:  "off",
			Autotrim:      "off",
			Autoexpand:    "on",
			Compression:   "lz4",
			Vdevs: []Vdev{
				{Name: "mirror-0", Type: "mirror", Health: "ONLINE", State: "ONLINE", IsDisk: false},
				{Name: "sdd", Type: "disk", Role: "mirror-0", Health: "ONLINE", State: "ONLINE", SizeBytes: 16000900661248, Path: "/dev/sdd", IsDisk: true},
				{Name: "sde", Type: "disk", Role: "mirror-0", Health: "ONLINE", State: "ONLINE", SizeBytes: 16000900661248, Path: "/dev/sde", IsDisk: true},
			},
			Scrub: ScrubStatus{
				Active:     false,
				Percentage: 100.0,
				Errors:     0,
				LastRun:    time.Now().Add(-24 * time.Hour),
			},
		}
		syncPoolFields(p)
		m.mockPools[targetName] = p

		ds := &Dataset{
			Name:           targetName,
			Pool:           targetName,
			Type:           "filesystem",
			Mountpoint:     "/mnt/" + targetName,
			UsedBytes:      348356784128,
			UsedHuman:      "324.43 GiB",
			AvailBytes:     15652543877120,
			AvailableBytes: 15652543877120,
			AvailHuman:     "14.23 TiB",
			Compression:    "lz4",
			Atime:          "off",
			RecordSize:     "128K",
		}
		syncDatasetFields(ds)
		m.mockDS[targetName] = ds
		return nil
	}

	args := []string{"import"}
	if force {
		args = append(args, "-f")
	}
	args = append(args, nameOrID)
	if newName != "" {
		args = append(args, newName)
	}

	out, err := runCommand(defaultZfsTimeout, "zpool", args...)
	if err != nil {
		// Fallback with -d /dev/disk/by-id
		byIdArgs := []string{"import", "-d", "/dev/disk/by-id"}
		if force {
			byIdArgs = append(byIdArgs, "-f")
		}
		byIdArgs = append(byIdArgs, nameOrID)
		if newName != "" {
			byIdArgs = append(byIdArgs, newName)
		}
		if out2, err2 := runCommand(defaultZfsTimeout, "zpool", byIdArgs...); err2 == nil {
			return nil
		} else {
			out = out2
			err = err2
		}
		return fmt.Errorf("zpool import failed: %v (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// TrimPool initiates a TRIM operation across all SSD / NVMe vdevs in the pool
func (m *Manager) TrimPool(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	name = strings.TrimSpace(name)
	if err := validate.PoolName(name); err != nil {
		return fmt.Errorf("invalid pool name: %w", err)
	}

	if m.mockMode {
		return nil
	}

	out, err := runCommand(defaultZfsTimeout, "zpool", "trim", name)
	if err != nil {
		return fmt.Errorf("zpool trim failed: %v (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// ClearPool clears errors and device faults on a pool
func (m *Manager) ClearPool(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	name = strings.TrimSpace(name)
	if err := validate.PoolName(name); err != nil {
		return fmt.Errorf("invalid pool name: %w", err)
	}

	if m.mockMode {
		if p, ok := m.mockPools[name]; ok {
			p.Health = HealthOnline
			p.Scrub.Errors = 0
			for i := range p.Vdevs {
				p.Vdevs[i].ReadErrors = 0
				p.Vdevs[i].WriteErrors = 0
				p.Vdevs[i].CksumErrors = 0
			}
			syncPoolFields(p)
		}
		return nil
	}

	out, err := runCommand(defaultZfsTimeout, "zpool", "clear", name)
	if err != nil {
		return fmt.Errorf("zpool clear failed: %v (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// AddVdev expands pool capacity or adds auxiliary vdevs (log, cache, special metadata, spare)
func (m *Manager) AddVdev(pool, vdevType string, disks []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	pool = strings.TrimSpace(pool)
	if err := validate.PoolName(pool); err != nil {
		return fmt.Errorf("invalid pool name: %w", err)
	}

	if len(disks) == 0 {
		return fmt.Errorf("at least one disk required")
	}

	var cleanDisks []string
	for _, d := range disks {
		d = strings.TrimSpace(d)
		if d == "" || d == "undefined" {
			continue
		}
		if err := validate.DevicePath(d); err != nil {
			return fmt.Errorf("invalid member device: %w", err)
		}
		if !strings.HasPrefix(d, "/") {
			d = "/dev/" + d
		}
		cleanDisks = append(cleanDisks, d)
	}
	if len(cleanDisks) == 0 {
		return fmt.Errorf("no valid block devices specified")
	}

	vdevType = strings.ToLower(strings.TrimSpace(vdevType))
	if vdevType == "mirror" && len(cleanDisks) < 2 {
		return fmt.Errorf("adding a mirror vdev requires at least 2 disks (got %d)", len(cleanDisks))
	}

	if m.mockMode {
		if p, ok := m.mockPools[pool]; ok {
			role := vdevType
			if role == "" || role == "data" || role == "disk" || role == "single" {
				role = "stripe"
			}
			for _, d := range cleanDisks {
				p.Vdevs = append(p.Vdevs, Vdev{
					Name:      filepath.Base(d),
					Type:      role,
					Role:      role,
					Health:    "ONLINE",
					State:     "ONLINE",
					SizeBytes: 16000900661248,
					Path:      d,
					IsDisk:    true,
				})
			}
			if vdevType != "cache" && vdevType != "log" && vdevType != "special" && vdevType != "spare" {
				addedBytes := uint64(len(cleanDisks)) * 16000900661248
				if vdevType == "mirror" {
					addedBytes = 16000900661248
				}
				p.SizeBytes += addedBytes
				p.FreeBytes += addedBytes
				p.SizeHuman = formatZfsBytes(p.SizeBytes)
				p.FreeHuman = formatZfsBytes(p.FreeBytes)
				p.Topology = "" // recompute in syncPoolFields
			}
			syncPoolFields(p)
		}
		return nil
	}

	args := []string{"add", "-f", pool}
	if vdevType != "stripe" && vdevType != "single" && vdevType != "disk" && vdevType != "data" && vdevType != "" {
		args = append(args, vdevType)
	}
	args = append(args, cleanDisks...)

	out, err := runCommand(defaultZfsTimeout, "zpool", args...)
	if err != nil {
		return fmt.Errorf("zpool add failed: %v (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// AttachDisk attaches a new disk to an existing device (creating or widening a mirror)
func (m *Manager) AttachDisk(pool, existingDev, newDev string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	pool = strings.TrimSpace(pool)
	existingDev = strings.TrimSpace(existingDev)
	newDev = strings.TrimSpace(newDev)
	if err := validate.PoolName(pool); err != nil {
		return fmt.Errorf("invalid pool name: %w", err)
	}
	if err := validate.DevicePath(existingDev); err != nil {
		return fmt.Errorf("invalid existing device: %w", err)
	}
	if err := validate.DevicePath(newDev); err != nil {
		return fmt.Errorf("invalid new device: %w", err)
	}

	if m.mockMode {
		if p, ok := m.mockPools[pool]; ok {
			newBase := filepath.Base(newDev)
			hasMirrorGroup := false
			for i := range p.Vdevs {
				if strings.HasPrefix(p.Vdevs[i].Name, "mirror") {
					hasMirrorGroup = true
				}
				if p.Vdevs[i].Name == existingDev || filepath.Base(p.Vdevs[i].Path) == existingDev {
					p.Vdevs[i].Role = "mirror-0"
				}
			}
			if !hasMirrorGroup {
				p.Vdevs = append([]Vdev{{
					Name:   "mirror-0",
					Type:   "mirror",
					Health: "ONLINE",
					State:  "ONLINE",
					IsDisk: false,
				}}, p.Vdevs...)
			}
			p.Vdevs = append(p.Vdevs, Vdev{
				Name:      newBase,
				Type:      "disk",
				Role:      "mirror-0",
				Health:    "ONLINE",
				State:     "ONLINE",
				SizeBytes: p.SizeBytes,
				Path:      "/dev/" + newBase,
				IsDisk:    true,
			})
			p.Topology = "" // recompute
			syncPoolFields(p)
		}
		return nil
	}

	existingMember := m.resolvePoolMember(pool, existingDev)
	if !strings.HasPrefix(newDev, "/") {
		newDev = "/dev/" + newDev
	}
	out, err := runCommand(defaultZfsTimeout, "zpool", "attach", "-f", pool, existingMember, newDev)
	if err != nil {
		return fmt.Errorf("zpool attach failed: %v (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// DetachDisk detaches a disk from a mirror vdev
func (m *Manager) DetachDisk(pool, device string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	pool = strings.TrimSpace(pool)
	device = strings.TrimSpace(device)
	if err := validate.PoolName(pool); err != nil {
		return fmt.Errorf("invalid pool name: %w", err)
	}
	if err := validate.DevicePath(device); err != nil {
		return fmt.Errorf("invalid device: %w", err)
	}

	if m.mockMode {
		if p, ok := m.mockPools[pool]; ok {
			diskCount := 0
			for _, v := range p.Vdevs {
				if v.IsDisk {
					diskCount++
				}
			}
			if diskCount <= 1 {
				return fmt.Errorf("cannot detach the only remaining disk in pool '%s'", pool)
			}
			for i, v := range p.Vdevs {
				if v.Name == device || v.Path == device || filepath.Base(v.Path) == device {
					p.Vdevs = append(p.Vdevs[:i], p.Vdevs[i+1:]...)
					break
				}
			}
			p.Topology = ""
			syncPoolFields(p)
		}
		return nil
	}

	targetMember := m.resolvePoolMember(pool, device)
	out, err := runCommand(defaultZfsTimeout, "zpool", "detach", pool, targetMember)
	if err != nil {
		return fmt.Errorf("zpool detach failed: %v (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// ReplaceDisk replaces a disk in a pool with another device
func (m *Manager) ReplaceDisk(pool, oldDev, newDev string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	pool = strings.TrimSpace(pool)
	oldDev = strings.TrimSpace(oldDev)
	newDev = strings.TrimSpace(newDev)
	if err := validate.PoolName(pool); err != nil {
		return fmt.Errorf("invalid pool name: %w", err)
	}
	if err := validate.DevicePath(oldDev); err != nil {
		return fmt.Errorf("invalid old device: %w", err)
	}
	if err := validate.DevicePath(newDev); err != nil {
		return fmt.Errorf("invalid new device: %w", err)
	}

	if m.mockMode {
		if p, ok := m.mockPools[pool]; ok {
			newBase := filepath.Base(newDev)
			for i, v := range p.Vdevs {
				if v.Name == oldDev || v.Path == oldDev || filepath.Base(v.Path) == oldDev {
					p.Vdevs[i].Name = newBase
					p.Vdevs[i].Path = "/dev/" + newBase
					p.Vdevs[i].Health = "ONLINE"
					p.Vdevs[i].State = "ONLINE"
					break
				}
			}
			syncPoolFields(p)
		}
		return nil
	}

	oldMember := m.resolvePoolMember(pool, oldDev)
	if !strings.HasPrefix(newDev, "/") {
		newDev = "/dev/" + newDev
	}
	out, err := runCommand(defaultZfsTimeout, "zpool", "replace", "-f", pool, oldMember, newDev)
	if err != nil {
		return fmt.Errorf("zpool replace failed: %v (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// OfflineDisk sets a disk offline
func (m *Manager) OfflineDisk(pool, device string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	pool = strings.TrimSpace(pool)
	device = strings.TrimSpace(device)
	if err := validate.PoolName(pool); err != nil {
		return fmt.Errorf("invalid pool name: %w", err)
	}
	if err := validate.DevicePath(device); err != nil {
		return fmt.Errorf("invalid device: %w", err)
	}

	if m.mockMode {
		if p, ok := m.mockPools[pool]; ok {
			for i, v := range p.Vdevs {
				if v.Name == device || v.Path == device || filepath.Base(v.Path) == device {
					p.Vdevs[i].Health = "OFFLINE"
					p.Vdevs[i].State = "OFFLINE"
					break
				}
			}
			syncPoolFields(p)
		}
		return nil
	}

	targetMember := m.resolvePoolMember(pool, device)
	out, err := runCommand(defaultZfsTimeout, "zpool", "offline", pool, targetMember)
	if err != nil {
		return fmt.Errorf("zpool offline failed: %v (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// OnlineDisk brings an offline disk back online
func (m *Manager) OnlineDisk(pool, device string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	pool = strings.TrimSpace(pool)
	device = strings.TrimSpace(device)
	if err := validate.PoolName(pool); err != nil {
		return fmt.Errorf("invalid pool name: %w", err)
	}
	if err := validate.DevicePath(device); err != nil {
		return fmt.Errorf("invalid device: %w", err)
	}

	if m.mockMode {
		if p, ok := m.mockPools[pool]; ok {
			for i, v := range p.Vdevs {
				if v.Name == device || v.Path == device || filepath.Base(v.Path) == device {
					p.Vdevs[i].Health = "ONLINE"
					p.Vdevs[i].State = "ONLINE"
					break
				}
			}
			syncPoolFields(p)
		}
		return nil
	}

	targetMember := m.resolvePoolMember(pool, device)
	out, err := runCommand(defaultZfsTimeout, "zpool", "online", pool, targetMember)
	if err != nil {
		return fmt.Errorf("zpool online failed: %v (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// SetPoolProperty sets a zpool property like autotrim, autoexpand, failmode
func (m *Manager) SetPoolProperty(pool, property, value string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	pool = strings.TrimSpace(pool)
	property = strings.TrimSpace(strings.ToLower(property))
	value = strings.TrimSpace(value)
	if err := validate.PoolName(pool); err != nil {
		return fmt.Errorf("invalid pool name: %w", err)
	}
	if err := validate.PoolProperty(property, value); err != nil {
		return fmt.Errorf("invalid pool property: %w", err)
	}

	if m.mockMode {
		if p, ok := m.mockPools[pool]; ok {
			switch property {
			case "autotrim":
				p.Autotrim = value
			case "autoexpand":
				p.Autoexpand = value
			}
			syncPoolFields(p)
		}
		return nil
	}

	out, err := runCommand(defaultZfsTimeout, "zpool", "set", fmt.Sprintf("%s=%s", property, value), pool)
	if err != nil {
		return fmt.Errorf("zpool set failed: %v (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// CreateZvol creates a block volume for VMs, iSCSI or raw block storage
func (m *Manager) CreateZvol(name string, sizeBytes uint64, volblocksize string, sparse bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	name = strings.TrimSpace(name)
	if err := validate.DatasetName(name); err != nil {
		return fmt.Errorf("invalid zvol name: %w", err)
	}
	if sizeBytes == 0 {
		return fmt.Errorf("zvol size must be greater than 0")
	}

	if volblocksize == "" {
		volblocksize = "16k"
	}

	if m.mockMode {
		poolName := strings.Split(name, "/")[0]
		ds := &Dataset{
			Name:           name,
			Pool:           poolName,
			Type:           "volume",
			Mountpoint:     "/dev/zvol/" + name,
			UsedBytes:      65536,
			UsedHuman:      "64 KiB",
			AvailBytes:     sizeBytes,
			AvailableBytes: sizeBytes,
			AvailHuman:     formatZfsBytes(sizeBytes),
			Compression:    "lz4",
			Atime:          "off",
			RecordSize:     volblocksize,
		}
		syncDatasetFields(ds)
		m.mockDS[name] = ds
		return nil
	}

	args := []string{"create", "-V", fmt.Sprintf("%d", sizeBytes), "-b", volblocksize}
	if sparse {
		args = append(args, "-s")
	}
	args = append(args, name)

	out, err := runCommand(defaultZfsTimeout, "zfs", args...)
	if err != nil {
		return fmt.Errorf("zfs create -V failed: %v (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// CloneSnapshot creates a writable dataset clone from a read-only snapshot
func (m *Manager) CloneSnapshot(snapshot, targetDataset string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	snapshot = strings.TrimSpace(snapshot)
	targetDataset = strings.TrimSpace(targetDataset)
	parts := strings.Split(snapshot, "@")
	if len(parts) != 2 {
		return fmt.Errorf("invalid snapshot '%s': must be dataset@snap", snapshot)
	}
	if err := validate.SnapshotName(parts[0], parts[1]); err != nil {
		return fmt.Errorf("invalid source snapshot: %w", err)
	}
	if err := validate.DatasetName(targetDataset); err != nil {
		return fmt.Errorf("invalid target dataset: %w", err)
	}
	if !strings.Contains(targetDataset, "/") {
		return fmt.Errorf("target clone '%s' must include pool prefix (e.g. '%s/clone-name')", targetDataset, strings.Split(parts[0], "/")[0])
	}
	sourcePool := strings.Split(parts[0], "/")[0]
	targetPool := strings.Split(targetDataset, "/")[0]
	if sourcePool != targetPool {
		return fmt.Errorf("cannot clone '%s' into a different pool '%s': target must be inside pool '%s'", snapshot, targetPool, sourcePool)
	}

	if m.mockMode {
		var srcSnap *Snapshot
		for _, s := range m.mockSnaps {
			if s.Name == snapshot {
				srcSnap = s
				break
			}
		}
		if srcSnap == nil {
			return fmt.Errorf("source snapshot '%s' does not exist", snapshot)
		}
		if _, exists := m.mockDS[targetDataset]; exists {
			return fmt.Errorf("target dataset '%s' already exists", targetDataset)
		}

		dsType := "filesystem"
		mountpoint := "/mnt/" + targetDataset
		recordSize := "128K"
		compression := "lz4"
		if srcDS, ok := m.mockDS[parts[0]]; ok {
			if srcDS.Type == "volume" {
				dsType = "volume"
				mountpoint = "/dev/zvol/" + targetDataset
			}
			if srcDS.RecordSize != "" {
				recordSize = srcDS.RecordSize
			}
			if srcDS.Compression != "" {
				compression = srcDS.Compression
			}
		}

		var avail uint64 = 1546188226560
		if p, ok := m.mockPools[targetPool]; ok && p.FreeBytes > 0 {
			avail = p.FreeBytes
		}
		ds := &Dataset{
			Name:           targetDataset,
			Pool:           targetPool,
			Type:           dsType,
			Mountpoint:     mountpoint,
			Origin:         snapshot,
			UsedBytes:      1048576,
			UsedHuman:      "1.0 MiB",
			AvailBytes:     avail,
			AvailableBytes: avail,
			AvailHuman:     formatZfsBytes(avail),
			Compression:    compression,
			Atime:          "off",
			RecordSize:     recordSize,
		}
		syncDatasetFields(ds)
		m.mockDS[targetDataset] = ds
		return nil
	}

	// Only apply filesystem property 'atime=off' when cloning a filesystem (zvols do not support atime).
	isFilesystem := true
	if typeOut, typeErr := runCommandOutput(fastZfsTimeout, "zfs", "list", "-H", "-p", "-o", "type", parts[0]); typeErr == nil {
		if strings.TrimSpace(string(typeOut)) == "volume" {
			isFilesystem = false
		}
	}

	args := []string{"clone"}
	if isFilesystem {
		args = append(args, "-o", "atime=off")
	}
	args = append(args, snapshot, targetDataset)

	out, err := runCommand(defaultZfsTimeout, "zfs", args...)
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
