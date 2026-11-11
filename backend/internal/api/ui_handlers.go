package api

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"

	"debian-nas/internal/disks"
	"debian-nas/internal/shares"
	"debian-nas/internal/system"
	"debian-nas/internal/zfs"
)

// ViewData is passed to the root layout and all partials
type ViewData struct {
	System        *system.SystemStatus
	Disks         []*disks.DiskInfo
	Pools         []*zfs.Pool
	Datasets      []*zfs.Dataset
	SmbShares     []*shares.SmbShare
	NfsExports    []*shares.NfsExport
	Users         []shares.StorageUser
	Snapshots     []*zfs.Snapshot
	ZfsVersion    string
	TotalDisks    int
	TotalRawBytes uint64
	StandbyCount  int
	SpinningCount int
}

func (s *Server) buildViewData() *ViewData {
	status := s.sys.GetStatus()
	allDisks, _ := s.disks.ListDisks()
	pools, _ := s.zfs.ListPools()
	datasets, _ := s.zfs.ListDatasets()
	snapshots, _ := s.zfs.ListSnapshots()
	smbShares, _ := s.shares.ListSmb()
	nfsExports, _ := s.shares.ListNfs()
	users, _ := s.shares.ListUsers()

	standbyCount := 0
	spinningCount := 0
	var totalRawBytes uint64
	for _, d := range allDisks {
		totalRawBytes += d.SizeBytes
		if d.IsRotational {
			if d.State == disks.StateStandby || d.State == disks.StateSleep {
				standbyCount++
			} else if d.State == disks.StateActive {
				spinningCount++
			}
		}
	}

	zfsVer := "2.2.6-native"
	if len(pools) > 0 {
		zfsVer = "OpenZFS 2.2 Native"
	}

	return &ViewData{
		System:        status,
		Disks:         allDisks,
		Pools:         pools,
		Datasets:      datasets,
		SmbShares:     smbShares,
		NfsExports:    nfsExports,
		Users:         users,
		Snapshots:     snapshots,
		ZfsVersion:    zfsVer,
		TotalDisks:    len(allDisks),
		TotalRawBytes: totalRawBytes,
		StandbyCount:  standbyCount,
		SpinningCount: spinningCount,
	}
}

func (s *Server) setHXTrigger(w http.ResponseWriter, toastMsg, toastType string, extraTriggers ...string) {
	tr := make(map[string]interface{})
	if toastMsg != "" {
		tr["showToast"] = map[string]string{
			"message": toastMsg,
			"type":    toastType,
		}
	}
	for _, et := range extraTriggers {
		tr[et] = true
	}
	if b, err := json.Marshal(tr); err == nil {
		w.Header().Set("HX-Trigger", string(b))
	}
}

// ==============================================================================
// Page & Partial Handlers
// ==============================================================================

func (s *Server) handleUIRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		// Serve static files or 404
		if s.staticFS != nil {
			http.FileServer(http.FS(s.staticFS)).ServeHTTP(w, r)
			return
		}
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	if err := s.web.Render(w, "layout.html", s.buildViewData()); err != nil {
		log.Printf("[UI] Template render error: %v", err)
		http.Error(w, "UI Rendering Error: "+err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) handleUIPartialSystem(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = s.web.Render(w, "partials/system_metrics.html", s.buildViewData())
}

func (s *Server) handleUIPartialTop(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = s.web.Render(w, "partials/topbar.html", s.buildViewData())
}

func (s *Server) handleUIPartialPools(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = s.web.Render(w, "partials/pools_list.html", s.buildViewData())
}

func (s *Server) handleUIPartialDisks(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = s.web.Render(w, "partials/disks_table.html", s.buildViewData())
}

func (s *Server) handleUIPartialDatasets(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = s.web.Render(w, "partials/datasets_list.html", s.buildViewData())
}

func (s *Server) handleUIPartialShares(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = s.web.Render(w, "partials/shares_list.html", s.buildViewData())
}

func (s *Server) handleUIPartialUsers(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = s.web.Render(w, "partials/users_list.html", s.buildViewData())
}

func (s *Server) handleUIPartialSnapshots(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = s.web.Render(w, "partials/snapshots_list.html", s.buildViewData())
}

func (s *Server) handleUIPartialNetwork(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = s.web.Render(w, "partials/network_list.html", s.buildViewData())
}

func (s *Server) handleUIPartialCreateShareModal(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = s.web.Render(w, "modals/create_share.html", s.buildViewData())
}

func (s *Server) handleUIPartialShareModalUsers(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = s.web.Render(w, "partials/share_users_checkboxes.html", s.buildViewData())
}

// ==============================================================================
// UI Action Handlers (HTMX Form submissions & button triggers)
// ==============================================================================

func (s *Server) handleUIActionSaveSmb(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.setHXTrigger(w, "Invalid form: "+err.Error(), "error")
		s.handleUIPartialShares(w, r)
		return
	}

	proto := strings.ToLower(strings.TrimSpace(r.FormValue("proto")))
	name := strings.TrimSpace(r.FormValue("name"))
	path := strings.TrimSpace(r.FormValue("path"))
	comment := strings.TrimSpace(r.FormValue("comment"))
	access := strings.ToLower(strings.TrimSpace(r.FormValue("access")))
	allowedClients := strings.TrimSpace(r.FormValue("allowed_clients"))
	if allowedClients == "" {
		allowedClients = "*"
	}
	if proto == "nfs" && name == "" {
		name = path
	}

	if name == "" || path == "" {
		s.setHXTrigger(w, "Share name and path are required", "error")
		s.handleUIPartialShares(w, r)
		return
	}

	if proto == "nfs" {
		origPath := strings.TrimSpace(r.FormValue("orig_path"))
		if origPath != "" && origPath != path {
			_ = s.shares.DeleteNfs(origPath)
		}

		isReadOnly := (access == "readonly")
		opts := "rw,sync,no_subtree_check"
		if isReadOnly {
			opts = "ro,sync,no_subtree_check"
		}
		exp := &shares.NfsExport{
			Path:           path,
			AllowedClients: allowedClients,
			Options:        opts,
			ReadOnly:       isReadOnly,
			Enabled:        true,
		}
		if err := s.shares.SaveNfs(exp); err != nil {
			s.setHXTrigger(w, "Error saving NFS export: "+err.Error(), "error")
		} else {
			s.setHXTrigger(w, fmt.Sprintf("NFS export '%s' successfully saved", path), "success", "shareUpdate")
		}
	} else {
		origName := strings.TrimSpace(r.FormValue("orig_name"))
		if origName != "" && origName != name {
			_ = s.shares.DeleteSmb(origName)
		}

		users := r.Form["valid_users"]
		if len(users) == 0 {
			users = r.Form["users"]
		}
		readOnly := (access == "readonly") || (r.FormValue("read_only") == "true")
		guestOk := (access == "guest")

		smb := &shares.SmbShare{
			Name:       name,
			Path:       path,
			Comment:    comment,
			ValidUsers: users,
			ReadOnly:   readOnly,
			GuestOk:    guestOk,
			Enabled:    true,
		}
		if err := s.shares.SaveSmb(smb); err != nil {
			s.setHXTrigger(w, "Error saving SMB share: "+err.Error(), "error")
		} else {
			s.setHXTrigger(w, fmt.Sprintf("SMB share '%s' successfully configured", name), "success", "shareUpdate", "userUpdate")
		}
	}

	s.handleUIPartialShares(w, r)
}

func (s *Server) handleUIActionDeleteSmb(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if name == "" {
		_ = r.ParseForm()
		name = strings.TrimSpace(r.FormValue("name"))
	}
	if name == "" {
		s.setHXTrigger(w, "Share name is required", "error")
		s.handleUIPartialShares(w, r)
		return
	}

	if err := s.shares.DeleteSmb(name); err != nil {
		s.setHXTrigger(w, "Error deleting share: "+err.Error(), "error")
	} else {
		s.setHXTrigger(w, fmt.Sprintf("SMB share '%s' removed", name), "success", "shareUpdate", "userUpdate")
	}
	s.handleUIPartialShares(w, r)
}

func (s *Server) handleUIActionDeleteNfs(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimSpace(r.URL.Query().Get("path"))
	if path == "" {
		_ = r.ParseForm()
		path = strings.TrimSpace(r.FormValue("path"))
	}
	if path == "" {
		s.setHXTrigger(w, "NFS export path is required", "error")
		s.handleUIPartialShares(w, r)
		return
	}

	if err := s.shares.DeleteNfs(path); err != nil {
		s.setHXTrigger(w, "Error removing NFS export: "+err.Error(), "error")
	} else {
		s.setHXTrigger(w, fmt.Sprintf("NFS export '%s' removed", path), "success", "shareUpdate")
	}
	s.handleUIPartialShares(w, r)
}

func (s *Server) handleUIActionCreateUser(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	username := strings.TrimSpace(r.FormValue("username"))
	password := r.FormValue("password")

	if username == "" || password == "" {
		s.setHXTrigger(w, "Username and password are required", "error")
		s.handleUIPartialUsers(w, r)
		return
	}

	if err := s.shares.CreateUser(username, password, "user"); err != nil {
		s.setHXTrigger(w, "Error creating user: "+err.Error(), "error")
	} else {
		s.setHXTrigger(w, fmt.Sprintf("Storage user '%s' created", username), "success", "userUpdate", "shareUpdate")
	}
	s.handleUIPartialUsers(w, r)
}

func (s *Server) handleUIActionDeleteUser(w http.ResponseWriter, r *http.Request) {
	username := strings.TrimSpace(r.URL.Query().Get("username"))
	if username == "" {
		_ = r.ParseForm()
		username = strings.TrimSpace(r.FormValue("username"))
	}
	if username == "" {
		s.setHXTrigger(w, "Username is required", "error")
		s.handleUIPartialUsers(w, r)
		return
	}

	if err := s.shares.DeleteUser(username); err != nil {
		s.setHXTrigger(w, "Error deleting user: "+err.Error(), "error")
	} else {
		s.setHXTrigger(w, fmt.Sprintf("Storage user '%s' deleted", username), "success", "userUpdate", "shareUpdate")
	}
	s.handleUIPartialUsers(w, r)
}

func (s *Server) handleUIActionCreatePool(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	name := strings.TrimSpace(r.FormValue("name"))
	vdevType := r.FormValue("vdev_type")
	disksList := r.Form["disks"]
	ashift, _ := strconv.Atoi(r.FormValue("ashift"))
	if ashift == 0 {
		ashift = 12
	}
	compression := r.FormValue("compression")
	if compression == "" {
		compression = "lz4"
	}

	if name == "" || len(disksList) == 0 {
		s.setHXTrigger(w, "Pool name and at least one disk are required", "error")
		s.handleUIPartialPools(w, r)
		return
	}

	if err := s.zfs.CreatePool(name, vdevType, disksList, ashift, compression); err != nil {
		s.setHXTrigger(w, "Error creating pool: "+err.Error(), "error")
	} else {
		s.setHXTrigger(w, fmt.Sprintf("ZFS pool '%s' successfully created", name), "success", "poolUpdate", "datasetUpdate", "diskUpdate")
	}
	s.handleUIPartialPools(w, r)
}

func (s *Server) handleUIActionImportPool(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	nameOrID := strings.TrimSpace(r.FormValue("name_or_id"))
	newName := strings.TrimSpace(r.FormValue("new_name"))
	force := r.FormValue("force") == "true"

	if nameOrID == "" {
		s.setHXTrigger(w, "Pool name or ID is required", "error")
		s.handleUIPartialPools(w, r)
		return
	}

	if err := s.zfs.ImportPool(nameOrID, newName, force); err != nil {
		s.setHXTrigger(w, "Error importing pool: "+err.Error(), "error")
	} else {
		s.setHXTrigger(w, fmt.Sprintf("Pool '%s' successfully imported", nameOrID), "success", "poolUpdate", "datasetUpdate", "diskUpdate")
	}
	s.handleUIPartialPools(w, r)
}

func (s *Server) handleUIActionExportPool(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	pool := strings.TrimSpace(r.FormValue("pool"))
	if pool == "" {
		s.setHXTrigger(w, "Pool name is required", "error")
		s.handleUIPartialPools(w, r)
		return
	}

	if err := s.zfs.ExportPool(pool); err != nil {
		s.setHXTrigger(w, "Error exporting pool: "+err.Error(), "error")
	} else {
		s.setHXTrigger(w, fmt.Sprintf("Pool '%s' safely exported", pool), "success", "poolUpdate", "datasetUpdate", "diskUpdate")
	}
	s.handleUIPartialPools(w, r)
}

func (s *Server) handleUIActionClearPool(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	pool := strings.TrimSpace(r.FormValue("pool"))
	if pool == "" {
		s.setHXTrigger(w, "Pool name is required", "error")
		s.handleUIPartialPools(w, r)
		return
	}

	if err := s.zfs.ClearPool(pool); err != nil {
		s.setHXTrigger(w, "Error clearing pool errors: "+err.Error(), "error")
	} else {
		s.setHXTrigger(w, fmt.Sprintf("Error counters for pool '%s' cleared", pool), "success", "poolUpdate")
	}
	s.handleUIPartialPools(w, r)
}

func (s *Server) handleUIActionTrimPool(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	pool := strings.TrimSpace(r.FormValue("pool"))
	if pool == "" {
		s.setHXTrigger(w, "Pool name is required", "error")
		s.handleUIPartialPools(w, r)
		return
	}

	if err := s.zfs.TrimPool(pool); err != nil {
		s.setHXTrigger(w, "TRIM error: "+err.Error(), "error")
	} else {
		s.setHXTrigger(w, fmt.Sprintf("SSD TRIM started for pool '%s'", pool), "success", "poolUpdate")
	}
	s.handleUIPartialPools(w, r)
}

func (s *Server) handleUIActionScrub(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	pool := strings.TrimSpace(r.FormValue("pool"))
	action := r.FormValue("action")

	if pool == "" {
		s.setHXTrigger(w, "Pool name is required", "error")
		s.handleUIPartialPools(w, r)
		return
	}

	if action == "stop" {
		if err := s.zfs.StopScrub(pool); err != nil {
			s.setHXTrigger(w, "Scrub stop error: "+err.Error(), "error")
		} else {
			s.setHXTrigger(w, fmt.Sprintf("Scrub stopped for pool '%s'", pool), "info", "poolUpdate")
		}
	} else {
		if err := s.zfs.StartScrub(pool); err != nil {
			s.setHXTrigger(w, "Scrub start error: "+err.Error(), "error")
		} else {
			s.setHXTrigger(w, fmt.Sprintf("ZFS data scrub started for pool '%s'", pool), "success", "poolUpdate")
		}
	}
	s.handleUIPartialPools(w, r)
}

func (s *Server) handleUIActionAddVdev(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	pool := strings.TrimSpace(r.FormValue("pool"))
	vdevType := r.FormValue("vdev_type")
	disksList := r.Form["disks"]

	if pool == "" || len(disksList) == 0 {
		s.setHXTrigger(w, "Pool and at least one disk are required", "error")
		s.handleUIPartialPools(w, r)
		return
	}

	if err := s.zfs.AddVdev(pool, vdevType, disksList); err != nil {
		s.setHXTrigger(w, "Error adding VDEV: "+err.Error(), "error")
	} else {
		s.setHXTrigger(w, fmt.Sprintf("VDEV '%s' added to pool '%s'", vdevType, pool), "success", "poolUpdate", "diskUpdate")
	}
	s.handleUIPartialPools(w, r)
}

func (s *Server) handleUIActionSetPoolProperty(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	pool := strings.TrimSpace(r.FormValue("pool"))
	autotrim := r.FormValue("autotrim")
	autoexpand := r.FormValue("autoexpand")

	if pool == "" {
		s.setHXTrigger(w, "Pool name is required", "error")
		s.handleUIPartialPools(w, r)
		return
	}

	if autotrim != "" {
		_ = s.zfs.SetPoolProperty(pool, "autotrim", autotrim)
	}
	if autoexpand != "" {
		_ = s.zfs.SetPoolProperty(pool, "autoexpand", autoexpand)
	}
	s.setHXTrigger(w, fmt.Sprintf("Properties updated for pool '%s'", pool), "success", "poolUpdate")
	s.handleUIPartialPools(w, r)
}

func (s *Server) handleUIActionDiskAction(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	actionType := strings.ToLower(strings.TrimSpace(r.FormValue("action_type")))
	if actionType == "" {
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		if len(parts) > 0 {
			last := strings.ToLower(parts[len(parts)-1])
			if last == "replace" || last == "attach" || last == "detach" || last == "offline" || last == "online" {
				actionType = last
			}
		}
	}
	pool := strings.TrimSpace(r.FormValue("pool"))
	targetDevice := strings.TrimSpace(r.FormValue("target_device"))
	if targetDevice == "" {
		targetDevice = strings.TrimSpace(r.FormValue("device"))
	}
	newDevice := strings.TrimSpace(r.FormValue("new_device"))

	var err error
	switch actionType {
	case "replace":
		err = s.zfs.ReplaceDisk(pool, targetDevice, newDevice)
	case "attach":
		err = s.zfs.AttachDisk(pool, targetDevice, newDevice)
	case "detach":
		err = s.zfs.DetachDisk(pool, targetDevice)
	case "offline":
		err = s.zfs.OfflineDisk(pool, targetDevice)
	case "online":
		err = s.zfs.OnlineDisk(pool, targetDevice)
	default:
		err = fmt.Errorf("unknown disk action: %s", actionType)
	}

	if err != nil {
		s.setHXTrigger(w, "Action error: "+err.Error(), "error")
	} else {
		s.setHXTrigger(w, fmt.Sprintf("Action '%s' completed successfully", actionType), "success", "poolUpdate", "diskUpdate")
	}
	s.handleUIPartialPools(w, r)
}

func (s *Server) handleUIActionCreateDataset(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	parent := strings.TrimSpace(r.FormValue("parent"))
	name := strings.TrimSpace(r.FormValue("name"))
	compression := r.FormValue("compression")
	recordsize := r.FormValue("recordsize")
	quota := strings.TrimSpace(r.FormValue("quota"))
	atime := r.FormValue("atime")

	if parent == "" || name == "" {
		s.setHXTrigger(w, "Parent and dataset name are required", "error")
		s.handleUIPartialDatasets(w, r)
		return
	}

	fullName := parent + "/" + name
	if err := s.zfs.CreateDataset(fullName, compression, recordsize); err != nil {
		s.setHXTrigger(w, "Error creating dataset: "+err.Error(), "error")
	} else {
		if atime != "" && atime != "off" {
			_ = s.zfs.SetDatasetProperty(fullName, "atime", atime)
		}
		if quota != "" && quota != "none" {
			_ = s.zfs.SetDatasetProperty(fullName, "quota", quota)
		}
		s.setHXTrigger(w, fmt.Sprintf("Dataset '%s' created", fullName), "success", "datasetUpdate")
	}
	s.handleUIPartialDatasets(w, r)
}

func (s *Server) handleUIActionSetDatasetProperty(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	dataset := strings.TrimSpace(r.FormValue("dataset"))
	compression := r.FormValue("compression")
	recordsize := r.FormValue("recordsize")
	quota := strings.TrimSpace(r.FormValue("quota"))
	readonly := r.FormValue("readonly")
	atime := r.FormValue("atime")

	if dataset == "" {
		s.setHXTrigger(w, "Dataset name is required", "error")
		s.handleUIPartialDatasets(w, r)
		return
	}

	if compression != "" {
		_ = s.zfs.SetDatasetProperty(dataset, "compression", compression)
	}
	if recordsize != "" {
		_ = s.zfs.SetDatasetProperty(dataset, "recordsize", recordsize)
	}
	if quota != "" {
		_ = s.zfs.SetDatasetProperty(dataset, "quota", quota)
	}
	if readonly != "" {
		_ = s.zfs.SetDatasetProperty(dataset, "readonly", readonly)
	}
	if atime != "" {
		_ = s.zfs.SetDatasetProperty(dataset, "atime", atime)
	}

	s.setHXTrigger(w, fmt.Sprintf("Properties updated for '%s'", dataset), "success", "datasetUpdate")
	s.handleUIPartialDatasets(w, r)
}

func (s *Server) handleUIActionCreateZvol(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	parent := strings.TrimSpace(r.FormValue("parent"))
	name := strings.TrimSpace(r.FormValue("name"))
	sizeVal, _ := strconv.ParseUint(r.FormValue("size_val"), 10, 64)
	sizeUnit := r.FormValue("size_unit")
	volblocksize := r.FormValue("volblocksize")
	sparse := r.FormValue("sparse") == "true"

	if parent == "" || name == "" || sizeVal == 0 {
		s.setHXTrigger(w, "Parent, name, and size are required", "error")
		s.handleUIPartialDatasets(w, r)
		return
	}

	var multiplier uint64 = 1024 * 1024 * 1024 // GiB
	if sizeUnit == "T" {
		multiplier = 1024 * 1024 * 1024 * 1024
	} else if sizeUnit == "M" {
		multiplier = 1024 * 1024
	}
	totalBytes := sizeVal * multiplier

	fullName := parent + "/" + name
	if err := s.zfs.CreateZvol(fullName, totalBytes, volblocksize, sparse); err != nil {
		s.setHXTrigger(w, "Error creating zvol: "+err.Error(), "error")
	} else {
		s.setHXTrigger(w, fmt.Sprintf("Zvol block device '%s' created", fullName), "success", "datasetUpdate")
	}
	s.handleUIPartialDatasets(w, r)
}

func (s *Server) handleUIActionCreateSnapshot(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	dataset := strings.TrimSpace(r.FormValue("dataset"))
	snapName := strings.TrimSpace(r.FormValue("snapshot_name"))

	if dataset == "" || snapName == "" {
		s.setHXTrigger(w, "Dataset and snapshot name are required", "error")
		s.handleUIPartialSnapshots(w, r)
		return
	}

	if err := s.zfs.CreateSnapshot(dataset, snapName); err != nil {
		s.setHXTrigger(w, "Error creating snapshot: "+err.Error(), "error")
	} else {
		s.setHXTrigger(w, fmt.Sprintf("Snapshot '%s@%s' created", dataset, snapName), "success", "snapshotUpdate", "datasetUpdate")
	}
	s.handleUIPartialSnapshots(w, r)
}

func (s *Server) handleUIActionRollbackSnapshot(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	snapshot := strings.TrimSpace(r.FormValue("snapshot"))
	if snapshot == "" {
		s.setHXTrigger(w, "Snapshot name is required", "error")
		s.handleUIPartialSnapshots(w, r)
		return
	}

	if err := s.zfs.RollbackSnapshot(snapshot); err != nil {
		s.setHXTrigger(w, "Rollback failed: "+err.Error(), "error")
	} else {
		s.setHXTrigger(w, fmt.Sprintf("Dataset rolled back to '%s'", snapshot), "success", "snapshotUpdate", "datasetUpdate")
	}
	s.handleUIPartialSnapshots(w, r)
}

func (s *Server) handleUIActionCloneSnapshot(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	snapshot := strings.TrimSpace(r.FormValue("snapshot"))
	targetDataset := strings.TrimSpace(r.FormValue("target_dataset"))

	if snapshot == "" || targetDataset == "" {
		s.setHXTrigger(w, "Snapshot and target dataset are required", "error")
		s.handleUIPartialDatasets(w, r)
		return
	}

	if err := s.zfs.CloneSnapshot(snapshot, targetDataset); err != nil {
		s.setHXTrigger(w, "Clone error: "+err.Error(), "error")
	} else {
		s.setHXTrigger(w, fmt.Sprintf("Snapshot cloned to '%s'", targetDataset), "success", "datasetUpdate", "snapshotUpdate")
	}
	s.handleUIPartialDatasets(w, r)
}

func (s *Server) handleUIActionDestroySnapshot(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if name == "" {
		_ = r.ParseForm()
		name = strings.TrimSpace(r.FormValue("name"))
	}
	if name == "" {
		s.setHXTrigger(w, "Snapshot name is required", "error")
		s.handleUIPartialSnapshots(w, r)
		return
	}

	if err := s.zfs.DestroySnapshot(name); err != nil {
		s.setHXTrigger(w, "Destroy error: "+err.Error(), "error")
	} else {
		s.setHXTrigger(w, fmt.Sprintf("Snapshot '%s' destroyed", name), "success", "snapshotUpdate", "datasetUpdate")
	}
	s.handleUIPartialSnapshots(w, r)
}

func (s *Server) handleUIActionDangerDestroy(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	targetType := r.FormValue("type")
	name := strings.TrimSpace(r.FormValue("name"))
	deleteShares := r.FormValue("delete_shares") == "true"

	if name == "" {
		s.setHXTrigger(w, "Target name is required", "error")
		s.handleUIPartialPools(w, r)
		return
	}

	if targetType == "pool" {
		if deleteShares {
			// Clean up any SMB shares starting with /mnt/<name> or named <name>
			if smbList, err := s.shares.ListSmb(); err == nil {
				for _, smb := range smbList {
					if smb.Name == name || strings.HasPrefix(smb.Path, "/mnt/"+name) || strings.HasPrefix(smb.Path, "/"+name) {
						_ = s.shares.DeleteSmb(smb.Name)
					}
				}
			}
		}
		if err := s.zfs.DestroyPool(name); err != nil {
			s.setHXTrigger(w, "Error destroying pool: "+err.Error(), "error")
		} else {
			s.setHXTrigger(w, fmt.Sprintf("ZFS pool '%s' permanently destroyed", name), "success", "poolUpdate", "datasetUpdate", "shareUpdate", "diskUpdate")
		}
	} else {
		// Dataset destruction
		if deleteShares {
			if smbList, err := s.shares.ListSmb(); err == nil {
				for _, smb := range smbList {
					if strings.HasSuffix(smb.Path, name) || smb.Name == name {
						_ = s.shares.DeleteSmb(smb.Name)
					}
				}
			}
		}
		if err := s.zfs.DestroyDataset(name); err != nil {
			s.setHXTrigger(w, "Error destroying dataset: "+err.Error(), "error")
		} else {
			s.setHXTrigger(w, fmt.Sprintf("Dataset '%s' destroyed", name), "success", "datasetUpdate", "shareUpdate")
		}
	}
	s.handleUIPartialPools(w, r)
}

// Disks
func (s *Server) handleUIActionSetSpindown(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	path := strings.TrimSpace(r.FormValue("path"))
	mins, _ := strconv.Atoi(r.FormValue("timeout_minutes"))
	if path == "" {
		s.setHXTrigger(w, "Device path is required", "error")
		s.handleUIPartialDisks(w, r)
		return
	}

	seconds := mins * 60
	if err := s.disks.SetSpindown(path, seconds); err != nil {
		s.setHXTrigger(w, "Error setting spindown timeout: "+err.Error(), "error")
	} else {
		s.setHXTrigger(w, fmt.Sprintf("Spindown for %s configured to %d minutes", path, mins), "success", "diskUpdate")
	}
	s.handleUIPartialDisks(w, r)
}

func (s *Server) handleUIActionSetAPM(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	path := strings.TrimSpace(r.FormValue("path"))
	level := strings.TrimSpace(r.FormValue("level"))
	if path == "" {
		s.setHXTrigger(w, "Device path is required", "error")
		s.handleUIPartialDisks(w, r)
		return
	}
	if level == "" {
		level = "Disabled"
	}

	if err := s.disks.SetAPM(path, level); err != nil {
		s.setHXTrigger(w, "Error configuring APM: "+err.Error(), "error")
	} else {
		s.setHXTrigger(w, fmt.Sprintf("APM for %s set to %s", path, level), "success", "diskUpdate")
	}
	s.handleUIPartialDisks(w, r)
}

func (s *Server) handleUIActionForceStandby(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	path := strings.TrimSpace(r.FormValue("path"))
	if path == "" {
		s.setHXTrigger(w, "Device path is required", "error")
		s.handleUIPartialDisks(w, r)
		return
	}

	if err := s.disks.ForceStandby(path); err != nil {
		s.setHXTrigger(w, "Standby error: "+err.Error(), "error")
	} else {
		s.setHXTrigger(w, fmt.Sprintf("Drive %s placed in standby mode", path), "success", "diskUpdate")
	}
	s.handleUIPartialDisks(w, r)
}

func (s *Server) handleUIActionToggleLocate(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	path := strings.TrimSpace(r.FormValue("path"))
	if path == "" {
		s.setHXTrigger(w, "Device path is required", "error")
		s.handleUIPartialDisks(w, r)
		return
	}

	newState, err := s.disks.ToggleLocate(path)
	if err != nil {
		s.setHXTrigger(w, "LED toggle error: "+err.Error(), "error")
	} else {
		stateStr := "disabled"
		if newState {
			stateStr = "blinking active"
		}
		s.setHXTrigger(w, fmt.Sprintf("Locate LED for %s: %s", path, stateStr), "info", "diskUpdate")
	}
	s.handleUIPartialDisks(w, r)
}

func (s *Server) handleUIActionTriggerSmartTest(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	path := strings.TrimSpace(r.FormValue("path"))
	testType := r.FormValue("type")
	if testType == "" {
		testType = "short"
	}
	if path == "" {
		s.setHXTrigger(w, "Device path is required", "error")
		s.handleUIPartialDisks(w, r)
		return
	}

	if err := s.disks.TriggerSmartTest(path, testType); err != nil {
		s.setHXTrigger(w, "SMART error: "+err.Error(), "error")
	} else {
		s.setHXTrigger(w, fmt.Sprintf("SMART %s self-test started for %s", testType, path), "success", "diskUpdate")
	}
	s.handleUIPartialDisks(w, r)
}

func (s *Server) handleUIActionWipeDisk(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	path := strings.TrimSpace(r.FormValue("path"))
	confirm := r.FormValue("confirm_wipe")

	if path == "" || confirm != "true" {
		s.setHXTrigger(w, "Confirmation required to wipe disk", "error")
		s.handleUIPartialDisks(w, r)
		return
	}

	if err := s.disks.WipeDisk(path, "quick"); err != nil {
		s.setHXTrigger(w, "Wipe error: "+err.Error(), "error")
	} else {
		s.setHXTrigger(w, fmt.Sprintf("Disk %s wiped and initialized successfully", path), "success", "diskUpdate")
	}
	s.handleUIPartialDisks(w, r)
}
