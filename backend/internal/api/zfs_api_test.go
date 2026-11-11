package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"debian-nas/internal/zfs"
)

func doJSONRequest(t *testing.T, handler http.Handler, method, target string, payload any) *httptest.ResponseRecorder {
	t.Helper()
	var body *bytes.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal %s payload failed: %v", target, err)
		}
		body = bytes.NewReader(raw)
	} else {
		body = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, target, body)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func TestZfsRestAPI_AllEndpoints(t *testing.T) {
	srv := createTestServer(t)
	handler := srv.Routes()

	// 1. Create pool & verify listed
	rec := doJSONRequest(t, handler, http.MethodPost, "/api/v1/zfs/pools/create", map[string]any{
		"name":        "apipool",
		"vdev_type":   "mirror",
		"disks":       []string{"/dev/sdb", "/dev/sdc"},
		"ashift":      12,
		"compression": "zstd",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/v1/zfs/pools/create status=%d body=%s", rec.Code, rec.Body.String())
	}

	listPoolsRec := doJSONRequest(t, handler, http.MethodGet, "/api/v1/zfs/pools", nil)
	if listPoolsRec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/zfs/pools status=%d", listPoolsRec.Code)
	}
	var pools []*zfs.Pool
	if err := json.Unmarshal(listPoolsRec.Body.Bytes(), &pools); err != nil {
		t.Fatalf("unmarshal pools failed: %v", err)
	}
	foundPool := false
	for _, p := range pools {
		if p.Name == "apipool" {
			foundPool = true
			break
		}
	}
	if !foundPool {
		t.Fatal("created pool apipool not returned by GET /api/v1/zfs/pools")
	}

	// 2. Pool properties, Scrub start/stop, Trim, Clear
	for _, step := range []struct {
		method string
		path   string
		body   any
	}{
		{http.MethodPost, "/api/v1/zfs/pools/property", map[string]any{"pool": "apipool", "property": "autotrim", "value": "on"}},
		{http.MethodPost, "/api/v1/zfs/scrub", map[string]any{"pool": "apipool", "action": "start"}},
		{http.MethodPost, "/api/v1/zfs/scrub", map[string]any{"pool": "apipool", "action": "stop"}},
		{http.MethodPost, "/api/v1/zfs/pools/trim", map[string]any{"pool": "apipool"}},
		{http.MethodPost, "/api/v1/zfs/pools/clear", map[string]any{"pool": "apipool"}},
	} {
		r := doJSONRequest(t, handler, step.method, step.path, step.body)
		if r.Code != http.StatusOK {
			t.Fatalf("%s %s status=%d body=%s", step.method, step.path, r.Code, r.Body.String())
		}
	}

	// 3. Add Vdev + Disk attach/replace/offline/online/detach
	for _, step := range []struct {
		path string
		body any
	}{
		{"/api/v1/zfs/pools/vdev", map[string]any{"pool": "apipool", "vdev_type": "cache", "disks": []string{"/dev/nvme0n1"}}},
		{"/api/v1/zfs/pools/disk/attach", map[string]any{"pool": "apipool", "existing_dev": "sdb", "new_dev": "sdd"}},
		{"/api/v1/zfs/pools/disk/replace", map[string]any{"pool": "apipool", "old_dev": "sdd", "new_dev": "sde"}},
		{"/api/v1/zfs/pools/disk/offline", map[string]any{"pool": "apipool", "device": "sde"}},
		{"/api/v1/zfs/pools/disk/online", map[string]any{"pool": "apipool", "device": "sde"}},
		{"/api/v1/zfs/pools/disk/detach", map[string]any{"pool": "apipool", "device": "sde"}},
	} {
		r := doJSONRequest(t, handler, http.MethodPost, step.path, step.body)
		if r.Code != http.StatusOK {
			t.Fatalf("POST %s status=%d body=%s", step.path, r.Code, r.Body.String())
		}
	}

	// 4. Datasets, Properties & Zvols
	for _, step := range []struct {
		method string
		path   string
		body   any
	}{
		{http.MethodPost, "/api/v1/zfs/datasets", map[string]any{"name": "apipool/work", "compression": "lz4", "recordsize": "128K"}},
		{http.MethodPost, "/api/v1/zfs/datasets/property", map[string]any{"dataset": "apipool/work", "property": "quota", "value": "100G"}},
		{http.MethodPost, "/api/v1/zfs/zvols", map[string]any{"name": "apipool/block0", "size_bytes": uint64(4 * 1024 * 1024 * 1024), "volblocksize": "16k", "sparse": true}},
	} {
		r := doJSONRequest(t, handler, step.method, step.path, step.body)
		if r.Code != http.StatusOK {
			t.Fatalf("%s %s status=%d body=%s", step.method, step.path, r.Code, r.Body.String())
		}
	}

	dsRec := doJSONRequest(t, handler, http.MethodGet, "/api/v1/zfs/datasets", nil)
	if dsRec.Code != http.StatusOK || !bytes.Contains(dsRec.Body.Bytes(), []byte("apipool/work")) || !bytes.Contains(dsRec.Body.Bytes(), []byte("apipool/block0")) {
		t.Fatalf("GET /api/v1/zfs/datasets missing created items: %d %s", dsRec.Code, dsRec.Body.String())
	}

	// 5. Snapshots: create -> clone -> rollback -> destroy -> delete dataset
	for _, step := range []struct {
		method string
		path   string
		body   any
	}{
		{http.MethodPost, "/api/v1/zfs/snapshots", map[string]any{"dataset": "apipool/work", "name": "s1", "recursive": true}},
		{http.MethodPost, "/api/v1/zfs/snapshots", map[string]any{"dataset": "apipool/work", "name": "s2"}},
		{http.MethodPost, "/api/v1/zfs/snapshots/clone", map[string]any{"snapshot": "apipool/work@s1", "target_dataset": "apipool/work-clone"}},
		{http.MethodPost, "/api/v1/zfs/snapshots/rollback", map[string]any{"snapshot": "apipool/work@s1"}},
	} {
		r := doJSONRequest(t, handler, step.method, step.path, step.body)
		if r.Code != http.StatusOK {
			t.Fatalf("%s %s status=%d body=%s", step.method, step.path, r.Code, r.Body.String())
		}
	}

	snapListRec := doJSONRequest(t, handler, http.MethodGet, "/api/v1/zfs/snapshots", nil)
	if snapListRec.Code != http.StatusOK || bytes.Contains(snapListRec.Body.Bytes(), []byte("apipool/work@s2")) {
		t.Fatalf("expected s2 rolled back cleanly, got: %s", snapListRec.Body.String())
	}

	// Clean clone, then snapshot, then dataset
	if r := doJSONRequest(t, handler, http.MethodDelete, "/api/v1/zfs/datasets?name=apipool/work-clone", nil); r.Code != http.StatusOK {
		t.Fatalf("DELETE cloned dataset status=%d body=%s", r.Code, r.Body.String())
	}
	if r := doJSONRequest(t, handler, http.MethodDelete, "/api/v1/zfs/snapshots?snapshot=apipool/work@s1", nil); r.Code != http.StatusOK {
		t.Fatalf("DELETE snapshot status=%d body=%s", r.Code, r.Body.String())
	}
	if r := doJSONRequest(t, handler, http.MethodDelete, "/api/v1/zfs/datasets?name=apipool/work", nil); r.Code != http.StatusOK {
		t.Fatalf("DELETE dataset status=%d body=%s", r.Code, r.Body.String())
	}

	// 6. Export -> list importable -> import -> destroy pool
	if r := doJSONRequest(t, handler, http.MethodPost, "/api/v1/zfs/pools/export", map[string]any{"pool": "apipool"}); r.Code != http.StatusOK {
		t.Fatalf("POST /api/v1/zfs/pools/export status=%d body=%s", r.Code, r.Body.String())
	}
	impList := doJSONRequest(t, handler, http.MethodGet, "/api/v1/zfs/pools/importable", nil)
	if impList.Code != http.StatusOK || !bytes.Contains(impList.Body.Bytes(), []byte("apipool")) {
		t.Fatalf("expected apipool in importable list: %d %s", impList.Code, impList.Body.String())
	}
	if r := doJSONRequest(t, handler, http.MethodPost, "/api/v1/zfs/pools/import", map[string]any{"name_or_id": "apipool", "new_name": "apipool2", "force": false}); r.Code != http.StatusOK {
		t.Fatalf("POST /api/v1/zfs/pools/import status=%d body=%s", r.Code, r.Body.String())
	}
	if r := doJSONRequest(t, handler, http.MethodDelete, "/api/v1/zfs/pools?name=apipool2", nil); r.Code != http.StatusOK {
		t.Fatalf("DELETE /api/v1/zfs/pools status=%d body=%s", r.Code, r.Body.String())
	}
}
