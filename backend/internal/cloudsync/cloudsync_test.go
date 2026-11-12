package cloudsync

import (
	"testing"
	"time"
)

func TestCloudSync_JobFormattingCronAndSavePreservation(t *testing.T) {
	m := NewManager(true, "")

	newJob := &SyncJob{
		Name:         "nightly-docs",
		Description:  "Nightly Docs",
		SourcePath:   "/mnt/tank/docs",
		RemoteTarget: "b2_backup:docs",
		Direction:    DirectionPush,
		Schedule:     "0 2 * * *",
		Status:       StatusIdle,
		Enabled:      false,
	}
	if err := m.SaveJob(newJob); err != nil {
		t.Fatalf("SaveJob failed: %v", err)
	}
	if newJob.GetLastRun() != "Never" {
		t.Fatalf("expected brand-new job LastRun to be 'Never', got %q", newJob.GetLastRun())
	}
	if newJob.GetNextRun() != "Disabled" {
		t.Fatalf("expected disabled job NextRun to be 'Disabled', got %q", newJob.GetNextRun())
	}
	if newJob.GetStateClass() != "idle" || newJob.GetStateLabel() != "Idle" {
		t.Fatalf("expected StatusIdle to render idle/Idle, got %s/%s", newJob.GetStateClass(), newJob.GetStateLabel())
	}

	// Simulate completed run, then save an edit without passing LastRun/Status -> preserved
	runAt := time.Now().Add(-2 * time.Hour)
	newJob.LastRun = runAt
	newJob.Status = StatusSuccess
	newJob.LastMessage = "Completed: 12 GB"
	newJob.Enabled = true
	if err := m.SaveJob(&SyncJob{
		ID:           newJob.ID,
		Name:         "nightly-docs",
		Description:  "Nightly Docs Updated",
		SourcePath:   "/mnt/tank/docs",
		RemoteTarget: "b2_backup:docs",
		Direction:    DirectionPush,
		Schedule:     "*/15 * * * *",
		Status:       StatusIdle,
		Enabled:      true,
	}); err != nil {
		t.Fatalf("SaveJob edit failed: %v", err)
	}
	jobs, _ := m.ListJobs()
	var edited *SyncJob
	for _, j := range jobs {
		if j.ID == newJob.ID {
			edited = j
			break
		}
	}
	if edited == nil {
		t.Fatalf("edited job %s missing", newJob.ID)
	}
	if edited.LastRun.IsZero() || edited.Status != StatusSuccess {
		t.Fatalf("expected edit to preserve LastRun and StatusSuccess, got LastRun=%v Status=%s", edited.LastRun, edited.Status)
	}
	if edited.GetStateClass() != "completed" || edited.GetNextRun() == "Disabled" {
		t.Fatalf("expected completed state and active next run, got class=%q next=%q", edited.GetStateClass(), edited.GetNextRun())
	}

	// Verify interval/hourly schedule matcher works for */15 and 0 * * * *
	tm15 := time.Date(2026, time.November, 12, 10, 30, 0, 0, time.UTC)
	if !matchSchedule("*/15 * * * *", tm15) {
		t.Fatalf("expected */15 * * * * to match :30")
	}
	if matchSchedule("0 * * * *", tm15) {
		t.Fatalf("did not expect 0 * * * * to match :30")
	}
	tmHour := time.Date(2026, time.November, 12, 10, 0, 0, 0, time.UTC)
	if !matchSchedule("0 * * * *", tmHour) {
		t.Fatalf("expected 0 * * * * to match :00")
	}

	// Reject malformed cron expressions
	if err := m.SaveJob(&SyncJob{Name: "bad", Schedule: "bad cron"}); err == nil {
		t.Fatalf("expected invalid schedule to fail SaveJob")
	}
}

