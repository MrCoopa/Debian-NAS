package cloudsync

import (
	"strings"
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

func TestCloudSync_RemoteManagementAndTransferModes(t *testing.T) {
	m := NewManager(true, "")

	// 1. Create SFTP Hetzner remote
	err := m.CreateRemote(CreateRemoteRequest{
		Name: "my_hetzner",
		Type: "sftp",
		Host: "u123456.your-storagebox.de",
		User: "u123456",
		Port: 23,
		Pass: "secret",
	})
	if err != nil {
		t.Fatalf("CreateRemote SFTP failed: %v", err)
	}

	// 2. Create Crypt remote wrapping hetzner
	err = m.CreateRemote(CreateRemoteRequest{
		Name:     "my_hetzner_crypt",
		Type:     "crypt",
		Remote:   "my_hetzner:Backup",
		Password: "enc_password",
		Salt:     "enc_salt",
	})
	if err != nil {
		t.Fatalf("CreateRemote Crypt failed: %v", err)
	}

	remotes, err := m.ListRemotes()
	if err != nil {
		t.Fatalf("ListRemotes failed: %v", err)
	}
	foundSftp := false
	foundCrypt := false
	for _, r := range remotes {
		if r.Name == "my_hetzner" && r.Type == "sftp" {
			foundSftp = true
		}
		if r.Name == "my_hetzner_crypt" && r.Type == "crypt" {
			foundCrypt = true
		}
	}
	if !foundSftp || !foundCrypt {
		t.Fatalf("expected both remotes to be listed, got: %+v", remotes)
	}

	// Test remote connection
	if err := m.TestRemote("my_hetzner"); err != nil {
		t.Fatalf("TestRemote failed: %v", err)
	}
	if err := m.TestRemote("error_remote"); err == nil {
		t.Fatalf("expected error_remote to fail TestRemote")
	}

	// 3. Reject invalid remote name
	if err := m.CreateRemote(CreateRemoteRequest{Name: "bad:name", Type: "sftp"}); err == nil {
		t.Fatalf("expected error for invalid remote name containing colon")
	}

	// 4. Test Transfer Modes and Dry Run on Job
	job := &SyncJob{
		Name:             "test-job",
		SourcePath:       "/mnt/tank/data",
		RemoteCredential: "my_hetzner_crypt",
		RemotePath:       "data",
		Direction:        DirectionPush,
		TransferMode:     ModeCopy,
		Schedule:         "0 2 * * *",
		Enabled:          true,
	}
	if err := m.SaveJob(job); err != nil {
		t.Fatalf("SaveJob failed: %v", err)
	}
	if job.GetTarget() != "my_hetzner_crypt:data" {
		t.Fatalf("expected target 'my_hetzner_crypt:data', got %q", job.GetTarget())
	}
	if job.GetTransferMode() != "COPY" {
		t.Fatalf("expected mode 'COPY', got %q", job.GetTransferMode())
	}

	// Dry run trigger
	if err := m.TriggerJobWithOptions(job.ID, true); err != nil {
		t.Fatalf("TriggerJobWithOptions dry-run failed: %v", err)
	}
	time.Sleep(1100 * time.Millisecond)

	jobs, _ := m.ListJobs()
	for _, j := range jobs {
		if j.ID == job.ID {
			if !strings.Contains(j.LastMessage, "Dry run") {
				t.Fatalf("expected LastMessage to mention Dry run, got %q", j.LastMessage)
			}
			break
		}
	}

	// 5. Delete remote
	if err := m.DeleteRemote("my_hetzner"); err != nil {
		t.Fatalf("DeleteRemote failed: %v", err)
	}
	remotesAfter, _ := m.ListRemotes()
	for _, r := range remotesAfter {
		if r.Name == "my_hetzner" {
			t.Fatalf("expected my_hetzner to be deleted")
		}
	}
}

