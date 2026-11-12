package api

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"debian-nas/internal/cloudsync"
	"debian-nas/internal/config"
	"debian-nas/internal/disks"
	"debian-nas/internal/shares"
	"debian-nas/internal/system"
	"debian-nas/internal/web"
	"debian-nas/internal/zfs"
)

func createTestServer(t *testing.T) *Server {
	cfg := &config.Config{
		MockMode: true,
	}
	sys := system.NewCollector(true)
	dm := disks.NewManager(true, "")
	zm := zfs.NewManager(true)
	sm := shares.NewManager(true, "", "")
	cm := cloudsync.NewManager(true, "")
	staticSub, err := web.GetFS()
	if err != nil {
		t.Fatalf("web.GetFS error: %v", err)
	}

	return NewServer(cfg, sys, dm, zm, sm, cm, staticSub)
}

func TestUI_RootAndPartials(t *testing.T) {
	srv := createTestServer(t)
	handler := srv.Routes()

	// 1. Unauthenticated Root Layout redirects to /login
	unauthReq := httptest.NewRequest("GET", "/", nil)
	unauthRR := httptest.NewRecorder()
	handler.ServeHTTP(unauthRR, unauthReq)

	if unauthRR.Code != http.StatusSeeOther {
		t.Fatalf("Unauthenticated GET / returned status %d, expected 303 See Other", unauthRR.Code)
	}
	if loc := unauthRR.Header().Get("Location"); loc != "/login" {
		t.Fatalf("Expected redirect to /login, got: %s", loc)
	}

	// 2. Login Page
	loginReq := httptest.NewRequest("GET", "/login", nil)
	loginRR := httptest.NewRecorder()
	handler.ServeHTTP(loginRR, loginReq)

	if loginRR.Code != http.StatusOK {
		t.Fatalf("GET /login returned status %d, expected 200", loginRR.Code)
	}
	loginBody := loginRR.Body.String()
	if !strings.Contains(loginBody, "debian") || !strings.Contains(loginBody, "root") {
		t.Errorf("GET /login missing expected login markup: %s", loginBody[:min(len(loginBody), 500)])
	}

	// 3. Login Submit (POST /login)
	loginForm := url.Values{
		"username": {"root"},
		"password": {"openmediavault"},
	}
	submitReq := httptest.NewRequest("POST", "/login", strings.NewReader(loginForm.Encode()))
	submitReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	submitRR := httptest.NewRecorder()
	handler.ServeHTTP(submitRR, submitReq)

	if submitRR.Code != http.StatusSeeOther {
		t.Fatalf("POST /login returned status %d, expected 303", submitRR.Code)
	}
	authCookie := submitRR.Result().Cookies()
	var userCookie *http.Cookie
	for _, c := range authCookie {
		if c.Name == "nas_user" {
			userCookie = c
			break
		}
	}
	if userCookie == nil || userCookie.Value != "root" {
		t.Fatalf("Expected nas_user cookie to be 'root', got: %v", userCookie)
	}

	// 4. Authenticated Root Layout
	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(userCookie)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("GET / (authenticated) returned status %d, expected 200. Body: %s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, "Debian") || !strings.Contains(body, "/static/htmx.min.js") || !strings.Contains(body, "/static/alpine.min.js") || strings.Contains(body, `hx-ext="morph"`) {
		t.Errorf("GET / missing expected markup: %s", body[:min(len(body), 500)])
	}

	// 5. Partials (authenticated)
	partials := []string{
		"/ui/partials/system",
		"/ui/partials/topbar",
		"/ui/partials/pools",
		"/ui/partials/disks",
		"/ui/partials/datasets",
		"/ui/partials/shares",
		"/ui/partials/users",
		"/ui/partials/snapshots",
		"/ui/partials/network",
		"/ui/partials/cloudsync",
		"/ui/partials/modals/snap_datasets",
	}

	for _, p := range partials {
		pReq := httptest.NewRequest("GET", p, nil)
		pRR := httptest.NewRecorder()
		handler.ServeHTTP(pRR, pReq)

		if pRR.Code != http.StatusOK {
			t.Errorf("GET %s returned status %d, expected 200", p, pRR.Code)
		}
		if pRR.Body.Len() == 0 {
			t.Errorf("GET %s returned empty body", p)
		}
	}
}

func TestUI_Actions(t *testing.T) {
	srv := createTestServer(t)
	handler := srv.Routes()

	// 1. Create User
	form := url.Values{
		"username": {"testuser"},
		"password": {"secret123"},
	}
	req := httptest.NewRequest("POST", "/ui/actions/shares/users", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("POST /ui/actions/shares/users failed with status %d", rr.Code)
	}
	hxTrigger := rr.Header().Get("HX-Trigger")
	if !strings.Contains(hxTrigger, "showToast") || !strings.Contains(hxTrigger, "testuser") {
		t.Errorf("Expected HX-Trigger header with toast, got: %s", hxTrigger)
	}

	// 2. Create SMB Share
	shareForm := url.Values{
		"name":            {"testshare"},
		"path":            {"/mnt/tank/testshare"},
		"comment":         {"Test Share"},
		"proto":           {"smb"},
		"access":          {"restricted"},
		"allowed_clients": {"*"},
		"users":           {"testuser"},
	}
	req2 := httptest.NewRequest("POST", "/ui/actions/shares/smb", strings.NewReader(shareForm.Encode()))
	req2.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr2 := httptest.NewRecorder()
	handler.ServeHTTP(rr2, req2)

	if rr2.Code != http.StatusOK {
		t.Fatalf("POST /ui/actions/shares/smb failed with status %d", rr2.Code)
	}
	hxTrigger2 := rr2.Header().Get("HX-Trigger")
	if !strings.Contains(hxTrigger2, "testshare") {
		t.Errorf("Expected HX-Trigger to contain testshare, got: %s", hxTrigger2)
	}

	// 3. Delete SMB Share
	delReq := httptest.NewRequest("DELETE", "/ui/actions/shares/smb?name=testshare", nil)
	delRR := httptest.NewRecorder()
	handler.ServeHTTP(delRR, delReq)

	if delRR.Code != http.StatusOK {
		t.Fatalf("DELETE /ui/actions/shares/smb failed with status %d", delRR.Code)
	}
	if !strings.Contains(delRR.Header().Get("HX-Trigger"), "removed") {
		t.Errorf("Expected delete confirmation toast, got: %s", delRR.Header().Get("HX-Trigger"))
	}

	// 4. Create Share via unified /ui/actions/shares/save (SMB)
	unifiedForm := url.Values{
		"name":        {"mail-archiver"},
		"path":        {"/mnt/SSD-MIRROR/mail-archiver"},
		"proto":       {"smb"},
		"access":      {"restricted"},
		"valid_users": {"testuser"},
	}
	req4 := httptest.NewRequest("POST", "/ui/actions/shares/save", strings.NewReader(unifiedForm.Encode()))
	req4.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr4 := httptest.NewRecorder()
	handler.ServeHTTP(rr4, req4)

	if rr4.Code != http.StatusOK {
		t.Fatalf("POST /ui/actions/shares/save failed with status %d: %s", rr4.Code, rr4.Body.String())
	}
	if !strings.Contains(rr4.Header().Get("HX-Trigger"), "mail-archiver") {
		t.Errorf("Expected HX-Trigger to contain mail-archiver, got: %s", rr4.Header().Get("HX-Trigger"))
	}

	// 5. Create Share via unified /ui/actions/shares/save (NFS)
	nfsForm := url.Values{
		"path":            {"/mnt/SSD-MIRROR/nfs-export"},
		"proto":           {"nfs"},
		"access":          {"restricted"},
		"allowed_clients": {"192.168.1.0/24"},
	}
	req5 := httptest.NewRequest("POST", "/ui/actions/shares/save", strings.NewReader(nfsForm.Encode()))
	req5.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr5 := httptest.NewRecorder()
	handler.ServeHTTP(rr5, req5)

	if rr5.Code != http.StatusOK {
		t.Fatalf("POST /ui/actions/shares/save (NFS) failed with status %d: %s", rr5.Code, rr5.Body.String())
	}
	if !strings.Contains(rr5.Header().Get("HX-Trigger"), "nfs-export") {
		t.Errorf("Expected HX-Trigger to contain nfs-export, got: %s", rr5.Header().Get("HX-Trigger"))
	}

	// 6. Delete NFS Export via /ui/actions/shares/nfs
	delNfsReq := httptest.NewRequest("DELETE", "/ui/actions/shares/nfs?path=/mnt/SSD-MIRROR/nfs-export", nil)
	delNfsRR := httptest.NewRecorder()
	handler.ServeHTTP(delNfsRR, delNfsReq)

	if delNfsRR.Code != http.StatusOK {
		t.Fatalf("DELETE /ui/actions/shares/nfs failed with status %d", delNfsRR.Code)
	}
	if !strings.Contains(delNfsRR.Header().Get("HX-Trigger"), "removed") {
		t.Errorf("Expected delete confirmation toast, got: %s", delNfsRR.Header().Get("HX-Trigger"))
	}

	// 7. Disk actions endpoint check
	diskForm := url.Values{
		"pool":   {"DATA"},
		"device": {"sdb"},
	}
	diskReq := httptest.NewRequest("POST", "/ui/actions/zfs/pools/disk/offline", strings.NewReader(diskForm.Encode()))
	diskReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	diskRR := httptest.NewRecorder()
	handler.ServeHTTP(diskRR, diskReq)

	if diskRR.Code != http.StatusOK {
		t.Fatalf("POST /ui/actions/zfs/pools/disk/offline failed with status %d", diskRR.Code)
	}

	// 8. Cloud Sync: Toggle Job
	toggleReq := httptest.NewRequest("POST", "/ui/actions/cloudsync/toggle?id=job-1", nil)
	toggleRR := httptest.NewRecorder()
	handler.ServeHTTP(toggleRR, toggleReq)
	if toggleRR.Code != http.StatusOK {
		t.Fatalf("POST /ui/actions/cloudsync/toggle failed with status %d", toggleRR.Code)
	}
	if !strings.Contains(toggleRR.Header().Get("HX-Trigger"), "cloudSyncUpdate") {
		t.Errorf("Expected cloudSyncUpdate trigger, got: %s", toggleRR.Header().Get("HX-Trigger"))
	}

	// 9. Cloud Sync: Run Job
	runReq := httptest.NewRequest("POST", "/ui/actions/cloudsync/run?id=job-1", nil)
	runRR := httptest.NewRecorder()
	handler.ServeHTTP(runRR, runReq)
	if runRR.Code != http.StatusOK {
		t.Fatalf("POST /ui/actions/cloudsync/run failed with status %d", runRR.Code)
	}

	// 10. Cloud Sync: Save Job
	saveCsForm := url.Values{
		"description":   {"Daily Backup to B2"},
		"source_path":   {"/mnt/DATA"},
		"remote_target": {"b2_backup:nas-backups/DATA"},
		"direction":     {"PUSH"},
		"schedule":      {"0 2 00 * *"},
		"enabled":       {"true"},
	}
	saveCsReq := httptest.NewRequest("POST", "/ui/actions/cloudsync/save", strings.NewReader(saveCsForm.Encode()))
	saveCsReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	saveCsRR := httptest.NewRecorder()
	handler.ServeHTTP(saveCsRR, saveCsReq)
	if saveCsRR.Code != http.StatusOK {
		t.Fatalf("POST /ui/actions/cloudsync/save failed with status %d", saveCsRR.Code)
	}

	// 11. Cloud Sync: Delete Job
	delCsReq := httptest.NewRequest("DELETE", "/ui/actions/cloudsync/delete?id=job-2", nil)
	delCsRR := httptest.NewRecorder()
	handler.ServeHTTP(delCsRR, delCsReq)
	if delCsRR.Code != http.StatusOK {
		t.Fatalf("DELETE /ui/actions/cloudsync/delete failed with status %d", delCsRR.Code)
	}

	// 12. Snapshot UI Lifecycle (Create -> Clone -> Rollback -> Destroy)
	snapForm := url.Values{
		"dataset":       {"DATA/Fileshare"},
		"snapshot_name": {"ui-snap1"},
		"recursive":     {"true"},
	}
	snapReq := httptest.NewRequest("POST", "/ui/actions/zfs/snapshots", strings.NewReader(snapForm.Encode()))
	snapReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	snapRR := httptest.NewRecorder()
	handler.ServeHTTP(snapRR, snapReq)
	if snapRR.Code != http.StatusOK || !strings.Contains(snapRR.Body.String(), "DATA/Fileshare@ui-snap1") {
		t.Fatalf("Snapshot creation UI failed (status %d): %s", snapRR.Code, snapRR.Header().Get("HX-Trigger"))
	}

	cloneForm := url.Values{
		"snapshot":       {"DATA/Fileshare@ui-snap1"},
		"target_dataset": {"DATA/Fileshare-ui-clone"},
	}
	cloneReq := httptest.NewRequest("POST", "/ui/actions/zfs/snapshots/clone", strings.NewReader(cloneForm.Encode()))
	cloneReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	cloneRR := httptest.NewRecorder()
	handler.ServeHTTP(cloneRR, cloneReq)
	if cloneRR.Code != http.StatusOK || !strings.Contains(cloneRR.Header().Get("HX-Trigger"), "DATA/Fileshare-ui-clone") {
		t.Fatalf("Snapshot clone UI failed (status %d): %s", cloneRR.Code, cloneRR.Header().Get("HX-Trigger"))
	}

	rbForm := url.Values{"snapshot": {"DATA/Fileshare@ui-snap1"}}
	rbReq := httptest.NewRequest("POST", "/ui/actions/zfs/snapshots/rollback", strings.NewReader(rbForm.Encode()))
	rbReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rbRR := httptest.NewRecorder()
	handler.ServeHTTP(rbRR, rbReq)
	if rbRR.Code != http.StatusOK || !strings.Contains(rbRR.Header().Get("HX-Trigger"), "rolled back") {
		t.Fatalf("Snapshot rollback UI failed: %s", rbRR.Header().Get("HX-Trigger"))
	}

	delSnapReq := httptest.NewRequest("DELETE", "/ui/actions/zfs/snapshots?name="+url.QueryEscape("DATA/Fileshare/Milena@ui-snap1"), nil)
	delSnapRR := httptest.NewRecorder()
	handler.ServeHTTP(delSnapRR, delSnapReq)
	if delSnapRR.Code != http.StatusOK || !strings.Contains(delSnapRR.Header().Get("HX-Trigger"), "destroyed") {
		t.Fatalf("Snapshot destroy UI failed: %s", delSnapRR.Header().Get("HX-Trigger"))
	}
}

func TestUI_PoolLifecycleFreesDisksAndAllowsReCreateAndWipe(t *testing.T) {
	srv := createTestServer(t)
	handler := srv.Routes()

	allDisks, err := srv.disks.ListDisks()
	if err != nil {
		t.Fatalf("ListDisks failed: %v", err)
	}
	var freePaths []string
	var freeNames []string
	var freeDevPaths []string
	for _, d := range allDisks {
		if !d.IsSystemDisk && d.ZfsPool == "" {
			p := d.ByIDPath
			if p == "" {
				p = d.Path
			}
			freePaths = append(freePaths, p)
			freeNames = append(freeNames, d.Name)
			freeDevPaths = append(freeDevPaths, d.Path)
		}
	}
	if len(freePaths) < 1 {
		t.Fatalf("expected at least 1 free mock disk, got %d", len(freePaths))
	}
	d1Path := freePaths[0]
	d1Name := freeNames[0]
	d1Dev := freeDevPaths[0]

	// 1. Create a test pool via UI
	createForm := url.Values{
		"name":        {"Test"},
		"vdev_type":   {"stripe"},
		"disks":       {d1Path},
		"ashift":      {"12"},
		"compression": {"zstd"},
	}
	createReq := httptest.NewRequest("POST", "/ui/actions/zfs/pools/create", strings.NewReader(createForm.Encode()))
	createReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	createRR := httptest.NewRecorder()
	handler.ServeHTTP(createRR, createReq)
	if createRR.Code != http.StatusOK || !strings.Contains(createRR.Body.String(), "Test") {
		t.Fatalf("pool create failed (status %d): %s", createRR.Code, createRR.Header().Get("HX-Trigger"))
	}

	// 2. Confirm modal disk checkboxes show assigned to Pool: Test
	modalReq := httptest.NewRequest("GET", "/ui/partials/modals/pool_disks", nil)
	modalRR := httptest.NewRecorder()
	handler.ServeHTTP(modalRR, modalReq)
	if modalRR.Code != http.StatusOK || !strings.Contains(modalRR.Body.String(), "Pool: Test") {
		t.Fatalf("expected Pool: Test in modal checkboxes, got: %s", modalRR.Body.String())
	}

	// 3. Wipe while assigned must fail
	wipeBusyForm := url.Values{"path": {d1Dev}, "confirm_wipe": {"true"}}
	wipeBusyReq := httptest.NewRequest("POST", "/ui/actions/disks/wipe", strings.NewReader(wipeBusyForm.Encode()))
	wipeBusyReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	wipeBusyRR := httptest.NewRecorder()
	handler.ServeHTTP(wipeBusyRR, wipeBusyReq)
	if !strings.Contains(wipeBusyRR.Header().Get("HX-Trigger"), "cannot wipe disk") {
		t.Fatalf("expected wipe block while assigned to pool, got: %s", wipeBusyRR.Header().Get("HX-Trigger"))
	}

	// 4. Destroy pool -> disks must immediately become Available again
	destroyForm := url.Values{
		"type": {"pool"},
		"name": {"Test"},
	}
	destroyReq := httptest.NewRequest("POST", "/ui/actions/zfs/destroy", strings.NewReader(destroyForm.Encode()))
	destroyReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	destroyRR := httptest.NewRecorder()
	handler.ServeHTTP(destroyRR, destroyReq)
	if destroyRR.Code != http.StatusOK || !strings.Contains(destroyRR.Header().Get("HX-Trigger"), "diskUpdate") {
		t.Fatalf("pool destroy failed: %s", destroyRR.Header().Get("HX-Trigger"))
	}

	modalAfterDestroyReq := httptest.NewRequest("GET", "/ui/partials/modals/pool_disks", nil)
	modalAfterDestroyRR := httptest.NewRecorder()
	handler.ServeHTTP(modalAfterDestroyRR, modalAfterDestroyReq)
	bodyAfterDestroy := modalAfterDestroyRR.Body.String()
	if strings.Contains(bodyAfterDestroy, "Pool: Test") {
		t.Fatalf("disks still assigned to deleted pool 'Test': %s", bodyAfterDestroy)
	}
	if !strings.Contains(bodyAfterDestroy, d1Name) {
		t.Fatalf("expected freed disk %s in modal body", d1Name)
	}

	disksAfterDestroyReq := httptest.NewRequest("GET", "/ui/partials/disks", nil)
	disksAfterDestroyRR := httptest.NewRecorder()
	handler.ServeHTTP(disksAfterDestroyRR, disksAfterDestroyReq)
	if strings.Contains(disksAfterDestroyRR.Body.String(), "assigned (Test)") {
		t.Fatalf("disks table still shows stale 'assigned (Test)' after destroy")
	}

	// 5. Re-create pool on freed disks, then export -> verify freed + wipeable
	createForm.Set("name", "abc")
	recreateReq := httptest.NewRequest("POST", "/ui/actions/zfs/pools/create", strings.NewReader(createForm.Encode()))
	recreateReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	recreateRR := httptest.NewRecorder()
	handler.ServeHTTP(recreateRR, recreateReq)
	if recreateRR.Code != http.StatusOK || !strings.Contains(recreateRR.Body.String(), "abc") {
		t.Fatalf("re-creating pool 'abc' on freed disks failed: %s", recreateRR.Header().Get("HX-Trigger"))
	}

	// Simulate external ZFS export/destroy bypassing disks manager to verify resync on read & wipe
	if err := srv.zfs.ExportPool("abc"); err != nil {
		t.Fatalf("direct ExportPool failed: %v", err)
	}

	modalAfterExportReq := httptest.NewRequest("GET", "/ui/partials/modals/pool_disks", nil)
	modalAfterExportRR := httptest.NewRecorder()
	handler.ServeHTTP(modalAfterExportRR, modalAfterExportReq)
	if strings.Contains(modalAfterExportRR.Body.String(), "Pool: abc") {
		t.Fatalf("modal checkboxes still show exported pool 'abc'")
	}

	// Re-create via ZFS directly so cache in disks is stale before wipe
	if err := srv.zfs.CreatePool("abc", "stripe", []string{d1Path}, 12, "zstd"); err != nil {
		t.Fatalf("direct CreatePool failed: %v", err)
	}
	// Sync disks into busy state first, then destroy outside disks manager
	poolMap, roleMap := srv.zfs.GetDiskPoolMap()
	srv.disks.SyncPoolMembership(poolMap, roleMap)
	if err := srv.zfs.DestroyPool("abc"); err != nil {
		t.Fatalf("direct DestroyPool failed: %v", err)
	}

	// Wipe must now sync membership first and succeed!
	wipeFreeForm := url.Values{"path": {d1Dev}, "confirm_wipe": {"true"}}
	wipeFreeReq := httptest.NewRequest("POST", "/ui/actions/disks/wipe", strings.NewReader(wipeFreeForm.Encode()))
	wipeFreeReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	wipeFreeRR := httptest.NewRecorder()
	handler.ServeHTTP(wipeFreeRR, wipeFreeReq)
	if !strings.Contains(wipeFreeRR.Header().Get("HX-Trigger"), "wiped and initialized successfully") {
		t.Fatalf("expected wipe to succeed after pool destruction, got: %s", wipeFreeRR.Header().Get("HX-Trigger"))
	}
}

func TestUI_StandbySafeSmartBadgeIsGreen(t *testing.T) {
	srv := createTestServer(t)
	var buf strings.Builder
	data := &ViewData{
		Disks: []*disks.DiskInfo{
			{
				Name:          "sdc",
				Path:          "/dev/sdc",
				ByIDPath:      "/dev/disk/by-id/scsi-0QEMU_QEMU_HARDDISK_drive-scsi2",
				Model:         "QEMU QEMU HARDDISK",
				SizeBytes:     5 * 1024 * 1024 * 1024,
				State:         disks.StateStandby,
				SmartStatus:   "PASSED (Standby Safe)",
				SmartLastTest: "",
				VdevType:      "unassigned (Ready for Pool)",
			},
		},
	}
	if err := srv.web.Render(&buf, "partials/disks_table.html", data); err != nil {
		t.Fatalf("Render disks_table.html failed: %v", err)
	}
	rendered := buf.String()
	if !strings.Contains(rendered, `badge-status green`) || strings.Contains(rendered, `<span class="badge-status red">`) {
		t.Fatalf("expected PASSED (Standby Safe) badge to be green, got HTML: %s", rendered)
	}
}

func TestUI_DatasetRowActionsAndDestroyLifecycle(t *testing.T) {
	srv := createTestServer(t)
	handler := srv.Routes()

	// 1. Create a child dataset under DATA
	createDSForm := url.Values{
		"parent":      {"DATA"},
		"name":        {"Any12"},
		"compression": {"lz4"},
		"recordsize":  {"128K"},
	}
	createReq := httptest.NewRequest("POST", "/ui/actions/zfs/datasets", strings.NewReader(createDSForm.Encode()))
	createReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	createRR := httptest.NewRecorder()
	handler.ServeHTTP(createRR, createReq)
	if createRR.Code != http.StatusOK || !strings.Contains(createRR.Body.String(), "Any12") {
		t.Fatalf("dataset create failed: %s", createRR.Header().Get("HX-Trigger"))
	}

	// 2. Datasets partial must render an always-visible Actions column containing Destroy for DATA/Any12
	dsReq := httptest.NewRequest("GET", "/ui/partials/datasets", nil)
	dsRR := httptest.NewRecorder()
	handler.ServeHTTP(dsRR, dsReq)
	dsBody := dsRR.Body.String()
	if !strings.Contains(dsBody, `>Actions</th>`) {
		t.Fatalf("expected explicit Actions column in datasets table")
	}
	if strings.Contains(dsBody, "display: none;\n      gap: 0.35rem;") {
		t.Fatalf("expected row actions to be always visible rather than hover-hidden")
	}
	if !strings.Contains(dsBody, `data-dataset="DATA/Any12"`) || !strings.Contains(dsBody, `openDangerConfirm('dataset', $el.dataset.dataset)`) {
		t.Fatalf("expected Destroy action for child dataset DATA/Any12, got: %s", dsBody)
	}
	if !strings.Contains(dsBody, `data-dataset="DATA"`) || !strings.Contains(dsBody, `openCreateDataset($el.dataset.dataset)`) {
		t.Fatalf("expected + Dataset action using dataset attribute for DATA")
	}

	// 3. Destroy dataset via danger confirm endpoint
	destroyForm := url.Values{
		"type":          {"dataset"},
		"name":          {"DATA/Any12"},
		"delete_shares": {"true"},
	}
	destroyReq := httptest.NewRequest("POST", "/ui/actions/zfs/destroy", strings.NewReader(destroyForm.Encode()))
	destroyReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	destroyRR := httptest.NewRecorder()
	handler.ServeHTTP(destroyRR, destroyReq)
	if destroyRR.Code != http.StatusOK {
		t.Fatalf("destroy dataset returned status %d", destroyRR.Code)
	}
	hxTrig := destroyRR.Header().Get("HX-Trigger")
	if !strings.Contains(hxTrig, "Dataset 'DATA/Any12' destroyed") || !strings.Contains(hxTrig, "datasetUpdate") {
		t.Fatalf("expected dataset destroy success + datasetUpdate trigger, got: %s", hxTrig)
	}

	// 4. Datasets partial after destroy must no longer include DATA/Any12
	afterReq := httptest.NewRequest("GET", "/ui/partials/datasets", nil)
	afterRR := httptest.NewRecorder()
	handler.ServeHTTP(afterRR, afterReq)
	if strings.Contains(afterRR.Body.String(), "DATA/Any12") {
		t.Fatalf("destroyed dataset DATA/Any12 still present in datasets partial")
	}
}

func TestUI_DestroyPoolWithChildDatasetsAndShares(t *testing.T) {
	srv := createTestServer(t)
	handler := srv.Routes()

	allDisks, err := srv.disks.ListDisks()
	if err != nil {
		t.Fatalf("disks.ListDisks failed: %v", err)
	}
	var freeDiskPath string
	for _, d := range allDisks {
		if !d.IsSystemDisk && d.ZfsPool == "" {
			if d.ByIDPath != "" {
				freeDiskPath = d.ByIDPath
			} else {
				freeDiskPath = d.Path
			}
			break
		}
	}
	if freeDiskPath == "" {
		t.Fatalf("expected at least 1 free mock disk")
	}

	// 1. Create pool "Any" and child dataset "Any/Any12"
	createForm := url.Values{
		"name":        {"Any"},
		"vdev_type":   {"stripe"},
		"disks":       {freeDiskPath},
		"ashift":      {"12"},
		"compression": {"zstd"},
	}
	createReq := httptest.NewRequest("POST", "/ui/actions/zfs/pools/create", strings.NewReader(createForm.Encode()))
	createReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	createRR := httptest.NewRecorder()
	handler.ServeHTTP(createRR, createReq)
	if createRR.Code != http.StatusOK || !strings.Contains(createRR.Body.String(), "Any") {
		t.Fatalf("pool 'Any' create failed: %s", createRR.Header().Get("HX-Trigger"))
	}

	dsForm := url.Values{
		"parent":      {"Any"},
		"name":        {"Any12"},
		"compression": {"lz4"},
		"recordsize":  {"128K"},
	}
	dsReq := httptest.NewRequest("POST", "/ui/actions/zfs/datasets", strings.NewReader(dsForm.Encode()))
	dsReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	dsRR := httptest.NewRecorder()
	handler.ServeHTTP(dsRR, dsReq)
	if dsRR.Code != http.StatusOK || !strings.Contains(dsRR.Body.String(), "Any12") {
		t.Fatalf("dataset 'Any/Any12' create failed: %s", dsRR.Header().Get("HX-Trigger"))
	}

	// 2. Attach SMB share on child dataset path and NFS export on pool path
	if err := srv.shares.SaveSmb(&shares.SmbShare{
		Name:    "any12share",
		Path:    "/mnt/Any/Any12",
		Enabled: true,
	}); err != nil {
		t.Fatalf("SaveSmb failed: %v", err)
	}
	if err := srv.shares.SaveNfs(&shares.NfsExport{
		Path:           "/mnt/Any",
		AllowedClients: "192.168.2.0/24",
		Options:        "rw,sync,no_subtree_check",
		Enabled:        true,
	}); err != nil {
		t.Fatalf("SaveNfs failed: %v", err)
	}

	// 3. Destroy pool "Any" with delete_shares=true
	destroyForm := url.Values{
		"type":          {"pool"},
		"name":          {"Any"},
		"delete_shares": {"true"},
	}
	destroyReq := httptest.NewRequest("POST", "/ui/actions/zfs/destroy", strings.NewReader(destroyForm.Encode()))
	destroyReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	destroyRR := httptest.NewRecorder()
	handler.ServeHTTP(destroyRR, destroyReq)
	if destroyRR.Code != http.StatusOK {
		t.Fatalf("destroy pool returned status %d", destroyRR.Code)
	}
	trig := destroyRR.Header().Get("HX-Trigger")
	for _, expected := range []string{
		"ZFS pool 'Any' permanently destroyed",
		"poolUpdate",
		"datasetUpdate",
		"snapshotUpdate",
		"shareUpdate",
		"diskUpdate",
	} {
		if !strings.Contains(trig, expected) {
			t.Fatalf("expected HX-Trigger to contain %q, got: %s", expected, trig)
		}
	}

	// 4. Verify pool, child dataset, SMB share, and NFS export are all removed
	pools, _ := srv.zfs.ListPools()
	for _, p := range pools {
		if p.Name == "Any" {
			t.Fatalf("pool 'Any' still present after destroy")
		}
	}
	datasets, _ := srv.zfs.ListDatasets()
	for _, ds := range datasets {
		if ds.Name == "Any" || strings.HasPrefix(ds.Name, "Any/") {
			t.Fatalf("dataset %q still present after pool destroy", ds.Name)
		}
	}
	smbList, _ := srv.shares.ListSmb()
	for _, smb := range smbList {
		if smb.Name == "any12share" {
			t.Fatalf("SMB share 'any12share' still present after pool destroy")
		}
	}
	nfsList, _ := srv.shares.ListNfs()
	for _, nfs := range nfsList {
		if nfs.Path == "/mnt/Any" {
			t.Fatalf("NFS export '/mnt/Any' still present after pool destroy")
		}
	}

	// 5. Verify destroying an invalid/failing pool triggers an error toast rather than false success
	//    and that danger_confirm modal keeps errors visible inline via handleDangerDestroyResult.
	errForm := url.Values{
		"type": {"pool"},
		"name": {"bad pool!"},
	}
	errReq := httptest.NewRequest("POST", "/ui/actions/zfs/destroy", strings.NewReader(errForm.Encode()))
	errReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	errRR := httptest.NewRecorder()
	handler.ServeHTTP(errRR, errReq)
	errTrig := errRR.Header().Get("HX-Trigger")
	if !strings.Contains(errTrig, `"type":"error"`) || !strings.Contains(errTrig, "Error destroying pool") {
		t.Fatalf("expected error trigger for failing pool destroy, got: %s", errTrig)
	}

	var modalBuf strings.Builder
	if err := srv.web.Render(&modalBuf, "modals/danger_confirm.html", &ViewData{}); err != nil {
		t.Fatalf("Render danger_confirm.html failed: %v", err)
	}
	modalHTML := modalBuf.String()
	if !strings.Contains(modalHTML, "handleDangerDestroyResult($event)") || !strings.Contains(modalHTML, "dangerConfirm.error") {
		t.Fatalf("danger_confirm modal must wire handleDangerDestroyResult($event) and inline dangerConfirm.error")
	}
}

func TestUI_FreshShareCreateImmediatelyEditableAndModalsLiveRefresh(t *testing.T) {
	srv := createTestServer(t)
	handler := srv.Routes()

	// 1. All live modal partial endpoints must return 200 OK with no-store Cache-Control.
	liveModalPartials := []string{
		"/ui/partials/modals/share_datasets",
		"/ui/partials/modals/parent_datasets",
		"/ui/partials/modals/available_disks",
		"/ui/partials/modals/importable_pools",
		"/ui/partials/modals/cloudsync_sources",
		"/ui/partials/shares",
	}
	for _, ep := range liveModalPartials {
		req := httptest.NewRequest("GET", ep, nil)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("GET %s returned status %d, expected 200", ep, rr.Code)
		}
		if cc := rr.Header().Get("Cache-Control"); !strings.Contains(cc, "no-store") {
			t.Fatalf("GET %s missing no-store Cache-Control header, got: %q", ep, cc)
		}
	}

	// 2. Create a new pool and dataset without reloading the root page.
	poolForm := url.Values{
		"name":        {"livepool"},
		"vdev_type":   {"stripe"},
		"disks":       {"/dev/sdb"},
		"compression": {"lz4"},
	}
	poolReq := httptest.NewRequest("POST", "/ui/actions/zfs/pools/create", strings.NewReader(poolForm.Encode()))
	poolReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	poolRR := httptest.NewRecorder()
	handler.ServeHTTP(poolRR, poolReq)
	if poolRR.Code != http.StatusOK || !strings.Contains(poolRR.Header().Get("HX-Trigger"), "poolUpdate") {
		t.Fatalf("failed creating livepool: %d %s", poolRR.Code, poolRR.Header().Get("HX-Trigger"))
	}

	dsForm := url.Values{
		"parent":      {"livepool"},
		"name":        {"livefs"},
		"compression": {"lz4"},
		"recordsize":  {"128K"},
	}
	dsReq := httptest.NewRequest("POST", "/ui/actions/zfs/datasets", strings.NewReader(dsForm.Encode()))
	dsReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	dsRR := httptest.NewRecorder()
	handler.ServeHTTP(dsRR, dsReq)
	if dsRR.Code != http.StatusOK || !strings.Contains(dsRR.Header().Get("HX-Trigger"), "datasetUpdate") {
		t.Fatalf("failed creating livepool/livefs: %d %s", dsRR.Code, dsRR.Header().Get("HX-Trigger"))
	}

	// Verify live modal endpoints immediately include the new dataset without a page reload.
	for _, ep := range []string{
		"/ui/partials/modals/share_datasets",
		"/ui/partials/modals/parent_datasets",
		"/ui/partials/modals/cloudsync_sources",
	} {
		req := httptest.NewRequest("GET", ep, nil)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if !strings.Contains(rr.Body.String(), "livepool/livefs") {
			t.Fatalf("expected %s to immediately expose new dataset livepool/livefs, got: %s", ep, rr.Body.String())
		}
	}

	// 3. Create an SMB share on the newly created dataset and verify returned markup is directly editable.
	smbForm := url.Values{
		"name":   {"live-smb"},
		"path":   {"/mnt/livepool/livefs"},
		"proto":  {"smb"},
		"access": {"restricted"},
	}
	smbReq := httptest.NewRequest("POST", "/ui/actions/shares/save", strings.NewReader(smbForm.Encode()))
	smbReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	smbRR := httptest.NewRecorder()
	handler.ServeHTTP(smbRR, smbReq)
	smbBody := smbRR.Body.String()
	for _, expect := range []string{
		`data-name="live-smb"`,
		`data-path="/mnt/livepool/livefs"`,
		`openEditSmbShare($el.dataset.name`,
		`hx-swap="outerHTML"`,
	} {
		if !strings.Contains(smbBody, expect) {
			t.Fatalf("saved SMB share partial missing %q so Edit without reload would fail", expect)
		}
	}

	// Edit that SMB share right away without reloading and verify Read-Only updates immediately in returned HTML.
	smbEditForm := url.Values{
		"orig_name": {"live-smb"},
		"orig_path": {"/mnt/livepool/livefs"},
		"name":      {"live-smb"},
		"path":      {"/mnt/livepool/livefs"},
		"proto":     {"smb"},
		"access":    {"readonly"},
	}
	smbEditReq := httptest.NewRequest("POST", "/ui/actions/shares/save", strings.NewReader(smbEditForm.Encode()))
	smbEditReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	smbEditRR := httptest.NewRecorder()
	handler.ServeHTTP(smbEditRR, smbEditReq)
	if !strings.Contains(smbEditRR.Body.String(), `data-readonly="true"`) {
		t.Fatalf("expected immediately updated SMB share to reflect data-readonly=\"true\", got: %s", smbEditRR.Body.String())
	}

	// 4. Create an NFS export and verify immediate editability without reload.
	nfsForm := url.Values{
		"name":            {"/mnt/livepool/livefs"},
		"path":            {"/mnt/livepool/livefs"},
		"proto":           {"nfs"},
		"access":          {"restricted"},
		"allowed_clients": {"192.168.10.0/24"},
	}
	nfsReq := httptest.NewRequest("POST", "/ui/actions/shares/save", strings.NewReader(nfsForm.Encode()))
	nfsReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	nfsRR := httptest.NewRecorder()
	handler.ServeHTTP(nfsRR, nfsReq)
	nfsBody := nfsRR.Body.String()
	if !strings.Contains(nfsBody, `data-clients="192.168.10.0/24"`) || !strings.Contains(nfsBody, `openEditNfsShare($el.dataset.path`) {
		t.Fatalf("saved NFS export missing immediate edit bindings: %s", nfsBody)
	}

	// 5. Render create_share modal and ensure error responses stay inline via nasTriggerHasError/shareFormError.
	var shareModalBuf strings.Builder
	if err := srv.web.Render(&shareModalBuf, "modals/create_share.html", srv.buildViewData()); err != nil {
		t.Fatalf("failed rendering modals/create_share.html: %v", err)
	}
	shareModalHTML := shareModalBuf.String()
	for _, expect := range []string{
		`/ui/partials/modals/share_datasets`,
		`hx-target="this"`,
		`nasTriggerHasError`,
		`shareFormError`,
		`hx-swap="outerHTML"`,
	} {
		if !strings.Contains(shareModalHTML, expect) {
			t.Fatalf("modals/create_share.html missing %q", expect)
		}
	}
}

func TestSnapshotRollback_UI_LifecycleAndErrors(t *testing.T) {
	srv := createTestServer(t)
	handler := srv.Routes()

	pools, err := srv.zfs.ListPools()
	if err != nil || len(pools) == 0 {
		t.Fatalf("expected mock pool, got len=%d err=%v", len(pools), err)
	}
	targetDS := pools[0].Name + "/rollbackui"
	if err := srv.zfs.CreateDataset(targetDS, "lz4", "128K"); err != nil {
		t.Fatalf("CreateDataset failed: %v", err)
	}

	// 1. Create older snapshot rb-s1 and newer snapshot rb-s2 via UI endpoint.
	for _, snapName := range []string{"rb-s1", "rb-s2"} {
		form := url.Values{
			"dataset":       {targetDS},
			"snapshot_name": {snapName},
		}
		req := httptest.NewRequest("POST", "/ui/actions/zfs/snapshots", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("create snapshot %s status=%d", snapName, rr.Code)
		}
		body := rr.Body.String()
		if !strings.Contains(body, targetDS+"@"+snapName) {
			t.Fatalf("created snapshot %s missing from returned partial", snapName)
		}
		if strings.Contains(body, `hx-swap="morph"`) {
			t.Fatalf("snapshots partial still contains hx-swap=\"morph\"")
		}
	}

	// 2. Clone newer snapshot rb-s2 -> rolling back to rb-s1 must fail cleanly with error toast.
	cloneTarget := pools[0].Name + "/rollbackclone"
	cloneForm := url.Values{
		"snapshot":       {targetDS + "@rb-s2"},
		"target_dataset": {cloneTarget},
	}
	cloneReq := httptest.NewRequest("POST", "/ui/actions/zfs/snapshots/clone", strings.NewReader(cloneForm.Encode()))
	cloneReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	cloneRR := httptest.NewRecorder()
	handler.ServeHTTP(cloneRR, cloneReq)
	if cloneRR.Code != http.StatusOK || strings.Contains(cloneRR.Header().Get("HX-Trigger"), `"error"`) {
		t.Fatalf("clone failed unexpectedly: status=%d trigger=%s", cloneRR.Code, cloneRR.Header().Get("HX-Trigger"))
	}

	blockedReq := httptest.NewRequest("POST", "/ui/actions/zfs/snapshots/rollback", strings.NewReader(url.Values{"snapshot": {targetDS + "@rb-s1"}}.Encode()))
	blockedReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	blockedRR := httptest.NewRecorder()
	handler.ServeHTTP(blockedRR, blockedReq)
	blockedTrig := blockedRR.Header().Get("HX-Trigger")
	if !strings.Contains(blockedTrig, `"error"`) || !strings.Contains(blockedTrig, "dependent clone") {
		t.Fatalf("expected clone-blocked rollback error toast, got: %s", blockedTrig)
	}
	if !strings.Contains(blockedRR.Body.String(), targetDS+"@rb-s2") {
		t.Fatalf("newer snapshot rb-s2 should remain when rollback fails")
	}

	// 3. Remove clone, then roll back to rb-s1 -> succeeds and removes rb-s2 immediately in HTML.
	if err := srv.zfs.DestroyDataset(cloneTarget); err != nil {
		t.Fatalf("DestroyDataset clone failed: %v", err)
	}

	rbReq := httptest.NewRequest("POST", "/ui/actions/zfs/snapshots/rollback", strings.NewReader(url.Values{"snapshot": {targetDS + "@rb-s1"}}.Encode()))
	rbReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rbRR := httptest.NewRecorder()
	handler.ServeHTTP(rbRR, rbReq)
	rbTrig := rbRR.Header().Get("HX-Trigger")
	for _, wantTrig := range []string{`"success"`, `"snapshotUpdate"`, `"datasetUpdate"`, `rolled back`} {
		if !strings.Contains(rbTrig, wantTrig) {
			t.Fatalf("expected HX-Trigger to contain %s, got: %s", wantTrig, rbTrig)
		}
	}
	rbBody := rbRR.Body.String()
	if !strings.Contains(rbBody, targetDS+"@rb-s1") {
		t.Fatalf("expected rollback target snapshot rb-s1 to remain in HTML")
	}
	if strings.Contains(rbBody, targetDS+"@rb-s2") {
		t.Fatalf("expected newer snapshot rb-s2 to be removed immediately after rollback")
	}
}

func TestUI_ExportedPoolAppearsInImportModalAndReimports(t *testing.T) {
	srv := createTestServer(t)
	handler := srv.Routes()

	pools, _ := srv.zfs.ListPools()
	if len(pools) == 0 {
		t.Fatalf("expected mock pool to exist initially")
	}
	targetPool := pools[0].Name

	// Export pool via UI action
	expReq := httptest.NewRequest("POST", "/ui/actions/zfs/pools/export", strings.NewReader(url.Values{"pool": {targetPool}}.Encode()))
	expReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	expRR := httptest.NewRecorder()
	handler.ServeHTTP(expRR, expReq)
	if expRR.Code != http.StatusOK || !strings.Contains(expRR.Header().Get("HX-Trigger"), "poolUpdate") {
		t.Fatalf("export failed: %d %s", expRR.Code, expRR.Header().Get("HX-Trigger"))
	}

	// Verify exported pool immediately appears in /ui/partials/modals/importable_pools
	impListReq := httptest.NewRequest("GET", "/ui/partials/modals/importable_pools", nil)
	impListRR := httptest.NewRecorder()
	handler.ServeHTTP(impListRR, impListReq)
	if impListRR.Code != http.StatusOK || !strings.Contains(impListRR.Body.String(), targetPool) {
		t.Fatalf("expected exported pool %q in importable_pools partial, got %d: %s", targetPool, impListRR.Code, impListRR.Body.String())
	}

	// Re-import via UI action and verify it disappears from importable_pools and returns to pools
	impReq := httptest.NewRequest("POST", "/ui/actions/zfs/pools/import", strings.NewReader(url.Values{"name_or_id": {targetPool}}.Encode()))
	impReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	impRR := httptest.NewRecorder()
	handler.ServeHTTP(impRR, impReq)
	if impRR.Code != http.StatusOK || !strings.Contains(impRR.Body.String(), targetPool) {
		t.Fatalf("import failed or pool missing from response: %d %s", impRR.Code, impRR.Header().Get("HX-Trigger"))
	}

	afterReq := httptest.NewRequest("GET", "/ui/partials/modals/importable_pools", nil)
	afterRR := httptest.NewRecorder()
	handler.ServeHTTP(afterRR, afterReq)
	if strings.Contains(afterRR.Body.String(), `data-pool-name="`+targetPool+`"`) {
		t.Fatalf("did not expect re-imported pool %q to remain in importable_pools", targetPool)
	}
}

func TestUI_SnapshotRollbackTreeAndChildWarning(t *testing.T) {
	srv := createTestServer(t)
	handler := srv.Routes()

	// Create a recursive snapshot on DATA (which has nested datasets DATA/Fileshare, etc.)
	snapForm := url.Values{
		"dataset":       {"DATA"},
		"snapshot_name": {"docker-like-snap"},
		"recursive":     {"true"},
	}
	snapReq := httptest.NewRequest("POST", "/ui/actions/zfs/snapshots", strings.NewReader(snapForm.Encode()))
	snapReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	snapRR := httptest.NewRecorder()
	handler.ServeHTTP(snapRR, snapReq)
	if snapRR.Code != http.StatusOK {
		t.Fatalf("snapshot create failed: %d", snapRR.Code)
	}
	if !strings.Contains(snapRR.Body.String(), "Rollback Tree") {
		t.Fatalf("expected 'Rollback Tree' button for parent snapshot with matching children, got:\n%s", snapRR.Body.String())
	}

	// Non-recursive rollback of DATA@docker-like-snap must warn about untouched child datasets
	rbSingleReq := httptest.NewRequest("POST", "/ui/actions/zfs/snapshots/rollback", strings.NewReader(url.Values{
		"snapshot": {"DATA@docker-like-snap"},
	}.Encode()))
	rbSingleReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rbSingleRR := httptest.NewRecorder()
	handler.ServeHTTP(rbSingleRR, rbSingleReq)
	if rbSingleRR.Code != http.StatusOK || !strings.Contains(rbSingleRR.Header().Get("HX-Trigger"), "child datasets unchanged") {
		t.Fatalf("expected child datasets unchanged note in HX-Trigger, got: %s", rbSingleRR.Header().Get("HX-Trigger"))
	}

	// Recursive tree rollback must roll back parent and child snapshots cleanly
	rbTreeReq := httptest.NewRequest("POST", "/ui/actions/zfs/snapshots/rollback", strings.NewReader(url.Values{
		"snapshot":  {"DATA@docker-like-snap"},
		"recursive": {"true"},
	}.Encode()))
	rbTreeReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rbTreeRR := httptest.NewRecorder()
	handler.ServeHTTP(rbTreeRR, rbTreeReq)
	if rbTreeRR.Code != http.StatusOK || !strings.Contains(rbTreeRR.Header().Get("HX-Trigger"), "rolled back recursively") {
		t.Fatalf("expected recursive rollback toast in HX-Trigger, got: %s", rbTreeRR.Header().Get("HX-Trigger"))
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func TestUI_AuditFixes_ServicesDiskDrawerDangerDestroyAndCloudSync(t *testing.T) {
	srv := createTestServer(t)
	handler := srv.Routes()

	// 1. Services partial renders real service rows
	svcReq := httptest.NewRequest("GET", "/ui/partials/services", nil)
	svcRR := httptest.NewRecorder()
	handler.ServeHTTP(svcRR, svcReq)
	if svcRR.Code != http.StatusOK || !strings.Contains(svcRR.Body.String(), "smbd.service") || !strings.Contains(svcRR.Body.String(), "nfs-kernel-server.service") {
		t.Fatalf("GET /ui/partials/services failed: code=%d body=%s", svcRR.Code, svcRR.Body.String())
	}

	// 2. Disk drawer actions work with both 'device'/'seconds' and 'path'/'timeout_minutes'
	allDisks, _ := srv.disks.ListDisks()
	if len(allDisks) == 0 {
		t.Fatalf("expected mock disks")
	}
	targetDisk := allDisks[0]
	for _, tc := range []struct {
		endpoint string
		form     url.Values
	}{
		{"/ui/actions/disks/spindown", url.Values{"device": {targetDisk.Path}, "seconds": {"1800"}}},
		{"/ui/actions/disks/apm", url.Values{"device": {targetDisk.Path}, "level": {"127"}}},
		{"/ui/actions/disks/locate", url.Values{"device": {targetDisk.Path}}},
		{"/ui/actions/disks/smart-test", url.Values{"device": {targetDisk.Path}, "type": {"short"}}},
		{"/ui/actions/disks/standby", url.Values{"device": {targetDisk.Path}}},
	} {
		req := httptest.NewRequest("POST", tc.endpoint, strings.NewReader(tc.form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK || strings.Contains(rr.Header().Get("HX-Trigger"), `"type":"error"`) {
			t.Fatalf("POST %s failed: code=%d trig=%s", tc.endpoint, rr.Code, rr.Header().Get("HX-Trigger"))
		}
	}

	// 3. Danger destroy with type=dataset returns #datasets-container (not #pools-container)
	_ = srv.zfs.CreateDataset("DATA/audit-del", "lz4", "128K")
	delReq := httptest.NewRequest("POST", "/ui/actions/zfs/destroy", strings.NewReader(url.Values{
		"type": {"dataset"},
		"name": {"DATA/audit-del"},
	}.Encode()))
	delReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	delRR := httptest.NewRecorder()
	handler.ServeHTTP(delRR, delReq)
	if delRR.Code != http.StatusOK || !strings.Contains(delRR.Body.String(), `id="datasets-container"`) {
		t.Fatalf("dataset danger destroy must render #datasets-container, got code=%d body=%s", delRR.Code, delRR.Body.String()[:min(300, delRR.Body.Len())])
	}

	// 4. CloudSync API preserves enabled=false and UI save creates a task with "Never" LastRun
	csApiReq := httptest.NewRequest("POST", "/api/v1/cloudsync/jobs", strings.NewReader(`{"name":"disabled-job","source_path":"/mnt/DATA","remote_target":"b2_backup:test","schedule":"0 2 * * *","enabled":false}`))
	csApiReq.Header.Set("Content-Type", "application/json")
	csApiRR := httptest.NewRecorder()
	handler.ServeHTTP(csApiRR, csApiReq)
	if csApiRR.Code != http.StatusOK {
		t.Fatalf("POST /api/v1/cloudsync/jobs failed: %d %s", csApiRR.Code, csApiRR.Body.String())
	}

	csPartialReq := httptest.NewRequest("GET", "/ui/partials/cloudsync", nil)
	csPartialRR := httptest.NewRecorder()
	handler.ServeHTTP(csPartialRR, csPartialReq)
	csHTML := csPartialRR.Body.String()
	if !strings.Contains(csHTML, "Disabled") || !strings.Contains(csHTML, "Never") {
		t.Fatalf("expected disabled/unrun CloudSync job to show 'Disabled' and 'Never', got:\n%s", csHTML)
	}
}

func TestUI_LiveUsedDisplayAndPollingWiring(t *testing.T) {
	srv := createTestServer(t)
	handler := srv.Routes()

	// 1. Root page must load /static/app.js so window.currentTab & nasRefreshTab are active on first paint.
	rootReq := httptest.NewRequest("GET", "/", nil)
	rootReq.AddCookie(&http.Cookie{Name: "nas_user", Value: "root"})
	rootRR := httptest.NewRecorder()
	handler.ServeHTTP(rootRR, rootReq)
	if rootRR.Code != http.StatusOK || !strings.Contains(rootRR.Body.String(), `<script src="/static/app.js"></script>`) {
		t.Fatalf("GET / must serve layout with /static/app.js, got status=%d", rootRR.Code)
	}

	// 2. Dynamic Used updates on datasets/system partials reflect fresh ZFS state without caching.
	if err := srv.zfs.CreateDataset("DATA/used-probe", "lz4", "128K"); err != nil {
		t.Fatalf("CreateDataset failed: %v", err)
	}
	dsReq1 := httptest.NewRequest("GET", "/ui/partials/datasets", nil)
	dsRR1 := httptest.NewRecorder()
	handler.ServeHTTP(dsRR1, dsReq1)
	if dsRR1.Code != http.StatusOK || !strings.Contains(dsRR1.Body.String(), "row-ds-DATA_used-probe") {
		t.Fatalf("initial datasets partial missing new dataset: status=%d", dsRR1.Code)
	}

	sysReq1 := httptest.NewRequest("GET", "/ui/partials/system", nil)
	sysRR1 := httptest.NewRecorder()
	handler.ServeHTTP(sysRR1, sysReq1)
	if sysRR1.Code != http.StatusOK || !strings.Contains(sysRR1.Body.String(), "DATA/used-probe") {
		t.Fatalf("system metrics partial missing DATA/used-probe: status=%d", sysRR1.Code)
	}

	// 3. Snapshot create / rollback / destroy must broadcast datasetUpdate & poolUpdate for live Used refresh.
	snapReq := httptest.NewRequest("POST", "/ui/actions/zfs/snapshots", strings.NewReader(url.Values{
		"dataset":       {"DATA/used-probe"},
		"snapshot_name": {"snap1"},
	}.Encode()))
	snapReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	snapRR := httptest.NewRecorder()
	handler.ServeHTTP(snapRR, snapReq)
	trig := snapRR.Header().Get("HX-Trigger")
	for _, want := range []string{"snapshotUpdate", "datasetUpdate", "poolUpdate"} {
		if !strings.Contains(trig, want) {
			t.Fatalf("snapshot create HX-Trigger missing %q: %s", want, trig)
		}
	}

	rbReq := httptest.NewRequest("POST", "/ui/actions/zfs/snapshots/rollback", strings.NewReader(url.Values{
		"snapshot": {"DATA/used-probe@snap1"},
	}.Encode()))
	rbReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rbRR := httptest.NewRecorder()
	handler.ServeHTTP(rbRR, rbReq)
	rbTrig := rbRR.Header().Get("HX-Trigger")
	for _, want := range []string{"snapshotUpdate", "datasetUpdate", "poolUpdate"} {
		if !strings.Contains(rbTrig, want) {
			t.Fatalf("snapshot rollback HX-Trigger missing %q: %s", want, rbTrig)
		}
	}
}


