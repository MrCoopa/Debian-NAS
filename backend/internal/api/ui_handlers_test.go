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
	if !strings.Contains(body, "Debian") || !strings.Contains(body, "hx-ext=\"morph\"") {
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

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

