package cloudsync

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
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

func (j *SyncJob) GetRemoteCredential() string {
	if j.RemoteCredential != "" {
		return j.RemoteCredential
	}
	if idx := strings.Index(j.RemoteTarget, ":"); idx != -1 {
		return j.RemoteTarget[:idx]
	}
	return ""
}

func (j *SyncJob) GetRemotePath() string {
	if j.RemotePath != "" {
		return j.RemotePath
	}
	if idx := strings.Index(j.RemoteTarget, ":"); idx != -1 {
		return j.RemoteTarget[idx+1:]
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
		if strings.Contains(strings.ToLower(j.LastMessage), "restore") {
			return "Restoring"
		}
		return "Running"
	case StatusFailed:
		return "Failed"
	case StatusSuccess:
		return "Completed"
	default:
		return "Idle"
	}
}

type runningTask struct {
	cmd    *exec.Cmd
	cancel context.CancelFunc
}

type Manager struct {
	mu           sync.RWMutex
	eventMu      sync.RWMutex
	mockMode     bool
	rcloneConfig string
	configPath   string
	remotes      []Remote
	jobs         map[string]*SyncJob
	running      map[string]*runningTask
	onJobEvent   func(jobID, name, status, message string)
}

func (m *Manager) SetOnJobEvent(fn func(jobID, name, status, message string)) {
	m.eventMu.Lock()
	defer m.eventMu.Unlock()
	m.onJobEvent = fn
}

func (m *Manager) notifyJobEvent(jobID, name, status, message string) {
	m.eventMu.RLock()
	fn := m.onJobEvent
	m.eventMu.RUnlock()
	if fn != nil {
		go fn(jobID, name, status, message)
	}
}

func NewManager(mockMode bool, rcloneConfig string) *Manager {
	m := &Manager{
		mockMode:     mockMode || runtime.GOOS != "linux",
		rcloneConfig: rcloneConfig,
		configPath:   "/etc/nas/cloudsync.json",
		jobs:         make(map[string]*SyncJob),
		running:      make(map[string]*runningTask),
	}

	if m.mockMode {
		m.initMockCloudSync()
	} else {
		ensureSftpKnownHostsInConfigFile(m.rcloneConfig)
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
		args = append(args, "known_hosts_file=none")
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

	ensureSftpKnownHostsInConfigFile(m.rcloneConfig)
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
	return m.triggerJobExecution(jobID, false, false)
}

func (m *Manager) TriggerJobWithOptions(jobID string, dryRun bool) error {
	return m.triggerJobExecution(jobID, dryRun, false)
}

// RestoreJob performs a one-time safe PULL (rclone copy from Cloud -> Local SourcePath, with automatic decryption if encrypted)
// without permanently modifying the job's scheduled PUSH direction.
func (m *Manager) RestoreJob(jobID string) error {
	return m.triggerJobExecution(jobID, false, true)
}

func (m *Manager) AbortJob(jobID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	job, exists := m.jobs[jobID]
	if !exists {
		return fmt.Errorf("job not found")
	}

	if rt, ok := m.running[jobID]; ok {
		if rt.cancel != nil {
			rt.cancel()
		}
		if rt.cmd != nil && rt.cmd.Process != nil {
			_ = rt.cmd.Process.Kill()
		}
		delete(m.running, jobID)
	}

	job.Status = StatusFailed
	job.LastMessage = "Aborted by user"
	job.LastRun = time.Now()
	m.saveJobsConfig()
	jobName := job.Name

	if runtime.GOOS == "linux" {
		_ = exec.Command("logger", "-t", "debian-nas-cloudsync", fmt.Sprintf("Job %s aborted by user", job.ID)).Run()
		cryptTarget := fmt.Sprintf("cs_crypt_%s:", job.ID)
		_ = exec.Command("pkill", "-f", cryptTarget).Run()
	}
	m.notifyJobEvent(jobID, jobName, string(StatusFailed), "Aborted by user")
	return nil
}

func (m *Manager) triggerJobExecution(jobID string, dryRun bool, forceRestorePull bool) error {
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

	if job.SourcePath != "" {
		rel := strings.TrimPrefix(job.SourcePath, "/mnt/")
		rel = strings.TrimPrefix(rel, "/")
		cleanRemote := strings.Trim(job.RemotePath, "/")
		if rel != "" && (cleanRemote == "" || cleanRemote == "backup" || cleanRemote == "Backup" || strings.HasPrefix(rel, cleanRemote+"/")) {
			job.RemotePath = "/backup/" + rel
			m.saveJobsConfig()
		}
	}

	target := job.GetTarget()
	if target == "" {
		target = job.RemoteTarget
	}
	if target == "" || !strings.Contains(target, ":") {
		m.mu.Unlock()
		return fmt.Errorf("No valid cloud destination configured (missing remote prefix with colon, e.g. hetzner:backup). Please select a Cloud Credential.")
	}

	if job.EncryptionEnabled && job.EncryptionPassword != "" {
		cryptName := fmt.Sprintf("cs_crypt_%s", job.ID)
		underlying := ""
		if job.RemoteCredential != "" {
			underlying = fmt.Sprintf("%s:%s", job.RemoteCredential, strings.TrimPrefix(job.RemotePath, "/"))
		} else if job.RemoteTarget != "" {
			underlying = job.RemoteTarget
		}
		if underlying != "" {
			fnEnc := job.FilenameEncryption
			if fnEnc == "" {
				fnEnc = "standard"
			}
			_ = m.createRemoteLocked(CreateRemoteRequest{
				Name:               cryptName,
				Type:               "crypt",
				Remote:             underlying,
				Password:           job.EncryptionPassword,
				Salt:               job.EncryptionSalt,
				FilenameEncryption: fnEnc,
			})
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	m.running[jobID] = &runningTask{cancel: cancel}

	job.Status = StatusRunning
	if forceRestorePull {
		job.LastMessage = "Restore from cloud in progress..."
	} else if dryRun {
		job.LastMessage = "Dry run in progress..."
	} else {
		job.LastMessage = "Sync in progress..."
	}
	jobSnap := *job
	m.mu.Unlock()
	m.notifyJobEvent(jobID, jobSnap.Name, string(StatusRunning), job.LastMessage)

	go func() {
		defer func() {
			m.mu.Lock()
			delete(m.running, jobID)
			m.mu.Unlock()
		}()

		if m.mockMode {
			select {
			case <-time.After(1 * time.Second):
			case <-ctx.Done():
				m.mu.Lock()
				job.Status = StatusFailed
				job.LastRun = time.Now()
				job.LastMessage = "Aborted by user"
				m.saveJobsConfig()
				m.mu.Unlock()
				m.notifyJobEvent(jobID, jobSnap.Name, string(StatusFailed), "Aborted by user")
				return
			}
			m.mu.Lock()
			job.Status = StatusSuccess
			job.LastRun = time.Now()
			if forceRestorePull {
				job.LastMessage = "Restore completed: files restored from cloud"
			} else if dryRun {
				job.LastMessage = "Dry run completed: 0 bytes modified (Trial)"
			} else {
				job.LastMessage = "Completed: 324 MB transferred"
			}
			lastMsg := job.LastMessage
			m.saveJobsConfig()
			m.mu.Unlock()
			m.notifyJobEvent(jobID, jobSnap.Name, string(StatusSuccess), lastMsg)
			return
		}

		action := "sync"
		switch jobSnap.TransferMode {
		case ModeCopy:
			action = "copy"
		case ModeMove:
			action = "move"
		default:
			action = "sync"
		}
		if forceRestorePull {
			// Always use non-destructive 'copy' during a 1-click Restore so local files are never deleted
			action = "copy"
		}

		srcPath := jobSnap.SourcePath
		if _, err := os.Stat(srcPath); os.IsNotExist(err) {
			candidate := "/mnt/" + strings.TrimPrefix(srcPath, "/")
			if info, err2 := os.Stat(candidate); err2 == nil && info.IsDir() {
				srcPath = candidate
				m.mu.Lock()
				job.SourcePath = candidate
				m.saveJobsConfig()
				m.mu.Unlock()
			} else if !strings.HasPrefix(srcPath, "/mnt/") && runtime.GOOS == "linux" {
				srcPath = candidate
			}
		}

		var args []string
		args = append(args, action)
		if forceRestorePull || jobSnap.Direction == DirectionPull {
			args = append(args, target, srcPath)
		} else {
			args = append(args, srcPath, target)
		}

		ensureSftpKnownHostsInConfigFile(m.rcloneConfig)
		if m.rcloneConfig != "" {
			args = append(args, "--config", m.rcloneConfig)
		}
		args = append(args, "--create-empty-src-dirs")
		if dryRun || jobSnap.DryRun {
			args = append(args, "--dry-run")
		}
		if jobSnap.BandwidthLimit != "" && jobSnap.BandwidthLimit != "0" {
			args = append(args, "--bwlimit", jobSnap.BandwidthLimit)
		}
		args = append(args, "--stats", "1s", "--stats-one-line")

		cmd := exec.CommandContext(ctx, "rclone", args...)
		m.mu.Lock()
		if rt, ok := m.running[jobID]; ok {
			rt.cmd = cmd
		}
		m.mu.Unlock()

		stdout, errOut := cmd.StdoutPipe()
		stderr, errErr := cmd.StderrPipe()
		if errOut != nil || errErr != nil {
			out, err := cmd.CombinedOutput()
			m.mu.Lock()
			job.LastRun = time.Now()
			if ctx.Err() == context.Canceled {
				job.Status = StatusFailed
				job.LastMessage = "Aborted by user"
			} else if err != nil {
				job.Status = StatusFailed
				job.LastMessage = summarizeRcloneErrorOutput(err, string(out))
			} else {
				job.Status = StatusSuccess
				msg := summarizeRcloneSuccessOutput(string(out), dryRun)
				if forceRestorePull {
					msg = strings.Replace(msg, "Completed", "Restore completed", 1)
				}
				job.LastMessage = msg
			}
			lastMsg := job.LastMessage
			lastStatus := job.Status
			m.saveJobsConfig()
			m.mu.Unlock()
			if (forceRestorePull || jobSnap.Direction == DirectionPull) && lastStatus == StatusSuccess && runtime.GOOS == "linux" {
				_ = FixPermissionsPath(srcPath)
			}
			m.notifyJobEvent(jobID, jobSnap.Name, string(lastStatus), lastMsg)
			return
		}

		if err := cmd.Start(); err != nil {
			m.mu.Lock()
			job.Status = StatusFailed
			job.LastRun = time.Now()
			job.LastMessage = fmt.Sprintf("Failed to start rclone: %v", err)
			lastMsg := job.LastMessage
			m.saveJobsConfig()
			m.mu.Unlock()
			m.notifyJobEvent(jobID, jobSnap.Name, string(StatusFailed), lastMsg)
			return
		}

		var outputBuf bytes.Buffer
		var outMu sync.Mutex
		multi := io.MultiReader(stdout, stderr)
		scanner := bufio.NewScanner(multi)
		for scanner.Scan() {
			line := scanner.Text()
			outMu.Lock()
			outputBuf.WriteString(line + "\n")
			outMu.Unlock()

			trim := strings.TrimSpace(line)
			if trim == "" {
				continue
			}
			lower := strings.ToLower(trim)
			if strings.Contains(lower, "no host key validation is being performed") ||
				strings.Contains(lower, "known_hosts_file") {
				continue
			}

			// Capture live stats lines from rclone
			if strings.Contains(trim, "Transferred:") || strings.Contains(trim, "ETA") || strings.Contains(trim, "%") {
				m.mu.Lock()
				if job.Status == StatusRunning {
					prefix := "Syncing: "
					if forceRestorePull {
						prefix = "Restoring: "
					}
					cleanLine := strings.TrimPrefix(trim, "* ")
					job.LastMessage = prefix + cleanLine
				}
				m.mu.Unlock()
			}
		}

		err := cmd.Wait()
		out := outputBuf.Bytes()

		m.mu.Lock()
		job.LastRun = time.Now()
		if ctx.Err() == context.Canceled {
			job.Status = StatusFailed
			job.LastMessage = "Aborted by user"
		} else if err != nil {
			job.Status = StatusFailed
			job.LastMessage = summarizeRcloneErrorOutput(err, string(out))
		} else {
			job.Status = StatusSuccess
			msg := summarizeRcloneSuccessOutput(string(out), dryRun)
			if forceRestorePull {
				msg = strings.Replace(msg, "Completed", "Restore completed", 1)
			}
			job.LastMessage = msg
		}
		lastMsg := job.LastMessage
		lastStatus := job.Status
		m.saveJobsConfig()
		m.mu.Unlock()

		if runtime.GOOS == "linux" {
			_ = exec.Command("logger", "-t", "debian-nas-cloudsync", fmt.Sprintf("Job %s (%s): %s", jobSnap.ID, action, lastMsg)).Run()
			if (forceRestorePull || jobSnap.Direction == DirectionPull) && lastStatus == StatusSuccess {
				_ = FixPermissionsPath(srcPath)
			}
		}
		m.notifyJobEvent(jobID, jobSnap.Name, string(lastStatus), lastMsg)
	}()

	return nil
}

func (m *Manager) GetJob(jobID string) (*SyncJob, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	j, ok := m.jobs[jobID]
	if !ok || j == nil {
		return nil, fmt.Errorf("job not found")
	}
	cp := *j
	return &cp, nil
}

func (m *Manager) ListJobs() ([]*SyncJob, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var list []*SyncJob
	for _, j := range m.jobs {
		if j == nil {
			continue
		}
		cp := *j
		list = append(list, &cp)
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

	if job.SourcePath != "" && !strings.HasPrefix(job.SourcePath, "/mnt/") {
		candidate := "/mnt/" + strings.TrimPrefix(job.SourcePath, "/")
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			job.SourcePath = candidate
		} else if runtime.GOOS == "linux" {
			job.SourcePath = candidate
		}
	}

	if job.SourcePath != "" {
		rel := strings.TrimPrefix(job.SourcePath, "/mnt/")
		rel = strings.TrimPrefix(rel, "/")
		if rel != "" {
			cleanRemote := strings.Trim(job.RemotePath, "/")
			if cleanRemote == "" || cleanRemote == "backup" || cleanRemote == "Backup" || strings.HasPrefix(rel, cleanRemote+"/") {
				job.RemotePath = "/backup/" + rel
			} else if !strings.HasSuffix(cleanRemote, rel) && !strings.HasSuffix(cleanRemote, filepath.Base(rel)) {
				basePrefix := "backup"
				if parts := strings.Split(cleanRemote, "/"); len(parts) > 0 && parts[0] != "" {
					basePrefix = parts[0]
				}
				job.RemotePath = "/" + basePrefix + "/" + rel
			}
		}
	}
	if job.RemotePath != "" && !strings.HasPrefix(job.RemotePath, "/") {
		job.RemotePath = "/" + job.RemotePath
	}

	if job.RemoteCredential == "" && strings.Contains(job.RemoteTarget, ":") {
		parts := strings.SplitN(job.RemoteTarget, ":", 2)
		job.RemoteCredential = parts[0]
		if job.RemotePath == "" {
			job.RemotePath = parts[1]
		}
	}
	if job.RemoteTarget == "" && job.RemoteCredential != "" {
		sub := strings.TrimPrefix(job.RemotePath, "/")
		job.RemoteTarget = fmt.Sprintf("%s:%s", job.RemoteCredential, sub)
	}

	target := job.GetTarget()
	if target == "" {
		target = job.RemoteTarget
	}
	if job.RemoteCredential == "" && (target == "" || !strings.Contains(target, ":")) {
		return fmt.Errorf("No valid cloud destination specified. Please select a Cloud Credential.")
	}

	if job.ID == "" {
		job.ID = fmt.Sprintf("job-%d", time.Now().UnixNano())
	} else if existing, ok := m.jobs[job.ID]; ok {
		if job.RemoteCredential == "" && existing.RemoteCredential != "" {
			job.RemoteCredential = existing.RemoteCredential
		}
		if job.RemotePath == "" && existing.RemotePath != "" {
			job.RemotePath = existing.RemotePath
		}
		if job.RemoteTarget == "" && existing.RemoteTarget != "" {
			job.RemoteTarget = existing.RemoteTarget
		}
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

func sameCronMinute(a, b time.Time) bool {
	if a.IsZero() || b.IsZero() {
		return false
	}
	return a.Year() == b.Year() &&
		a.Month() == b.Month() &&
		a.Day() == b.Day() &&
		a.Hour() == b.Hour() &&
		a.Minute() == b.Minute()
}

func (m *Manager) startScheduler() {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		now := time.Now()
		m.mu.RLock()
		var jobsToRun []string
		for id, job := range m.jobs {
			if !job.Enabled || job.Status == StatusRunning {
				continue
			}
			if sameCronMinute(job.LastRun, now) {
				continue
			}
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
		changed := false
		for _, j := range list {
			if j.Status == StatusRunning {
				j.Status = StatusIdle
				if strings.Contains(j.LastMessage, "in progress") {
					j.LastMessage = "Interrupted by daemon restart"
				}
				changed = true
			}
			j.Schedule = cronutil.NormalizeLegacySchedule(j.Schedule)
			if j.SourcePath != "" && !strings.HasPrefix(j.SourcePath, "/mnt/") {
				candidate := "/mnt/" + strings.TrimPrefix(j.SourcePath, "/")
				if info, err := os.Stat(candidate); err == nil && info.IsDir() {
					j.SourcePath = candidate
					changed = true
				} else if runtime.GOOS == "linux" {
					j.SourcePath = candidate
					changed = true
				}
			}
			if j.SourcePath != "" {
				rel := strings.TrimPrefix(j.SourcePath, "/mnt/")
				rel = strings.TrimPrefix(rel, "/")
				cleanRemote := strings.Trim(j.RemotePath, "/")
				if rel != "" && (cleanRemote == "" || cleanRemote == "backup" || cleanRemote == "Backup" || strings.HasPrefix(rel, cleanRemote+"/")) {
					j.RemotePath = "/backup/" + rel
					changed = true
				}
			}
			if strings.Contains(j.LastMessage, "No host key validation is being performed") {
				j.LastMessage = summarizeRcloneSuccessOutput(strings.TrimPrefix(j.LastMessage, "Completed: "), false)
				changed = true
			}
			m.jobs[j.ID] = j
		}
		if changed {
			m.saveJobsConfig()
		}
	}
}

// summarizeRcloneSuccessOutput strips noisy SFTP host-key NOTICE lines from rclone output and returns a clean completion message.
func summarizeRcloneSuccessOutput(raw string, dryRun bool) string {
	if dryRun {
		return "Dry run completed successfully"
	}
	var kept []string
	for _, line := range strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n") {
		trim := strings.TrimSpace(line)
		if trim == "" {
			continue
		}
		lower := strings.ToLower(trim)
		if strings.Contains(lower, "no host key validation is being performed") ||
			strings.Contains(lower, "known_hosts_file") {
			continue
		}
		kept = append(kept, trim)
	}
	if len(kept) == 0 {
		return "Completed successfully"
	}

	for i := len(kept) - 1; i >= 0; i-- {
		l := kept[i]
		if strings.Contains(l, "Transferred:") && (strings.Contains(l, "100%") || strings.Contains(l, "/") || strings.Contains(l, "Bytes")) {
			clean := strings.TrimPrefix(l, "* ")
			clean = strings.TrimSpace(strings.TrimPrefix(clean, "Transferred:"))
			return "Completed: " + clean
		}
	}

	last := kept[len(kept)-1]
	if strings.HasPrefix(last, "Completed:") {
		return last
	}
	return "Completed: " + last
}

// summarizeRcloneErrorOutput extracts the actual failure message from rclone output,
// discarding lengthy CLI usage/flag lists if rclone failed with a syntax or flag error.
func summarizeRcloneErrorOutput(err error, raw string) string {
	lines := strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n")
	var fatalLines []string
	for _, l := range lines {
		trim := strings.TrimSpace(l)
		if trim == "" || strings.HasPrefix(trim, "Flags:") || strings.HasPrefix(trim, "-") || strings.HasPrefix(trim, "Usage:") {
			continue
		}
		if strings.Contains(trim, "Fatal error:") || strings.Contains(trim, "Error:") || strings.Contains(trim, "Failed to") {
			clean := trim
			if idx := strings.Index(clean, "NOTICE: "); idx != -1 {
				clean = clean[idx+8:]
			}
			fatalLines = append(fatalLines, clean)
		}
	}
	if len(fatalLines) > 0 {
		return fmt.Sprintf("Error: %s", fatalLines[len(fatalLines)-1])
	}
	clean := strings.TrimSpace(raw)
	if len(clean) > 200 {
		clean = clean[:200] + "..."
	}
	if clean != "" {
		return fmt.Sprintf("Error: %v (%s)", err, clean)
	}
	return fmt.Sprintf("Error: %v", err)
}

// ensureSftpKnownHostsInConfigFile ensures all existing [remote] sections with 'type = sftp' in rclone.conf
// include 'known_hosts_file = none' (unless already configured) so rclone does not emit SFTP host-key NOTICE warnings.
func ensureSftpKnownHostsInConfigFile(path string) {
	if path == "" {
		path = "/root/.config/rclone/rclone.conf"
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	var out []string
	inSection := false
	isSftp := false
	hasKnownHosts := false
	modified := false

	flushSection := func() {
		if inSection && isSftp && !hasKnownHosts {
			// Insert 'known_hosts_file = none' before trailing blank lines of the section
			insertIdx := len(out)
			for insertIdx > 0 && strings.TrimSpace(out[insertIdx-1]) == "" {
				insertIdx--
			}
			out = append(out[:insertIdx], append([]string{"known_hosts_file = none"}, out[insertIdx:]...)...)
			modified = true
		}
	}

	for _, line := range lines {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "[") && strings.HasSuffix(trim, "]") {
			flushSection()
			inSection = true
			isSftp = false
			hasKnownHosts = false
			out = append(out, line)
			continue
		}
		if inSection && !strings.HasPrefix(trim, "#") && !strings.HasPrefix(trim, ";") {
			if idx := strings.Index(trim, "="); idx != -1 {
				k := strings.ToLower(strings.TrimSpace(trim[:idx]))
				v := strings.ToLower(strings.TrimSpace(trim[idx+1:]))
				if k == "type" && v == "sftp" {
					isSftp = true
				} else if k == "known_hosts_file" || k == "pubkey" || k == "host_key_algorithms" {
					hasKnownHosts = true
				}
			}
		}
		out = append(out, line)
	}
	flushSection()

	if modified {
		_ = os.WriteFile(path, []byte(strings.Join(out, "\n")), 0600)
	}
}

// FixPermissionsPath repairs file ownership and group permissions for restored files and shares.
// Since rclone runs as root, files restored from cloud otherwise default to root:root (0644),
// causing Windows SMB clients to get "Permission denied by Unix User\root".
func FixPermissionsPath(targetPath string) error {
	if runtime.GOOS != "linux" || targetPath == "" {
		return nil
	}

	cleanPath := filepath.Clean(targetPath)
	if _, err := os.Stat(cleanPath); err != nil {
		return err
	}

	// 1. Detect if targetPath or any parent has an existing non-root owner
	primaryOwner := ""
	for p := cleanPath; p != "" && p != "/" && p != "/mnt"; p = filepath.Dir(p) {
		out, err := exec.Command("stat", "-c", "%u %U", p).Output()
		if err == nil {
			parts := strings.Fields(strings.TrimSpace(string(out)))
			if len(parts) >= 2 {
				uidStr := parts[0]
				userName := parts[1]
				if uid, errAtoi := strconv.Atoi(uidStr); errAtoi == nil && uid >= 1000 && userName != "UNKNOWN" {
					primaryOwner = userName
					break
				}
			}
		}
	}

	// 2. If no non-root owner found, check /etc/samba/smb.conf for the share's valid users
	if primaryOwner == "" {
		primaryOwner = findShareUserForPath(cleanPath)
	}

	// 3. If still not found, check members of group 'nasusers' or /etc/passwd
	if primaryOwner == "" {
		primaryOwner = findFirstNasUser()
	}

	// 4. Apply ownership: primaryOwner:nasusers if found, else root:nasusers
	if primaryOwner != "" {
		_ = exec.Command("chown", "-R", primaryOwner+":nasusers", cleanPath).Run()
	} else {
		_ = exec.Command("chgrp", "-R", "nasusers", cleanPath).Run()
	}

	// 5. Ensure full read/write for user and group nasusers, plus setgid bit on directories
	_ = exec.Command("chmod", "-R", "u+rwX,g+rwX,o+rX", cleanPath).Run()
	_ = exec.Command("find", cleanPath, "-type", "d", "-exec", "chmod", "2775", "{}", "+").Run()

	return nil
}

func findShareUserForPath(targetPath string) string {
	data, err := os.ReadFile("/etc/samba/smb.conf")
	if err != nil {
		return ""
	}

	var currentPath string
	var currentUsers []string
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			if currentPath != "" && (currentPath == targetPath || strings.HasPrefix(targetPath, currentPath+"/")) {
				for _, u := range currentUsers {
					if u != "" && !strings.HasPrefix(u, "@") && u != "root" {
						return u
					}
				}
			}
			currentPath = ""
			currentUsers = nil
			continue
		}

		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			k := strings.ToLower(strings.TrimSpace(parts[0]))
			v := strings.TrimSpace(parts[1])
			if k == "path" {
				currentPath = filepath.Clean(v)
			} else if k == "valid users" || k == "force user" {
				for _, item := range strings.Fields(v) {
					cleanItem := strings.Trim(item, ",;")
					if cleanItem != "" {
						currentUsers = append(currentUsers, cleanItem)
					}
				}
			}
		}
	}
	if currentPath != "" && (currentPath == targetPath || strings.HasPrefix(targetPath, currentPath+"/")) {
		for _, u := range currentUsers {
			if u != "" && !strings.HasPrefix(u, "@") && u != "root" {
				return u
			}
		}
	}
	return ""
}

func findFirstNasUser() string {
	data, err := os.ReadFile("/etc/group")
	if err == nil {
		scanner := bufio.NewScanner(bytes.NewReader(data))
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if strings.HasPrefix(line, "nasusers:") {
				parts := strings.Split(line, ":")
				if len(parts) >= 4 {
					members := strings.Split(parts[3], ",")
					for _, m := range members {
						m = strings.TrimSpace(m)
						if m != "" && m != "root" {
							return m
						}
					}
				}
			}
		}
	}

	pData, pErr := os.ReadFile("/etc/passwd")
	if pErr == nil {
		pScanner := bufio.NewScanner(bytes.NewReader(pData))
		for pScanner.Scan() {
			line := strings.TrimSpace(pScanner.Text())
			parts := strings.Split(line, ":")
			if len(parts) >= 3 {
				uName := parts[0]
				if uid, err := strconv.Atoi(parts[2]); err == nil && uid >= 1000 && uid < 65534 {
					return uName
				}
			}
		}
	}
	return ""
}
