package validate

import (
	"testing"
)

func TestPoolName(t *testing.T) {
	valid := []string{"tank", "TANK", "tank_1", "tank-pool", "pool.fast", "data:pool"}
	for _, name := range valid {
		if err := PoolName(name); err != nil {
			t.Errorf("expected valid for %q, got: %v", name, err)
		}
	}

	invalid := []string{
		"", "-tank", "1tank", "tank/pool", "tank pool", "mirror", "raidz1", "log", "cache",
		"tank;rm -rf /", "tank\nnewline",
	}
	for _, name := range invalid {
		if err := PoolName(name); err == nil {
			t.Errorf("expected error for invalid pool name %q, got nil", name)
		}
	}
}

func TestDatasetName(t *testing.T) {
	valid := []string{"tank", "tank/data", "tank/data/backup", "pool-1/sub.dataset"}
	for _, name := range valid {
		if err := DatasetName(name); err != nil {
			t.Errorf("expected valid for %q, got: %v", name, err)
		}
	}

	invalid := []string{
		"", "/tank", "tank/", "tank//data", "-tank/data", "tank/-data",
		"mirror/data", "tank/sub..dir", "tank/dir\nbreak",
	}
	for _, name := range invalid {
		if err := DatasetName(name); err == nil {
			t.Errorf("expected error for invalid dataset name %q, got nil", name)
		}
	}
}

func TestUsername(t *testing.T) {
	valid := []string{"niklas", "milena", "user1", "nas_user", "storage-user"}
	for _, name := range valid {
		if err := Username(name); err != nil {
			t.Errorf("expected valid username for %q, got: %v", name, err)
		}
	}

	invalid := []string{
		"", "root", "nobody", "daemon", "www-data", "-user", "UserCap", "user;id", "user\nroot:pass",
	}
	for _, name := range invalid {
		if err := Username(name); err == nil {
			t.Errorf("expected error for invalid username %q, got nil", name)
		}
	}
}

func TestPassword(t *testing.T) {
	if err := Password("secret123"); err != nil {
		t.Errorf("expected valid password, got: %v", err)
	}

	invalid := []string{
		"", "12", "pass\nroot:newpass", "pass\rbreak", "null\x00byte",
	}
	for _, pw := range invalid {
		if err := Password(pw); err == nil {
			t.Errorf("expected error for invalid password %q, got nil", pw)
		}
	}
}

func TestShareName(t *testing.T) {
	valid := []string{"Media", "Backup_1", "Public Share", "shared.folder"}
	for _, name := range valid {
		if err := ShareName(name); err != nil {
			t.Errorf("expected valid share name for %q, got: %v", name, err)
		}
	}

	invalid := []string{
		"", "global", "homes", "printers", "ipc$", "[section]", "share/sub", "share=val",
		"share\n[injected]", "../../cron",
	}
	for _, name := range invalid {
		if err := ShareName(name); err == nil {
			t.Errorf("expected error for invalid share name %q, got nil", name)
		}
	}
}

func TestSharePath(t *testing.T) {
	valid := []string{"/mnt/DATA/media", "/srv/nfs/share", "/DATA/files"}
	for _, path := range valid {
		if err := SharePath(path); err != nil {
			t.Errorf("expected valid share path for %q, got: %v", path, err)
		}
	}

	invalid := []string{
		"", "relative/path", "/", "/etc", "/etc/samba", "/root", "/var",
		"/tmp/danger", "/mnt/DATA/../../etc", "/mnt/DATA/share\nline",
	}
	for _, path := range invalid {
		if err := SharePath(path); err == nil {
			t.Errorf("expected error for invalid share path %q, got nil", path)
		}
	}
}

func TestNfsOptions(t *testing.T) {
	valid := []string{"ro", "rw,sync,no_subtree_check", "rw,async,root_squash,anonuid=1000"}
	for _, opt := range valid {
		if err := NfsOptions(opt); err != nil {
			t.Errorf("expected valid options for %q, got: %v", opt, err)
		}
	}

	invalid := []string{
		"rw\n/evil *(rw)", "rw;rm -rf /", "rw(no_root_squash)", "unknown_opt_xyz",
	}
	for _, opt := range invalid {
		if err := NfsOptions(opt); err == nil {
			t.Errorf("expected error for invalid options %q, got nil", opt)
		}
	}
}

