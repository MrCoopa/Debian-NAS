package cloudsync

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"debian-nas/internal/cronutil"
)

type SyncDirection string

const (
	DirectionPush SyncDirection = "PUSH" // Local -> Cloud
	DirectionPull SyncDirection = "PULL" // Cloud -> Local
)

type TransferMode string

const (
	ModeSync TransferMode = "SYNC" // Mirror source to destination (deletes missing)
	ModeCopy TransferMode = "COPY" // Copy new/modified files (keeps destination intact)
	ModeMove TransferMode = "MOVE" // Move files (deletes local after upload)
)

type JobStatus string

const (
	StatusIdle    JobStatus = "IDLE"
	StatusRunning JobStatus = "RUNNING"
	StatusSuccess JobStatus = "SUCCESS"
	StatusFailed  JobStatus = "FAILED"
)

type Remote struct {
	Name    string `json:"name"`
	Type    string `json:"type"` // sftp, crypt, webdav, b2, s3, etc.
	Details string `json:"details,omitempty"`
}

type CreateRemoteRequest struct {
	Name               string            `json:"name"`
	Type               string            `json:"type"` // sftp, crypt, webdav, b2, s3
	Host               string            `json:"host,omitempty"`
	User               string            `json:"user,omitempty"`
	Port               int               `json:"port,omitempty"`
	Pass               string            `json:"pass,omitempty"`
	Remote             string            `json:"remote,omitempty"`
	Password           string            `json:"password,omitempty"`
	Salt               string            `json:"salt,omitempty"`
	FilenameEncryption string            `json:"filename_encryption,omitempty"` // standard, off, obfuscate
	URL                string            `json:"url,omitempty"`
	Vendor             string            `json:"vendor,omitempty"`
	Account            string            `json:"account,omitempty"`
	Key                string            `json:"key,omitempty"`
	Endpoint           string            `json:"endpoint,omitempty"`
	AccessKey          string            `json:"access_key,omitempty"`
	SecretKey          string            `json:"secret_key,omitempty"`
	Region             string            `json:"region,omitempty"`
	Extra              map[string]string `json:"extra,omitempty"`
}

func (req CreateRemoteRequest) DetailsSummary() string {
	switch strings.ToLower(req.Type) {
	case "sftp":
		port := req.Port
		if port <= 0 {
			port = 22
		}
		if req.User != "" {
			return fmt.Sprintf("%s@%s:%d", req.User, req.Host, port)
		}
		return fmt.Sprintf("%s:%d", req.Host, port)
	case "crypt":
		if strings.ToLower(req.FilenameEncryption) == "off" {
			return fmt.Sprintf("Encrypted (Plain Filenames) -> %s", req.Remote)
		}
		return fmt.Sprintf("Encrypted -> %s", req.Remote)
	case "webdav":
		return req.URL
	case "b2":
		if req.Account != "" {
			return fmt.Sprintf("Account: %s", req.Account)
		}
		return "Backblaze B2"
	case "s3":
		if req.Endpoint != "" {
			return req.Endpoint
		}
		return "Amazon S3"
	default:
		return strings.ToUpper(req.Type)
	}
}

type SyncJob struct {
	ID                 string        `json:"id"`
	Name               string        `json:"name"`
	Description        string        `json:"description,omitempty"`
	SourcePath         string        `json:"source_path"`                 // e.g. "/mnt/DATA"
	RemoteTarget       string        `json:"remote_target"`               // e.g. "hetzner_storagebox:backup/DATA"
	RemoteCredential   string        `json:"remote_credential,omitempty"` // e.g. "hetzner_storagebox"
	RemotePath         string        `json:"remote_path,omitempty"`       // e.g. "backup/DATA"
	Direction          SyncDirection `json:"direction"`
	TransferMode       TransferMode  `json:"transfer_mode,omitempty"` // SYNC, COPY, MOVE
	DryRun             bool          `json:"dry_run,omitempty"`
	EncryptionEnabled  bool          `json:"encryption_enabled,omitempty"`
	EncryptionPassword string        `json:"encryption_password,omitempty"`
	EncryptionSalt     string        `json:"encryption_salt,omitempty"`
	FilenameEncryption string        `json:"filename_encryption,omitempty"` // "standard", "off", "obfuscate"
	Schedule           string        `json:"schedule"`                      // e.g. "0 3 45 * *"
	FrequencyText      string        `json:"frequency_text,omitempty"`
	NextRunText        string        `json:"next_run_text,omitempty"`
	LastRunText        string        `json:"last_run_text,omitempty"`
	BandwidthLimit     string        `json:"bandwidth_limit"` // e.g. "50M"
	Status             JobStatus     `json:"status"`
	LastRun            time.Time     `json:"last_run"`
	LastMessage        string        `json:"last_message"`
	Enabled            bool          `json:"enabled"`
}

func (j *SyncJob) GetTarget() string {
	if j.EncryptionEnabled {
		return fmt.Sprintf("cs_crypt_%s:", j.ID)
	}
	if j.RemoteTarget != "" {
		return j.RemoteTarget
	}
	if j.RemoteCredential != "" {
		sub := strings.TrimPrefix(j.RemotePath, "/")
		return fmt.Sprintf("%s:%s", j.RemoteCredential, sub)
	}
	return ""
}

func (j *SyncJob) GetDisplayTarget() string {
	base := ""
	if j.RemoteCredential != "" {
		sub := strings.TrimPrefix(j.RemotePath, "/")
		base = fmt.Sprintf("%s:%s", j.RemoteCredential, sub)
	} else if j.RemoteTarget != "" {
		base = j.RemoteTarget
	}
	if j.EncryptionEnabled {
		if strings.ToLower(j.FilenameEncryption) == "off" {
			return fmt.Sprintf("%s [🔒 Encrypted (Plain Names)]", base)
		}
		return fmt.Sprintf("%s [🔒 Encrypted (AES-256)]", base)
	}
	return base
}

func (j *SyncJob) GetTransferMode() string {
	if j.TransferMode != "" {
		return string(j.TransferMode)
	}
	return string(ModeSync)
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
	if strings.TrimSpace(j.FrequencyText) != "" {
		return j.FrequencyText
	}
	return cronutil.FormatFrequency(j.Schedule, "At 02:00 (02:00 AM), every day,...")
}

func (j *SyncJob) GetNextRun() string {
	if !j.Enabled {
		return "Disabled"
	}
	now := time.Now()
	next := cronutil.NextRun(j.Schedule, now)
	if !next.IsZero() {
		return cronutil.FormatRelativeFuture(next.Sub(now))
	}
	if strings.TrimSpace(j.NextRunText) != "" {
		return j.NextRunText
	}
	return "Scheduled"
}

func (j *SyncJob) GetLastRun() string {
	if j.LastRun.IsZero() {
		if strings.TrimSpace(j.LastRunText) != "" {
			return j.LastRunText
		}
		return "Never"
	}
	return cronutil.FormatRelativePast(time.Since(j.LastRun))
}

func (j *SyncJob) GetStateClass() string {
	switch j.Status {
	case StatusRunning:
		return "running"
	case StatusFailed:
		return "failed"
	case StatusSuccess:
		return "completed"
	default:
		return "idle"
	}
}

func (j *SyncJob) GetStateLabel() string {
	switch j.Status {
	case StatusRunning:
		return "Running"
	case StatusFailed:
		return "Failed"
	case StatusSuccess:
		return "Completed"
	default:
		return "Idle"
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
		{Name: "hetzner_storagebox", Type: "sftp", Details: "u123456@u123456.your-storagebox.de:23"},
		{Name: "hetzner-crypt", Type: "crypt", Details: "Encrypted -> hetzner_storagebox:backup"},
		{Name: "b2_backup", Type: "b2", Details: "Backblaze B2"},
	}

	m.jobs["job-1"] = &SyncJob{
		ID:               "job-1",
		Name:             "DATA Hetzner Upload",
		Description:      "DATA Hetzner Upload",
		SourcePath:       "/mnt/DATA",
		RemoteTarget:     "hetzner_storagebox:backup/DATA",
		RemoteCredential: "hetzner_storagebox",
		RemotePath:       "backup/DATA",
		Direction:        DirectionPush,
		TransferMode:     ModeSync,
		Schedule:         "45 3 * * 2", // At 03:45 only on Tuesday
		FrequencyText:    "At 03:45 (03:45 AM), only on Tuesday,...",
		BandwidthLimit:   "50M",
		Status:           StatusSuccess,
		LastRun:          time.Now().Add(-26 * time.Hour),
		LastMessage:      "Completed: 324.4 GB synced",
		Enabled:          true,
	}

	m.jobs["job-2"] = &SyncJob{
		ID:               "job-2",
		Name:             "HetznerUpload mailarchiver",
		Description:      "HetznerUpload mailarchiver",
		SourcePath:       "/mnt/SSD-MIRROR/mail-archiver",
		RemoteTarget:     "hetzner-crypt:mail-archiver",
		RemoteCredential: "hetzner-crypt",
		RemotePath:       "mail-archiver",
		Direction:        DirectionPush,
		TransferMode:     ModeCopy,
		Schedule:         "0 2 * * *", // At 02:00 every day
		FrequencyText:    "At 02:00 (02:00 AM), every day",
		BandwidthLimit:   "50M",
		Status:           StatusSuccess,
		LastRun:          time.Now().Add(-13 * time.Hour),
		LastMessage:      "Completed: 45.2 GB copied",
		Enabled:          true,
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

func formatRemoteType(t string) string {
	switch strings.ToLower(strings.TrimSpace(t)) {
	case "sftp":
		return "Hetzner Storage Box / SFTP"
	case "crypt":
		return "Encrypted Volume (Crypt)"
	case "webdav":
		return "WebDAV / Nextcloud"
	case "b2":
		return "Backblaze B2"
	case "s3":
		return "Amazon S3 / S3-Compatible"
	default:
		return strings.ToUpper(t)
	}
}

func (m *Manager) ListRemotes() ([]Remote, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.mockMode {
		var list []Remote
		for _, r := range m.remotes {
			if strings.HasPrefix(r.Name, "cs_crypt_") {
				continue
			}
			list = append(list, r)
		}
		return list, nil
	}

	args := []string{"listremotes", "--long"}
	if m.rcloneConfig != "" {
		args = append(args, "--config", m.rcloneConfig)
	}

	cmd := exec.Command("rclone", args...)
	out, err := cmd.Output()
	if err != nil {
		var list []Remote
		for _, r := range m.remotes {
			if strings.HasPrefix(r.Name, "cs_crypt_") {
				continue
			}
			list = append(list, r)
		}
		return list, nil // fallback to cached
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
			if strings.HasPrefix(name, "cs_crypt_") {
				continue
			}
			remType := parts[1]
			remotes = append(remotes, Remote{
				Name:    name,
				Type:    remType,
				Details: formatRemoteType(remType),
			})
		}
	}
	return remotes, nil
}

func (m *Manager) CreateRemote(req CreateRemoteRequest) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.createRemoteLocked(req)
}

func (m *Manager) createRemoteLocked(req CreateRemoteRequest) error {
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		return fmt.Errorf("remote name cannot be empty")
	}
	if strings.ContainsAny(req.Name, ":/ \\*?\"<>|") {
		return fmt.Errorf("remote name cannot contain spaces or special characters (: / \\ * ? < > |)")
	}
	req.Type = strings.ToLower(strings.TrimSpace(req.Type))
	if req.Type == "" {
		return fmt.Errorf("remote type is required")
	}

	if m.mockMode {
		summary := req.DetailsSummary()
		for i, r := range m.remotes {
			if r.Name == req.Name {
				m.remotes[i] = Remote{Name: req.Name, Type: req.Type, Details: summary}
				return nil
			}
		}
		m.remotes = append(m.remotes, Remote{Name: req.Name, Type: req.Type, Details: summary})
		return nil
	}

	args := []string{"config", "create", req.Name, req.Type}
	if m.rcloneConfig != "" {
		args = append(args, "--config", m.rcloneConfig)
	}

	switch req.Type {
	case "sftp":
		if req.Host == "" {
			return fmt.Errorf("SFTP host is required (e.g. uXXXXX.your-storagebox.de)")
		}
		port := req.Port
		if port <= 0 {
			port = 22
		}
		args = append(args, fmt.Sprintf("host=%s", req.Host))
		if req.User != "" {
			args = append(args, fmt.Sprintf("user=%s", req.User))
		}
		args = append(args, fmt.Sprintf("port=%d", port))
		if req.Pass != "" {
			args = append(args, fmt.Sprintf("pass=%s", req.Pass))
		}
	case "crypt":
		if req.Remote == "" {
			return fmt.Errorf("underlying remote is required for crypt (e.g. hetzner_storagebox:backup)")
		}
		args = append(args, fmt.Sprintf("remote=%s", req.Remote))
		if req.Password != "" {
			args = append(args, fmt.Sprintf("password=%s", req.Password))
		}
		if req.Salt != "" {
			args = append(args, fmt.Sprintf("salt=%s", req.Salt))
		}
		fnEnc := strings.ToLower(strings.TrimSpace(req.FilenameEncryption))
		if fnEnc == "off" {
			args = append(args, "filename_encryption=off", "directory_name_encryption=false")
		} else if fnEnc == "obfuscate" {
			args = append(args, "filename_encryption=obfuscate")
		} else {
			args = append(args, "filename_encryption=standard", "directory_name_encryption=true")
		}
	case "webdav":
		if req.URL == "" {
			return fmt.Errorf("WebDAV URL is required")
		}
		args = append(args, fmt.Sprintf("url=%s", req.URL))
		if req.User != "" {
			args = append(args, fmt.Sprintf("user=%s", req.User))
		}
		if req.Pass != "" {
			args = append(args, fmt.Sprintf("pass=%s", req.Pass))
		}
		if req.Vendor != "" {
			args = append(args, fmt.Sprintf("vendor=%s", req.Vendor))
		}
	case "b2":
		if req.Account != "" {
			args = append(args, fmt.Sprintf("account=%s", req.Account))
		}
		if req.Key != "" {
			args = append(args, fmt.Sprintf("key=%s", req.Key))
		}
	case "s3":
		args = append(args, "provider=Other", "env_auth=false")
		if req.AccessKey != "" {
			args = append(args, fmt.Sprintf("access_key_id=%s", req.AccessKey))
		}
		if req.SecretKey != "" {
			args = append(args, fmt.Sprintf("secret_access_key=%s", req.SecretKey))
		}
		if req.Endpoint != "" {
			args = append(args, fmt.Sprintf("endpoint=%s", req.Endpoint))
		}
		if req.Region != "" {
			args = append(args, fmt.Sprintf("region=%s", req.Region))
		}
	}

	for k, v := range req.Extra {
		if k != "" && v != "" {
			args = append(args, fmt.Sprintf("%s=%s", k, v))
		}
	}

	cmd := exec.Command("rclone", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("rclone config create failed: %v (%s)", err, strings.TrimSpace(string(out)))
	}

	return nil
}

func (m *Manager) DeleteRemote(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.deleteRemoteLocked(name)
}

func (m *Manager) deleteRemoteLocked(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("remote name is required")
	}

	if m.mockMode {
		var filtered []Remote
		for _, r := range m.remotes {
			if r.Name != name {
				filtered = append(filtered, r)
			}
		}
		m.remotes = filtered
		return nil
	}

	args := []string{"config", "delete", name}
	if m.rcloneConfig != "" {
		args = append(args, "--config", m.rcloneConfig)
	}

	cmd := exec.Command("rclone", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("rclone config delete failed: %v (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (m *Manager) TestRemote(name string) error {
	m.mu.RLock()
	defer m.mu.RUnlock()

	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("remote name is required")
	}

	if m.mockMode {
		if strings.HasPrefix(name, "invalid") || name == "error_remote" {
			return fmt.Errorf("connection refused")
		}
		return nil
	}

	args := []string{"lsd", name + ":", "--max-depth", "1", "--contimeout", "5s"}
	if m.rcloneConfig != "" {
		args = append(args, "--config", m.rcloneConfig)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "rclone", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		outStr := strings.TrimSpace(string(out))
		if outStr != "" {
			return fmt.Errorf("%s", outStr)
		}
		return fmt.Errorf("connection test failed: %w", err)
	}
	return nil
}

func (m *Manager) TriggerJob(jobID string) error {
	return m.TriggerJobWithOptions(jobID, false)
}

func (m *Manager) TriggerJobWithOptions(jobID string, dryRun bool) error {
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
	if dryRun {
		job.LastMessage = "Dry run in progress..."
	} else {
		job.LastMessage = "Sync in progress..."
	}
	m.mu.Unlock()

	go func() {
		if m.mockMode {
			time.Sleep(1 * time.Second)
			m.mu.Lock()
			job.Status = StatusSuccess
			job.LastRun = time.Now()
			if dryRun {
				job.LastMessage = "Dry run completed: 0 bytes modified (Trial)"
			} else {
				job.LastMessage = "Completed: 324 MB transferred"
			}
			m.saveJobsConfig()
			m.mu.Unlock()
			return
		}

		var args []string
		if m.rcloneConfig != "" {
			args = append(args, "--config", m.rcloneConfig)
		}

		action := "sync"
		switch job.TransferMode {
		case ModeCopy:
			action = "copy"
		case ModeMove:
			action = "move"
		default:
			action = "sync"
		}

		target := job.GetTarget()
		if target == "" {
			target = job.RemoteTarget
		}

		if job.Direction == DirectionPush {
			args = append(args, action, job.SourcePath, target)
		} else {
			args = append(args, action, target, job.SourcePath)
		}

		if dryRun || job.DryRun {
			args = append(args, "--dry-run")
		}
		if job.BandwidthLimit != "" && job.BandwidthLimit != "0" {
			args = append(args, "--bwlimit", job.BandwidthLimit)
		}
		args = append(args, "--stats", "1s", "--stats-one-line")

		cmd := exec.Command("rclone", args...)
		out, err := cmd.CombinedOutput()

		m.mu.Lock()
		job.LastRun = time.Now()
		if err != nil {
			job.Status = StatusFailed
			job.LastMessage = fmt.Sprintf("Error: %v (%s)", err, strings.TrimSpace(string(out)))
		} else {
			job.Status = StatusSuccess
			outStr := strings.TrimSpace(string(out))
			if dryRun {
				job.LastMessage = "Dry run completed successfully"
			} else if outStr != "" {
				job.LastMessage = "Completed: " + outStr
			} else {
				job.LastMessage = "Completed successfully"
			}
		}
		m.saveJobsConfig()
		m.mu.Unlock()

		if runtime.GOOS == "linux" {
			_ = exec.Command("logger", "-t", "debian-nas-cloudsync", fmt.Sprintf("Job %s (%s): %s", job.ID, action, job.LastMessage)).Run()
		}
	}()

	return nil
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

	job.Schedule = cronutil.NormalizeLegacySchedule(job.Schedule)
	if job.Schedule == "" {
		job.Schedule = "0 2 * * *"
	}
	if err := cronutil.Validate(job.Schedule); err != nil {
		return err
	}

	if job.ID == "" {
		job.ID = fmt.Sprintf("job-%d", time.Now().UnixNano())
	} else if existing, ok := m.jobs[job.ID]; ok {
		if job.LastRun.IsZero() && !existing.LastRun.IsZero() {
			job.LastRun = existing.LastRun
		}
		if job.LastMessage == "" && existing.LastMessage != "" {
			job.LastMessage = existing.LastMessage
		}
		if job.Status == "" || (job.Status == StatusIdle && existing.Status != "") {
			job.Status = existing.Status
		}
		if job.Name == "" {
			job.Name = existing.Name
		}
		if job.Description == "" {
			job.Description = existing.Description
		}
		if job.EncryptionEnabled {
			if job.EncryptionPassword == "" {
				job.EncryptionPassword = existing.EncryptionPassword
			}
			if job.EncryptionSalt == "" {
				job.EncryptionSalt = existing.EncryptionSalt
			}
			if job.FilenameEncryption == "" {
				job.FilenameEncryption = existing.FilenameEncryption
			}
		}
	}
	if job.Status == "" {
		job.Status = StatusIdle
	}

	// Manage task-level crypt remote
	cryptName := fmt.Sprintf("cs_crypt_%s", job.ID)
	if job.EncryptionEnabled {
		if job.FilenameEncryption == "" {
			job.FilenameEncryption = "standard"
		}
		underlying := ""
		if job.RemoteCredential != "" {
			underlying = fmt.Sprintf("%s:%s", job.RemoteCredential, strings.TrimPrefix(job.RemotePath, "/"))
		} else if job.RemoteTarget != "" {
			underlying = job.RemoteTarget
		}
		if underlying != "" {
			_ = m.createRemoteLocked(CreateRemoteRequest{
				Name:               cryptName,
				Type:               "crypt",
				Remote:             underlying,
				Password:           job.EncryptionPassword,
				Salt:               job.EncryptionSalt,
				FilenameEncryption: job.FilenameEncryption,
			})
		}
	} else {
		_ = m.deleteRemoteLocked(cryptName)
	}

	m.jobs[job.ID] = job
	m.saveJobsConfig()
	return nil
}

func (m *Manager) DeleteJob(jobID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	delete(m.jobs, jobID)
	_ = m.deleteRemoteLocked(fmt.Sprintf("cs_crypt_%s", jobID))
	m.saveJobsConfig()
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
	return cronutil.Matches(sched, t)
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
			j.Schedule = cronutil.NormalizeLegacySchedule(j.Schedule)
			m.jobs[j.ID] = j
		}
	}
}
