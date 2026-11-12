package system

import (
	"bufio"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

const (
	defaultArcStatsPath    = "/proc/spl/kstat/zfs/arcstats"
	defaultArcParamsDir    = "/sys/module/zfs/parameters"
	defaultArcModprobePath = "/etc/modprobe.d/zfs.conf"
	defaultMemInfoPath     = "/proc/meminfo"

	minAllowedArcMaxBytes uint64 = 64 * 1024 * 1024
	minAllowedArcMinBytes uint64 = 32 * 1024 * 1024
	mockTotalRamBytes     uint64 = 16750372454
	mockDefaultArcBytes   uint64 = 13314398617
)

// ZfsArcConfig holds live OpenZFS ARC statistics and configured kernel limits.
type ZfsArcConfig struct {
	CurrentBytes       uint64  `json:"current_bytes"`
	CurrentHuman       string  `json:"current_human"`
	TargetBytes        uint64  `json:"target_bytes"`
	TargetHuman        string  `json:"target_human"`
	MaxBytes           uint64  `json:"max_bytes"`
	MaxHuman           string  `json:"max_human"`
	EffectiveMaxBytes  uint64  `json:"effective_max_bytes"`
	EffectiveMaxHuman  string  `json:"effective_max_human"`
	MinBytes           uint64  `json:"min_bytes"`
	MinHuman           string  `json:"min_human"`
	EffectiveMinBytes  uint64  `json:"effective_min_bytes"`
	EffectiveMinHuman  string  `json:"effective_min_human"`
	TotalRamBytes      uint64  `json:"total_ram_bytes"`
	TotalRamHuman      string  `json:"total_ram_human"`
	MaxInputValue      string  `json:"max_input_value"`
	MaxInputUnit       string  `json:"max_input_unit"`
	MinInputValue      string  `json:"min_input_value"`
	MinInputUnit       string  `json:"min_input_unit"`
	HitRatioPercent    float64 `json:"hit_ratio_percent"`
	UsedOfMaxPercent   float64 `json:"used_of_max_percent"`
	IsAutoMax          bool    `json:"is_auto_max"`
	IsAutoMin          bool    `json:"is_auto_min"`
	Persisted          bool    `json:"persisted"`
	KernelModuleLoaded bool    `json:"kernel_module_loaded"`
	ModprobePath       string  `json:"modprobe_path"`
}

type arcKstatSnapshot struct {
	loaded bool
	size   uint64
	c      uint64
	cMax   uint64
	cMin   uint64
	hits   uint64
	misses uint64
}

// SetArcPaths overrides filesystem paths used for reading/persisting ZFS ARC configuration (for tests).
func (c *Collector) SetArcPaths(statsPath, paramsDir, modprobePath, memInfoPath string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if statsPath != "" {
		c.arcStatsPath = statsPath
	}
	if paramsDir != "" {
		c.arcParamsDir = paramsDir
	}
	if modprobePath != "" {
		c.arcModprobePath = modprobePath
	}
	if memInfoPath != "" {
		c.memInfoPath = memInfoPath
	}
}

func (c *Collector) ensureArcPathsLocked() {
	if c.arcStatsPath == "" {
		c.arcStatsPath = defaultArcStatsPath
	}
	if c.arcParamsDir == "" {
		c.arcParamsDir = defaultArcParamsDir
	}
	if c.arcModprobePath == "" {
		c.arcModprobePath = defaultArcModprobePath
	}
	if c.memInfoPath == "" {
		c.memInfoPath = defaultMemInfoPath
	}
}

// GetArcConfig returns live ZFS ARC usage and configured limits.
func (c *Collector) GetArcConfig() ZfsArcConfig {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ensureArcPathsLocked()

	totalRAM := mockTotalRamBytes
	if !c.mockMode {
		totalRAM = readTotalRamBytes(c.memInfoPath)
	}
	return c.readArcConfigLocked(totalRAM)
}

// ParseArcSizeInput parses a human size + optional unit ("GiB", "MiB", "TiB", "auto", "0") into bytes.
func ParseArcSizeInput(rawVal, unit string) (uint64, error) {
	s := strings.TrimSpace(rawVal)
	lower := strings.ToLower(s)
	if s == "" || lower == "0" || lower == "auto" || lower == "default" {
		return 0, nil
	}

	s = strings.ReplaceAll(s, ",", ".")
	s = strings.ReplaceAll(s, " ", "")
	upper := strings.ToUpper(s)

	effectiveUnit := strings.ToUpper(strings.TrimSpace(unit))
	for _, suf := range []struct {
		suffix string
		unit   string
	}{
		{"TIB", "TIB"}, {"TB", "TIB"}, {"T", "TIB"},
		{"GIB", "GIB"}, {"GB", "GIB"}, {"G", "GIB"},
		{"MIB", "MIB"}, {"MB", "MIB"}, {"M", "MIB"},
		{"B", "B"},
	} {
		if strings.HasSuffix(upper, suf.suffix) {
			numPart := strings.TrimSpace(s[:len(s)-len(suf.suffix)])
			if numPart == "" {
				return 0, fmt.Errorf("invalid ARC size '%s'", rawVal)
			}
			s = numPart
			effectiveUnit = suf.unit
			break
		}
	}

	val, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(val) || math.IsInf(val, 0) || val < 0 {
		return 0, fmt.Errorf("invalid ARC size '%s'", rawVal)
	}
	if val == 0 {
		return 0, nil
	}

	var mult float64
	switch effectiveUnit {
	case "", "GIB", "GB", "G":
		mult = 1024 * 1024 * 1024
	case "MIB", "MB", "M":
		mult = 1024 * 1024
	case "TIB", "TB", "T":
		mult = 1024 * 1024 * 1024 * 1024
	case "B":
		mult = 1
	default:
		return 0, fmt.Errorf("unsupported size unit '%s'", unit)
	}

	bytesFloat := val * mult
	if bytesFloat >= float64(math.MaxUint64) {
		return 0, fmt.Errorf("ARC size '%s' is too large", rawVal)
	}
	return uint64(math.Round(bytesFloat)), nil
}

// SetArcConfig validates, applies live to the OpenZFS kernel module, and persists zfs_arc_max/zfs_arc_min.
func (c *Collector) SetArcConfig(maxBytes, minBytes uint64) (ZfsArcConfig, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ensureArcPathsLocked()

	totalRAM := mockTotalRamBytes
	if !c.mockMode {
		totalRAM = readTotalRamBytes(c.memInfoPath)
	}

	if err := validateArcLimits(maxBytes, minBytes, totalRAM); err != nil {
		return c.readArcConfigLocked(totalRAM), err
	}

	if c.mockMode {
		c.mockArcMax = maxBytes
		c.mockArcMin = minBytes
		return c.readArcConfigLocked(totalRAM), nil
	}

	if err := applyKernelArcParams(c.arcParamsDir, maxBytes, minBytes); err != nil {
		return c.readArcConfigLocked(totalRAM), err
	}
	if err := upsertModprobeZfsArc(c.arcModprobePath, maxBytes, minBytes); err != nil {
		return c.readArcConfigLocked(totalRAM), fmt.Errorf("applied live kernel setting, but failed to persist %s: %w", c.arcModprobePath, err)
	}

	return c.readArcConfigLocked(totalRAM), nil
}

// ApplyPersistedArcOnStartup applies values persisted in /etc/modprobe.d/zfs.conf if the kernel currently differs.
func (c *Collector) ApplyPersistedArcOnStartup() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ensureArcPathsLocked()
	if c.mockMode || runtime.GOOS != "linux" {
		return nil
	}
	maxVal, minVal, ok := readModprobeZfsArc(c.arcModprobePath)
	if !ok {
		return nil
	}
	totalRAM := readTotalRamBytes(c.memInfoPath)
	if err := validateArcLimits(maxVal, minVal, totalRAM); err != nil {
		return err
	}
	return applyKernelArcParams(c.arcParamsDir, maxVal, minVal)
}

func validateArcLimits(maxBytes, minBytes, totalRAM uint64) error {
	if maxBytes > 0 && maxBytes < minAllowedArcMaxBytes {
		return fmt.Errorf("ZFS ARC maximum must be at least 64 MiB (or 0 for Auto)")
	}
	if maxBytes > 0 && totalRAM > 0 {
		limit90 := (totalRAM * 90) / 100
		if maxBytes > limit90 {
			return fmt.Errorf("ZFS ARC maximum (%s) cannot exceed 90%% of total RAM (%s)", formatBytes(maxBytes), formatBytes(limit90))
		}
	}
	if minBytes > 0 && minBytes < minAllowedArcMinBytes {
		return fmt.Errorf("ZFS ARC minimum must be at least 32 MiB (or 0 for Auto)")
	}
	if minBytes > 0 && totalRAM > 0 {
		limit75 := (totalRAM * 75) / 100
		if minBytes > limit75 {
			return fmt.Errorf("ZFS ARC minimum (%s) cannot exceed 75%% of total RAM (%s)", formatBytes(minBytes), formatBytes(limit75))
		}
	}
	if maxBytes > 0 && minBytes > maxBytes {
		return fmt.Errorf("ZFS ARC minimum (%s) cannot exceed maximum (%s)", formatBytes(minBytes), formatBytes(maxBytes))
	}
	return nil
}

func (c *Collector) readArcConfigLocked(totalRAM uint64) ZfsArcConfig {
	modPath := c.arcModprobePath
	if totalRAM == 0 {
		totalRAM = 16 * 1024 * 1024 * 1024
	}

	var (
		currentBytes uint64
		targetBytes  uint64
		maxBytes     uint64
		minBytes     uint64
		effectiveMax uint64
		effectiveMin uint64
		hitRatio     float64
		loaded       bool
		persisted    bool
	)

	if c.mockMode {
		loaded = true
		maxBytes = c.mockArcMax
		minBytes = c.mockArcMin
		effectiveMax = totalRAM / 2
		if maxBytes > 0 {
			effectiveMax = maxBytes
		}
		effectiveMin = totalRAM / 32
		if minBytes > 0 {
			effectiveMin = minBytes
		}
		currentBytes = mockDefaultArcBytes
		if effectiveMax > 0 && currentBytes > effectiveMax {
			currentBytes = effectiveMax
		}
		if effectiveMin > 0 && currentBytes < effectiveMin {
			currentBytes = effectiveMin
		}
		targetBytes = effectiveMax
		hitRatio = 98.4
		persisted = maxBytes > 0 || minBytes > 0
	} else {
		stats := readArcStatsFile(c.arcStatsPath)
		loaded = stats.loaded
		currentBytes = stats.size
		targetBytes = stats.c
		if stats.hits+stats.misses > 0 {
			hitRatio = float64(stats.hits) / float64(stats.hits+stats.misses) * 100.0
		}

		pMax, hasPMax := readKernelUintParam(filepath.Join(c.arcParamsDir, "zfs_arc_max"))
		pMin, hasPMin := readKernelUintParam(filepath.Join(c.arcParamsDir, "zfs_arc_min"))
		modMax, modMin, hasMod := readModprobeZfsArc(modPath)
		persisted = hasMod

		if hasPMax {
			maxBytes = pMax
		} else if hasMod {
			maxBytes = modMax
		}
		if hasPMin {
			minBytes = pMin
		} else if hasMod {
			minBytes = modMin
		}

		if stats.cMax > 0 {
			effectiveMax = stats.cMax
		} else if maxBytes > 0 {
			effectiveMax = maxBytes
		} else {
			effectiveMax = totalRAM / 2
		}

		if stats.cMin > 0 {
			effectiveMin = stats.cMin
		} else if minBytes > 0 {
			effectiveMin = minBytes
		} else {
			effectiveMin = totalRAM / 32
		}
	}

	usedPct := 0.0
	if effectiveMax > 0 {
		usedPct = float64(currentBytes) / float64(effectiveMax) * 100.0
		if usedPct > 100.0 {
			usedPct = 100.0
		}
	}

	maxInVal, maxInUnit := splitArcBytesForInput(maxBytes)
	minInVal, minInUnit := splitArcBytesForInput(minBytes)

	maxHuman := "Auto (OpenZFS Default)"
	if maxBytes > 0 {
		maxHuman = formatBytes(maxBytes)
	}
	minHuman := "Auto (OpenZFS Default)"
	if minBytes > 0 {
		minHuman = formatBytes(minBytes)
	}

	return ZfsArcConfig{
		CurrentBytes:       currentBytes,
		CurrentHuman:       formatBytes(currentBytes),
		TargetBytes:        targetBytes,
		TargetHuman:        formatBytes(targetBytes),
		MaxBytes:           maxBytes,
		MaxHuman:           maxHuman,
		EffectiveMaxBytes:  effectiveMax,
		EffectiveMaxHuman:  formatBytes(effectiveMax),
		MinBytes:           minBytes,
		MinHuman:           minHuman,
		EffectiveMinBytes:  effectiveMin,
		EffectiveMinHuman:  formatBytes(effectiveMin),
		TotalRamBytes:      totalRAM,
		TotalRamHuman:      formatBytes(totalRAM),
		MaxInputValue:      maxInVal,
		MaxInputUnit:       maxInUnit,
		MinInputValue:      minInVal,
		MinInputUnit:       minInUnit,
		HitRatioPercent:    hitRatio,
		UsedOfMaxPercent:   usedPct,
		IsAutoMax:          maxBytes == 0,
		IsAutoMin:          minBytes == 0,
		Persisted:          persisted,
		KernelModuleLoaded: loaded,
		ModprobePath:       modPath,
	}
}

func splitArcBytesForInput(b uint64) (string, string) {
	if b == 0 {
		return "", "GiB"
	}
	const mib = uint64(1024 * 1024)
	const gib = uint64(1024) * mib
	if b >= gib && b%(256*mib) == 0 {
		val := float64(b) / float64(gib)
		s := strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.2f", val), "0"), ".")
		return s, "GiB"
	}
	val := float64(b) / float64(mib)
	s := strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.2f", val), "0"), ".")
	return s, "MiB"
}

func readArcStatsFile(path string) arcKstatSnapshot {
	var snap arcKstatSnapshot
	f, err := os.Open(path)
	if err != nil {
		return snap
	}
	defer f.Close()

	snap.loaded = true
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 3 {
			continue
		}
		v, err := strconv.ParseUint(fields[2], 10, 64)
		if err != nil {
			continue
		}
		switch fields[0] {
		case "size":
			snap.size = v
		case "c":
			snap.c = v
		case "c_max":
			snap.cMax = v
		case "c_min":
			snap.cMin = v
		case "hits":
			snap.hits = v
		case "misses":
			snap.misses = v
		}
	}
	return snap
}

func readTotalRamBytes(memInfoPath string) uint64 {
	if memInfoPath == "" {
		memInfoPath = defaultMemInfoPath
	}
	f, err := os.Open(memInfoPath)
	if err != nil {
		return 16 * 1024 * 1024 * 1024
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) >= 2 && fields[0] == "MemTotal:" {
			if kb, err := strconv.ParseUint(fields[1], 10, 64); err == nil && kb > 0 {
				return kb * 1024
			}
		}
	}
	return 16 * 1024 * 1024 * 1024
}

func readKernelUintParam(path string) (uint64, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	v, err := strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

func applyKernelArcParams(paramsDir string, maxBytes, minBytes uint64) error {
	maxPath := filepath.Join(paramsDir, "zfs_arc_max")
	minPath := filepath.Join(paramsDir, "zfs_arc_min")

	if _, err := os.Stat(paramsDir); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	curMin, hasCurMin := readKernelUintParam(minPath)
	curMax, hasCurMax := readKernelUintParam(maxPath)

	writeParam := func(path string, val uint64) error {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			return nil
		}
		return os.WriteFile(path, []byte(strconv.FormatUint(val, 10)), 0644)
	}

	// OpenZFS requires zfs_arc_min <= zfs_arc_max at all times when nonzero.
	writeMinFirst := (maxBytes > 0 && hasCurMin && minBytes < curMin) || (maxBytes == 0 && minBytes == 0)
	if writeMinFirst || (hasCurMax && curMax > 0 && minBytes > curMax && maxBytes > minBytes) {
		if writeMinFirst {
			if err := writeParam(minPath, minBytes); err != nil {
				return fmt.Errorf("failed setting %s: %w", minPath, err)
			}
			if err := writeParam(maxPath, maxBytes); err != nil {
				return fmt.Errorf("failed setting %s: %w", maxPath, err)
			}
			return nil
		}
	}

	if err := writeParam(maxPath, maxBytes); err != nil {
		return fmt.Errorf("failed setting %s: %w", maxPath, err)
	}
	if err := writeParam(minPath, minBytes); err != nil {
		return fmt.Errorf("failed setting %s: %w", minPath, err)
	}
	return nil
}

func readModprobeZfsArc(path string) (uint64, uint64, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, 0, false
	}
	var maxVal, minVal uint64
	found := false
	for _, line := range strings.Split(string(b), "\n") {
		trim := strings.TrimSpace(line)
		if trim == "" || strings.HasPrefix(trim, "#") {
			continue
		}
		fields := strings.Fields(trim)
		if len(fields) >= 2 && fields[0] == "options" && fields[1] == "zfs" {
			for _, tok := range fields[2:] {
				if strings.HasPrefix(tok, "zfs_arc_max=") {
					if v, err := strconv.ParseUint(strings.TrimPrefix(tok, "zfs_arc_max="), 10, 64); err == nil {
						maxVal = v
						found = true
					}
				} else if strings.HasPrefix(tok, "zfs_arc_min=") {
					if v, err := strconv.ParseUint(strings.TrimPrefix(tok, "zfs_arc_min="), 10, 64); err == nil {
						minVal = v
						found = true
					}
				}
			}
		}
	}
	return maxVal, minVal, found
}

func upsertModprobeZfsArc(path string, maxBytes, minBytes uint64) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}

	var keptLines []string
	var extraZfsOpts []string
	if existing, err := os.ReadFile(path); err == nil {
		for _, line := range strings.Split(strings.ReplaceAll(string(existing), "\r\n", "\n"), "\n") {
			trim := strings.TrimSpace(line)
			if trim == "" {
				continue
			}
			fields := strings.Fields(trim)
			if len(fields) >= 2 && fields[0] == "options" && fields[1] == "zfs" {
				for _, tok := range fields[2:] {
					if strings.HasPrefix(tok, "zfs_arc_max=") || strings.HasPrefix(tok, "zfs_arc_min=") {
						continue
					}
					extraZfsOpts = append(extraZfsOpts, tok)
				}
				continue
			}
			keptLines = append(keptLines, line)
		}
	}

	var opts []string
	opts = append(opts, extraZfsOpts...)
	if maxBytes > 0 {
		opts = append(opts, fmt.Sprintf("zfs_arc_max=%d", maxBytes))
	}
	if minBytes > 0 {
		opts = append(opts, fmt.Sprintf("zfs_arc_min=%d", minBytes))
	}

	if len(opts) > 0 {
		keptLines = append(keptLines, "options zfs "+strings.Join(opts, " "))
	}

	content := ""
	if len(keptLines) > 0 {
		content = strings.Join(keptLines, "\n") + "\n"
	}
	return os.WriteFile(path, []byte(content), 0644)
}

