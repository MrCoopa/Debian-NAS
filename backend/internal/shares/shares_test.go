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
}

