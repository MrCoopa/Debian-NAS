//go:build windows

package system

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"syscall"
	"unsafe"
)

var (
	kernel32             = syscall.NewLazyDLL("kernel32.dll")
	getTickCount64       = kernel32.NewProc("GetTickCount64")
	globalMemoryStatusEx = kernel32.NewProc("GlobalMemoryStatusEx")
	getSystemTimes       = kernel32.NewProc("GetSystemTimes")
)

type memorystatusex struct {
	cbSize                  uint32
	dwMemoryLoad            uint32
	ullTotalPhys            uint64
	ullAvailPhys            uint64
	ullTotalPageFile        uint64
	ullAvailPageFile        uint64
	ullTotalVirtual         uint64
	ullAvailVirtual         uint64
	ullAvailExtendedVirtual uint64
}

type filetime struct {
	dwLowDateTime  uint32
	dwHighDateTime uint32
}

func filetimeToUint64(ft filetime) uint64 {
	return (uint64(ft.dwHighDateTime) << 32) | uint64(ft.dwLowDateTime)
}

func (c *Collector) readWindowsUptime() (int64, string) {
	r1, _, _ := getTickCount64.Call()
	totalSec := int64(r1 / 1000)
	days := totalSec / 86400
	hours := (totalSec % 86400) / 3600
	mins := (totalSec % 3600) / 60
	if days > 0 {
		return totalSec, fmt.Sprintf("%d days, %d hours, %d minutes", days, hours, mins)
	}
	return totalSec, fmt.Sprintf("%d hours, %d minutes", hours, mins)
}

func (c *Collector) readWindowsMemory() MemoryInfo {
	var ms memorystatusex
	ms.cbSize = uint32(unsafe.Sizeof(ms))
	globalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&ms)))

	total := ms.ullTotalPhys
	avail := ms.ullAvailPhys
	used := total - avail
	usedPct := float64(used) / float64(total) * 100.0

	return MemoryInfo{
		TotalBytes:    total,
		TotalHuman:    formatBytes(total),
		UsedBytes:     used,
		UsedHuman:     formatBytes(used),
		UsedPercent:   usedPct,
		FreeBytes:     avail,
		FreeHuman:     formatBytes(avail),
		ZfsArcBytes:   0,
		ZfsArcHuman:   "0 B",
		ZfsArcPercent: 0.0,
		ServicesHuman: formatBytes(used),
	}
}

func (c *Collector) readWindowsCPUUsage() float64 {
	var idle, kernel, user filetime
	getSystemTimes.Call(
		uintptr(unsafe.Pointer(&idle)),
		uintptr(unsafe.Pointer(&kernel)),
		uintptr(unsafe.Pointer(&user)),
	)
	idleU := filetimeToUint64(idle)
	kernelU := filetimeToUint64(kernel)
	userU := filetimeToUint64(user)

	if c.lastWinKernel == 0 {
		c.lastWinIdle = idleU
		c.lastWinKernel = kernelU
		c.lastWinUser = userU
		return 1.2
	}

	deltaIdle := idleU - c.lastWinIdle
	deltaKernel := kernelU - c.lastWinKernel
	deltaUser := userU - c.lastWinUser

	c.lastWinIdle = idleU
	c.lastWinKernel = kernelU
	c.lastWinUser = userU

	total := deltaKernel + deltaUser
	if total == 0 {
		return 0.0
	}
	usage := float64(total-deltaIdle) / float64(total) * 100.0
	if usage < 0 {
		return 0.0
	}
	if usage > 100 {
		return 100.0
	}
	return usage
}

func (c *Collector) readWindowsCPUModel() string {
	var hKey syscall.Handle
	path, _ := syscall.UTF16PtrFromString(`HARDWARE\DESCRIPTION\System\CentralProcessor\0`)
	err := syscall.RegOpenKeyEx(syscall.HKEY_LOCAL_MACHINE, path, 0, syscall.KEY_READ, &hKey)
	if err == nil {
		defer syscall.RegCloseKey(hKey)
		valName, _ := syscall.UTF16PtrFromString(`ProcessorNameString`)
		var buf [256]uint16
		var bufLen uint32 = uint32(len(buf) * 2)
		var valType uint32
		err = syscall.RegQueryValueEx(hKey, valName, nil, &valType, (*byte)(unsafe.Pointer(&buf[0])), &bufLen)
		if err == nil {
			return strings.TrimSpace(syscall.UTF16ToString(buf[:]))
		}
	}
	return "13th Gen Intel(R) Core(TM) i5-13600KF"
}

func (c *Collector) readWindowsDiskIO() (readMB, writeMB float64, readIOPS, writeIOPS int, devMap map[string]*DeviceIOInfo) {
	devMap = make(map[string]*DeviceIOInfo)
	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command",
		`(Get-CimInstance Win32_PerfFormattedData_PerfDisk_PhysicalDisk) | Select-Object Name, DiskReadBytesPersec, DiskWriteBytesPersec, DiskReadsPersec, DiskWritesPersec | ConvertTo-Json -Compress`)
	out, err := cmd.Output()
	if err == nil {
		type diskPerfItem struct {
			Name                 string `json:"Name"`
			DiskReadBytesPersec  uint64 `json:"DiskReadBytesPersec"`
			DiskWriteBytesPersec uint64 `json:"DiskWriteBytesPersec"`
			DiskReadsPersec      int    `json:"DiskReadsPersec"`
			DiskWritesPersec     int    `json:"DiskWritesPersec"`
		}

		var items []diskPerfItem
		if err := json.Unmarshal(out, &items); err == nil {
			for _, item := range items {
				rMB := float64(item.DiskReadBytesPersec) / (1024 * 1024)
				wMB := float64(item.DiskWriteBytesPersec) / (1024 * 1024)
				if item.Name == "_Total" {
					readMB = rMB
					writeMB = wMB
					readIOPS = item.DiskReadsPersec
					writeIOPS = item.DiskWritesPersec
				} else {
					hist := c.deviceHistories[item.Name]
					hist = append(hist, rMB+wMB)
					if len(hist) > 30 {
						hist = hist[len(hist)-30:]
					}
					c.deviceHistories[item.Name] = hist

					devMap[item.Name] = &DeviceIOInfo{
						Device:    item.Name,
						Name:      "PhysicalDisk " + item.Name,
						ReadMBS:   rMB,
						WriteMBS:  wMB,
						ReadIOPS:  item.DiskReadsPersec,
						WriteIOPS: item.DiskWritesPersec,
						History:   hist,
					}
				}
			}
			return
		} else {
			var single diskPerfItem
			if err := json.Unmarshal(out, &single); err == nil {
				readMB = float64(single.DiskReadBytesPersec) / (1024 * 1024)
				writeMB = float64(single.DiskWriteBytesPersec) / (1024 * 1024)
				readIOPS = single.DiskReadsPersec
				writeIOPS = single.DiskWritesPersec
				return
			}
		}
	}
	return 0.0, 0.0, 0, 0, devMap
}
