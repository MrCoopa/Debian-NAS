package api

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"debian-nas/internal/cloudsync"
	"debian-nas/internal/disks"
	"debian-nas/internal/system"
	"debian-nas/internal/web"
	"debian-nas/internal/zfs"
)

func TestNotificationsPanel_RendersWithRealViewData(t *testing.T) {
	eng, err := web.NewEngine()
	if err != nil {
		t.Fatalf("failed to create web engine: %v", err)
	}

	srv := &Server{}
	srv.recordEvent("Scrub started on tank", "success", "poolUpdate")
	srv.recordEvent("SMART check failed on sdb", "error", "diskUpdate")

	vd := &ViewData{
		CurrentUser: "root",
		System: &system.SystemStatus{
			Hostname: "debian-nas",
			Uptime:   "5d 12h",
		},
		Pools: []*zfs.Pool{
			{
				Name:       "tank",
				Health:     "DEGRADED",
				CapPercent: 90,
				FreeHuman:  "100 GiB",
				Topology:   "RAIDZ1",
				Scrub: zfs.ScrubStatus{
					Active:     true,
					Percentage: 42.5,
				},
			},
		},
		Disks: []*disks.DiskInfo{
			{
				Name:          "sda",
				Path:          "/dev/sda",
				State:         disks.StateStandby,
				TemperatureC:  34,
				SmartStatus:   "PASSED",
				SmartLastTest: "# 1 Short offline Completed without error",
			},
			{
				Name:         "sdb",
				Path:         "/dev/sdb",
				State:        disks.StateActive,
				TemperatureC: 55,
				SmartStatus:  "FAILED",
			},
		},
		SnapshotTasks: []*zfs.SnapshotTask{
			{
				ID:          "snap-1",
				Dataset:     "tank/data",
				Schedule:    "0 * * * *",
				Enabled:     true,
				State:       "FINISHED",
				LastRun:     time.Now(),
				LastRunText: "2026-11-14 12:00",
			},
		},
		CloudJobs: []*cloudsync.SyncJob{
			{
				ID:           "cloud-1",
				Name:         "Offsite Backup",
				SourcePath:   "/mnt/tank/data",
				RemoteTarget: "hetzner:backup",
				Status:       cloudsync.StatusRunning,
				LastRun:      time.Now(),
			},
			{
				ID:           "cloud-2",
				Name:         "Archive Sync",
				SourcePath:   "/mnt/tank/archive",
				RemoteTarget: "hetzner:archive",
				Status:       cloudsync.StatusFailed,
				LastMessage:  "connection reset",
				LastRun:      time.Now(),
			},
		},
		TotalDisks:   2,
		StandbyCount: 1,
	}

	srv.populateNotificationsAndJobs(vd)

	if len(vd.RunningJobs) == 0 {
		t.Fatalf("expected RunningJobs to be populated")
	}
	if len(vd.CompletedJobs) == 0 {
		t.Fatalf("expected CompletedJobs to be populated")
	}
	if len(vd.SystemAlerts) == 0 {
		t.Fatalf("expected SystemAlerts to be populated")
	}
	if len(vd.RecentEvents) == 0 {
		t.Fatalf("expected RecentEvents to be populated")
	}

	var buf bytes.Buffer
	if err := eng.Render(&buf, "layout.html", vd); err != nil {
		t.Fatalf("layout.html failed to render with populated ViewData: %v", err)
	}
	html := buf.String()
	if !strings.Contains(html, "</html>") {
		t.Fatalf("layout.html was truncated and did not reach </html>")
	}
	if !strings.Contains(html, "Pool Scrub: tank") {
		t.Errorf("expected rendered layout.html to contain running scrub job")
	}
	if !strings.Contains(html, "Offsite Backup") {
		t.Errorf("expected rendered layout.html to contain running cloud sync job")
	}
	if !strings.Contains(html, "Archive Sync") {
		t.Errorf("expected rendered layout.html to contain failed cloud sync job")
	}
}
