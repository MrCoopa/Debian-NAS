package shares

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"debian-nas/internal/validate"
)

func runCmdWithTimeout(timeout time.Duration, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	return cmd.CombinedOutput()
}

type ShareType string

const (
	TypeSMB ShareType = "SMB"
	TypeNFS ShareType = "NFS"
)

type SmbShare struct {
	Name        string   `json:"name"`
	Path        string   `json:"path"`
	Comment     string   `json:"comment"`
	ReadOnly    bool     `json:"read_only"`
	GuestOk     bool     `json:"guest_ok"`
	TimeMachine bool     `json:"time_machine"` // macOS Time Machine support
	ValidUsers  []string `json:"valid_users"`
	Enabled     bool     `json:"enabled"`
}

type NfsExport struct {
	Path           string `json:"path"`
	AllowedClients string `json:"allowed_clients"` // e.g. "192.168.1.0/24" or "*"
	AllowedHosts   string `json:"allowed_hosts"`   // frontend alias fallback
	Options        string `json:"options"`         // rw,sync,no_subtree_check
	ReadOnly       bool   `json:"read_only"`
	Enabled        bool   `json:"enabled"`
}

type StorageUser struct {
	Username string `json:"username"`
	Role     string `json:"role"`
}

type Manager struct {
	mu           sync.RWMutex
	mockMode     bool
	sambaConfDir string
	nfsConfFile  string
	smbShares    map[string]*SmbShare
	nfsExports   map[string]*NfsExport
	users        map[string]StorageUser
	runCmd       func(timeout time.Duration, name string, args ...string) ([]byte, error)
}

func NewManager(mockMode bool, sambaConfDir, nfsConfFile string) *Manager {
	m := &Manager{
		mockMode:     mockMode || runtime.GOOS != "linux",
		sambaConfDir: sambaConfDir,
		nfsConfFile:  nfsConfFile,
		smbShares:    make(map[string]*SmbShare),
		nfsExports:   make(map[string]*NfsExport),
		users:        make(map[string]StorageUser),
		runCmd:       runCmdWithTimeout,
	}

	if m.mockMode {
		m.initMockShares()
	} else {
		m.loadLinuxShares()
		m.loadLinuxUsers()
		m.ensureLinuxUserGroups()
		_ = m.syncSambaMasterConfig()
	}
	return m
}

func (m *Manager) initMockShares() {
	m.smbShares = map[string]*SmbShare{
		"Milena": {
			Name:        "Milena",
			Path:        "/mnt/DATA/Fileshare/Milena",
			Comment:     "Milenas Fileshare",
			ReadOnly:    false,
			GuestOk:     false,
			TimeMachine: false,
			ValidUsers:  []string{"milena", "admin"},
			Enabled:     true,
		},
		"Niklas": {
			Name:        "Niklas",
			Path:        "/mnt/DATA/Fileshare/Niklas",
			Comment:     "Niklas Fileshare",
			ReadOnly:    false,
			GuestOk:     false,
			TimeMachine: true,
			ValidUsers:  []string{"niklas", "admin"},
			Enabled:     true,
		},
	}

	m.nfsExports = map[string]*NfsExport{
		"/mnt/DATA/Backup": {
			Path:           "/mnt/DATA/Backup",
			AllowedClients: "192.168.2.0/24",
			Options:        "rw,sync,no_subtree_check",
			Enabled:        true,
		},
		"/mnt/MEDIA-12TB/data": {
			Path:           "/mnt/MEDIA-12TB/data",
			AllowedClients: "192.168.2.0/24",
			Options:        "rw,async,no_subtree_check",
			Enabled:        true,
		},
		"/mnt/SSD-MIRROR/mail-archiver": {
			Path:           "/mnt/SSD-MIRROR/mail-archiver",
			AllowedClients: "192.168.2.0/24",
			Options:        "rw,sync,no_subtree_check,no_root_squash",
			Enabled:        true,
		},
	}

	m.users = map[string]StorageUser{
		"root":   {Username: "root", Role: "admin"},
		"admin":  {Username: "admin", Role: "admin"},
		"niklas": {Username: "niklas", Role: "user"},
		"milena": {Username: "milena", Role: "user"},
	}
}

func (m *Manager) loadLinuxShares() {
	// 1. Read existing SMB shares from conf.d
	if entries, err := os.ReadDir(m.sambaConfDir); err == nil {
		for _, entry := range entries {
			if strings.HasSuffix(entry.Name(), ".conf") {
				shareName := strings.TrimSuffix(entry.Name(), ".conf")
				fullPath := filepath.Join(m.sambaConfDir, entry.Name())
				if share := m.parseSmbConfigFile(fullPath, shareName); share != nil {
					m.smbShares[shareName] = share
				}
			}
		}
	}

	// 2. Read existing NFS exports
	if f, err := os.Open(m.nfsConfFile); err == nil {
		defer f.Close()
		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				path := parts[0]
				clientOpt := parts[1]
				// clientOpt is e.g. 192.168.2.0/24(rw,sync,no_subtree_check)
				client := "*"
				opts := "rw,sync,no_subtree_check"
				if idx := strings.Index(clientOpt, "("); idx > 0 {
					client = clientOpt[:idx]
					opts = strings.TrimSuffix(clientOpt[idx+1:], ")")
				}
				m.nfsExports[path] = &NfsExport{
					Path:           path,
					AllowedClients: client,
					Options:        opts,
					ReadOnly:       strings.Contains(","+opts+",", ",ro,") || strings.HasPrefix(opts, "ro,") || opts == "ro",
					Enabled:        true,
				}
			}
		}
	}
}

func (m *Manager) parseSmbConfigFile(filePath, name string) *SmbShare {
	b, err := os.ReadFile(filePath)
	if err != nil {
		return nil
	}

	share := &SmbShare{Name: name, Enabled: true}
	scanner := bufio.NewScanner(bytes.NewReader(b))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "path") {
			parts := strings.Split(line, "=")
			if len(parts) >= 2 {
				share.Path = strings.TrimSpace(parts[1])
			}
		} else if strings.HasPrefix(line, "comment") {
			parts := strings.Split(line, "=")
			if len(parts) >= 2 {
				share.Comment = strings.TrimSpace(parts[1])
			}
		} else if strings.HasPrefix(line, "read only") {
			share.ReadOnly = strings.Contains(strings.ToLower(line), "yes")
		} else if strings.HasPrefix(line, "guest ok") {
			share.GuestOk = strings.Contains(strings.ToLower(line), "yes")
		} else if strings.Contains(line, "fruit:time machine = yes") {
			share.TimeMachine = true
		} else if strings.HasPrefix(line, "valid users") {
			parts := strings.Split(line, "=")
			if len(parts) >= 2 {
				raw := strings.Fields(strings.TrimSpace(parts[1]))
				var uList []string
				for _, u := range raw {
					if u != "@nasusers" && u != "" {
						uList = append(uList, u)
					}
				}
				share.ValidUsers = uList
			}
		}
	}
	return share
}

func (m *Manager) ListSmb() ([]*SmbShare, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var list []*SmbShare
	for _, s := range m.smbShares {
		list = append(list, s)
	}
	sort.Slice(list, func(i, j int) bool {
		return list[i].Name < list[j].Name
	})
	return list, nil
}

func (m *Manager) ListNfs() ([]*NfsExport, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var list []*NfsExport
	for _, e := range m.nfsExports {
		list = append(list, e)
	}
	sort.Slice(list, func(i, j int) bool {
		return list[i].Path < list[j].Path
	})
	return list, nil
}

// normalizeMainSmbConf keeps all [global] settings inside [global] and places the
// nas-shares.conf include immediately after the [global] section (before any
// subsequent [homes]/[printers] sections) so [global] parameters never bleed into shares.
func normalizeMainSmbConf(content string) string {
	rawLines := strings.Split(content, "\n")
	var cleanLines []string
	inGlobal := false
	for _, line := range rawLines {
		trimmed := strings.TrimSpace(line)
		lower := strings.ToLower(trimmed)
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			inGlobal = strings.EqualFold(trimmed, "[global]")
		}
		if strings.HasPrefix(lower, "include = /etc/samba/nas-shares.conf") ||
			strings.HasPrefix(lower, "include = /etc/samba/conf.d/") ||
			strings.Contains(trimmed, "Auto-include Debian NAS shares") ||
			strings.Contains(trimmed, "Dynamic Debian NAS shares") {
			continue
		}
		if inGlobal && (lower == "map to guest = bad user" ||
			lower == "usershare allow guests = yes" ||
			lower == "browseable = yes") {
			continue
		}
		cleanLines = append(cleanLines, line)
	}

	globalIdx := -1
	for i, line := range cleanLines {
		if strings.EqualFold(strings.TrimSpace(line), "[global]") {
			globalIdx = i
			break
		}
	}
	if globalIdx == -1 {
		cleanLines = append([]string{
			"[global]",
			"   workgroup = WORKGROUP",
			"   server role = standalone server",
		}, cleanLines...)
		globalIdx = 0
	}

	globalOpts := []string{
		"   map to guest = Bad User",
		"   usershare allow guests = yes",
	}

	nextSectionIdx := len(cleanLines)
	for i := globalIdx + 1; i < len(cleanLines); i++ {
		trimmed := strings.TrimSpace(cleanLines[i])
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			nextSectionIdx = i
			break
		}
	}

	var out []string
	out = append(out, cleanLines[:globalIdx+1]...)
	out = append(out, globalOpts...)
	globalBody := cleanLines[globalIdx+1 : nextSectionIdx]
	for len(globalBody) > 0 && strings.TrimSpace(globalBody[len(globalBody)-1]) == "" {
		globalBody = globalBody[:len(globalBody)-1]
	}
	out = append(out, globalBody...)
	out = append(out, "")
	out = append(out, "# Dynamic Debian NAS shares include")
	out = append(out, "include = /etc/samba/nas-shares.conf")
	out = append(out, cleanLines[nextSectionIdx:]...)

	return strings.TrimRight(strings.Join(out, "\n"), "\n") + "\n"
}

// syncSambaMasterConfig writes all active SMB shares into /etc/samba/nas-shares.conf
// and ensures /etc/samba/smb.conf cleanly includes it after [global].
func (m *Manager) syncSambaMasterConfig() error {
	if m.mockMode {
		return nil
	}

	masterIncludePath := "/etc/samba/nas-shares.conf"
	var sb strings.Builder
	sb.WriteString("# =====================================================================\n")
	sb.WriteString("# Auto-generated by Debian NAS Daemon (nasd) - DO NOT EDIT MANUALLY\n")
	sb.WriteString("# =====================================================================\n\n")

	var names []string
	for name := range m.smbShares {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		s := m.smbShares[name]
		if s == nil {
			continue
		}
		sb.WriteString(m.generateSmbConfig(s))
		sb.WriteString("\n")
	}

	if err := os.WriteFile(masterIncludePath, []byte(sb.String()), 0644); err != nil {
		return fmt.Errorf("failed to write %s: %w", masterIncludePath, err)
	}

	smbConfPath := "/etc/samba/smb.conf"
	if data, err := os.ReadFile(smbConfPath); err == nil {
		updated := normalizeMainSmbConf(string(data))
		if updated != string(data) {
			_ = os.WriteFile(smbConfPath, []byte(updated), 0644)
		}
	}

	// Validate with testparm if available
	if _, err := exec.LookPath("testparm"); err == nil {
		_, _ = runCmdWithTimeout(5*time.Second, "testparm", "-s")
	}

	// Reload smbd and nmbd so Windows clients see changes immediately
	_, _ = runCmdWithTimeout(5*time.Second, "smbcontrol", "all", "reload-config")
	if out, err := runCmdWithTimeout(10*time.Second, "systemctl", "reload-or-restart", "smbd"); err != nil {
		return fmt.Errorf("failed to reload Samba (smbd): %v (%s)", err, strings.TrimSpace(string(out)))
	}
	_, _ = runCmdWithTimeout(5*time.Second, "systemctl", "reload-or-restart", "nmbd")
	return nil
}

func (m *Manager) execCmd(timeout time.Duration, name string, args ...string) ([]byte, error) {
	if m.runCmd != nil {
		return m.runCmd(timeout, name, args...)
	}
	return runCmdWithTimeout(timeout, name, args...)
}

// PathOverlapsMount reports whether a share/export path sits on, beneath, or encompasses a dataset mountpoint.
func PathOverlapsMount(sharePath, mountpoint string) bool {
	sp := strings.TrimRight(filepath.ToSlash(filepath.Clean(strings.TrimSpace(sharePath))), "/")
	mp := strings.TrimRight(filepath.ToSlash(filepath.Clean(strings.TrimSpace(mountpoint))), "/")
	if sp == "" || mp == "" || !strings.HasPrefix(sp, "/") || !strings.HasPrefix(mp, "/") {
		return false
	}
	return sp == mp || strings.HasPrefix(sp, mp+"/") || strings.HasPrefix(mp, sp+"/")
}

// SharesMatchingMountpoints returns SMB share names (sorted) and whether any NFS export overlaps the given mountpoints.
func (m *Manager) SharesMatchingMountpoints(mountpoints []string) ([]string, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var smbNames []string
	for name, s := range m.smbShares {
		if s == nil {
			continue
		}
		for _, mp := range mountpoints {
			if PathOverlapsMount(s.Path, mp) {
				smbNames = append(smbNames, name)
				break
			}
		}
	}
	sort.Strings(smbNames)

	hasNfs := false
	for expPath := range m.nfsExports {
		for _, mp := range mountpoints {
			if PathOverlapsMount(expPath, mp) {
				hasNfs = true
				break
			}
		}
		if hasNfs {
			break
		}
	}
	return smbNames, hasNfs
}

// ReleaseShareConnections cleanly closes active client sessions for the listed SMB shares so ZFS can unmount without lazy-detach.
func (m *Manager) ReleaseShareConnections(smbNames []string) {
	if m.mockMode || len(smbNames) == 0 {
		return
	}
	m.closeSambaShares(smbNames)
}

func (m *Manager) closeSambaShares(smbNames []string) {
	seen := make(map[string]bool, len(smbNames))
	for _, rawName := range smbNames {
		name := strings.TrimSpace(rawName)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		// Try smbd first, then all smb daemons in case worker routing needs broadcast.
		_, _ = m.execCmd(5*time.Second, "smbcontrol", "smbd", "close-share", name)
		_, _ = m.execCmd(5*time.Second, "smbcontrol", "all", "close-share", name)
	}
}

func (m *Manager) applySmbDirPermissions(s *SmbShare, recursive bool) error {
	if s == nil {
		return nil
	}
	if err := os.MkdirAll(s.Path, 0775); err != nil {
		return fmt.Errorf("failed to create share directory %s: %w", s.Path, err)
	}

	// Ensure parent mount dirs under /mnt/ have traverse (+x) permission so Samba users can enter the share.
	for p := filepath.Dir(s.Path); strings.HasPrefix(filepath.ToSlash(p), "/mnt/"); p = filepath.Dir(p) {
		if info, err := os.Stat(p); err == nil && info.IsDir() {
			if info.Mode().Perm()&0001 == 0 {
				_, _ = m.execCmd(5*time.Second, "chmod", "a+x", p)
			}
		}
	}

	_, _ = m.execCmd(5*time.Second, "chgrp", "nasusers", s.Path)
	if s.ReadOnly {
		_, _ = m.execCmd(5*time.Second, "chmod", "0755", s.Path)
		return nil
	}
	_, _ = m.execCmd(5*time.Second, "chmod", "2775", s.Path)
	if !recursive {
		return nil
	}

	// Repair existing subfolders/files so imported pools or root-created subdirs become writable for @nasusers.
	done := make(chan struct{})
	go func(targetPath string) {
		defer close(done)
		_, _ = m.execCmd(60*time.Second, "chgrp", "-R", "nasusers", targetPath)
		_, _ = m.execCmd(60*time.Second, "chmod", "-R", "g+rwX", targetPath)
		_, _ = m.execCmd(60*time.Second, "find", targetPath, "-type", "d", "-exec", "chmod", "g+s", "{}", "+")
	}(s.Path)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		// Large directory tree continues applying permissions in background without blocking HTTP UI
	}
	return nil
}

// RefreshSharesAfterRollback ensures Samba/NFS clients reconnect to the newly rolled-back filesystem mount
// instead of holding stale directory handles or cached SMB2 listings.
func (m *Manager) RefreshSharesAfterRollback(smbNames []string, hasNfs bool) {
	if m.mockMode {
		return
	}
	m.mu.RLock()
	sharesSnapshot := make([]*SmbShare, 0, len(smbNames))
	for _, name := range smbNames {
		if s := m.smbShares[name]; s != nil {
			cp := *s
			sharesSnapshot = append(sharesSnapshot, &cp)
		}
	}
	m.mu.RUnlock()

	for _, s := range sharesSnapshot {
		_ = m.applySmbDirPermissions(s, false)
	}
	if len(smbNames) > 0 {
		m.closeSambaShares(smbNames)
		_, _ = m.execCmd(5*time.Second, "smbcontrol", "all", "reload-config")
		_, _ = m.execCmd(10*time.Second, "systemctl", "reload-or-restart", "smbd")
	}
	if hasNfs {
		_, _ = m.execCmd(10*time.Second, "exportfs", "-ra")
	}
}

func (m *Manager) SaveSmb(s *SmbShare) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := validate.ShareName(s.Name); err != nil {
		return fmt.Errorf("invalid share name: %w", err)
	}
	if err := validate.SharePath(s.Path); err != nil {
		return fmt.Errorf("invalid share path: %w", err)
	}
	if err := validate.ShareComment(s.Comment); err != nil {
		return fmt.Errorf("invalid share comment: %w", err)
	}
	for _, u := range s.ValidUsers {
		if err := validate.Username(u); err != nil {
			return fmt.Errorf("invalid user in valid_users: %w", err)
		}
	}

	s.Path = filepath.ToSlash(filepath.Clean(s.Path))
	s.Enabled = true
	m.smbShares[s.Name] = s

	if m.mockMode {
		return nil
	}

	if err := os.MkdirAll(m.sambaConfDir, 0755); err != nil {
		return fmt.Errorf("failed to create samba conf.d dir: %w", err)
	}

	// Ensure nasusers group exists and all assigned users belong to nasusers
	_, _ = m.execCmd(5*time.Second, "groupadd", "-f", "nasusers")
	for _, u := range s.ValidUsers {
		_, _ = m.execCmd(5*time.Second, "usermod", "-aG", "nasusers", u)
	}

	if err := m.applySmbDirPermissions(s, true); err != nil {
		return err
	}

	confPath := filepath.Join(m.sambaConfDir, fmt.Sprintf("%s.conf", s.Name))
	if filepath.Dir(filepath.Clean(confPath)) != filepath.Clean(m.sambaConfDir) {
		return fmt.Errorf("path traversal detected in share name")
	}

	content := m.generateSmbConfig(s)
	tmpPath := confPath + ".tmp"
	if err := os.WriteFile(tmpPath, []byte(content), 0644); err != nil {
		return fmt.Errorf("failed to write samba share config: %w", err)
	}

	if err := os.Rename(tmpPath, confPath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("failed to commit samba share config: %w", err)
	}

	return m.syncSambaMasterConfig()
}

func (m *Manager) DeleteSmb(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := validate.ShareName(name); err != nil {
		return fmt.Errorf("invalid share name: %w", err)
	}

	delete(m.smbShares, name)

	if m.mockMode {
		return nil
	}

	confPath := filepath.Join(m.sambaConfDir, fmt.Sprintf("%s.conf", name))
	if filepath.Dir(filepath.Clean(confPath)) == filepath.Clean(m.sambaConfDir) {
		_ = os.Remove(confPath)
	}

	err := m.syncSambaMasterConfig()
	// Forcibly disconnect active SMB clients holding files/directories open on this share.
	_, _ = runCmdWithTimeout(5*time.Second, "smbcontrol", "smbd", "close-share", name)
	return err
}

func (m *Manager) generateSmbConfig(s *SmbShare) string {
	readOnly := "no"
	writable := "yes"
	if s.ReadOnly {
		readOnly = "yes"
		writable = "no"
	}
	guestOk := "no"
	if s.GuestOk {
		guestOk = "yes"
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("# Managed by Debian NAS\n[%s]\n", s.Name))
	sb.WriteString(fmt.Sprintf("   path = %s\n", s.Path))
	if s.Comment != "" {
		sb.WriteString(fmt.Sprintf("   comment = %s\n", s.Comment))
	}
	sb.WriteString(fmt.Sprintf("   read only = %s\n", readOnly))
	sb.WriteString(fmt.Sprintf("   writable = %s\n", writable))
	sb.WriteString(fmt.Sprintf("   guest ok = %s\n", guestOk))
	sb.WriteString(fmt.Sprintf("   public = %s\n", guestOk))
	sb.WriteString("   browseable = yes\n")
	sb.WriteString("   create mask = 0664\n")
	sb.WriteString("   directory mask = 2775\n")
	sb.WriteString("   force create mode = 0664\n")
	sb.WriteString("   force directory mode = 0775\n")
	sb.WriteString("   inherit permissions = yes\n")
	if s.GuestOk {
		sb.WriteString("   force user = root\n")
		sb.WriteString("   force group = nasusers\n")
	} else {
		sb.WriteString("   force group = nasusers\n")
		if len(s.ValidUsers) > 0 {
			usersStr := strings.Join(s.ValidUsers, " ")
			sb.WriteString(fmt.Sprintf("   valid users = %s\n", usersStr))
			if !s.ReadOnly {
				sb.WriteString(fmt.Sprintf("   write list = %s\n", usersStr))
			}
		} else {
			sb.WriteString("   valid users = @nasusers\n")
			if !s.ReadOnly {
				sb.WriteString("   write list = @nasusers\n")
			}
		}
	}

	if s.TimeMachine {
		sb.WriteString("   vfs objects = catia fruit streams_xattr\n")
		sb.WriteString("   fruit:time machine = yes\n")
		sb.WriteString("   fruit:aapl = yes\n")
	}

	return sb.String()
}

func (m *Manager) SaveNfs(e *NfsExport) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := validate.SharePath(e.Path); err != nil {
		return fmt.Errorf("invalid export path: %w", err)
	}

	if e.AllowedClients == "" && e.AllowedHosts != "" {
		e.AllowedClients = e.AllowedHosts
	}
	if e.AllowedClients == "" {
		e.AllowedClients = "*"
	}
	if err := validate.NfsClient(e.AllowedClients); err != nil {
		return fmt.Errorf("invalid NFS client: %w", err)
	}

	if e.Options != "" {
		if err := validate.NfsOptions(e.Options); err != nil {
			return fmt.Errorf("invalid NFS options: %w", err)
		}
	}

	// Normalise options and enforce ReadOnly consistency and root_squash
	options := e.Options
	if options == "" {
		if e.ReadOnly {
			options = "ro,sync,no_subtree_check,root_squash"
		} else {
			options = "rw,sync,no_subtree_check,root_squash"
		}
	} else if e.ReadOnly {
		options = strings.ReplaceAll(options, "rw,", "ro,")
		if strings.HasSuffix(options, ",rw") {
			options = strings.TrimSuffix(options, ",rw") + ",ro"
		} else if options == "rw" {
			options = "ro"
		}
	}
	e.Options = options
	e.Path = filepath.ToSlash(filepath.Clean(e.Path))

	m.nfsExports[e.Path] = e

	if m.mockMode {
		return nil
	}

	// Ensure folder exists
	if _, err := os.Stat(e.Path); os.IsNotExist(err) {
		_ = os.MkdirAll(e.Path, 0775)
	}

	return m.writeNfsExportsLocked()
}

func (m *Manager) DeleteNfs(path string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	path = filepath.ToSlash(filepath.Clean(path))
	delete(m.nfsExports, path)

	if m.mockMode {
		return nil
	}

	return m.writeNfsExportsLocked()
}

func (m *Manager) writeNfsExportsLocked() error {
	if err := os.MkdirAll(filepath.Dir(m.nfsConfFile), 0755); err != nil {
		return fmt.Errorf("failed to create nfs conf dir: %w", err)
	}

	var sb strings.Builder
	sb.WriteString("# Managed by Debian NAS\n")
	for _, exp := range m.nfsExports {
		if exp.Enabled {
			options := exp.Options
			if options == "" {
				if exp.ReadOnly {
					options = "ro,sync,no_subtree_check,no_root_squash"
				} else {
					options = "rw,sync,no_subtree_check,no_root_squash"
				}
			}
			clients := exp.AllowedClients
			if clients == "" {
				clients = exp.AllowedHosts
			}
			if clients == "" {
				clients = "*"
			}
			sb.WriteString(fmt.Sprintf("%s %s(%s)\n", exp.Path, clients, options))
		}
	}

	tmpFile := m.nfsConfFile + ".tmp"
	if err := os.WriteFile(tmpFile, []byte(sb.String()), 0644); err != nil {
		return fmt.Errorf("failed to write nfs exports file: %w", err)
	}
	if err := os.Rename(tmpFile, m.nfsConfFile); err != nil {
		_ = os.Remove(tmpFile)
		return fmt.Errorf("failed to commit nfs exports file: %w", err)
	}

	// Reload NFS kernel exports
	out, err := runCmdWithTimeout(10*time.Second, "exportfs", "-ra")
	if err != nil {
		return fmt.Errorf("exportfs failed: %v (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (m *Manager) loadLinuxUsers() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "pdbedit", "-L")
	out, err := cmd.Output()
	if err == nil {
		scanner := bufio.NewScanner(bytes.NewReader(out))
		for scanner.Scan() {
			line := scanner.Text()
			parts := strings.Split(line, ":")
			if len(parts) >= 1 && parts[0] != "" {
				role := "user"
				if parts[0] == "root" || parts[0] == "admin" {
					role = "admin"
				}
				m.users[parts[0]] = StorageUser{Username: parts[0], Role: role}
			}
		}
	}
	if len(m.users) == 0 {
		m.users["admin"] = StorageUser{Username: "admin", Role: "admin"}
	}
}

func (m *Manager) ensureLinuxUserGroups() {
	if m.mockMode {
		return
	}
	_, _ = runCmdWithTimeout(5*time.Second, "groupadd", "-f", "nasusers")
	for username := range m.users {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		err := exec.CommandContext(ctx, "id", username).Run()
		cancel()
		if err == nil {
			_, _ = runCmdWithTimeout(5*time.Second, "usermod", "-aG", "nasusers", username)
		}
	}
	for _, s := range m.smbShares {
		if s != nil && s.Path != "" && !s.ReadOnly {
			if info, err := os.Stat(s.Path); err == nil && info.IsDir() {
				_, _ = runCmdWithTimeout(5*time.Second, "chgrp", "nasusers", s.Path)
				_, _ = runCmdWithTimeout(5*time.Second, "chmod", "2775", s.Path)
			}
		}
	}
}

func (m *Manager) ListUsers() ([]StorageUser, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var list []StorageUser
	for _, u := range m.users {
		list = append(list, u)
	}
	sort.Slice(list, func(i, j int) bool {
		return list[i].Username < list[j].Username
	})
	return list, nil
}

func (m *Manager) CreateUser(username, password, role string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	username = strings.ToLower(strings.TrimSpace(username))
	if err := validate.Username(username); err != nil {
		return fmt.Errorf("invalid username: %w", err)
	}
	if err := validate.Password(password); err != nil {
		return fmt.Errorf("invalid password: %w", err)
	}
	if role == "" {
		role = "user"
	}

	if m.mockMode {
		m.users[username] = StorageUser{Username: username, Role: role}
		return nil
	}

	// 1. Ensure dedicated 'nasusers' group exists
	_, _ = runCmdWithTimeout(5*time.Second, "groupadd", "-f", "nasusers")

	// 2. Ensure Linux system user exists with nologin shell and nasusers group
	ctxId, cancelId := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelId()
	idCmd := exec.CommandContext(ctxId, "id", username)
	if err := idCmd.Run(); err != nil {
		nologin := "/usr/sbin/nologin"
		if _, err := os.Stat("/usr/sbin/nologin"); err != nil {
			if _, err2 := os.Stat("/sbin/nologin"); err2 == nil {
				nologin = "/sbin/nologin"
			} else {
				nologin = "/bin/false"
			}
		}
		uOut, uErr := runCmdWithTimeout(5*time.Second, "useradd", "-M", "-s", nologin, "-G", "nasusers", "-c", "NAS Storage User", username)
		if uErr != nil {
			outStr := strings.TrimSpace(string(uOut))
			if !strings.Contains(outStr, "already exists") && !strings.Contains(outStr, "existiert bereits") {
				return fmt.Errorf("useradd failed: %v (%s)", uErr, outStr)
			}
		}
	}
	// Always ensure even pre-existing Linux accounts belong to nasusers
	_, _ = runCmdWithTimeout(5*time.Second, "usermod", "-aG", "nasusers", username)

	// 3. Set Samba password via smbpasswd (no shadow / chpasswd needed for nologin storage users)
	if _, err := exec.LookPath("smbpasswd"); err != nil {
		m.users[username] = StorageUser{Username: username, Role: role}
		return nil
	}

	ctxPw, cancelPw := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelPw()
	cmd := exec.CommandContext(ctxPw, "smbpasswd", "-a", "-s", username)
	cmd.Stdin = strings.NewReader(fmt.Sprintf("%s\n%s\n", password, password))
	out, err := cmd.CombinedOutput()
	if err != nil {
		outStr := strings.TrimSpace(string(out))
		if strings.Contains(outStr, "Failed to add") || strings.Contains(outStr, "already exists") {
			ctxUp, cancelUp := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancelUp()
			updateCmd := exec.CommandContext(ctxUp, "smbpasswd", "-s", username)
			updateCmd.Stdin = strings.NewReader(fmt.Sprintf("%s\n%s\n", password, password))
			if uOut, uErr := updateCmd.CombinedOutput(); uErr != nil {
				return fmt.Errorf("smbpasswd update failed: %v (%s)", uErr, strings.TrimSpace(string(uOut)))
			}
		} else {
			return fmt.Errorf("smbpasswd failed: %v (%s)", err, outStr)
		}
	}

	// 4. Ensure user is enabled in Samba
	_, _ = runCmdWithTimeout(5*time.Second, "smbpasswd", "-e", username)

	m.users[username] = StorageUser{Username: username, Role: role}
	return nil
}

func (m *Manager) DeleteUser(username string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	username = strings.ToLower(strings.TrimSpace(username))
	if err := validate.Username(username); err != nil {
		return fmt.Errorf("invalid username: %w", err)
	}

	delete(m.users, username)

	if m.mockMode {
		return nil
	}

	// Safety check: verify UID >= 1000 to prevent accidental deletion of system daemons
	uidOut, uidErr := runCmdWithTimeout(5*time.Second, "id", "-u", username)
	if uidErr == nil {
		var uid int
		if _, err := fmt.Sscanf(strings.TrimSpace(string(uidOut)), "%d", &uid); err == nil && uid < 1000 {
			return fmt.Errorf("refusing to delete system account with UID %d", uid)
		}
	}

	_, _ = runCmdWithTimeout(5*time.Second, "smbpasswd", "-x", username)
	out, err := runCmdWithTimeout(10*time.Second, "userdel", username)
	if err != nil {
		outStr := strings.TrimSpace(string(out))
		if !strings.Contains(outStr, "does not exist") && !strings.Contains(outStr, "existiert nicht") {
			return fmt.Errorf("userdel failed: %v (%s)", err, outStr)
		}
	}
	return nil
}

// SetUserPassword updates the password for an existing storage user and syncs with Samba.
func (m *Manager) SetUserPassword(username, password string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	username = strings.ToLower(strings.TrimSpace(username))
	if err := validate.Username(username); err != nil {
		return fmt.Errorf("invalid username: %w", err)
	}
	if err := validate.Password(password); err != nil {
		return fmt.Errorf("invalid password: %w", err)
	}

	if m.mockMode {
		if u, ok := m.users[username]; ok {
			m.users[username] = u
		} else {
			m.users[username] = StorageUser{Username: username, Role: "user"}
		}
		return nil
	}

	// 1. Ensure nasusers group exists and user is member
	_, _ = runCmdWithTimeout(5*time.Second, "groupadd", "-f", "nasusers")
	_, _ = runCmdWithTimeout(5*time.Second, "usermod", "-aG", "nasusers", username)

	// 2. Set Samba password via smbpasswd
	ctxPw, cancelPw := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelPw()
	cmd := exec.CommandContext(ctxPw, "smbpasswd", "-a", "-s", username)
	cmd.Stdin = strings.NewReader(fmt.Sprintf("%s\n%s\n", password, password))
	out, err := cmd.CombinedOutput()
	if err != nil {
		outStr := strings.TrimSpace(string(out))
		if strings.Contains(outStr, "Failed to add") || strings.Contains(outStr, "already exists") {
			ctxUp, cancelUp := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancelUp()
			updateCmd := exec.CommandContext(ctxUp, "smbpasswd", "-s", username)
			updateCmd.Stdin = strings.NewReader(fmt.Sprintf("%s\n%s\n", password, password))
			if uOut, uErr := updateCmd.CombinedOutput(); uErr != nil {
				return fmt.Errorf("smbpasswd update failed: %v (%s)", uErr, strings.TrimSpace(string(uOut)))
			}
		} else {
			return fmt.Errorf("smbpasswd failed: %v (%s)", err, outStr)
		}
	}

	// 3. Ensure user is enabled in Samba
	_, _ = runCmdWithTimeout(5*time.Second, "smbpasswd", "-e", username)

	if _, ok := m.users[username]; !ok {
		m.users[username] = StorageUser{Username: username, Role: "user"}
	}
	return nil
}

// FixSharePermissions applies appropriate ownership and group permissions on the share path.
func (m *Manager) FixSharePermissions(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.smbShares[name]
	if !ok || s == nil {
		return fmt.Errorf("share '%s' not found", name)
	}

	if m.mockMode {
		return nil
	}

	// 1. Ensure group nasusers exists and valid users are members
	_, _ = runCmdWithTimeout(5*time.Second, "groupadd", "-f", "nasusers")
	for _, u := range s.ValidUsers {
		_, _ = runCmdWithTimeout(5*time.Second, "usermod", "-aG", "nasusers", u)
	}

	// 2. Ensure parent dirs under /mnt/ are traversable (chmod a+x)
	for p := filepath.Dir(s.Path); strings.HasPrefix(filepath.ToSlash(p), "/mnt/"); p = filepath.Dir(p) {
		if info, err := os.Stat(p); err == nil && info.IsDir() {
			_, _ = m.execCmd(5*time.Second, "chmod", "a+x", p)
		}
	}

	// 3. Primary owner: single user if assigned, else root
	primaryUser := "root"
	if len(s.ValidUsers) == 1 && s.ValidUsers[0] != "" {
		primaryUser = s.ValidUsers[0]
	}

	_, _ = m.execCmd(15*time.Second, "chown", "-R", primaryUser+":nasusers", s.Path)
	_, _ = m.execCmd(15*time.Second, "chmod", "-R", "2775", s.Path)

	// Reload Samba so clients see permissions immediately
	_, _ = m.execCmd(5*time.Second, "smbcontrol", "all", "reload-config")
	_, _ = m.execCmd(10*time.Second, "systemctl", "reload-or-restart", "smbd")

	return nil
}
