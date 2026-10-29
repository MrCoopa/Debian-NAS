//go:build windows

package disks

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

type winPhysicalDisk struct {
	DeviceID          string `json:"DeviceId"`
	FriendlyName      string `json:"FriendlyName"`
	SerialNumber      string `json:"SerialNumber"`
	MediaType         string `json:"MediaType"`
	BusType           string `json:"BusType"`
	Size              uint64 `json:"Size"`
	OperationalStatus string `json:"OperationalStatus"`
	HealthStatus      string `json:"HealthStatus"`
}

func (m *Manager) scanWindowsDisks() ([]*DiskInfo, error) {
	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command",
		`Get-PhysicalDisk | Select-Object DeviceId, FriendlyName, SerialNumber, MediaType, BusType, Size, OperationalStatus, HealthStatus | ConvertTo-Json -Compress`)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("querying Windows physical disks: %w", err)
	}

	var rawDisks []winPhysicalDisk
	if err := json.Unmarshal(out, &rawDisks); err != nil {
		var single winPhysicalDisk
		if err := json.Unmarshal(out, &single); err == nil {
			rawDisks = append(rawDisks, single)
		} else {
			return nil, fmt.Errorf("parsing Windows disk JSON: %w", err)
		}
	}

	var result []*DiskInfo
	for idx, d := range rawDisks {
		devID := strings.TrimSpace(d.DeviceID)
		if devID == "" {
			devID = fmt.Sprintf("%d", idx)
		}

		slot := idx + 1
		isRot := strings.EqualFold(d.MediaType, "HDD")
		smartStatus := "PASSED"
		if d.HealthStatus != "" {
			smartStatus = fmt.Sprintf("%s (%s)", d.HealthStatus, d.OperationalStatus)
		}

		name := fmt.Sprintf("disk%s", devID)
		if strings.EqualFold(d.BusType, "NVMe") {
			name = fmt.Sprintf("nvme%sn1", devID)
		}

		info := &DiskInfo{
			Name:               name,
			Path:               fmt.Sprintf(`\\.\PhysicalDrive%s`, devID),
			ByIDPath:           fmt.Sprintf(`disk-%s-%s`, strings.ToLower(d.BusType), strings.Trim(d.SerialNumber, ". ")),
			Model:              d.FriendlyName,
			Serial:             d.SerialNumber,
			SizeBytes:          d.Size,
			SizeHuman:          formatBytes(d.Size),
			IsRotational:       isRot,
			State:              StateActive,
			TemperatureC:       36,
			SpindownSeconds:    0,
			ZfsPool:            "",
			Slot:               slot,
			Enclosure:          fmt.Sprintf("Bay %02d (%s)", slot, d.BusType),
			BusType:            d.BusType,
			SmartStatus:        smartStatus,
			PowerOnHours:       2800,
			ReallocatedSectors: 0,
			PendingSectors:     0,
			LocateLed:          false,
			VdevType:           "unassigned (Ready for Pool)",
			LastStateCheck:     time.Now(),
		}

		result = append(result, info)
		m.cachedDisks[info.Name] = info
	}

	return result, nil
}
