package shares

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

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
	Options        string `json:"options"`         // rw,sync,no_subtree_check
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
}

func NewManager(mockMode bool, sambaConfDir, nfsConfFile string) *Manager {
	m := &Manager{
		mockMode:     mockMode || runtime.GOOS != "linux",
		sambaConfDir: sambaConfDir,
		nfsConfFile:  nfsConfFile,
		smbShares:    make(map[string]*SmbShare),
		nfsExports:   make(map[string]*NfsExport),
		users:        make(map[string]StorageUser),
	}

	if m.mockMode {
		m.initMockShares()
	} else {
		m.loadLinuxShares()
		m.loadLinuxUsers()
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
		"milena": {Username: "milena", Role: "user"},
		"niklas": {Username: "niklas", Role: "user"},
		"admin":  {Username: "admin", Role: "admin"},
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
				share.ValidUsers = strings.Fields(strings.TrimSpace(parts[1]))
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
	return list, nil
}

func (m *Manager) ListNfs() ([]*NfsExport, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var list []*NfsExport
	for _, e := range m.nfsExports {
		list = append(list, e)
	}
	return list, nil
}

func (m *Manager) SaveSmb(s *SmbShare) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if s.Name == "" || s.Path == "" {
		return fmt.Errorf("share name and path are required")
	}

	m.smbShares[s.Name] = s

	if m.mockMode {
		return nil
	}

	if err := os.MkdirAll(m.sambaConfDir, 0755); err != nil {
		return err
	}

	// Ensure share folder exists
	_ = os.MkdirAll(s.Path, 0775)

	confPath := filepath.Join(m.sambaConfDir, fmt.Sprintf("%s.conf", s.Name))
	content := m.generateSmbConfig(s)

	if err := os.WriteFile(confPath, []byte(content), 0644); err != nil {
		return err
	}

	// Reload smbd
	_ = exec.Command("smbcontrol", "all", "reload-config").Run()
	_ = exec.Command("systemctl", "reload", "smbd").Run()
	return nil
}

func (m *Manager) DeleteSmb(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	delete(m.smbShares, name)

	if m.mockMode {
		return nil
	}

	confPath := filepath.Join(m.sambaConfDir, fmt.Sprintf("%s.conf", name))
	_ = os.Remove(confPath)

	_ = exec.Command("smbcontrol", "all", "reload-config").Run()
	_ = exec.Command("systemctl", "reload", "smbd").Run()
	return nil
}

func (m *Manager) generateSmbConfig(s *SmbShare) string {
	readOnly := "no"
	if s.ReadOnly {
		readOnly = "yes"
	}
	guestOk := "no"
	if s.GuestOk {
		guestOk = "yes"
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("[%s]\n", s.Name))
	sb.WriteString(fmt.Sprintf("   path = %s\n", s.Path))
	if s.Comment != "" {
		sb.WriteString(fmt.Sprintf("   comment = %s\n", s.Comment))
	}
	sb.WriteString(fmt.Sprintf("   read only = %s\n", readOnly))
	sb.WriteString(fmt.Sprintf("   guest ok = %s\n", guestOk))
	sb.WriteString("   browseable = yes\n")
	sb.WriteString("   create mask = 0664\n")
	sb.WriteString("   directory mask = 0775\n")

	if s.TimeMachine {
		sb.WriteString("   vfs objects = catia fruit streams_xattr\n")
		sb.WriteString("   fruit:time machine = yes\n")
		sb.WriteString("   fruit:aapl = yes\n")
	}

	if len(s.ValidUsers) > 0 {
		sb.WriteString(fmt.Sprintf("   valid users = %s\n", strings.Join(s.ValidUsers, " ")))
	}

	return sb.String()
}

func (m *Manager) SaveNfs(e *NfsExport) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if e.Path == "" {
		return fmt.Errorf("export path is required")
	}

	m.nfsExports[e.Path] = e

	if m.mockMode {
		return nil
	}

	// Ensure folder exists
	_ = os.MkdirAll(e.Path, 0755)

	if err := os.MkdirAll(filepath.Dir(m.nfsConfFile), 0755); err != nil {
		return err
	}

	var sb strings.Builder
	for _, exp := range m.nfsExports {
		if exp.Enabled {
			options := exp.Options
			if options == "" {
				options = "rw,sync,no_subtree_check"
			}
			clients := exp.AllowedClients
			if clients == "" {
				clients = "*"
			}
			sb.WriteString(fmt.Sprintf("%s %s(%s)\n", exp.Path, clients, options))
		}
	}

	if err := os.WriteFile(m.nfsConfFile, []byte(sb.String()), 0644); err != nil {
		return err
	}

	// Reload NFS kernel exports
	return exec.Command("exportfs", "-ra").Run()
}

func (m *Manager) DeleteNfs(path string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	delete(m.nfsExports, path)

	if m.mockMode {
		return nil
	}

	var sb strings.Builder
	for _, exp := range m.nfsExports {
		if exp.Enabled {
			options := exp.Options
			if options == "" {
				options = "rw,sync,no_subtree_check"
			}
			clients := exp.AllowedClients
			if clients == "" {
				clients = "*"
			}
			sb.WriteString(fmt.Sprintf("%s %s(%s)\n", exp.Path, clients, options))
		}
	}

	_ = os.WriteFile(m.nfsConfFile, []byte(sb.String()), 0644)
	return exec.Command("exportfs", "-ra").Run()
}

func (m *Manager) loadLinuxUsers() {
	cmd := exec.Command("pdbedit", "-L")
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

func (m *Manager) ListUsers() ([]StorageUser, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var list []StorageUser
	for _, u := range m.users {
		list = append(list, u)
	}
	return list, nil
}

func (m *Manager) CreateUser(username, password, role string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	username = strings.ToLower(strings.TrimSpace(username))
	if username == "" {
		return fmt.Errorf("username is required")
	}
	if role == "" {
		role = "user"
	}

	if m.mockMode {
		m.users[username] = StorageUser{Username: username, Role: role}
		return nil
	}

	// 1. Ensure Linux system user exists
	idCmd := exec.Command("id", username)
	if err := idCmd.Run(); err != nil {
		// User does not exist, create it with nologin shell
		nologin := "/usr/sbin/nologin"
		if _, err := os.Stat("/usr/sbin/nologin"); err != nil {
			if _, err2 := os.Stat("/sbin/nologin"); err2 == nil {
				nologin = "/sbin/nologin"
			} else {
				nologin = "/bin/false"
			}
		}
		useraddCmd := exec.Command("useradd", "-M", "-s", nologin, username)
		if uOut, uErr := useraddCmd.CombinedOutput(); uErr != nil {
			if !strings.Contains(string(uOut), "already exists") {
				return fmt.Errorf("useradd failed: %v (%s)", uErr, strings.TrimSpace(string(uOut)))
			}
		}
	}

	// 2. Set Linux system password via chpasswd
	chCmd := exec.Command("chpasswd")
	chCmd.Stdin = strings.NewReader(fmt.Sprintf("%s:%s\n", username, password))
	_ = chCmd.Run()

	// 3. Set Samba password via smbpasswd
	if _, err := exec.LookPath("smbpasswd"); err != nil {
		// If smbpasswd is not installed, record in local storage users map
		m.users[username] = StorageUser{Username: username, Role: role}
		return nil
	}

	cmd := exec.Command("smbpasswd", "-a", "-s", username)
	cmd.Stdin = strings.NewReader(fmt.Sprintf("%s\n%s\n", password, password))
	out, err := cmd.CombinedOutput()
	if err != nil {
		outStr := strings.TrimSpace(string(out))
		// If already in passdb, update without -a
		if strings.Contains(outStr, "Failed to add") || strings.Contains(outStr, "already exists") {
			updateCmd := exec.Command("smbpasswd", "-s", username)
			updateCmd.Stdin = strings.NewReader(fmt.Sprintf("%s\n%s\n", password, password))
			if uOut, uErr := updateCmd.CombinedOutput(); uErr != nil {
				return fmt.Errorf("smbpasswd failed: %v (%s)", uErr, strings.TrimSpace(string(uOut)))
			}
		} else {
			return fmt.Errorf("smbpasswd failed: %v (%s)", err, outStr)
		}
	}

	// 4. Ensure user is enabled in Samba
	_ = exec.Command("smbpasswd", "-e", username).Run()

	m.users[username] = StorageUser{Username: username, Role: role}
	return nil
}

func (m *Manager) DeleteUser(username string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	delete(m.users, username)

	if m.mockMode {
		return nil
	}

	_ = exec.Command("smbpasswd", "-x", username).Run()
	_ = exec.Command("userdel", username).Run()
	return nil
}
