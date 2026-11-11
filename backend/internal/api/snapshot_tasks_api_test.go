package api

import (
	"bytes"
	"encoding/json"
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

func newTestSnapshotTaskServer(t *testing.T) *Server {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.MockMode = true
	staticSub, err := web.GetFS()
	if err != nil {
		t.Fatalf("web.GetFS failed: %v", err)
	}
	return NewServer(
		cfg,
		system.NewCollector(true),
		disks.NewManager(true, cfg.HdIdleConfig),
		zfs.NewManager(true),
		shares.NewManager(true, cfg.SambaConfDir, cfg.NfsConfFile),
		cloudsync.NewManager(true, cfg.RcloneConfig),
		staticSub,
	)
}

func TestSnapshotTasks_UIAndAPIRoutes(t *testing.T) {
	srv := newTestSnapshotTaskServer(t)
	handler := srv.Routes()

	// 1. UI partial GET
	req := httptest.NewRequest(http.MethodGet, "/ui/partials/snapshot-tasks", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /ui/partials/snapshot-tasks status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"Periodic Snapshot Tasks", "DATA", "MEDIA-12TB", "SSD-MIRROR/mail-archiver"} {
		if !strings.Contains(body, want) {
			t.Errorf("GET /ui/partials/snapshot-tasks missing %q", want)
		}
	}

	// 2. UI save action
	form := url.Values{
		"dataset":        {"DATA/Backup"},
		"recursive":      {"true"},
		"lifetime_value": {"14"},
		"lifetime_unit":  {"day(s)"},
		"naming_schema":  {"auto-%Y-%m-%d_%H-%M"},
		"schedule":       {"0 0 * * *"},
		"enabled":        {"true"},
	}
	saveReq := httptest.NewRequest(http.MethodPost, "/ui/actions/zfs/snapshot-tasks/save", strings.NewReader(form.Encode()))
	saveReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	saveRec := httptest.NewRecorder()
	handler.ServeHTTP(saveRec, saveReq)
	if saveRec.Code != http.StatusOK {
		t.Fatalf("POST /ui/actions/zfs/snapshot-tasks/save status = %d", saveRec.Code)
	}
	if !strings.Contains(saveRec.Body.String(), "DATA/Backup") || !strings.Contains(saveRec.Body.String(), "14 day(s)") {
		t.Errorf("saved task not reflected in partial HTML")
	}

	// Locate created task ID via JSON GET
	apiListReq := httptest.NewRequest(http.MethodGet, "/api/v1/zfs/snapshot-tasks", nil)
	apiListRec := httptest.NewRecorder()
	handler.ServeHTTP(apiListRec, apiListReq)
	if apiListRec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/zfs/snapshot-tasks status = %d", apiListRec.Code)
	}
	var tasks []zfs.SnapshotTask
	if err := json.Unmarshal(apiListRec.Body.Bytes(), &tasks); err != nil {
		t.Fatalf("unmarshal tasks JSON failed: %v", err)
	}
	var createdID string
	for _, item := range tasks {
		if item.Dataset == "DATA/Backup" {
			createdID = item.ID
			break
		}
	}
	if createdID == "" {
		t.Fatal("created task DATA/Backup not found via /api/v1/zfs/snapshot-tasks")
	}

	// 3. UI toggle action
	toggleReq := httptest.NewRequest(http.MethodPost, "/ui/actions/zfs/snapshot-tasks/toggle", strings.NewReader("id="+url.QueryEscape(createdID)))
	toggleReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	toggleRec := httptest.NewRecorder()
	handler.ServeHTTP(toggleRec, toggleReq)
	if toggleRec.Code != http.StatusOK || !strings.Contains(toggleRec.Body.String(), "Disabled") {
		t.Errorf("expected toggled task to display Disabled")
	}

	// 4. UI Run Now action
	runReq := httptest.NewRequest(http.MethodPost, "/ui/actions/zfs/snapshot-tasks/run", strings.NewReader("id="+url.QueryEscape(createdID)))
	runReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	runRec := httptest.NewRecorder()
	handler.ServeHTTP(runRec, runReq)
	if runRec.Code != http.StatusOK {
		t.Fatalf("POST /ui/actions/zfs/snapshot-tasks/run status = %d", runRec.Code)
	}
	if hxTrig := runRec.Header().Get("HX-Trigger"); !strings.Contains(hxTrig, "snapshotUpdate") || !strings.Contains(hxTrig, "snapshotTaskUpdate") {
		t.Errorf("expected HX-Trigger to include snapshotUpdate and snapshotTaskUpdate, got %q", hxTrig)
	}

	// 5. JSON API toggle / delete roundtrip
	apiDelPayload, _ := json.Marshal(map[string]string{"id": createdID})
	delReq := httptest.NewRequest(http.MethodDelete, "/api/v1/zfs/snapshot-tasks", bytes.NewReader(apiDelPayload))
	delReq.Header.Set("Content-Type", "application/json")
	delRec := httptest.NewRecorder()
	handler.ServeHTTP(delRec, delReq)
	if delRec.Code != http.StatusOK {
		t.Fatalf("DELETE /api/v1/zfs/snapshot-tasks status = %d: %s", delRec.Code, delRec.Body.String())
	}
}

