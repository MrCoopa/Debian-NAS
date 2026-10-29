package cloudsync

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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
	SourcePath     string        `json:"source_path"`   // e.g. "/mnt/DATA"
	RemoteTarget   string        `json:"remote_target"` // e.g. "hetzner_storagebox:backup/DATA"
	Direction      SyncDirection `json:"direction"`
	Schedule       string        `json:"schedule"`        // e.g. "0 3 45 * *"
	BandwidthLimit string        `json:"bandwidth_limit"` // e.g. "50M"
	Status         JobStatus     `json:"status"`
	LastRun        time.Time     `json:"last_run"`
	LastMessage    string        `json:"last_message"`
	Enabled        bool          `json:"enabled"`
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
		// If empty, load default sample jobs
		if len(m.jobs) == 0 {
			m.initMockCloudSync()
			m.saveJobsConfig()
		}
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
		SourcePath:     "/mnt/DATA",
		RemoteTarget:   "hetzner_storagebox:backup/DATA",
		Direction:      DirectionPush,
		Schedule:       "0 3 45 * *", // At 03:45
		BandwidthLimit: "50M",
		Status:         StatusSuccess,
		LastRun:        time.Now().Add(-7 * time.Hour),
		LastMessage:    "Completed: 324.4 GB synced",
		Enabled:        true,
	}

	m.jobs["job-2"] = &SyncJob{
		ID:             "job-2",
		Name:           "Hetzner Upload",
		SourcePath:     "/mnt/DATA/Fileshare",
		RemoteTarget:   "hetzner_storagebox:backup/Fileshare",
		Direction:      DirectionPush,
		Schedule:       "0 2 00 * *", // At 02:00
		BandwidthLimit: "50M",
		Status:         StatusSuccess,
		LastRun:        time.Now().Add(-9 * time.Hour),
		LastMessage:    "Completed: 224.5 GB synced",
		Enabled:        true,
	}
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
		_ = os.WriteFile(m.configPath, data, 0644)
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
