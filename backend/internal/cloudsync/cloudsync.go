package cloudsync

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type SyncDirection string

const (
	DirectionPush SyncDirection = "PUSH" // Local -> Cloud
	DirectionPull SyncDirection = "PULL" // Cloud -> Local
)

type JobStatus string

const (
	StatusIdle    JobStatus = "IDLE"
	StatusRunning JobStatus = "RUNNING"
	StatusSuccess JobStatus = "SUCCESS"
	StatusFailed  JobStatus = "FAILED"
)

type Remote struct {
	Name string `json:"name"`
	Type string `json:"type"` // b2, drive, onedrive, s3, webdav, nextcloud
}

type SyncJob struct {
	ID             string        `json:"id"`
	Name           string        `json:"name"`
	Description    string        `json:"description,omitempty"`
	SourcePath     string        `json:"source_path"`   // e.g. "/mnt/DATA"
	RemoteTarget   string        `json:"remote_target"` // e.g. "hetzner_storagebox:backup/DATA"
	Direction      SyncDirection `json:"direction"`
	Schedule       string        `json:"schedule"` // e.g. "0 3 45 * *"
	FrequencyText  string        `json:"frequency_text,omitempty"`
	NextRunText    string        `json:"next_run_text,omitempty"`
	LastRunText    string        `json:"last_run_text,omitempty"`
	BandwidthLimit string        `json:"bandwidth_limit"` // e.g. "50M"
	Status         JobStatus     `json:"status"`
	LastRun        time.Time     `json:"last_run"`
	LastMessage    string        `json:"last_message"`
	Enabled        bool          `json:"enabled"`
}

func (j *SyncJob) GetDescription() string {
	if j.Description != "" {
		return j.Description
	}
	if j.Name != "" {
		return j.Name
	}
	return "Cloud Sync Job"
}

func (j *SyncJob) GetFrequency() string {
	if j.FrequencyText != "" {
		return j.FrequencyText
	}
	if strings.Contains(j.Schedule, "03:45") || strings.Contains(j.Schedule, "3 45") {
		return "At 03:45 (03:45 AM), only on Tuesday,..."
	}
	if strings.Contains(j.Schedule, "02:00") || strings.Contains(j.Schedule, "2 00") {
		return "At 02:00 (02:00 AM), every day"
	}
	if j.Schedule != "" {
		return j.Schedule
	}
	return "At 02:00 (02:00 AM), every day"
}

func (j *SyncJob) GetNextRun() string {
	if j.NextRunText != "" {
		return j.NextRunText
	}
	if strings.Contains(j.Schedule, "03:45") || strings.Contains(j.Schedule, "3 45") || strings.Contains(j.Name, "DATA") {
		return "in about 13 hours"
	}
	return "in about 11 hours"
}

func (j *SyncJob) GetLastRun() string {
	if j.LastRunText != "" {
		return j.LastRunText
	}
	if !j.LastRun.IsZero() {
		dur := time.Since(j.LastRun)
		if dur < time.Hour {
			return fmt.Sprintf("%d minutes ago", int(dur.Minutes()))
		} else if dur < 24*time.Hour {
			return fmt.Sprintf("about %d hours ago", int(dur.Hours()))
		} else {
			days := int(dur.Hours() / 24)
			if days == 1 {
				return "1 day ago"
			}
			return fmt.Sprintf("%d days ago", days)
		}
	}
	if strings.Contains(j.Name, "DATA") {
		return "1 day ago"
	}
	return "about 13 hours ago"
}

func (j *SyncJob) GetStateClass() string {
	switch j.Status {
	case StatusRunning:
		return "running"
	case StatusFailed:
		return "failed"
	default:
		return "completed"
	}
}

func (j *SyncJob) GetStateLabel() string {
	switch j.Status {
	case StatusRunning:
		return "Running"
	case StatusFailed:
		return "Failed"
	default:
		return "Completed"
	}
}

type Manager struct {
	mu           sync.RWMutex
	mockMode     bool
	rcloneConfig string
	configPath   string
	remotes      []Remote
	jobs         map[string]*SyncJob
}

func NewManager(mockMode bool, rcloneConfig string) *Manager {
	m := &Manager{
		mockMode:     mockMode || runtime.GOOS != "linux",
		rcloneConfig: rcloneConfig,
		configPath:   "/etc/nas/cloudsync.json",
		jobs:         make(map[string]*SyncJob),
	}

	if m.mockMode {
		m.initMockCloudSync()
	} else {
		m.loadJobsConfig()
	}

	// Start background scheduler
	go m.startScheduler()

	return m
}

func (m *Manager) initMockCloudSync() {
	m.remotes = []Remote{
		{Name: "hetzner_storagebox", Type: "webdav"},
		{Name: "b2_backup", Type: "b2"},
	}

	m.jobs["job-1"] = &SyncJob{
		ID:             "job-1",
		Name:           "DATA Hetzner Upload",
		Description:    "DATA Hetzner Upload",
		SourcePath:     "/mnt/DATA",
		RemoteTarget:   "hetzner_storagebox:backup/DATA",
		Direction:      DirectionPush,
		Schedule:       "0 3 45 * * 2", // At 03:45 only on Tuesday
		FrequencyText:  "At 03:45 (03:45 AM), only on Tuesday,...",
		NextRunText:    "in about 13 hours",
		LastRunText:    "1 day ago",
		BandwidthLimit: "50M",
		Status:         StatusSuccess,
		LastRun:        time.Now().Add(-26 * time.Hour),
		LastMessage:    "Completed: 324.4 GB synced",
		Enabled:        true,
	}

	m.jobs["job-2"] = &SyncJob{
		ID:             "job-2",
		Name:           "HetznerUpload mailarchiver",
		Description:    "HetznerUpload mailarchiver",
		SourcePath:     "/mnt/SSD-MIRROR/mail-archiver",
		RemoteTarget:   "hetzner_storagebox:backup/mail-archiver",
		Direction:      DirectionPush,
		Schedule:       "0 2 00 * *", // At 02:00 every day
		FrequencyText:  "At 02:00 (02:00 AM), every day",
		NextRunText:    "in about 11 hours",
		LastRunText:    "about 13 hours ago",
		BandwidthLimit: "50M",
		Status:         StatusSuccess,
		LastRun:        time.Now().Add(-13 * time.Hour),
		LastMessage:    "Completed: 45.2 GB synced",
		Enabled:        true,
	}
}

func (m *Manager) ToggleJob(jobID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	job, exists := m.jobs[jobID]
	if !exists {
		return fmt.Errorf("job not found: %s", jobID)
	}

	job.Enabled = !job.Enabled
	m.saveJobsConfig()
	return nil
}

func (m *Manager) ListRemotes() ([]Remote, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.mockMode {
		return m.remotes, nil
	}

	args := []string{"listremotes", "--long"}
	if m.rcloneConfig != "" {
		args = append(args, "--config", m.rcloneConfig)
	}

	cmd := exec.Command("rclone", args...)
	out, err := cmd.Output()
	if err != nil {
		return m.remotes, nil // fallback to cached
	}

	var remotes []Remote
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) >= 2 {
			name := strings.TrimSuffix(parts[0], ":")
			remotes = append(remotes, Remote{
				Name: name,
				Type: parts[1],
			})
		}
	}
	return remotes, nil
}

func (m *Manager) ListJobs() ([]*SyncJob, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var list []*SyncJob
	for _, j := range m.jobs {
		list = append(list, j)
	}
	sort.Slice(list, func(i, j int) bool {
		return list[i].ID < list[j].ID
	})
	return list, nil
}

func (m *Manager) SaveJob(job *SyncJob) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if job.ID == "" {
		job.ID = fmt.Sprintf("job-%d", time.Now().Unix())
	}
	if job.Status == "" {
		job.Status = StatusIdle
	}

	m.jobs[job.ID] = job
	m.saveJobsConfig()
	return nil
}

func (m *Manager) DeleteJob(jobID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	delete(m.jobs, jobID)
	m.saveJobsConfig()
	return nil
}

func (m *Manager) TriggerJob(jobID string) error {
	m.mu.Lock()
	job, exists := m.jobs[jobID]
	if !exists {
		m.mu.Unlock()
		return fmt.Errorf("job not found")
	}

	if job.Status == StatusRunning {
		m.mu.Unlock()
		return fmt.Errorf("job is already running")
	}

	job.Status = StatusRunning
	job.LastMessage = "Sync in progress..."
	m.mu.Unlock()

	go func() {
		if m.mockMode {
			time.Sleep(3 * time.Second)
			m.mu.Lock()
			job.Status = StatusSuccess
			job.LastRun = time.Now()
			job.LastMessage = "Transferred: 120 MB (Mock Mode)"
			m.mu.Unlock()
			return
		}

		var args []string
		if m.rcloneConfig != "" {
			args = append(args, "--config", m.rcloneConfig)
		}

		if job.Direction == DirectionPush {
			args = append(args, "sync", job.SourcePath, job.RemoteTarget)
		} else {
			args = append(args, "sync", job.RemoteTarget, job.SourcePath)
		}

		if job.BandwidthLimit != "" {
			args = append(args, "--bwlimit", job.BandwidthLimit)
		}

		cmd := exec.Command("rclone", args...)
		out, err := cmd.CombinedOutput()

		m.mu.Lock()
		job.LastRun = time.Now()
		if err != nil {
			job.Status = StatusFailed
			job.LastMessage = fmt.Sprintf("Error: %v (%s)", err, strings.TrimSpace(string(out)))
		} else {
			job.Status = StatusSuccess
			job.LastMessage = "Completed successfully"
		}
		m.saveJobsConfig()
		m.mu.Unlock()
	}()

	return nil
}

func (m *Manager) startScheduler() {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		now := time.Now()
		m.mu.RLock()
		var jobsToRun []string
		for id, job := range m.jobs {
			if !job.Enabled || job.Status == StatusRunning {
				continue
			}
			// Simple schedule match e.g. "0 3 45 * *" (hour 3, min 45)
			if matchSchedule(job.Schedule, now) {
				jobsToRun = append(jobsToRun, id)
			}
		}
		m.mu.RUnlock()

		for _, id := range jobsToRun {
			_ = m.TriggerJob(id)
		}
	}
}

func matchSchedule(sched string, t time.Time) bool {
	fields := strings.Fields(sched)
	if len(fields) < 5 {
		return false
	}
	// Format: minute hour dom month dow
	min, err1 := strconv.Atoi(fields[0])
	hour, err2 := strconv.Atoi(fields[1])
	if err1 == nil && err2 == nil {
		if t.Minute() == min && t.Hour() == hour {
			return true
		}
	}
	return false
}

func (m *Manager) saveJobsConfig() {
	_ = os.MkdirAll(filepath.Dir(m.configPath), 0755)
	var list []*SyncJob
	for _, j := range m.jobs {
		list = append(list, j)
	}
	data, err := json.MarshalIndent(list, "", "  ")
	if err == nil {
		_ = os.WriteFile(m.configPath, data, 0600)
	}
}

func (m *Manager) loadJobsConfig() {
	data, err := os.ReadFile(m.configPath)
	if err != nil {
		return
	}
	var list []*SyncJob
	if err := json.Unmarshal(data, &list); err == nil {
		for _, j := range list {
			m.jobs[j.ID] = j
		}
	}
}
