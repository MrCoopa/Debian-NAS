package zfs

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSnapshotTasks_DefaultSeeds(t *testing.T) {
	m := NewManager(true)
	tasks := m.ListSnapshotTasks()
	if len(tasks) < 3 {
		t.Fatalf("expected at least 3 seeded mock snapshot tasks, got %d", len(tasks))
	}

	seen := map[string]bool{}
	for _, task := range tasks {
		seen[task.Dataset] = true
		if task.GetKeepFor() == "" {
			t.Errorf("task %s has empty GetKeepFor", task.ID)
		}
		if task.GetFrequency() == "" {
			t.Errorf("task %s has empty GetFrequency", task.ID)
		}
		if task.GetStateLabel() != "Completed" {
			t.Errorf("expected seeded task %s state Completed, got %s", task.ID, task.GetStateLabel())
		}
	}

	for _, ds := range []string{"DATA", "MEDIA-12TB", "SSD-MIRROR/mail-archiver"} {
		if !seen[ds] {
			t.Errorf("expected seeded task for dataset %s", ds)
		}
	}
}

func TestSnapshotTasks_CRUDValidationAndPersistence(t *testing.T) {
	m := NewManager(true)

	// Invalid dataset name should fail
	if _, err := m.SaveSnapshotTask(SnapshotTask{Dataset: "../etc", LifetimeValue: 2, LifetimeUnit: "week(s)"}); err == nil {
		t.Fatal("expected error for invalid dataset name")
	}
	// Invalid lifetime unit should fail
	if _, err := m.SaveSnapshotTask(SnapshotTask{Dataset: "DATA", LifetimeValue: 2, LifetimeUnit: "centuries"}); err == nil {
		t.Fatal("expected error for invalid lifetime unit")
	}
	// Invalid cron schedule should fail
	if _, err := m.SaveSnapshotTask(SnapshotTask{Dataset: "DATA", LifetimeValue: 2, LifetimeUnit: "week(s)", Schedule: "* *"}); err == nil {
		t.Fatal("expected error for invalid cron schedule")
	}

	// Valid save
	saved, err := m.SaveSnapshotTask(SnapshotTask{
		Dataset:       "DATA/Backup",
		Recursive:     true,
		LifetimeValue: 7,
		LifetimeUnit:  "day(s)",
		NamingSchema:  "auto-%Y-%m-%d_%H-%M",
		Schedule:      "0 0 * * *",
		Enabled:       true,
	})
	if err != nil {
		t.Fatalf("SaveSnapshotTask failed: %v", err)
	}
	if saved.ID == "" {
		t.Fatal("expected generated ID")
	}
	if saved.GetKeepFor() != "7 day(s)" {
		t.Fatalf("expected keep_for '7 day(s)', got %q", saved.GetKeepFor())
	}

	// Toggle
	toggled, err := m.ToggleSnapshotTask(saved.ID, nil)
	if err != nil {
		t.Fatalf("ToggleSnapshotTask failed: %v", err)
	}
	if toggled.Enabled {
		t.Fatal("expected task to toggle to disabled")
	}
	if toggled.GetNextRun() != "Disabled" {
		t.Fatalf("expected Disabled next run when off, got %q", toggled.GetNextRun())
	}

	// Test persistence serialization/deserialization on temp path
	m.mu.Lock()
	m.mockMode = false
	m.snapTaskConfigPath = filepath.Join(t.TempDir(), "snapshot_tasks.json")
	m.saveSnapshotTasksConfigLocked()
	delete(m.snapTasks, saved.ID)
	loaded := m.loadSnapshotTasksConfigLocked()
	_, found := m.snapTasks[saved.ID]
	m.mockMode = true
	m.mu.Unlock()

	if !loaded || !found {
		t.Fatal("expected snapshot task to persist and reload from JSON file")
	}

	// Delete
	if err := m.DeleteSnapshotTask(saved.ID); err != nil {
		t.Fatalf("DeleteSnapshotTask failed: %v", err)
	}
	for _, item := range m.ListSnapshotTasks() {
		if item.ID == saved.ID {
			t.Fatal("deleted task still present in ListSnapshotTasks")
		}
	}
}

func TestSnapshotTasks_RunAndPruneRetention(t *testing.T) {
	m := NewManager(true)

	// Inject old expired auto snapshot, recent auto snapshot, and old manual snapshot
	now := time.Now()
	m.mu.Lock()
	m.mockSnaps = append(m.mockSnaps,
		&Snapshot{
			Name:    "DATA@auto-2020-01-01_00-00",
			Dataset: "DATA",
			Created: now.Add(-30 * 24 * time.Hour),
		},
		&Snapshot{
			Name:    "DATA@auto-recent-keep",
			Dataset: "DATA",
			Created: now.Add(-2 * time.Hour),
		},
		&Snapshot{
			Name:    "DATA@manual-keep",
			Dataset: "DATA",
			Created: now.Add(-45 * 24 * time.Hour),
		},
	)
	m.mu.Unlock()

	task, err := m.SaveSnapshotTask(SnapshotTask{
		Dataset:       "DATA",
		Recursive:     true,
		LifetimeValue: 2,
		LifetimeUnit:  "week(s)",
		NamingSchema:  "auto-%Y-%m-%d_%H-%M",
		Schedule:      "0 0 * * *",
		Enabled:       true,
	})
	if err != nil {
		t.Fatalf("SaveSnapshotTask failed: %v", err)
	}

	ranTask, snapName, err := m.RunSnapshotTask(task.ID)
	if err != nil {
		t.Fatalf("RunSnapshotTask failed: %v", err)
	}
	if !strings.HasPrefix(snapName, "auto-") {
		t.Fatalf("expected created snapshot name to start with 'auto-', got %q", snapName)
	}
	if ranTask.GetStateLabel() != "Completed" {
		t.Fatalf("expected Completed state, got %q", ranTask.GetStateLabel())
	}

	snaps, err := m.ListSnapshots()
	if err != nil {
		t.Fatalf("ListSnapshots failed: %v", err)
	}

	names := map[string]bool{}
	for _, s := range snaps {
		names[s.Name] = true
	}

	if !names["DATA@"+snapName] {
		t.Errorf("expected newly created snapshot DATA@%s to exist", snapName)
	}
	if names["DATA@auto-2020-01-01_00-00"] {
		t.Errorf("expected expired auto snapshot DATA@auto-2020-01-01_00-00 to be pruned")
	}
	if !names["DATA@auto-recent-keep"] {
		t.Errorf("expected recent auto snapshot DATA@auto-recent-keep to be kept")
	}
	if !names["DATA@manual-keep"] {
		t.Errorf("expected non-matching manual snapshot DATA@manual-keep to remain untouched")
	}
}

func TestSnapshotTasks_NoMockSeedInRealMode(t *testing.T) {
	m := &Manager{
		mockMode:           false,
		snapTasks:          make(map[string]*SnapshotTask),
		snapTaskConfigPath: filepath.Join(t.TempDir(), "nonexistent_snapshot_tasks.json"),
	}
	m.mu.Lock()
	m.initSnapshotTasksLocked()
	m.mu.Unlock()

	if len(m.snapTasks) != 0 {
		t.Fatalf("expected 0 mock snapshot tasks in non-mock mode when config is missing, got %d", len(m.snapTasks))
	}
}

func TestSnapshotTasks_CronMatchingAndNamingSchema(t *testing.T) {
	tm := time.Date(2026, time.November, 15, 14, 30, 0, 0, time.UTC) // Sunday (weekday=0)

	if got := expandNamingSchema("auto-%Y-%m-%d_%H-%M-%S", tm); got != "auto-2026-11-15_14-30-00" {
		t.Errorf("expandNamingSchema full = %q", got)
	}
	if got := namingSchemaPrefix("daily-%Y-%m-%d"); got != "daily-" {
		t.Errorf("namingSchemaPrefix = %q, want daily-", got)
	}

	cases := []struct {
		expr string
		tm   time.Time
		want bool
	}{
		{"30 14 * * *", tm, true},
		{"*/15 10-16 * * 0", tm, true},
		{"*/15 10-16 * * 7", tm, true}, // Sunday via 7
		{"0,30 14 15 11 *", tm, true},
		{"45 14 * * *", tm, false},
		{"30 14 * * 1-5", tm, false},
	}
	for _, tc := range cases {
		if got := matchesCronSchedule(tc.expr, tc.tm); got != tc.want {
			t.Errorf("matchesCronSchedule(%q) = %v, want %v", tc.expr, got, tc.want)
		}
	}

	next := computeNextCronTime("0 15 * * *", tm)
	if next.Hour() != 15 || next.Minute() != 0 {
		t.Errorf("computeNextCronTime = %v, want 15:00", next)
	}

	if u, err := normalizeLifetimeUnit("weeks"); err != nil || u != "week(s)" {
		t.Errorf("normalizeLifetimeUnit(weeks) = %q, %v", u, err)
	}
}

func TestSnapshotScheduler_EveryMinuteCronDoesNotSkipAdjacentMinutes(t *testing.T) {
	m := NewManager(true)

	task, err := m.SaveSnapshotTask(SnapshotTask{
		Dataset:       "DATA",
		Recursive:     false,
		LifetimeValue: 1,
		LifetimeUnit:  "day(s)",
		NamingSchema:  "every-%Y-%m-%d_%H-%M-%S",
		Schedule:      "* * * * *",
		Enabled:       true,
	})
	if err != nil {
		t.Fatalf("SaveSnapshotTask failed: %v", err)
	}

	// Simulate two scheduler ticks separated by only 4 seconds across a minute boundary (12:00:58 -> 12:01:02).
	t1 := time.Date(2026, time.November, 12, 12, 0, 58, 0, time.UTC)
	if ran := m.runDueSnapshotTasks(t1); ran == 0 {
		t.Fatalf("expected task to execute on initial minute tick %v", t1)
	}

	// Simulate LastRun occurring at the end of minute 12:00.
	m.mu.Lock()
	m.snapTasks[task.ID].LastRun = t1
	m.mu.Unlock()

	// Same minute (12:00:59) should NOT double trigger.
	tSameMin := time.Date(2026, time.November, 12, 12, 0, 59, 0, time.UTC)
	if !sameCronMinute(t1, tSameMin) {
		t.Fatalf("expected %v and %v to be in same cron minute", t1, tSameMin)
	}

	// Adjacent minute (12:01:02 — only 4 seconds later) MUST NOT be skipped!
	tNextMin := time.Date(2026, time.November, 12, 12, 1, 2, 0, time.UTC)
	if sameCronMinute(t1, tNextMin) {
		t.Fatalf("expected %v and %v to be in different cron minutes", t1, tNextMin)
	}
	if ran := m.runDueSnapshotTasks(tNextMin); ran == 0 {
		t.Fatalf("expected * * * * * task to execute on adjacent minute %v even <55s after %v", tNextMin, t1)
	}
}


