package web

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"debian-nas/internal/zfs"
)

func TestRender_PeriodicSnapshotTasksPartialAndModal(t *testing.T) {
	eng, err := NewEngine()
	if err != nil {
		t.Fatalf("NewEngine failed: %v", err)
	}

	tasks := []*zfs.SnapshotTask{
		{
			ID:            "t1",
			Dataset:       "DATA",
			Recursive:     true,
			LifetimeValue: 2,
			LifetimeUnit:  "week(s)",
			NamingSchema:  "auto-%Y-%m-%d_%H-%M",
			Schedule:      "0 0 * * *",
			FrequencyText: "At 00:00 (12:00 AM), every day,...",
			Enabled:       true,
			State:         "COMPLETED",
			LastRun:       time.Now().Add(-17 * time.Hour),
		},
		{
			ID:            "t2",
			Dataset:       "SSD-MIRROR/mail-archiver",
			Recursive:     true,
			LifetimeValue: 2,
			LifetimeUnit:  "week(s)",
			NamingSchema:  "auto-%Y-%m-%d_%H-%M",
			Schedule:      "0 0 * * *",
			FrequencyText: "At 00:00 (12:00 AM), every day,...",
			Enabled:       true,
			State:         "COMPLETED",
			LastRun:       time.Now().Add(-17 * time.Hour),
		},
	}

	data := map[string]interface{}{
		"Pools": []*zfs.Pool{
			{Name: "DATA"},
			{Name: "SSD-MIRROR"},
		},
		"Datasets": []*zfs.Dataset{
			{Name: "DATA", Type: "filesystem"},
			{Name: "SSD-MIRROR/mail-archiver", Type: "filesystem"},
		},
		"SnapshotTasks": tasks,
	}

	var partialBuf bytes.Buffer
	if err := eng.Render(&partialBuf, "partials/snapshot_tasks.html", data); err != nil {
		t.Fatalf("Render partials/snapshot_tasks.html failed: %v", err)
	}
	partialHTML := partialBuf.String()
	for _, want := range []string{
		"Periodic Snapshot Tasks",
		"Pool/Dataset",
		"Keep for",
		"DATA",
		"SSD-MIRROR/mail-archiver",
		"2 week(s)",
		"Completed",
		"VMware Snapshot Integration",
		"Snapshots",
		"/ui/actions/zfs/snapshot-tasks/toggle",
		"/ui/actions/zfs/snapshot-tasks/run",
		"/ui/actions/zfs/snapshot-tasks/delete",
	} {
		if !strings.Contains(partialHTML, want) {
			t.Errorf("partials/snapshot_tasks.html missing expected substring %q", want)
		}
	}

	var modalBuf bytes.Buffer
	if err := eng.Render(&modalBuf, "modals/create_snapshot_task.html", data); err != nil {
		t.Fatalf("Render modals/create_snapshot_task.html failed: %v", err)
	}
	modalHTML := modalBuf.String()
	for _, want := range []string{
		"/ui/actions/zfs/snapshot-tasks/save",
		"SSD-MIRROR/mail-archiver",
		"naming_schema",
		"lifetime_unit",
	} {
		if !strings.Contains(modalHTML, want) {
			t.Errorf("modals/create_snapshot_task.html missing expected substring %q", want)
		}
	}
}
