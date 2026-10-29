package config

import (
	"encoding/json"
	"os"
	"path/filepath"
)

type Config struct {
	Port         int               `json:"port"`
	Host         string            `json:"host"`
	MockMode     bool              `json:"mock_mode"`
	StoragePath  string            `json:"storage_path"`
	HdIdleConfig string            `json:"hd_idle_config"`
	SambaConfDir string            `json:"samba_conf_dir"`
	NfsConfFile  string            `json:"nfs_conf_file"`
	RcloneConfig string            `json:"rclone_config"`
	AuthToken    string            `json:"auth_token"`
	Settings     map[string]string `json:"settings"`
}

func DefaultConfig() *Config {
	return &Config{
		Port:         8080,
		Host:         "0.0.0.0",
		MockMode:     false,
		StoragePath:  "/etc/nas",
		HdIdleConfig: "/etc/default/hd-idle",
		SambaConfDir: "/etc/samba/conf.d",
		NfsConfFile:  "/etc/exports.d/nas.exports",
		RcloneConfig: "/etc/nas/rclone.conf",
		Settings: map[string]string{
			"default_atime":     "off",
			"default_spindown":  "900", // 15 minutes
			"scrub_schedule":    "monthly",
			"snapshot_schedule": "daily",
		},
	}
}

func LoadConfig(path string) (*Config, error) {
	cfg := DefaultConfig()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			_ = SaveConfig(path, cfg)
			return cfg, nil
		}
		return nil, err
	}
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

func SaveConfig(path string, cfg *Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}
