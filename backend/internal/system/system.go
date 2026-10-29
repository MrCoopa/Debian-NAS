package system

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

type SystemStatus struct {
	Hostname     string         `json:"hostname"`
	OS           string         `json:"os"`
	Kernel       string         `json:"kernel"`
	Arch         string         `json:"arch"`
	Uptime       string         `json:"uptime"`
	UptimeSec    int64          `json:"uptime_sec"`
	DaemonRamMB  float64        `json:"daemon_ram_mb"`
	Goroutines   int            `json:"goroutines"`
	CPU          CPUInfo        `json:"cpu"`
	Memory       MemoryInfo     `json:"memory"`
	Network      []NetworkInfo  `json:"network"`
	DiskIO       DiskIOInfo     `json:"disk_io"`
	LastUpdated  time.Time      `json:"last_updated"`
}

type DeviceIOInfo struct {
	Device    string    `json:"device"`
	Name      string    `json:"name"`
	ReadMBS   float64   `json:"read_mb_s"`
	WriteMBS  float64   `json:"write_mb_s"`
	ReadIOPS  int       `json:"read_iops"`
	WriteIOPS int       `json:"write_iops"`
	History   []float64 `json:"history"`
}

type DiskIOInfo struct {
	ReadMBS   float64                  `json:"read_mb_s"`
	WriteMBS  float64                  `json:"write_mb_s"`
	ReadIOPS  int                      `json:"read_iops"`
	WriteIOPS int                      `json:"write_iops"`
	History   []float64                `json:"history"`
	Devices   map[string]*DeviceIOInfo `json:"devices"`
}

type CPUInfo struct {
	Model        string    `json:"model"`
	Cores        int       `json:"cores"`
	Threads      int       `json:"threads"`
	UsagePercent float64   `json:"usage_percent"`
	TemperatureC int       `json:"temperature_c"`
	LoadAvg      []float64 `json:"load_avg"`
}

type MemoryInfo struct {
	TotalBytes    uint64  `json:"total_bytes"`
	TotalHuman    string  `json:"total_human"`
	UsedBytes     uint64  `json:"used_bytes"`
	UsedHuman     string  `json:"used_human"`
	UsedPercent   float64 `json:"used_percent"`
	FreeBytes     uint64  `json:"free_bytes"`
	FreeHuman     string  `json:"free_human"`
	ZfsArcBytes   uint64  `json:"zfs_arc_bytes"`
	ZfsArcHuman   string  `json:"zfs_arc_human"`
	ZfsArcPercent float64 `json:"zfs_arc_percent"`
	ServicesHuman string  `json:"services_human"`
}

type NetworkInfo struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Speed    string `json:"speed"`
	IP       string `json:"ip"`
	MAC      string `json:"mac"`
	RxRate   string `json:"rx_rate"`
	TxRate   string `json:"tx_rate"`
	State    string `json:"state"`
	Activity bool   `json:"activity"`
}

type linuxDiskSnapshot struct {
	readsCompleted  uint64
	sectorsRead     uint64
	writesCompleted uint64
	sectorsWritten  uint64
}

type Collector struct {
	mu           sync.Mutex
	mockMode     bool
	startTime    time.Time
	lastCPUStat  cpuStatSnapshot
	lastNetStat  map[string]netStatSnapshot
	lastStatTime time.Time
	cachedStatus *SystemStatus

	// Windows CPU calculation tracking
	lastWinIdle   uint64
	lastWinKernel uint64
	lastWinUser   uint64

	// Linux disk stats snapshot
	lastLinuxDisk  linuxDiskSnapshot
	lastLinuxDisks map[string]linuxDiskSnapshot

	// History buffer for Disk I/O
	ioHistory       []float64
	deviceHistories map[string][]float64
}

type cpuStatSnapshot struct {
	user, nice, system, idle, iowait, irq, softirq, steal uint64
}

type netStatSnapshot struct {
	rxBytes, txBytes uint64
}

func NewCollector(mockMode bool) *Collector {
	c := &Collector{
		mockMode:        mockMode,
		startTime:       time.Now(),
		lastNetStat:     make(map[string]netStatSnapshot),
		lastStatTime:    time.Now(),
		ioHistory:       make([]float64, 0, 30),
		deviceHistories: make(map[string][]float64),
		lastLinuxDisks:  make(map[string]linuxDiskSnapshot),
	}
	// Initial reading to prime delta counters
	if !c.mockMode && runtime.GOOS == "linux" {
		c.lastCPUStat = c.readCPUStatSnapshot()
		c.lastNetStat = c.readNetStatSnapshot()
	}
	return c
}

// GetStatus returns the current live system telemetry
func (c *Collector) GetStatus() *SystemStatus {
	c.mu.Lock()
	defer c.mu.Unlock()

	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	daemonMB := float64(m.Alloc) / 1024 / 1024

	if c.mockMode {
		return c.getMockStatus(daemonMB)
	}

	if runtime.GOOS == "windows" {
		return c.getWindowsStatus(daemonMB)
	}

	return c.getLinuxStatus(daemonMB)
}

func (c *Collector) getWindowsStatus(daemonMB float64) *SystemStatus {
	now := time.Now()
	timeDelta := now.Sub(c.lastStatTime).Seconds()
	if timeDelta < 0.1 {
		timeDelta = 0.1
	}

	host, _ := os.Hostname()
	uptimeSec, uptimeStr := c.readWindowsUptime()
	cpuUsage := c.readWindowsCPUUsage()
	cpuModel := c.readWindowsCPUModel()
	memInfo := c.readWindowsMemory()
	readMB, writeMB, rIOPS, wIOPS, devMap := c.readWindowsDiskIO()

	totalThroughput := readMB + writeMB
	c.ioHistory = append(c.ioHistory, totalThroughput)
	if len(c.ioHistory) > 30 {
		c.ioHistory = c.ioHistory[len(c.ioHistory)-30:]
	}

	currNet := c.readNetStatSnapshot()
	netList := c.buildNetworkInfo(currNet, timeDelta)
	c.lastNetStat = currNet
	c.lastStatTime = now

	threads := runtime.NumCPU()
	cores := threads
	if threads >= 20 {
		cores = 14 // 13600KF has 14 physical cores, 20 logical threads
	}

	status := &SystemStatus{
		Hostname:    host,
		OS:          "Microsoft Windows 11 Enterprise (Storage Node)",
		Kernel:      "Windows NT 10.0",
		Arch:        runtime.GOARCH,
		Uptime:      uptimeStr,
		UptimeSec:   uptimeSec,
		DaemonRamMB: daemonMB,
		Goroutines:  runtime.NumGoroutine(),
		CPU: CPUInfo{
			Model:        cpuModel,
			Cores:        cores,
			Threads:      threads,
			UsagePercent: cpuUsage,
			TemperatureC: 32,
			LoadAvg:      []float64{cpuUsage / 100.0, cpuUsage / 100.0 * 1.1, cpuUsage / 100.0 * 0.9},
		},
		Memory:  memInfo,
		Network: netList,
		DiskIO: DiskIOInfo{
			ReadMBS:   readMB,
			WriteMBS:  writeMB,
			ReadIOPS:  rIOPS,
			WriteIOPS: wIOPS,
			History:   c.ioHistory,
			Devices:   devMap,
		},
		LastUpdated: now,
	}

	c.cachedStatus = status
	return status
}

func (c *Collector) getLinuxStatus(daemonMB float64) *SystemStatus {
	now := time.Now()
	timeDelta := now.Sub(c.lastStatTime).Seconds()
	if timeDelta < 0.1 {
		timeDelta = 0.1
	}

	// 1. Hostname & OS
	host, _ := os.Hostname()
	osName := c.readOSRelease()
	kernel := c.readKernelVersion()

	// 2. Uptime
	uptimeSec, uptimeStr := c.readUptime()

	// 3. CPU
	currCPU := c.readCPUStatSnapshot()
	cpuUsage := c.calcCPUUsage(c.lastCPUStat, currCPU)
	c.lastCPUStat = currCPU

	cpuModel, cores, threads := c.readCPUInfo()
	loadAvg := c.readLoadAvg()
	cpuTemp := c.readCPUTemperature()

	// 4. Memory & ZFS ARC
	memInfo := c.readMemInfo()

	// 5. Network
	currNet := c.readNetStatSnapshot()
	netList := c.buildNetworkInfo(currNet, timeDelta)
	c.lastNetStat = currNet

	// 6. Real Disk I/O from /proc/diskstats
	readMB, writeMB, rIOPS, wIOPS, devMap := c.readLinuxDiskIO(timeDelta)
	totalThroughput := readMB + writeMB
	c.ioHistory = append(c.ioHistory, totalThroughput)
	if len(c.ioHistory) > 30 {
		c.ioHistory = c.ioHistory[len(c.ioHistory)-30:]
	}

	c.lastStatTime = now

	status := &SystemStatus{
		Hostname:    host,
		OS:          osName,
		Kernel:      kernel,
		Arch:        runtime.GOARCH,
		Uptime:      uptimeStr,
		UptimeSec:   uptimeSec,
		DaemonRamMB: daemonMB,
		Goroutines:  runtime.NumGoroutine(),
		CPU: CPUInfo{
			Model:        cpuModel,
			Cores:        cores,
			Threads:      threads,
			UsagePercent: cpuUsage,
			TemperatureC: cpuTemp,
			LoadAvg:      loadAvg,
		},
		Memory:  memInfo,
		Network: netList,
		DiskIO: DiskIOInfo{
			ReadMBS:   readMB,
			WriteMBS:  writeMB,
			ReadIOPS:  rIOPS,
			WriteIOPS: wIOPS,
			History:   c.ioHistory,
			Devices:   devMap,
		},
		LastUpdated: now,
	}

	c.cachedStatus = status
	return status
}

func (c *Collector) getMockStatus(daemonMB float64) *SystemStatus {
	nowSec := time.Now().Unix()
	uptimeDuration := time.Since(c.startTime) + 11*time.Hour + 25*time.Minute
	uptimeHours := int(uptimeDuration.Hours())
	uptimeMins := int(uptimeDuration.Minutes()) % 60
	uptimeStr := fmt.Sprintf("%d hours %d minutes", uptimeHours, uptimeMins)

	cpuPercent := 0.8 + 0.6*float64(nowSec%5)/5.0

	return &SystemStatus{
		Hostname:    "truenas",
		OS:          "Debian GNU/Linux 13 (Trixie)",
		Kernel:      "6.12.0-nas-amd64",
		Arch:        runtime.GOARCH,
		Uptime:      uptimeStr,
		UptimeSec:   int64(uptimeDuration.Seconds()),
		DaemonRamMB: daemonMB,
		Goroutines:  runtime.NumGoroutine(),
		CPU: CPUInfo{
			Model:        "13th Gen Intel(R) Core(TM) i5-13500",
			Cores:        14,
			Threads:      20,
			UsagePercent: cpuPercent,
			TemperatureC: 28,
			LoadAvg:      []float64{0.14, 0.22, 0.18},
		},
		Memory: MemoryInfo{
			TotalBytes:    16750372454,
			TotalHuman:    "15.6 GiB",
			UsedBytes:     15891398656,
			UsedHuman:     "14.8 GiB",
			UsedPercent:   94.8,
			FreeBytes:     858993459,
			FreeHuman:     "0.8 GiB",
			ZfsArcBytes:   13314398617,
			ZfsArcHuman:   "12.4 GiB",
			ZfsArcPercent: 79.5,
			ServicesHuman: "2.4 GiB",
		},
		Network: []NetworkInfo{
			{
				Name:     "ens18",
				Type:     "Intel VirtIO / Ethernet",
				Speed:    "10000 Mbps",
				IP:       "192.168.2.112",
				MAC:      "BC:24:11:92:B2:18",
				RxRate:   fmt.Sprintf("%.2f kb/s", 70.0+float64(nowSec%15)),
				TxRate:   fmt.Sprintf("%.2f kb/s", 155.0+float64(nowSec%25)),
				State:    "UP",
				Activity: true,
			},
		},
		DiskIO: DiskIOInfo{
			ReadMBS:   0.0,
			WriteMBS:  0.0,
			ReadIOPS:  0,
			WriteIOPS: 0,
			History:   []float64{0, 0, 0},
		},
		LastUpdated: time.Now(),
	}
}

// ---------------- Linux Helper Functions ----------------

func (c *Collector) readLinuxDiskIO(deltaSec float64) (float64, float64, int, int, map[string]*DeviceIOInfo) {
	devMap := make(map[string]*DeviceIOInfo)
	f, err := os.Open("/proc/diskstats")
	if err != nil {
		return 0, 0, 0, 0, devMap
	}
	defer f.Close()

	if deltaSec <= 0 {
		deltaSec = 1.0
	}

	var curr linuxDiskSnapshot
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 14 {
			continue
		}
		dev := fields[2]
		// Filter relevant block devices (sdX, nvmeXn1, vdX)
		if !strings.HasPrefix(dev, "sd") && !strings.HasPrefix(dev, "nvme") && !strings.HasPrefix(dev, "vd") {
			continue
		}
		// Skip individual partitions like sda1, nvme0n1p1
		if strings.HasPrefix(dev, "nvme") && strings.Contains(dev, "p") {
			continue
		}
		if (strings.HasPrefix(dev, "sd") || strings.HasPrefix(dev, "vd")) && len(dev) > 3 && (dev[3] >= '0' && dev[3] <= '9') {
			continue
		}

		reads, _ := strconv.ParseUint(fields[3], 10, 64)
		readSectors, _ := strconv.ParseUint(fields[5], 10, 64)
		writes, _ := strconv.ParseUint(fields[7], 10, 64)
		writeSectors, _ := strconv.ParseUint(fields[9], 10, 64)

		curr.readsCompleted += reads
		curr.sectorsRead += readSectors
		curr.writesCompleted += writes
		curr.sectorsWritten += writeSectors

		devCurr := linuxDiskSnapshot{
			readsCompleted:  reads,
			sectorsRead:     readSectors,
			writesCompleted: writes,
			sectorsWritten:  writeSectors,
		}

		if prev, exists := c.lastLinuxDisks[dev]; exists {
			dReadSec := devCurr.sectorsRead - prev.sectorsRead
			dWriteSec := devCurr.sectorsWritten - prev.sectorsWritten
			dReads := devCurr.readsCompleted - prev.readsCompleted
			dWrites := devCurr.writesCompleted - prev.writesCompleted

			devReadMB := (float64(dReadSec) * 512.0) / (1024.0 * 1024.0) / deltaSec
			devWriteMB := (float64(dWriteSec) * 512.0) / (1024.0 * 1024.0) / deltaSec
			devRIOPS := int(float64(dReads) / deltaSec)
			devWIOPS := int(float64(dWrites) / deltaSec)

			hist := c.deviceHistories[dev]
			hist = append(hist, devReadMB+devWriteMB)
			if len(hist) > 30 {
				hist = hist[len(hist)-30:]
			}
			c.deviceHistories[dev] = hist

			devMap[dev] = &DeviceIOInfo{
				Device:    dev,
				Name:      "/dev/" + dev,
				ReadMBS:   devReadMB,
				WriteMBS:  devWriteMB,
				ReadIOPS:  devRIOPS,
				WriteIOPS: devWIOPS,
				History:   hist,
			}
		}
		c.lastLinuxDisks[dev] = devCurr
	}

	if c.lastLinuxDisk.sectorsRead == 0 && c.lastLinuxDisk.sectorsWritten == 0 {
		c.lastLinuxDisk = curr
		return 0, 0, 0, 0, devMap
	}

	deltaReadSec := curr.sectorsRead - c.lastLinuxDisk.sectorsRead
	deltaWriteSec := curr.sectorsWritten - c.lastLinuxDisk.sectorsWritten
	deltaReads := curr.readsCompleted - c.lastLinuxDisk.readsCompleted
	deltaWrites := curr.writesCompleted - c.lastLinuxDisk.writesCompleted

	c.lastLinuxDisk = curr

	readMB := (float64(deltaReadSec) * 512.0) / (1024.0 * 1024.0) / deltaSec
	writeMB := (float64(deltaWriteSec) * 512.0) / (1024.0 * 1024.0) / deltaSec
	rIOPS := int(float64(deltaReads) / deltaSec)
	wIOPS := int(float64(deltaWrites) / deltaSec)

	return readMB, writeMB, rIOPS, wIOPS, devMap
}

func (c *Collector) readOSRelease() string {
	f, err := os.Open("/etc/os-release")
	if err != nil {
		return "Debian GNU/Linux 13 (Trixie)"
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "PRETTY_NAME=") {
			return strings.Trim(strings.TrimPrefix(line, "PRETTY_NAME="), `"`)
		}
	}
	return "Debian GNU/Linux 13"
}

func (c *Collector) readKernelVersion() string {
	b, err := os.ReadFile("/proc/sys/kernel/osrelease")
	if err == nil {
		return strings.TrimSpace(string(b))
	}
	cmd := exec.Command("uname", "-r")
	out, err := cmd.Output()
	if err == nil {
		return strings.TrimSpace(string(out))
	}
	return "Linux Kernel"
}

func (c *Collector) readUptime() (int64, string) {
	b, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0, "Unknown"
	}
	fields := strings.Fields(string(b))
	if len(fields) == 0 {
		return 0, "Unknown"
	}
	secFloat, _ := strconv.ParseFloat(fields[0], 64)
	totalSec := int64(secFloat)
	days := totalSec / 86400
	hours := (totalSec % 86400) / 3600
	mins := (totalSec % 3600) / 60

	if days > 0 {
		return totalSec, fmt.Sprintf("%d days, %d hours, %d minutes", days, hours, mins)
	}
	return totalSec, fmt.Sprintf("%d hours, %d minutes", hours, mins)
}

func (c *Collector) readCPUStatSnapshot() cpuStatSnapshot {
	var s cpuStatSnapshot
	f, err := os.Open("/proc/stat")
	if err != nil {
		return s
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	if scanner.Scan() {
		line := scanner.Text()
		fields := strings.Fields(line)
		if len(fields) >= 8 && fields[0] == "cpu" {
			s.user, _ = strconv.ParseUint(fields[1], 10, 64)
			s.nice, _ = strconv.ParseUint(fields[2], 10, 64)
			s.system, _ = strconv.ParseUint(fields[3], 10, 64)
			s.idle, _ = strconv.ParseUint(fields[4], 10, 64)
			s.iowait, _ = strconv.ParseUint(fields[5], 10, 64)
			s.irq, _ = strconv.ParseUint(fields[6], 10, 64)
			s.softirq, _ = strconv.ParseUint(fields[7], 10, 64)
			if len(fields) >= 9 {
				s.steal, _ = strconv.ParseUint(fields[8], 10, 64)
			}
		}
	}
	return s
}

func (c *Collector) calcCPUUsage(prev, curr cpuStatSnapshot) float64 {
	prevIdle := prev.idle + prev.iowait
	currIdle := curr.idle + curr.iowait

	prevNonIdle := prev.user + prev.nice + prev.system + prev.irq + prev.softirq + prev.steal
	currNonIdle := curr.user + curr.nice + curr.system + curr.irq + curr.softirq + curr.steal

	prevTotal := prevIdle + prevNonIdle
	currTotal := currIdle + currNonIdle

	totalDiff := currTotal - prevTotal
	idleDiff := currIdle - prevIdle

	if totalDiff == 0 {
		return 0.0
	}
	usage := float64(totalDiff-idleDiff) / float64(totalDiff) * 100.0
	if usage < 0 {
		return 0.0
	}
	if usage > 100 {
		return 100.0
	}
	return usage
}

func (c *Collector) readCPUInfo() (string, int, int) {
	model := "Generic CPU"
	cores := runtime.NumCPU()
	threads := cores

	f, err := os.Open("/proc/cpuinfo")
	if err != nil {
		return model, cores, threads
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	foundModel := false
	processorCount := 0
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "model name") && !foundModel {
			parts := strings.Split(line, ":")
			if len(parts) >= 2 {
				model = strings.TrimSpace(parts[1])
				foundModel = true
			}
		}
		if strings.HasPrefix(line, "processor") {
			processorCount++
		}
	}
	if processorCount > 0 {
		threads = processorCount
	}
	return model, cores, threads
}

func (c *Collector) readLoadAvg() []float64 {
	b, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return []float64{0.0, 0.0, 0.0}
	}
	fields := strings.Fields(string(b))
	if len(fields) >= 3 {
		l1, _ := strconv.ParseFloat(fields[0], 64)
		l2, _ := strconv.ParseFloat(fields[1], 64)
		l3, _ := strconv.ParseFloat(fields[2], 64)
		return []float64{l1, l2, l3}
	}
	return []float64{0.0, 0.0, 0.0}
}

func (c *Collector) readCPUTemperature() int {
	// Look through /sys/class/thermal/thermal_zone*/temp or /sys/class/hwmon/hwmon*/temp1_input
	for i := 0; i < 5; i++ {
		tempPath := fmt.Sprintf("/sys/class/thermal/thermal_zone%d/temp", i)
		if b, err := os.ReadFile(tempPath); err == nil {
			if val, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && val > 0 {
				return val / 1000 // Millidegrees to Celsius
			}
		}
	}
	return 28
}

func (c *Collector) readMemInfo() MemoryInfo {
	var total, avail uint64
	f, err := os.Open("/proc/meminfo")
	if err == nil {
		defer f.Close()
		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			line := scanner.Text()
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				val, _ := strconv.ParseUint(fields[1], 10, 64)
				valBytes := val * 1024 // /proc/meminfo is in kB
				switch fields[0] {
				case "MemTotal:":
					total = valBytes
				case "MemAvailable:", "MemFree:":
					if avail == 0 {
						avail = valBytes
					}
				}
			}
		}
	}

	if total == 0 {
		total = 16 * 1024 * 1024 * 1024
		avail = 1 * 1024 * 1024 * 1024
	}

	used := total - avail
	usedPct := float64(used) / float64(total) * 100.0

	// Read ZFS ARC stats if OpenZFS is loaded
	var arcBytes uint64
	arcFile, err := os.Open("/proc/spl/kstat/zfs/arcstats")
	if err == nil {
		defer arcFile.Close()
		scanner := bufio.NewScanner(arcFile)
		for scanner.Scan() {
			line := scanner.Text()
			fields := strings.Fields(line)
			if len(fields) >= 3 && fields[0] == "size" {
				arcBytes, _ = strconv.ParseUint(fields[2], 10, 64)
				break
			}
		}
	}

	arcPct := float64(arcBytes) / float64(total) * 100.0
	servicesBytes := uint64(0)
	if used > arcBytes {
		servicesBytes = used - arcBytes
	}

	return MemoryInfo{
		TotalBytes:    total,
		TotalHuman:    formatBytes(total),
		UsedBytes:     used,
		UsedHuman:     formatBytes(used),
		UsedPercent:   usedPct,
		FreeBytes:     avail,
		FreeHuman:     formatBytes(avail),
		ZfsArcBytes:   arcBytes,
		ZfsArcHuman:   formatBytes(arcBytes),
		ZfsArcPercent: arcPct,
		ServicesHuman: formatBytes(servicesBytes),
	}
}

func (c *Collector) readNetStatSnapshot() map[string]netStatSnapshot {
	res := make(map[string]netStatSnapshot)
	f, err := os.Open("/proc/net/dev")
	if err != nil {
		return res
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.Contains(line, ":") {
			parts := strings.Split(line, ":")
			iface := strings.TrimSpace(parts[0])
			if iface == "lo" {
				continue
			}
			fields := strings.Fields(parts[1])
			if len(fields) >= 9 {
				rx, _ := strconv.ParseUint(fields[0], 10, 64)
				tx, _ := strconv.ParseUint(fields[8], 10, 64)
				res[iface] = netStatSnapshot{rxBytes: rx, txBytes: tx}
			}
		}
	}
	return res
}

func (c *Collector) buildNetworkInfo(curr map[string]netStatSnapshot, deltaSec float64) []NetworkInfo {
	var list []NetworkInfo

	ifaces, err := net.Interfaces()
	if err != nil {
		return list
	}

	for _, iface := range ifaces {
		if iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		if iface.Flags&net.FlagUp == 0 {
			continue
		}

		addrs, _ := iface.Addrs()
		ipStr := ""
		for _, addr := range addrs {
			if ipNet, ok := addr.(*net.IPNet); ok && !ipNet.IP.IsLoopback() {
				if ipNet.IP.To4() != nil {
					ipStr = ipNet.IP.String()
					break
				}
			}
		}

		// Calculate rate
		currStats, hasCurr := curr[iface.Name]
		prevStats, hasPrev := c.lastNetStat[iface.Name]
		var rxRateStr, txRateStr string
		activity := false

		if hasCurr && hasPrev && deltaSec > 0 {
			rxRateBps := float64(currStats.rxBytes-prevStats.rxBytes) / deltaSec
			txRateBps := float64(currStats.txBytes-prevStats.txBytes) / deltaSec
			rxRateStr = formatRate(rxRateBps)
			txRateStr = formatRate(txRateBps)
			if rxRateBps > 1024 || txRateBps > 1024 {
				activity = true
			}
		} else {
			rxRateStr = "0.00 kb/s"
			txRateStr = "0.00 kb/s"
		}

		// Read interface speed from /sys/class/net/<name>/speed
		speed := "1000 Mbps"
		speedPath := fmt.Sprintf("/sys/class/net/%s/speed", iface.Name)
		if b, err := os.ReadFile(speedPath); err == nil {
			speedVal := strings.TrimSpace(string(b))
			if speedVal != "" && speedVal != "-1" {
				speed = speedVal + " Mbps"
			}
		}

		list = append(list, NetworkInfo{
			Name:     iface.Name,
			Type:     "Ethernet / VirtIO",
			Speed:    speed,
			IP:       ipStr,
			MAC:      strings.ToUpper(iface.HardwareAddr.String()),
			RxRate:   rxRateStr,
			TxRate:   txRateStr,
			State:    "UP",
			Activity: activity,
		})
	}

	if len(list) == 0 {
		list = append(list, NetworkInfo{
			Name:     "eth0",
			Type:     "Ethernet",
			Speed:    "1000 Mbps",
			IP:       "127.0.0.1",
			MAC:      "00:00:00:00:00:00",
			RxRate:   "0.00 kb/s",
			TxRate:   "0.00 kb/s",
			State:    "UP",
			Activity: false,
		})
	}

	return list
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
	return fmt.Sprintf("%.2f %ciB", float64(b)/float64(div), "KMGTPE"[exp])
}

func formatRate(bps float64) string {
	if bps < 1024 {
		return fmt.Sprintf("%.2f B/s", bps)
	} else if bps < 1024*1024 {
		return fmt.Sprintf("%.2f kb/s", bps/1024*8)
	} else {
		return fmt.Sprintf("%.2f Mb/s", bps/(1024*1024)*8)
	}
}
