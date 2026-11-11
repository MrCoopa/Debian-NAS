package shares

import (
	"strings"
	"testing"
)

func TestGenerateSmbConfig(t *testing.T) {
	m := NewManager(true, "", "")
	share := &SmbShare{
		Name:        "TestShare",
		Path:        "/mnt/DATA/test",
		Comment:     "Test share comment",
		ReadOnly:    false,
		GuestOk:     false,
		TimeMachine: true,
		ValidUsers:  []string{"niklas", "milena"},
		Enabled:     true,
	}

	conf := m.generateSmbConfig(share)

	if !strings.Contains(conf, "[TestShare]") {
		t.Errorf("expected [TestShare] in config, got:\n%s", conf)
	}
	if !strings.Contains(conf, "path = /mnt/DATA/test") {
		t.Errorf("expected path in config, got:\n%s", conf)
	}
	if !strings.Contains(conf, "read only = no") {
		t.Errorf("expected read only = no, got:\n%s", conf)
	}
	if !strings.Contains(conf, "fruit:time machine = yes") {
		t.Errorf("expected fruit:time machine = yes, got:\n%s", conf)
	}
	if !strings.Contains(conf, "valid users = niklas milena") {
		t.Errorf("expected valid users, got:\n%s", conf)
	}
	if !strings.Contains(conf, "write list = niklas milena") {
		t.Errorf("expected write list, got:\n%s", conf)
	}
	if !strings.Contains(conf, "inherit permissions = yes") {
		t.Errorf("expected inherit permissions = yes, got:\n%s", conf)
	}
	if !strings.Contains(conf, "directory mask = 2775") {
		t.Errorf("expected directory mask = 2775, got:\n%s", conf)
	}
}

func TestSmbShareReadOnly(t *testing.T) {
	m := NewManager(true, "", "")
	share := &SmbShare{
		Name:     "ReadOnlyShare",
		Path:     "/mnt/DATA/ro",
		ReadOnly: true,
	}

	conf := m.generateSmbConfig(share)
	if !strings.Contains(conf, "read only = yes") {
		t.Errorf("expected read only = yes, got:\n%s", conf)
	}
	if strings.Contains(conf, "write list =") {
		t.Errorf("read-only share must not emit write list, got:\n%s", conf)
	}
}

func TestNormalizeMainSmbConf_KeepsGlobalIntact(t *testing.T) {
	debianDefault := `[global]
   workgroup = WORKGROUP
   include = /etc/samba/nas-shares.conf
   passdb backend = tdbsam
   obey pam restrictions = yes
   map to guest = bad user

[homes]
   comment = Home Directories
   read only = yes
`
	normalized := normalizeMainSmbConf(debianDefault)

	globalPos := strings.Index(normalized, "[global]")
	passdbPos := strings.Index(normalized, "passdb backend = tdbsam")
	includePos := strings.Index(normalized, "include = /etc/samba/nas-shares.conf")
	homesPos := strings.Index(normalized, "[homes]")

	if globalPos == -1 || passdbPos == -1 || includePos == -1 || homesPos == -1 {
		t.Fatalf("missing expected section in normalized smb.conf:\n%s", normalized)
	}
	if !(globalPos < passdbPos && passdbPos < includePos && includePos < homesPos) {
		t.Fatalf("invalid ordering in smb.conf (global < passdb < include < homes required):\n%s", normalized)
	}
	if strings.Count(normalized, "include = /etc/samba/nas-shares.conf") != 1 {
		t.Fatalf("expected exactly 1 include line, got:\n%s", normalized)
	}
	// Idempotency check
	secondPass := normalizeMainSmbConf(normalized)
	if secondPass != normalized {
		t.Fatalf("normalizeMainSmbConf not idempotent:\nfirst:\n%s\nsecond:\n%s", normalized, secondPass)
	}
}

