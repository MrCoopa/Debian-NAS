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

	// 1. Root Layout
	req := httptest.NewRequest("GET", "/", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("GET / returned status %d, expected 200. Body: %s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, "Debian") || !strings.Contains(body, "hx-ext=\"morph\"") {
		t.Errorf("GET / missing expected markup: %s", body[:min(len(body), 500)])
	}

	// 2. Partials
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
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

