package web

import (
	"bytes"
	"testing"
)

func TestEngine_RenderAll(t *testing.T) {
	engine, err := NewEngine()
	if err != nil {
		t.Fatalf("Failed to initialize template engine: %v", err)
	}

	testData := map[string]interface{}{
		"System": map[string]interface{}{
			"Hostname":      "debian-nas-test",
			"OS":            "Debian GNU/Linux 13 (trixie)",
			"Kernel":        "6.12.0-amd64",
			"Arch":          "x86_64",
			"Uptime":        "1h 0m",
			"UptimeSec":     int64(3600),
			"DaemonRamMB":   1.2,
			"CPU": map[string]interface{}{
				"Model":        "AMD EPYC 7002",
				"Cores":        16,
				"Threads":      32,
				"UsagePercent": 12.5,
				"TemperatureC": 35,
				"LoadAvg":      []float64{0.15, 0.20, 0.18},
			},
			"Memory": map[string]interface{}{
				"TotalBytes":    uint64(34359738368),
				"UsedBytes":     uint64(8589934592),
				"UsedPercent":   25.0,
				"ZfsArcBytes":   uint64(17179869184),
				"ZfsArcPercent": 50.0,
			},
			"DiskIO": map[string]interface{}{
				"ReadMBS":   1.25,
				"WriteMBS":  2.50,
				"ReadIOPS":  120,
				"WriteIOPS": 250,
			},
			"Network": []map[string]interface{}{
				{
					"Name":   "eth0",
					"Type":   "10GbE SFP+",
					"Speed":  "10000 Mb/s",
					"IP":     "192.168.1.100",
					"MAC":    "00:11:22:33:44:55",
					"RxRate": "1.2 MB/s",
					"TxRate": "5.4 MB/s",
					"State":  "UP",
				},
			},
		},
		"ZfsVersion": "2.2.6-native",
		"Disks": []map[string]interface{}{
			{
				"Name":         "sda",
				"Path":         "/dev/sda",
				"ByIDPath":     "/dev/disk/by-id/ata-WDC_WD40EFRX",
				"Model":        "WDC WD40EFRX",
				"Serial":       "WD-WCC4E0123456",
				"SizeBytes":    uint64(4000787030016),
				"Type":         "hdd",
				"State":        "standby",
				"Temperature":  32,
				"PowerOnHours": 14200,
				"Bay":          1,
				"Smart": map[string]interface{}{
					"Passed": true,
				},
			},
		},
		"Pools": []map[string]interface{}{
			{
				"Name":          "tank",
				"Health":        "ONLINE",
				"Topology":      "mirror-0 (2x 4TB)",
				"Allocated":     uint64(1000000000),
				"FreeBytes":     uint64(3000000000),
				"SizeBytes":     uint64(4000000000),
				"CapPercent":    25.0,
				"Fragmentation": 2,
				"Autotrim":      "off",
				"Autoexpand":    "on",
				"Scrub": map[string]interface{}{
					"Active":     false,
					"Percentage": 0.0,
					"Errors":     0,
				},
				"VDevs": []map[string]interface{}{
					{
						"Type":   "mirror",
						"Status": "ONLINE",
						"Disks": []map[string]interface{}{
							{
								"Name":         "sda",
								"Status":       "ONLINE",
								"Path":         "/dev/sda",
								"ReadErrors":   0,
								"WriteErrors":  0,
								"ChksumErrors": 0,
							},
						},
					},
				},
			},
		},
		"Datasets": []map[string]interface{}{
			{
				"Name":        "tank/data",
				"Type":        "filesystem",
				"Mountpoint":  "/mnt/data",
				"UsedBytes":   uint64(500000000),
				"AvailBytes":  uint64(2500000000),
				"Compression": "lz4",
				"RecordSize":  "128K",
				"Quota":       "none",
				"ReadOnly":    "off",
				"Atime":       "off",
				"Snapshots":   2,
			},
		},
		"SmbShares": []map[string]interface{}{
			{
				"Name":           "data",
				"Path":           "/mnt/data",
				"Comment":        "Primary Storage Share",
				"ValidUsers":     []string{"alice", "bob"},
				"ReadOnly":       false,
				"GuestOk":        false,
				"Browsable":      true,
				"AllowedClients": "*",
			},
		},
		"NfsExports": []map[string]interface{}{},
		"Users": []map[string]interface{}{
			{
				"Username": "alice",
				"UID":      1001,
				"GID":      1001,
				"Shares":   []string{"data"},
			},
		},
		"Snapshots": []map[string]interface{}{
			{
				"Name":            "tank/data@init",
				"Dataset":         "tank/data",
				"ReferencedBytes": uint64(500000000),
				"UsedBytes":       uint64(0),
			},
		},
		"Network": []map[string]interface{}{
			{
				"Name":        "eth0",
				"MAC":         "00:11:22:33:44:55",
				"IPAddresses": []string{"192.168.1.100/24"},
				"SpeedMbps":   1000,
				"State":       "UP",
				"MTU":         1500,
			},
		},
		"CloudJobs": []map[string]interface{}{
			{
				"ID":             "job-1",
				"Name":           "DATA Hetzner Upload",
				"Description":    "DATA Hetzner Upload",
				"FrequencyText":  "At 03:45 (03:45 AM), only on Tuesday,...",
				"NextRunText":    "in about 13 hours",
				"LastRunText":    "1 day ago",
				"Enabled":        true,
				"GetDescription": "DATA Hetzner Upload",
				"GetFrequency":   "At 03:45 (03:45 AM), only on Tuesday,...",
				"GetNextRun":     "in about 13 hours",
				"GetLastRun":     "1 day ago",
				"GetStateClass":  "completed",
				"GetStateLabel":  "Completed",
			},
		},
		"CloudRemotes": []map[string]interface{}{
			{
				"Name": "hetzner_storagebox",
				"Type": "webdav",
			},
		},
		"TotalDisks":    1,
		"TotalRawBytes": uint64(4000787030016),
		"StandbyCount":  1,
	}

	// Render layout.html
	var buf bytes.Buffer
	if err := engine.Render(&buf, "layout.html", testData); err != nil {
		t.Fatalf("Failed to render layout.html: %v", err)
	}
	if buf.Len() == 0 {
		t.Fatalf("layout.html rendered empty")
	}

	// Render login.html
	var loginBuf bytes.Buffer
	if err := engine.Render(&loginBuf, "login.html", testData); err != nil {
		t.Fatalf("Failed to render login.html: %v", err)
	}
	if loginBuf.Len() == 0 {
		t.Fatalf("login.html rendered empty")
	}

	// Render all partials directly
	partials := []string{
		"partials/topbar.html",
		"partials/system_metrics.html",
		"partials/pools_list.html",
		"partials/disks_table.html",
		"partials/disk_drawer.html",
		"partials/datasets_list.html",
		"partials/shares_list.html",
		"partials/users_list.html",
		"partials/snapshots_list.html",
		"partials/network_list.html",
		"partials/cloudsync_tasks.html",
	}

	for _, p := range partials {
		var pBuf bytes.Buffer
		if err := engine.Render(&pBuf, p, testData); err != nil {
			t.Errorf("Failed to render partial %s: %v", p, err)
		}
	}
}
