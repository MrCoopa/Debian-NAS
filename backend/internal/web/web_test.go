package web

import (
	"bytes"
	"io/fs"
	"strings"
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
		"ImportablePools": []map[string]interface{}{
			{
				"Name":   "backup",
				"ID":     "1234567890",
				"State":  "ONLINE",
				"Action": "The pool can be imported.",
				"Config": "backup ONLINE",
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
		"partials/snapshot_tasks.html",
		"partials/network_list.html",
		"partials/cloudsync_tasks.html",
		"partials/share_datasets_picker.html",
		"partials/parent_datasets_options.html",
		"partials/available_disks_options.html",
		"partials/cloudsync_sources_options.html",
		"partials/share_users_checkboxes.html",
		"partials/snap_datasets_options.html",
		"partials/pool_disks_checkboxes.html",
		"partials/importable_pools_list.html",
	}

	for _, p := range partials {
		var pBuf bytes.Buffer
		if err := engine.Render(&pBuf, p, testData); err != nil {
			t.Errorf("Failed to render partial %s: %v", p, err)
		}
		if pBuf.Len() == 0 {
			t.Errorf("Partial %s rendered empty", p)
		}
	}
}

func TestUI_ResponsiveAndLiveCrudWiring(t *testing.T) {
	checks := []struct {
		file    string
		want    []string
		notWant []string
	}{
		{
			file: "templates/partials/topbar.html",
			want: []string{`toggleSidebar()`, `setTab('system')`},
		},
		{
			file: "templates/layout.html",
			want: []string{`omv-sidebar-backdrop`, `toggleSidebar()`, `setTab(`, `shareCustomPathMode`, `table-responsive`},
		},
		{
			file:    "templates/partials/shares_list.html",
			want:    []string{`hx-swap="outerHTML"`, `type="button"`, `@click.stop="openCreateShare(`, `data-name="`, `openEditSmbShare($el.dataset.name`, `openEditNfsShare($el.dataset.path`, `table-responsive`},
			notWant: []string{`hx-swap="morph"`, `hx-ext="morph"`},
		},
		{
			file: "templates/partials/snapshots_list.html",
			want: []string{`hx-swap="outerHTML"`, `hx-post="/ui/actions/zfs/snapshots/rollback"`, `data-snapshot="{{$s.Name}}"`, `type="button"`},
			notWant: []string{`hx-swap="morph"`, `hx-ext="morph"`},
		},
		{
			file: "templates/modals/create_snap.html",
			want: []string{`hx-swap="outerHTML"`, `nasTriggerHasError`},
			notWant: []string{`hx-swap="morph"`},
		},
		{
			file: "templates/modals/clone_snap.html",
			want: []string{`hx-swap="outerHTML"`, `nasTriggerHasError`},
			notWant: []string{`hx-swap="morph"`},
		},
		{
			file:    "templates/partials/datasets_list.html",
			want:    []string{`hx-swap="outerHTML"`, `every 4s [currentTab === 'datasets' && !document.hidden]`, `type="button"`, `openCreateDataset($el.dataset.dataset)`, `datasetSearch`, `toggleDataset`, `isDatasetVisible`},
			notWant: []string{`hx-swap="morph"`, `hx-ext="morph"`, `x-data=`},
		},
		{
			file:    "templates/modals/create_dataset.html",
			want:    []string{`hx-swap="outerHTML"`, `nasTriggerHasError`, `x-model="datasetParent"`, `refreshParentDatasets from:body`, `width: 100%`},
			notWant: []string{`hx-swap="morph"`, `$el.reset()`},
		},
		{
			file:    "templates/modals/create_zvol.html",
			want:    []string{`hx-swap="outerHTML"`, `nasTriggerHasError`},
			notWant: []string{`hx-swap="morph"`},
		},
		{
			file:    "templates/modals/dataset_props.html",
			want:    []string{`hx-swap="outerHTML"`, `nasTriggerHasError`},
			notWant: []string{`hx-swap="morph"`},
		},
		{
			file:    "templates/layout.html",
			want:    []string{`<script src="/static/app.js"></script>`, `window.currentTab = tab`, `snapSearch:`, `matchesSnap(`, `datasetSearch:`, `toggleDataset(`, `isDatasetVisible(`, `nextToastId:`, `@show-toast.window`, `selectedDisks:`, `toggleAllDisks(`, `openSnapTaskMenuId:`, `openCloudMenuId:`},
			notWant: []string{`@show-toast.camel.window`, `hx-ext="morph"`, `idiomorph`},
		},
		{
			file:    "templates/partials/system_metrics.html",
			want:    []string{`hx-swap="outerHTML"`, `every 4s [currentTab === 'dashboard' && !document.hidden]`, `expandedDisks[$el.dataset.disk] = true`, `table-responsive`},
			notWant: []string{`expandedDisk =`, `hx-swap="morph"`, `hx-ext="morph"`},
		},
		{
			file:    "templates/partials/disks_table.html",
			want:    []string{`hx-swap="outerHTML"`, `every 10s [currentTab === 'disks' && !document.hidden]`, `toggleAllDisks(`},
			notWant: []string{`hx-swap="morph"`, `hx-ext="morph"`, `x-data=`},
		},
		{
			file:    "templates/partials/pools_list.html",
			want:    []string{`hx-swap="outerHTML"`, `every 5s [currentTab === 'pools' && !document.hidden]`, `datasetUpdate from:body`, `snapshotUpdate from:body`, `type="button"`, `@click.stop="openCreatePool()`, `@click.stop="openAddVdev(`, `@click.stop="openDangerConfirm(`},
			notWant: []string{`hx-swap="morph"`, `hx-ext="morph"`, `x-data=`},
		},
		{
			file:    "templates/partials/network_list.html",
			want:    []string{`hx-swap="outerHTML"`, `every 5s [currentTab === 'system' && !document.hidden]`},
			notWant: []string{`hx-swap="morph"`, `hx-ext="morph"`},
		},
		{
			file:    "templates/partials/users_list.html",
			want:    []string{`hx-swap="outerHTML"`, `every 10s [currentTab === 'users' && !document.hidden]`, `type="button"`, `@click.stop="openCreateUserModal = true"`},
			notWant: []string{`hx-swap="morph"`, `hx-ext="morph"`, `x-data=`},
		},
		{
			file:    "templates/partials/snapshot_tasks.html",
			want:    []string{`hx-swap="outerHTML"`, `every 10s [currentTab === 'protection' && !document.hidden]`, `openSnapTaskMenuId`, `@click.stop="openCreateSnapTask()`},
			notWant: []string{`hx-swap="morph"`, `hx-ext="morph"`, `x-data=`},
		},
		{
			file:    "templates/partials/cloudsync_tasks.html",
			want:    []string{`hx-swap="outerHTML"`, `every 10s [currentTab === 'protection' && !document.hidden]`, `openCloudMenuId`, `@click.stop="openCreateCloudSync()`},
			notWant: []string{`hx-swap="morph"`, `hx-ext="morph"`, `x-data=`},
		},
		{
			file:    "templates/modals/import_pool.html",
			want:    []string{`/ui/partials/modals/importable_pools`, `refreshImportablePools from:body`, `x-model="importForm.nameOrId"`, `hx-swap="outerHTML"`},
			notWant: []string{`hx-swap="morph"`, `$el.reset()`},
		},
		{
			file:    "static/app.js",
			want:    []string{`window.currentTab`, `nasCanBackgroundPoll`, `visibilitychange`, `window.nasRefreshTab`},
			notWant: []string{`idiomorph`},
		},
	}

	for _, c := range checks {
		var raw []byte
		var err error
		if strings.HasPrefix(c.file, "static/") {
			raw, err = staticFiles.ReadFile(c.file)
		} else {
			raw, err = templateFiles.ReadFile(c.file)
		}
		if err != nil {
			t.Fatalf("failed reading %s: %v", c.file, err)
		}
		text := string(raw)
		for _, w := range c.want {
			if !strings.Contains(text, w) {
				t.Errorf("%s missing required markup %q", c.file, w)
			}
		}
		for _, nw := range c.notWant {
			if strings.Contains(text, nw) {
				t.Errorf("%s still contains stale/buggy markup %q", c.file, nw)
			}
		}
	}

	// Ensure no template anywhere uses idiomorph / hx-swap="morph"
	err := fs.WalkDir(templateFiles, "templates", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".html") {
			return err
		}
		raw, readErr := templateFiles.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		text := string(raw)
		for _, banned := range []string{`hx-swap="morph"`, `hx-ext="morph"`, `idiomorph`} {
			if strings.Contains(text, banned) {
				t.Errorf("%s still contains %q (must use outerHTML)", path, banned)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("failed walking templates: %v", err)
	}
}

func TestModals_NestedHxGetIsolatedFromParentForm(t *testing.T) {
	entries, err := fs.ReadDir(templateFiles, "templates/modals")
	if err != nil {
		t.Fatalf("failed reading modal templates: %v", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".html") {
			continue
		}
		raw, err := templateFiles.ReadFile("templates/modals/" + entry.Name())
		if err != nil {
			t.Fatalf("failed reading modal %s: %v", entry.Name(), err)
		}
		content := string(raw)
		if !strings.Contains(content, "hx-get=") {
			continue
		}
		if !strings.Contains(content, `hx-target="this"`) {
			t.Errorf("modal %s uses nested hx-get without hx-target=\"this\" (would inherit parent form hx-target)", entry.Name())
		}
		if strings.Contains(content, "@htmx:after-request") && !strings.Contains(content, "event.target === $el") {
			t.Errorf("modal %s uses @htmx:after-request with nested hx-get without checking event.target === $el (inner request would close modal)", entry.Name())
		}
	}
}

func TestWeb_CoreFixesTemplateIntegrity(t *testing.T) {
	engine, err := NewEngine()
	if err != nil {
		t.Fatalf("NewEngine error: %v", err)
	}

	// 1. Verify exact username matching in users_list.html (hasString avoids 'al' matching 'alice')
	var usersBuf bytes.Buffer
	err = engine.Render(&usersBuf, "partials/users_list.html", map[string]interface{}{
		"Users": []map[string]interface{}{
			{"Username": "al"},
			{"Username": "alice"},
		},
		"SmbShares": []map[string]interface{}{
			{"Name": "private", "GuestOk": false, "ValidUsers": []string{"alice"}},
		},
	})
	if err != nil {
		t.Fatalf("Render users_list failed: %v", err)
	}
	usersHTML := usersBuf.String()
	alIdx := strings.Index(usersHTML, "👤 al<")
	aliceIdx := strings.Index(usersHTML, "👤 alice<")
	if alIdx < 0 || aliceIdx < 0 || alIdx >= aliceIdx {
		t.Fatalf("expected both al and alice rendered in order, got:\n%s", usersHTML)
	}
	alRow := usersHTML[alIdx:aliceIdx]
	if strings.Contains(alRow, "📁 private") {
		t.Fatalf("user 'al' falsely matched share assigned only to 'alice':\n%s", alRow)
	}

	// 2. Verify disk_drawer uses runDrawerDiskAction with path & confirm_wipe and layout includes it
	drawerRaw, err := templateFiles.ReadFile("templates/partials/disk_drawer.html")
	if err != nil {
		t.Fatalf("read disk_drawer.html: %v", err)
	}
	drawerStr := string(drawerRaw)
	if strings.Contains(drawerStr, "device: activeDisk") || !strings.Contains(drawerStr, "runDrawerDiskAction('/ui/actions/disks/wipe', { path: activeDisk.path, confirm_wipe: 'true' })") {
		t.Fatalf("disk_drawer.html missing fixed runDrawerDiskAction / confirm_wipe wiring")
	}

	layoutRaw, err := templateFiles.ReadFile("templates/layout.html")
	if err != nil {
		t.Fatalf("read layout.html: %v", err)
	}
	layoutStr := string(layoutRaw)
	for _, want := range []string{`drawerOpen: false`, `activeDisk:`, `partials/disk_drawer.html`, `partials/services_list.html`, `runDrawerDiskAction(url, values)`} {
		if !strings.Contains(layoutStr, want) {
			t.Errorf("layout.html missing %q", want)
		}
	}

	// 3. Verify danger_confirm targets #datasets-container for dataset destroy
	dangerRaw, err := templateFiles.ReadFile("templates/modals/danger_confirm.html")
	if err != nil {
		t.Fatalf("read danger_confirm.html: %v", err)
	}
	if !strings.Contains(string(dangerRaw), `:hx-target="dangerConfirm.type === 'pool' ? '#pools-container' : '#datasets-container'"`) {
		t.Fatalf("danger_confirm.html does not dynamically target #datasets-container for dataset destroy")
	}

	// 4. Verify create_cloudsync uses valid 5-field cron options
	csRaw, err := templateFiles.ReadFile("templates/modals/create_cloudsync.html")
	if err != nil {
		t.Fatalf("read create_cloudsync.html: %v", err)
	}
	csStr := string(csRaw)
	for _, bad := range []string{`0 3 45 * * 2`, `0 2 00 * *`, `0 0 00 * *`} {
		if strings.Contains(csStr, bad) {
			t.Errorf("create_cloudsync.html still contains invalid cron value %q", bad)
		}
	}
	for _, good := range []string{`value="45 3 * * 2"`, `value="0 2 * * *"`, `value="0 0 * * *"`, `refreshCloudSyncSources from:body`} {
		if !strings.Contains(csStr, good) {
			t.Errorf("create_cloudsync.html missing %q", good)
		}
	}
}
