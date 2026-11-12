package zfs

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"debian-nas/internal/validate"
)

const defaultSnapshotTasksPath = "/etc/nas/snapshot_tasks.json"

// SnapshotTask represents a TrueNAS-style Periodic Snapshot Task for a ZFS dataset.
type SnapshotTask struct {
	ID               string    `json:"id"`
	Dataset          string    `json:"dataset"`
	Recursive        bool      `json:"recursive"`
	LifetimeValue    int       `json:"lifetime_value"`
	LifetimeUnit     string    `json:"lifetime_unit"`
	NamingSchema     string    `json:"naming_schema"`
	Schedule         string    `json:"schedule"`
	FrequencyText    string    `json:"frequency_text,omitempty"`
	NextRunText      string    `json:"next_run_text,omitempty"`
	LastRunText      string    `json:"last_run_text,omitempty"`
	Enabled          bool      `json:"enabled"`
	State            string    `json:"state"`
	LastRun          time.Time `json:"last_run"`
	LastMessage      string    `json:"last_message,omitempty"`
	SnapshotsCreated int       `json:"snapshots_created"`
}

func generateTaskID() string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("snaptask_%d", time.Now().UnixNano())
	}
	return "snaptask_" + hex.EncodeToString(b)
}

func normalizeLifetimeUnit(unit string) (string, error) {
	u := strings.ToLower(strings.TrimSpace(unit))
	switch u {
	case "", "week", "weeks", "week(s)":
		return "week(s)", nil
	case "hour", "hours", "hour(s)":
		return "hour(s)", nil
	case "day", "days", "day(s)":
		return "day(s)", nil
	case "month", "months", "month(s)":
		return "month(s)", nil
	case "year", "years", "year(s)":
		return "year(s)", nil
	default:
		return "", fmt.Errorf("invalid lifetime unit '%s'", unit)
	}
}

func (t *SnapshotTask) retentionDuration() time.Duration {
	val := t.LifetimeValue
	if val <= 0 {
		val = 2
	}
	switch t.LifetimeUnit {
	case "hour(s)":
		return time.Duration(val) * time.Hour
	case "day(s)":
		return time.Duration(val) * 24 * time.Hour
	case "month(s)":
		return time.Duration(val) * 30 * 24 * time.Hour
	case "year(s)":
		return time.Duration(val) * 365 * 24 * time.Hour
	default: // week(s)
		return time.Duration(val) * 7 * 24 * time.Hour
	}
}

// expandNamingSchema converts strftime tokens into a ZFS-compatible snapshot name.
func expandNamingSchema(schema string, at time.Time) string {
	s := strings.TrimSpace(schema)
	if s == "" {
		s = "auto-%Y-%m-%d_%H-%M"
	}
	replacer := strings.NewReplacer(
		"%Y", at.Format("2006"),
		"%y", at.Format("06"),
		"%m", at.Format("01"),
		"%d", at.Format("02"),
		"%H", at.Format("15"),
		"%M", at.Format("04"),
		"%S", at.Format("05"),
	)
	return replacer.Replace(s)
}

func namingSchemaPrefix(schema string) string {
	s := strings.TrimSpace(schema)
	if s == "" {
		return "auto-"
	}
	if idx := strings.Index(s, "%"); idx >= 0 {
		return s[:idx]
	}
	return s
}

func validateCronSchedule(schedule string) error {
	fields := strings.Fields(strings.TrimSpace(schedule))
	if len(fields) != 5 {
		return fmt.Errorf("invalid schedule '%s': expected 5 cron fields (min hour dom mon dow)", schedule)
	}
	for _, f := range fields {
		for _, r := range f {
			if !(r >= '0' && r <= '9') && r != '*' && r != ',' && r != '-' && r != '/' {
				return fmt.Errorf("invalid character '%c' in cron schedule '%s'", r, schedule)
			}
		}
	}
	return nil
}

// GetKeepFor formats the retention period like TrueNAS SCALE ("2 week(s)").
func (t *SnapshotTask) GetKeepFor() string {
	val := t.LifetimeValue
	if val <= 0 {
		val = 2
	}
	unit := t.LifetimeUnit
	if unit == "" {
		unit = "week(s)"
	}
	return fmt.Sprintf("%d %s", val, unit)
}

// GetFrequency returns a TrueNAS-style human readable frequency string.
func (t *SnapshotTask) GetFrequency() string {
	if strings.TrimSpace(t.FrequencyText) != "" {
		return t.FrequencyText
	}
	parts := strings.Fields(strings.TrimSpace(t.Schedule))
	if len(parts) != 5 {
		if t.Schedule == "" {
			return "Every day"
		}
		return t.Schedule
	}
	minStr, hourStr, domStr, monStr, dowStr := parts[0], parts[1], parts[2], parts[3], parts[4]
	if minStr == "*/15" && hourStr == "*" {
		return "Every 15 minutes"
	}
	if minStr == "*/30" && hourStr == "*" {
		return "Every 30 minutes"
	}
	if minStr == "0" && hourStr == "*" && domStr == "*" && monStr == "*" && dowStr == "*" {
		return "Every hour (at :00)"
	}
	h, hErr := strconv.Atoi(hourStr)
	m, mErr := strconv.Atoi(minStr)
	if hErr == nil && mErr == nil {
		period := "AM"
		h12 := h
		if h == 0 {
			h12 = 12
			period = "AM"
		} else if h == 12 {
			period = "PM"
		} else if h > 12 {
			h12 = h - 12
			period = "PM"
		}
		timeLabel := fmt.Sprintf("At %02d:%02d (%d:%02d %s)", h, m, h12, m, period)
		if h < 10 {
			timeLabel = fmt.Sprintf("At %02d:%02d (%02d:%02d %s)", h, m, h12, m, period)
		}
		if domStr == "*" && monStr == "*" && dowStr == "*" {
			return timeLabel + ", every day,..."
		}
		if domStr == "*" && monStr == "*" && dowStr != "*" {
			return timeLabel + ", on " + dowName(dowStr)
		}
		if domStr != "*" && monStr == "*" {
			return fmt.Sprintf("%s, on day %s of month", timeLabel, domStr)
		}
	}
	return t.Schedule
}

func dowName(dow string) string {
	switch dow {
	case "0", "7":
		return "Sunday"
	case "1":
		return "Monday"
	case "2":
		return "Tuesday"
	case "3":
		return "Wednesday"
	case "4":
		return "Thursday"
	case "5":
		return "Friday"
	case "6":
		return "Saturday"
	default:
		return "day " + dow
	}
}

func formatRelativeFuture(d time.Duration) string {
	if d <= 0 {
		return "due now"
	}
	if d < time.Minute {
		return "in less than a minute"
	}
	mins := int(d.Minutes() + 0.5)
	if mins < 60 {
		if mins == 1 {
			return "in 1 minute"
		}
		return fmt.Sprintf("in %d minutes", mins)
	}
	hours := int(d.Hours() + 0.5)
	if hours < 24 {
		if hours == 1 {
			return "in about 1 hour"
		}
		return fmt.Sprintf("in about %d hours", hours)
	}
	days := int(d.Hours()/24 + 0.5)
	if days == 1 {
		return "in 1 day"
	}
	return fmt.Sprintf("in %d days", days)
}

func formatRelativePast(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	if d < time.Minute {
		return "just now"
	}
	mins := int(d.Minutes() + 0.5)
	if mins < 60 {
		if mins == 1 {
			return "1 minute ago"
		}
		return fmt.Sprintf("%d minutes ago", mins)
	}
	hours := int(d.Hours() + 0.5)
	if hours < 24 {
		if hours == 1 {
			return "about 1 hour ago"
		}
		return fmt.Sprintf("about %d hours ago", hours)
	}
	days := int(d.Hours()/24 + 0.5)
	if days == 1 {
		return "1 day ago"
	}
	return fmt.Sprintf("%d days ago", days)
}

// GetNextRun returns a human-readable estimate for the next scheduled run.
func (t *SnapshotTask) GetNextRun() string {
	if !t.Enabled {
		return "Disabled"
	}
	now := time.Now()
	next := computeNextCronTime(t.Schedule, now)
	if !next.IsZero() {
		return formatRelativeFuture(next.Sub(now))
	}
	if strings.TrimSpace(t.NextRunText) != "" {
		return t.NextRunText
	}
	return "Scheduled"
}

// GetLastRun returns a human-readable relative string for the last execution.
func (t *SnapshotTask) GetLastRun() string {
	if t.LastRun.IsZero() {
		if strings.TrimSpace(t.LastRunText) != "" {
			return t.LastRunText
		}
		return "Never"
	}
	return formatRelativePast(time.Since(t.LastRun))
}

// GetStateClass maps the task state to CSS class suffixes in style.css.
func (t *SnapshotTask) GetStateClass() string {
	switch strings.ToUpper(t.State) {
	case "RUNNING":
		return "running"
	case "COMPLETED":
		return "completed"
	case "FAILED":
		return "failed"
	default:
		return "idle"
	}
}

// GetStateLabel returns the capitalized TrueNAS state pill text.
func (t *SnapshotTask) GetStateLabel() string {
	switch strings.ToUpper(t.State) {
	case "RUNNING":
		return "Running"
	case "COMPLETED":
		return "Completed"
	case "FAILED":
		return "Failed"
	default:
		return "Idle"
	}
}

func matchesCronField(expr string, val int) bool {
	if expr == "*" {
		return true
	}
	for _, part := range strings.Split(expr, ",") {
		part = strings.TrimSpace(part)
		if part == "*" {
			return true
		}
		if strings.HasPrefix(part, "*/") {
			step, err := strconv.Atoi(strings.TrimPrefix(part, "*/"))
			if err == nil && step > 0 && val%step == 0 {
				return true
			}
			continue
		}
		if strings.Contains(part, "-") {
			bounds := strings.SplitN(part, "-", 2)
			lo, err1 := strconv.Atoi(bounds[0])
			hi, err2 := strconv.Atoi(bounds[1])
			if err1 == nil && err2 == nil && val >= lo && val <= hi {
				return true
			}
			continue
		}
		num, err := strconv.Atoi(part)
		if err == nil && num == val {
			return true
		}
	}
	return false
}

func matchesCronSchedule(schedule string, tm time.Time) bool {
	fields := strings.Fields(strings.TrimSpace(schedule))
	if len(fields) != 5 {
		return false
	}
	dow := int(tm.Weekday())
	dowMatch := matchesCronField(fields[4], dow) || (dow == 0 && matchesCronField(fields[4], 7))
	return matchesCronField(fields[0], tm.Minute()) &&
		matchesCronField(fields[1], tm.Hour()) &&
		matchesCronField(fields[2], tm.Day()) &&
		matchesCronField(fields[3], int(tm.Month())) &&
		dowMatch
}

func computeNextCronTime(schedule string, from time.Time) time.Time {
	fields := strings.Fields(strings.TrimSpace(schedule))
	if len(fields) != 5 {
		return time.Time{}
	}
	candidate := from.Truncate(time.Minute).Add(time.Minute)
	// Scan up to 8 days (11520 minutes) for fast, exact next occurrence calculation
	for i := 0; i < 11520; i++ {
		if matchesCronSchedule(schedule, candidate) {
			return candidate
		}
		candidate = candidate.Add(time.Minute)
	}
	return time.Time{}
}

func (m *Manager) initSnapshotTasksLocked() {
	if m.snapTasks == nil {
		m.snapTasks = make(map[string]*SnapshotTask)
	}
	if m.snapTaskConfigPath == "" {
		m.snapTaskConfigPath = defaultSnapshotTasksPath
	}

	if !m.mockMode {
		m.loadSnapshotTasksConfigLocked()
		return
	}

	// Seed default TrueNAS-style snapshot tasks when running in mock mode or on first boot.
	lastRunDefault := time.Now().Add(-17 * time.Hour)
	seed := []*SnapshotTask{
		{
			ID:               "snap_data_daily",
			Dataset:          "DATA",
			Recursive:        true,
			LifetimeValue:    2,
			LifetimeUnit:     "week(s)",
			NamingSchema:     "auto-%Y-%m-%d_%H-%M",
			Schedule:         "0 0 * * *",
			FrequencyText:    "At 00:00 (12:00 AM), every day,...",
			Enabled:          true,
			State:            "COMPLETED",
			LastRun:          lastRunDefault,
			LastMessage:      "Snapshot DATA@auto completed cleanly",
			SnapshotsCreated: 14,
		},
		{
			ID:               "snap_media_daily",
			Dataset:          "MEDIA-12TB",
			Recursive:        true,
			LifetimeValue:    2,
			LifetimeUnit:     "week(s)",
			NamingSchema:     "auto-%Y-%m-%d_%H-%M",
			Schedule:         "0 0 * * *",
			FrequencyText:    "At 00:00 (12:00 AM), every day,...",
			Enabled:          true,
			State:            "COMPLETED",
			LastRun:          lastRunDefault,
			LastMessage:      "Snapshot MEDIA-12TB@auto completed cleanly",
			SnapshotsCreated: 14,
		},
		{
			ID:               "snap_mail_daily",
			Dataset:          "SSD-MIRROR/mail-archiver",
			Recursive:        true,
			LifetimeValue:    2,
			LifetimeUnit:     "week(s)",
			NamingSchema:     "auto-%Y-%m-%d_%H-%M",
			Schedule:         "0 0 * * *",
			FrequencyText:    "At 00:00 (12:00 AM), every day,...",
			Enabled:          true,
			State:            "COMPLETED",
			LastRun:          lastRunDefault,
			LastMessage:      "Snapshot SSD-MIRROR/mail-archiver@auto completed cleanly",
			SnapshotsCreated: 14,
		},
	}

	for _, t := range seed {
		m.snapTasks[t.ID] = t
	}
	if !m.mockMode {
		m.saveSnapshotTasksConfigLocked()
	}
}

func (m *Manager) loadSnapshotTasksConfigLocked() bool {
	data, err := os.ReadFile(m.snapTaskConfigPath)
	if err != nil {
		return false
	}
	var list []*SnapshotTask
	if err := json.Unmarshal(data, &list); err != nil {
		return false
	}
	for _, t := range list {
		if t != nil && t.ID != "" {
			if t.State == "RUNNING" {
				t.State = "IDLE"
			}
			m.snapTasks[t.ID] = t
		}
	}
	return len(m.snapTasks) > 0
}

func (m *Manager) saveSnapshotTasksConfigLocked() {
	if m.mockMode {
		return
	}
	path := m.snapTaskConfigPath
	if path == "" {
		path = defaultSnapshotTasksPath
	}
	_ = os.MkdirAll(filepath.Dir(path), 0755)
	list := make([]*SnapshotTask, 0, len(m.snapTasks))
	for _, t := range m.snapTasks {
		list = append(list, t)
	}
	sort.Slice(list, func(i, j int) bool {
		return list[i].Dataset < list[j].Dataset
	})
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(path, data, 0600)
}

func cloneSnapshotTask(t *SnapshotTask) *SnapshotTask {
	if t == nil {
		return nil
	}
	cp := *t
	return &cp
}

// ListSnapshotTasks returns all configured Periodic Snapshot Tasks sorted by dataset.
func (m *Manager) ListSnapshotTasks() []*SnapshotTask {
	m.mu.RLock()
	defer m.mu.RUnlock()

	list := make([]*SnapshotTask, 0, len(m.snapTasks))
	for _, t := range m.snapTasks {
		list = append(list, cloneSnapshotTask(t))
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].Dataset == list[j].Dataset {
			return list[i].ID < list[j].ID
		}
		return list[i].Dataset < list[j].Dataset
	})
	return list
}

// SaveSnapshotTask validates and persists a new or updated Periodic Snapshot Task.
func (m *Manager) SaveSnapshotTask(task SnapshotTask) (*SnapshotTask, error) {
	task.ID = strings.TrimSpace(task.ID)
	task.Dataset = strings.TrimSpace(task.Dataset)
	task.NamingSchema = strings.TrimSpace(task.NamingSchema)
	task.Schedule = strings.TrimSpace(task.Schedule)
	task.FrequencyText = strings.TrimSpace(task.FrequencyText)

	if err := validate.DatasetName(task.Dataset); err != nil {
		return nil, err
	}
	if task.LifetimeValue <= 0 {
		task.LifetimeValue = 2
	}
	unit, err := normalizeLifetimeUnit(task.LifetimeUnit)
	if err != nil {
		return nil, err
	}
	task.LifetimeUnit = unit

	if task.NamingSchema == "" {
		task.NamingSchema = "auto-%Y-%m-%d_%H-%M"
	}
	sampleSnap := expandNamingSchema(task.NamingSchema, time.Now())
	if err := validate.SnapshotName(task.Dataset, sampleSnap); err != nil {
		return nil, fmt.Errorf("invalid naming schema '%s': %w", task.NamingSchema, err)
	}

	if task.Schedule == "" {
		task.Schedule = "0 0 * * *"
	}
	if err := validateCronSchedule(task.Schedule); err != nil {
		return nil, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.snapTasks == nil {
		m.snapTasks = make(map[string]*SnapshotTask)
	}
	if task.ID == "" {
		task.ID = generateTaskID()
	}

	if existing, ok := m.snapTasks[task.ID]; ok {
		if task.State == "" {
			task.State = existing.State
		}
		if task.LastRun.IsZero() {
			task.LastRun = existing.LastRun
		}
		if task.LastMessage == "" {
			task.LastMessage = existing.LastMessage
		}
		if task.SnapshotsCreated == 0 {
			task.SnapshotsCreated = existing.SnapshotsCreated
		}
		if task.LastRunText == "" {
			task.LastRunText = existing.LastRunText
		}
		if task.NextRunText == "" {
			task.NextRunText = existing.NextRunText
		}
	} else if task.State == "" {
		task.State = "IDLE"
	}

	saved := cloneSnapshotTask(&task)
	m.snapTasks[saved.ID] = saved
	m.saveSnapshotTasksConfigLocked()
	return cloneSnapshotTask(saved), nil
}

// ToggleSnapshotTask flips or explicitly sets the Enabled status of a task.
func (m *Manager) ToggleSnapshotTask(id string, enabled *bool) (*SnapshotTask, error) {
	id = strings.TrimSpace(id)
	m.mu.Lock()
	defer m.mu.Unlock()

	t, ok := m.snapTasks[id]
	if !ok {
		return nil, fmt.Errorf("snapshot task '%s' not found", id)
	}
	if enabled != nil {
		t.Enabled = *enabled
	} else {
		t.Enabled = !t.Enabled
	}
	m.saveSnapshotTasksConfigLocked()
	return cloneSnapshotTask(t), nil
}

// DeleteSnapshotTask removes a Periodic Snapshot Task by ID.
func (m *Manager) DeleteSnapshotTask(id string) error {
	id = strings.TrimSpace(id)
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.snapTasks[id]; !ok {
		return fmt.Errorf("snapshot task '%s' not found", id)
	}
	delete(m.snapTasks, id)
	m.saveSnapshotTasksConfigLocked()
	return nil
}

func (m *Manager) snapshotNameExists(dataset, snapName string) bool {
	snaps, err := m.ListSnapshots()
	if err != nil {
		return false
	}
	full := dataset + "@" + snapName
	for _, s := range snaps {
		if s.Name == full {
			return true
		}
	}
	return false
}

// RunSnapshotTask executes a Periodic Snapshot Task immediately, creating the snapshot and pruning expired ones.
func (m *Manager) RunSnapshotTask(id string) (*SnapshotTask, string, error) {
	id = strings.TrimSpace(id)

	m.mu.Lock()
	t, ok := m.snapTasks[id]
	if !ok {
		m.mu.Unlock()
		return nil, "", fmt.Errorf("snapshot task '%s' not found", id)
	}
	taskCopy := cloneSnapshotTask(t)
	t.State = "RUNNING"
	t.LastMessage = "Creating snapshot..."
	m.mu.Unlock()

	now := time.Now()
	snapName := expandNamingSchema(taskCopy.NamingSchema, now)
	if m.snapshotNameExists(taskCopy.Dataset, snapName) {
		snapName = fmt.Sprintf("%s-%02d", snapName, now.Second())
		if m.snapshotNameExists(taskCopy.Dataset, snapName) {
			snapName = fmt.Sprintf("%s-%d", snapName, now.UnixNano()%1000)
		}
	}

	createErr := m.CreateSnapshotRecursive(taskCopy.Dataset, snapName, taskCopy.Recursive)
	pruned := 0
	if createErr == nil {
		pruned = m.pruneExpiredSnapshots(taskCopy, now)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok = m.snapTasks[id]
	if !ok {
		return nil, "", fmt.Errorf("snapshot task '%s' was removed during execution", id)
	}
	t.LastRun = now
	if createErr != nil {
		t.State = "FAILED"
		t.LastMessage = createErr.Error()
		m.saveSnapshotTasksConfigLocked()
		return cloneSnapshotTask(t), "", createErr
	}

	t.State = "COMPLETED"
	t.SnapshotsCreated++
	if pruned > 0 {
		t.LastMessage = fmt.Sprintf("Created %s@%s and pruned %d expired snapshot(s)", t.Dataset, snapName, pruned)
	} else {
		t.LastMessage = fmt.Sprintf("Created snapshot %s@%s", t.Dataset, snapName)
	}
	m.saveSnapshotTasksConfigLocked()
	return cloneSnapshotTask(t), snapName, nil
}

func (m *Manager) pruneExpiredSnapshots(task *SnapshotTask, now time.Time) int {
	if task == nil || task.LifetimeValue <= 0 {
		return 0
	}
	snaps, err := m.ListSnapshots()
	if err != nil {
		return 0
	}
	prefix := namingSchemaPrefix(task.NamingSchema)
	maxAge := task.retentionDuration()
	pruned := 0

	for _, snap := range snaps {
		if snap.Created.IsZero() || now.Sub(snap.Created) <= maxAge {
			continue
		}
		if snap.Dataset != task.Dataset && !(task.Recursive && strings.HasPrefix(snap.Dataset, task.Dataset+"/")) {
			continue
		}
		atIdx := strings.Index(snap.Name, "@")
		if atIdx < 0 {
			continue
		}
		shortName := snap.Name[atIdx+1:]
		if prefix != "" && !strings.HasPrefix(shortName, prefix) {
			continue
		}
		if err := m.DestroySnapshot(snap.Name); err == nil {
			pruned++
		}
	}
	return pruned
}

func sameCronMinute(a, b time.Time) bool {
	if a.IsZero() {
		return false
	}
	return a.In(b.Location()).Truncate(time.Minute).Equal(b.Truncate(time.Minute))
}

func (m *Manager) runDueSnapshotTasks(now time.Time) int {
	tasks := m.ListSnapshotTasks()
	executed := 0
	for _, t := range tasks {
		if !t.Enabled || strings.ToUpper(t.State) == "RUNNING" {
			continue
		}
		// Avoid double trigger within the same calendar minute while never skipping adjacent minutes due to jitter.
		if sameCronMinute(t.LastRun, now) {
			continue
		}
		if matchesCronSchedule(t.Schedule, now) {
			if _, _, err := m.RunSnapshotTask(t.ID); err == nil {
				executed++
			}
		}
	}
	return executed
}

func (m *Manager) startSnapshotScheduler() {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for now := range ticker.C {
		m.runDueSnapshotTasks(now)
	}
}

