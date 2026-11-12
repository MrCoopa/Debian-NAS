package api

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"debian-nas/internal/cloudsync"
	"debian-nas/internal/disks"
	"debian-nas/internal/shares"
	"debian-nas/internal/system"
	"debian-nas/internal/zfs"
)

type PoolSnapshotsGroup struct {
	PoolName    string
	Health      string
	TotalCount  int
	TotalPages  int
	CurrentPage int
	PrevPage    int
	NextPage    int
	HasPrev     bool
	HasNext     bool
	StartIdx    int
	EndIdx      int
	Filter      string
	Snapshots   []*zfs.Snapshot
}

// ViewData is passed to the root layout and all partials
type ViewData struct {
	CurrentUser   string
	System        *system.SystemStatus
	Disks           []*disks.DiskInfo
	Pools           []*zfs.Pool
	ImportablePools []*zfs.ImportablePool
	Datasets        []*zfs.Dataset
	SmbShares     []*shares.SmbShare
	NfsExports    []*shares.NfsExport
	Users         []shares.StorageUser
	Snapshots       []*zfs.Snapshot
	SnapshotsByPool []PoolSnapshotsGroup
	SnapshotTasks []*zfs.SnapshotTask
	CloudJobs     []*cloudsync.SyncJob
	CloudRemotes  []cloudsync.Remote
	ZfsVersion    string
	TotalDisks    int
	TotalRawBytes uint64
	StandbyCount  int
	SpinningCount int
	ErrorMessage  string
}

type viewFetchMask struct {
	system        bool
	pools         bool
	importable    bool
	disks         bool
	datasets      bool
	shares        bool
	users         bool
	snapshots     bool
	snapshotTasks bool
	cloud         bool
}

func (s *Server) buildViewData(req ...*http.Request) *ViewData {
	return s.buildPartialViewData(viewFetchMask{
		system:        true,
		pools:         true,
		importable:    false,
		disks:         true,
		datasets:      true,
		shares:        true,
		users:         true,
		snapshots:     true,
		snapshotTasks: true,
		cloud:         true,
	}, req...)
}

func (s *Server) buildPartialViewData(mask viewFetchMask, req ...*http.Request) *ViewData {
	currentUser := "root"
	if len(req) > 0 && req[0] != nil {
		if c, err := req[0].Cookie("nas_user"); err == nil && strings.TrimSpace(c.Value) != "" {
			currentUser = strings.TrimSpace(c.Value)
		}
	}

	var status *system.SystemStatus
	if mask.system && s.sys != nil {
		status = s.sys.GetStatus()
	}

	var pools []*zfs.Pool
	if (mask.pools || mask.disks) && s.zfs != nil {
		var poolsErr error
		pools, poolsErr = s.zfs.ListPools()
		if poolsErr == nil && mask.disks && s.disks != nil {
			_, _ = s.disks.ListDisks()
			s.syncDiskPoolMembershipFromPools(pools)
		}
	}

	var importablePools []*zfs.ImportablePool
	if mask.importable && s.zfs != nil {
		importablePools, _ = s.zfs.FindImportablePools()
	}

	var allDisks []*disks.DiskInfo
	if mask.disks && s.disks != nil {
		allDisks, _ = s.disks.ListDisks()
	}

	var datasets []*zfs.Dataset
	if mask.datasets && s.zfs != nil {
		datasets, _ = s.zfs.ListDatasets()
	}

	var snapshots []*zfs.Snapshot
	var snapshotsByPool []PoolSnapshotsGroup
	if mask.snapshots && s.zfs != nil {
		snapshots, _ = s.zfs.ListSnapshots()

		poolMap := make(map[string][]*zfs.Snapshot)
		var poolOrder []string

		for _, p := range pools {
			if _, exists := poolMap[p.Name]; !exists {
				poolMap[p.Name] = []*zfs.Snapshot{}
				poolOrder = append(poolOrder, p.Name)
			}
		}

		for _, snap := range snapshots {
			pName := snap.Dataset
			if idx := strings.Index(pName, "/"); idx != -1 {
				pName = pName[:idx]
			}
			if _, exists := poolMap[pName]; !exists {
				poolMap[pName] = []*zfs.Snapshot{}
				poolOrder = append(poolOrder, pName)
			}
			poolMap[pName] = append(poolMap[pName], snap)
		}

		sort.Strings(poolOrder)

		// Check query parameters for pool-specific pagination / search
		if len(req) > 0 && req[0] != nil {
			qPool := req[0].URL.Query().Get("pool")
			qPage := req[0].URL.Query().Get("page")
			qFilter := req[0].URL.Query().Get("q")
			clearFilter := req[0].URL.Query().Get("clear_filter")

			s.snapPageMu.Lock()
			if qPool != "" && qPage != "" {
				if pNum, err := strconv.Atoi(qPage); err == nil && pNum >= 1 {
					s.snapPages[qPool] = pNum
				}
			}
			if qPool != "" && clearFilter == "true" {
				delete(s.snapFilters, qPool)
				s.snapPages[qPool] = 1
			} else if qPool != "" && qFilter != "" {
				s.snapFilters[qPool] = strings.TrimSpace(qFilter)
				s.snapPages[qPool] = 1
			}
			s.snapPageMu.Unlock()
		}

		s.snapPageMu.RLock()
		for _, pName := range poolOrder {
			health := "ONLINE"
			for _, p := range pools {
				if p.Name == pName {
					health = string(p.Health)
					break
				}
			}

			allSnaps := poolMap[pName]
			filterVal := s.snapFilters[pName]

			var filteredSnaps []*zfs.Snapshot
			if filterVal != "" {
				lowFilter := strings.ToLower(filterVal)
				for _, snap := range allSnaps {
					if strings.Contains(strings.ToLower(snap.Name), lowFilter) || strings.Contains(strings.ToLower(snap.Dataset), lowFilter) {
						filteredSnaps = append(filteredSnaps, snap)
					}
				}
			} else {
				filteredSnaps = allSnaps
			}

			totCount := len(filteredSnaps)
			totPages := (totCount + 9) / 10
			if totPages < 1 {
				totPages = 1
			}

			curPage := s.snapPages[pName]
			if curPage < 1 {
				curPage = 1
			} else if curPage > totPages {
				curPage = totPages
			}

			start := (curPage - 1) * 10
			end := start + 10
			if start > totCount {
				start = totCount
			}
			if end > totCount {
				end = totCount
			}

			startIdx := 0
			if totCount > 0 {
				startIdx = start + 1
			}

			pagedSnaps := filteredSnaps[start:end]

			snapshotsByPool = append(snapshotsByPool, PoolSnapshotsGroup{
				PoolName:    pName,
				Health:      health,
				TotalCount:  totCount,
				TotalPages:  totPages,
				CurrentPage: curPage,
				PrevPage:    curPage - 1,
				NextPage:    curPage + 1,
				HasPrev:     curPage > 1,
				HasNext:     curPage < totPages,
				StartIdx:    startIdx,
				EndIdx:      end,
				Filter:      filterVal,
				Snapshots:   pagedSnaps,
			})
		}
		s.snapPageMu.RUnlock()
	}

	var snapTasks []*zfs.SnapshotTask
	if mask.snapshotTasks && s.zfs != nil {
		snapTasks = s.zfs.ListSnapshotTasks()
	}

	var smbShares []*shares.SmbShare
	var nfsExports []*shares.NfsExport
	if mask.shares && s.shares != nil {
		smbShares, _ = s.shares.ListSmb()
		nfsExports, _ = s.shares.ListNfs()
	}

	var users []shares.StorageUser
	if mask.users && s.shares != nil {
		users, _ = s.shares.ListUsers()
	}

	var cloudJobs []*cloudsync.SyncJob
	var cloudRemotes []cloudsync.Remote
	if mask.cloud && s.cloudsync != nil {
		cloudJobs, _ = s.cloudsync.ListJobs()
		cloudRemotes, _ = s.cloudsync.ListRemotes()
	}

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
		CurrentUser:   currentUser,
		System:        status,
		Disks:           allDisks,
		Pools:           pools,
		ImportablePools: importablePools,
		Datasets:        datasets,
		SmbShares:     smbShares,
		NfsExports:    nfsExports,
		Users:           users,
		Snapshots:       snapshots,
		SnapshotsByPool: snapshotsByPool,
		SnapshotTasks:   snapTasks,
		CloudJobs:     cloudJobs,
		CloudRemotes:  cloudRemotes,
		ZfsVersion:    zfsVer,
		TotalDisks:    len(allDisks),
		TotalRawBytes: totalRawBytes,
		StandbyCount:  standbyCount,
		SpinningCount: spinningCount,
	}
}

func (s *Server) renderUIPartial(w http.ResponseWriter, tmpl string, data *ViewData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	_ = s.web.Render(w, tmpl, data)
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

func (s *Server) handleUILogin(w http.ResponseWriter, r *http.Request) {
	// If already authenticated and not switching user, redirect to /
	if r.URL.Query().Get("switch") == "" {
		if c, err := r.Cookie("nas_user"); err == nil && strings.TrimSpace(c.Value) != "" {
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
	}

	data := s.buildViewData(r)
	if r.URL.Query().Get("error") != "" {
		data.ErrorMessage = "Invalid username or password. Please try again."
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	if err := s.web.Render(w, "login.html", data); err != nil {
		log.Printf("[UI] Login render error: %v", err)
		http.Error(w, "Login Render Error: "+err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) handleUILoginSubmit(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	username := strings.ToLower(strings.TrimSpace(r.FormValue("username")))

	if username == "" {
		http.Redirect(w, r, "/login?error=invalid", http.StatusSeeOther)
		return
	}

	// Verify user: check root/admin or any storage user in s.shares
	allUsers, _ := s.shares.ListUsers()
	validUser := false
	if username == "root" || username == "admin" {
		validUser = true
	} else {
		for _, u := range allUsers {
			if strings.ToLower(u.Username) == username {
				validUser = true
				break
			}
		}
	}

	if !validUser {
		http.Redirect(w, r, "/login?error=invalid", http.StatusSeeOther)
		return
	}

	// Valid login! Set cookie for 30 days
	http.SetCookie(w, &http.Cookie{
		Name:     "nas_user",
		Value:    username,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   86400 * 30, // 30 days
	})

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) handleUILogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     "nas_user",
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

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

	// Check if authenticated
	c, err := r.Cookie("nas_user")
	if err != nil || strings.TrimSpace(c.Value) == "" {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	if err := s.web.Render(w, "layout.html", s.buildViewData(r)); err != nil {
		log.Printf("[UI] Template render error: %v", err)
		http.Error(w, "UI Rendering Error: "+err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) handleUIPartialSystem(w http.ResponseWriter, r *http.Request) {
	s.renderUIPartial(w, "partials/system_metrics.html", s.buildPartialViewData(viewFetchMask{
		system: true, pools: true, disks: true, datasets: true,
	}, r))
}

func (s *Server) handleUIPartialTop(w http.ResponseWriter, r *http.Request) {
	s.renderUIPartial(w, "partials/topbar.html", s.buildPartialViewData(viewFetchMask{
		system: true,
	}, r))
}

func (s *Server) handleUIPartialPools(w http.ResponseWriter, r *http.Request) {
	s.renderUIPartial(w, "partials/pools_list.html", s.buildPartialViewData(viewFetchMask{
		pools: true,
	}))
}

func (s *Server) handleUIPartialDisks(w http.ResponseWriter, r *http.Request) {
	s.renderUIPartial(w, "partials/disks_table.html", s.buildPartialViewData(viewFetchMask{
		pools: true, disks: true,
	}))
}

func (s *Server) handleUIPartialDatasets(w http.ResponseWriter, r *http.Request) {
	s.renderUIPartial(w, "partials/datasets_list.html", s.buildPartialViewData(viewFetchMask{
		datasets: true, shares: true,
	}))
}

func (s *Server) handleUIPartialShares(w http.ResponseWriter, r *http.Request) {
	s.renderUIPartial(w, "partials/shares_list.html", s.buildPartialViewData(viewFetchMask{
		shares: true,
	}))
}

func (s *Server) handleUIPartialUsers(w http.ResponseWriter, r *http.Request) {
	s.renderUIPartial(w, "partials/users_list.html", s.buildPartialViewData(viewFetchMask{
		users: true, shares: true,
	}))
}

func (s *Server) handleUIPartialSnapshots(w http.ResponseWriter, r *http.Request) {
	s.renderUIPartial(w, "partials/snapshots_list.html", s.buildPartialViewData(viewFetchMask{
		snapshots: true,
		pools:     true,
	}, r))
}

func (s *Server) handleUIPartialNetwork(w http.ResponseWriter, r *http.Request) {
	s.renderUIPartial(w, "partials/network_list.html", s.buildPartialViewData(viewFetchMask{
		system: true,
	}))
}

func (s *Server) handleUIPartialArc(w http.ResponseWriter, r *http.Request) {
	s.renderUIPartial(w, "partials/arc_settings.html", s.buildPartialViewData(viewFetchMask{
		system: true,
	}))
}

func (s *Server) handleUIPartialArcStats(w http.ResponseWriter, r *http.Request) {
	s.renderUIPartial(w, "partials/arc_stats.html", s.buildPartialViewData(viewFetchMask{
		system: true,
	}))
}

func (s *Server) handleUIActionSetArc(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	action := strings.TrimSpace(r.FormValue("action"))
	var maxBytes, minBytes uint64
	if action != "reset" {
		var err error
		maxBytes, err = system.ParseArcSizeInput(r.FormValue("max_value"), r.FormValue("max_unit"))
		if err != nil {
			s.setHXTrigger(w, err.Error(), "error")
			s.handleUIPartialArc(w, r)
			return
		}
		minBytes, err = system.ParseArcSizeInput(r.FormValue("min_value"), r.FormValue("min_unit"))
		if err != nil {
			s.setHXTrigger(w, err.Error(), "error")
			s.handleUIPartialArc(w, r)
			return
		}
	}

	updated, err := s.sys.SetArcConfig(maxBytes, minBytes)
	if err != nil {
		s.setHXTrigger(w, err.Error(), "error")
	} else {
		msg := fmt.Sprintf("ZFS ARC cache updated (Max: %s)", updated.MaxHuman)
		if updated.IsAutoMax && updated.IsAutoMin {
			msg = "ZFS ARC cache reset to Auto (OpenZFS default)"
		}
		s.setHXTrigger(w, msg, "success", "arcUpdate", "nasRefresh")
	}
	s.handleUIPartialArc(w, r)
}

func (s *Server) handleUIPartialServices(w http.ResponseWriter, r *http.Request) {
	s.renderUIPartial(w, "partials/services_list.html", s.buildPartialViewData(viewFetchMask{
		system: true,
	}))
}

func (s *Server) handleUIPartialCloudSync(w http.ResponseWriter, r *http.Request) {
	s.renderUIPartial(w, "partials/cloudsync_tasks.html", s.buildPartialViewData(viewFetchMask{
		cloud: true,
	}))
}

func (s *Server) handleUIPartialCreateShareModal(w http.ResponseWriter, r *http.Request) {
	s.renderUIPartial(w, "modals/create_share.html", s.buildPartialViewData(viewFetchMask{
		pools: true, datasets: true, users: true,
	}))
}

func (s *Server) handleUIPartialShareModalUsers(w http.ResponseWriter, r *http.Request) {
	s.renderUIPartial(w, "partials/share_users_checkboxes.html", s.buildPartialViewData(viewFetchMask{
		users: true,
	}))
}

func (s *Server) handleUIPartialModalSnapDatasets(w http.ResponseWriter, r *http.Request) {
	s.renderUIPartial(w, "partials/snap_datasets_options.html", s.buildPartialViewData(viewFetchMask{
		pools: true, datasets: true,
	}))
}

func (s *Server) handleUIPartialModalParentDatasets(w http.ResponseWriter, r *http.Request) {
	s.renderUIPartial(w, "partials/parent_datasets_options.html", s.buildPartialViewData(viewFetchMask{
		pools: true, datasets: true,
	}))
}

func (s *Server) handleUIPartialModalPoolDisks(w http.ResponseWriter, r *http.Request) {
	s.renderUIPartial(w, "partials/pool_disks_checkboxes.html", s.buildPartialViewData(viewFetchMask{
		pools: true, disks: true,
	}, r))
}

func (s *Server) handleUIPartialModalShareDatasets(w http.ResponseWriter, r *http.Request) {
	s.renderUIPartial(w, "partials/share_datasets_picker.html", s.buildPartialViewData(viewFetchMask{
		pools: true, datasets: true,
	}))
}

func (s *Server) handleUIPartialModalAvailableDisks(w http.ResponseWriter, r *http.Request) {
	s.renderUIPartial(w, "partials/available_disks_options.html", s.buildPartialViewData(viewFetchMask{
		pools: true, disks: true,
	}))
}

func (s *Server) handleUIPartialModalImportablePools(w http.ResponseWriter, r *http.Request) {
	s.renderUIPartial(w, "partials/importable_pools_list.html", s.buildPartialViewData(viewFetchMask{
		importable: true,
	}, r))
}

func (s *Server) handleUIPartialModalCloudSyncSources(w http.ResponseWriter, r *http.Request) {
	s.renderUIPartial(w, "partials/cloudsync_sources_options.html", s.buildPartialViewData(viewFetchMask{
		pools: true, datasets: true,
	}))
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

	s.ensureSharePathMounted(path)

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
	readonly := r.FormValue("readonly") == "true"

	if nameOrID == "" {
		s.setHXTrigger(w, "Pool name or ID is required", "error")
		s.handleUIPartialPools(w, r)
		return
	}

	if err := s.zfs.ImportPool(nameOrID, newName, force, readonly); err != nil {
		s.setHXTrigger(w, "Error importing pool: "+err.Error(), "error")
	} else {
		msg := fmt.Sprintf("Pool '%s' successfully imported", nameOrID)
		if readonly {
			msg += " (read-only mode)"
		}
		s.setHXTrigger(w, msg, "success", "poolUpdate", "datasetUpdate", "diskUpdate")
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

	var propErr error
	if autotrim != "" {
		if err := s.zfs.SetPoolProperty(pool, "autotrim", autotrim); err != nil {
			propErr = err
		}
	}
	if autoexpand != "" {
		if err := s.zfs.SetPoolProperty(pool, "autoexpand", autoexpand); err != nil && propErr == nil {
			propErr = err
		}
	}
	if propErr != nil {
		s.setHXTrigger(w, "Error updating pool properties: "+propErr.Error(), "error")
	} else {
		s.setHXTrigger(w, fmt.Sprintf("Properties updated for pool '%s'", pool), "success", "poolUpdate")
	}
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
	if targetDevice == "" {
		targetDevice = strings.TrimSpace(r.FormValue("existing_device"))
	}
	if targetDevice == "" {
		targetDevice = strings.TrimSpace(r.FormValue("old_device"))
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
	isZvol := r.FormValue("is_zvol") == "true"

	if dataset == "" {
		s.setHXTrigger(w, "Dataset name is required", "error")
		s.handleUIPartialDatasets(w, r)
		return
	}

	var propErr error
	if compression != "" {
		if err := s.zfs.SetDatasetProperty(dataset, "compression", compression); err != nil {
			propErr = err
		}
	}
	if !isZvol && recordsize != "" {
		if err := s.zfs.SetDatasetProperty(dataset, "recordsize", recordsize); err != nil && propErr == nil {
			propErr = err
		}
	}
	if !isZvol && quota != "" {
		if err := s.zfs.SetDatasetProperty(dataset, "quota", quota); err != nil && propErr == nil {
			propErr = err
		}
	}
	if readonly != "" {
		if err := s.zfs.SetDatasetProperty(dataset, "readonly", readonly); err != nil && propErr == nil {
			propErr = err
		}
	}
	if !isZvol && atime != "" {
		if err := s.zfs.SetDatasetProperty(dataset, "atime", atime); err != nil && propErr == nil {
			propErr = err
		}
	}

	if propErr != nil {
		s.setHXTrigger(w, "Error updating properties: "+propErr.Error(), "error")
	} else {
		s.setHXTrigger(w, fmt.Sprintf("Properties updated for '%s'", dataset), "success", "datasetUpdate")
	}
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
	recursive := r.FormValue("recursive") == "true" || r.FormValue("recursive") == "on"

	if dataset == "" || snapName == "" {
		s.setHXTrigger(w, "Dataset and snapshot name are required", "error")
		s.handleUIPartialSnapshots(w, r)
		return
	}

	if err := s.zfs.CreateSnapshotRecursive(dataset, snapName, recursive); err != nil {
		s.setHXTrigger(w, "Error creating snapshot: "+err.Error(), "error")
	} else {
		msg := fmt.Sprintf("Snapshot '%s@%s' created", dataset, snapName)
		if recursive {
			msg = fmt.Sprintf("Recursive snapshot '%s@%s' created", dataset, snapName)
		}
		s.setHXTrigger(w, msg, "success", "snapshotUpdate", "datasetUpdate", "poolUpdate")
	}
	s.handleUIPartialSnapshots(w, r)
}

func (s *Server) handleUIActionRollbackSnapshot(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	snapshot := strings.TrimSpace(r.FormValue("snapshot"))
	recursive := r.FormValue("recursive") == "true" || r.FormValue("recursive") == "on"
	if snapshot == "" {
		s.setHXTrigger(w, "Snapshot name is required", "error")
		s.handleUIPartialSnapshots(w, r)
		return
	}

	rolledBack, skippedChildren, err := s.rollbackSnapshotSynced(snapshot, recursive)
	if err != nil {
		s.setHXTrigger(w, "Rollback failed: "+err.Error(), "error")
	} else {
		msg := fmt.Sprintf("Dataset rolled back to '%s'", snapshot)
		toastType := "success"
		if recursive && len(rolledBack) > 1 {
			msg = fmt.Sprintf("Dataset tree rolled back recursively to '%s' (%d datasets)", snapshot, len(rolledBack))
		} else if len(skippedChildren) > 0 {
			msg = fmt.Sprintf("Dataset rolled back to '%s' (child datasets unchanged: %s)", snapshot, strings.Join(skippedChildren, ", "))
		}
		s.setHXTrigger(w, msg, toastType, "snapshotUpdate", "datasetUpdate", "poolUpdate")
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
		s.setHXTrigger(w, fmt.Sprintf("Snapshot '%s' destroyed", name), "success", "snapshotUpdate", "datasetUpdate", "poolUpdate")
	}
	s.handleUIPartialSnapshots(w, r)
}

func (s *Server) handleUIActionDangerDestroy(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	targetType := r.FormValue("type")
	name := strings.TrimSpace(r.FormValue("name"))
	deleteShares := r.FormValue("delete_shares") == "true"

	renderTarget := func() {
		if targetType == "pool" {
			s.handleUIPartialPools(w, r)
			return
		}
		s.handleUIPartialDatasets(w, r)
	}

	if name == "" {
		s.setHXTrigger(w, "Target name is required", "error")
		renderTarget()
		return
	}

	if targetType == "pool" {
		if deleteShares {
			s.removeSharesForTarget(name)
		}
		if err := s.zfs.DestroyPool(name); err != nil {
			s.setHXTrigger(w, "Error destroying pool: "+err.Error(), "error")
		} else {
			s.setHXTrigger(w, fmt.Sprintf("ZFS pool '%s' permanently destroyed", name), "success", "poolUpdate", "datasetUpdate", "snapshotUpdate", "shareUpdate", "diskUpdate")
		}
	} else {
		// Dataset destruction
		if deleteShares {
			s.removeSharesForTarget(name)
		}
		if err := s.zfs.DestroyDataset(name); err != nil {
			s.setHXTrigger(w, "Error destroying dataset: "+err.Error(), "error")
		} else {
			s.setHXTrigger(w, fmt.Sprintf("Dataset '%s' destroyed", name), "success", "datasetUpdate", "poolUpdate", "snapshotUpdate", "shareUpdate")
		}
	}
	renderTarget()
}

func pathBelongsToTarget(sharePath, target string, mountpoints []string) bool {
	sharePath = strings.TrimRight(strings.TrimSpace(sharePath), "/")
	if sharePath == "" {
		return false
	}
	if sharePath == "/mnt/"+target || strings.HasPrefix(sharePath, "/mnt/"+target+"/") || sharePath == "/"+target || strings.HasPrefix(sharePath, "/"+target+"/") {
		return true
	}
	for _, mp := range mountpoints {
		if sharePath == mp || strings.HasPrefix(sharePath, mp+"/") {
			return true
		}
	}
	return false
}

func (s *Server) removeSharesForTarget(target string) {
	var mountpoints []string
	if datasets, err := s.zfs.ListDatasets(); err == nil {
		for _, ds := range datasets {
			if ds.Name != target && !strings.HasPrefix(ds.Name, target+"/") {
				continue
			}
			mp := strings.TrimRight(strings.TrimSpace(ds.Mountpoint), "/")
			if strings.HasPrefix(mp, "/") && mp != "" {
				mountpoints = append(mountpoints, mp)
			}
		}
	}

	if smbList, err := s.shares.ListSmb(); err == nil {
		for _, smb := range smbList {
			if smb.Name == target || pathBelongsToTarget(smb.Path, target, mountpoints) {
				_ = s.shares.DeleteSmb(smb.Name)
			}
		}
	}
	if nfsList, err := s.shares.ListNfs(); err == nil {
		for _, nfs := range nfsList {
			if pathBelongsToTarget(nfs.Path, target, mountpoints) {
				_ = s.shares.DeleteNfs(nfs.Path)
			}
		}
	}
}

// Disks
func formDiskTarget(r *http.Request) string {
	for _, key := range []string{"path", "device", "name"} {
		if v := strings.TrimSpace(r.FormValue(key)); v != "" {
			return v
		}
	}
	return ""
}

func (s *Server) handleUIActionSetSpindown(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	path := formDiskTarget(r)
	if path == "" {
		s.setHXTrigger(w, "Device path is required", "error")
		s.handleUIPartialDisks(w, r)
		return
	}

	var seconds int
	if secRaw := strings.TrimSpace(r.FormValue("seconds")); secRaw != "" {
		parsed, err := strconv.Atoi(secRaw)
		if err != nil || parsed < 0 {
			s.setHXTrigger(w, "Invalid spindown seconds value", "error")
			s.handleUIPartialDisks(w, r)
			return
		}
		seconds = parsed
	} else {
		minRaw := strings.TrimSpace(r.FormValue("timeout_minutes"))
		if minRaw == "" {
			minRaw = "0"
		}
		mins, err := strconv.Atoi(minRaw)
		if err != nil || mins < 0 {
			s.setHXTrigger(w, "Invalid spindown minutes value", "error")
			s.handleUIPartialDisks(w, r)
			return
		}
		seconds = mins * 60
	}

	if err := s.disks.SetSpindown(path, seconds); err != nil {
		s.setHXTrigger(w, "Error setting spindown timeout: "+err.Error(), "error")
	} else if seconds == 0 {
		s.setHXTrigger(w, fmt.Sprintf("Spindown for %s disabled (Always On)", path), "success", "diskUpdate")
	} else {
		s.setHXTrigger(w, fmt.Sprintf("Spindown for %s configured to %d minutes", path, seconds/60), "success", "diskUpdate")
	}
	s.handleUIPartialDisks(w, r)
}

func (s *Server) handleUIActionSetAPM(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	path := formDiskTarget(r)
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
	path := formDiskTarget(r)
	if path == "" {
		s.setHXTrigger(w, "Device path is required", "error")
		s.handleUIPartialDisks(w, r)
		return
	}

	if err := s.disks.ForceStandby(path); err != nil {
		s.setHXTrigger(w, "Standby error: "+err.Error(), "error")
	} else {
		s.setHXTrigger(w, fmt.Sprintf("Drive %s placed in standby mode", path), "success")
	}
	s.handleUIPartialDisks(w, r)
}

func (s *Server) handleUIActionToggleLocate(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	path := formDiskTarget(r)
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
	path := formDiskTarget(r)
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
	path := formDiskTarget(r)
	confirm := r.FormValue("confirm_wipe")

	if path == "" || confirm != "true" {
		s.setHXTrigger(w, "Confirmation required to wipe disk", "error")
		s.handleUIPartialDisks(w, r)
		return
	}

	s.syncDiskPoolMembership()
	if err := s.disks.WipeDisk(path, "quick"); err != nil {
		s.setHXTrigger(w, "Wipe error: "+err.Error(), "error")
	} else {
		s.setHXTrigger(w, fmt.Sprintf("Disk %s wiped and initialized successfully", path), "success", "diskUpdate")
	}
	s.handleUIPartialDisks(w, r)
}

// ---------------- Cloud Sync UI Actions ----------------

func (s *Server) handleUIActionToggleCloudSync(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	id := strings.TrimSpace(r.FormValue("id"))
	if id == "" {
		id = strings.TrimSpace(r.URL.Query().Get("id"))
	}
	if id == "" {
		s.setHXTrigger(w, "Task ID is required", "error")
		s.handleUIPartialCloudSync(w, r)
		return
	}

	if err := s.cloudsync.ToggleJob(id); err != nil {
		s.setHXTrigger(w, "Error toggling task: "+err.Error(), "error")
	} else {
		s.setHXTrigger(w, "Cloud Sync task status toggled", "success", "cloudSyncUpdate")
	}
	s.handleUIPartialCloudSync(w, r)
}

func (s *Server) handleUIActionRunCloudSync(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	id := strings.TrimSpace(r.FormValue("id"))
	if id == "" {
		id = strings.TrimSpace(r.URL.Query().Get("id"))
	}
	if id == "" {
		s.setHXTrigger(w, "Task ID is required", "error")
		s.handleUIPartialCloudSync(w, r)
		return
	}

	if err := s.cloudsync.TriggerJob(id); err != nil {
		s.setHXTrigger(w, "Failed to start Cloud Sync: "+err.Error(), "error")
	} else {
		s.setHXTrigger(w, "Cloud Sync task started successfully", "success", "cloudSyncUpdate")
	}
	s.handleUIPartialCloudSync(w, r)
}

func (s *Server) handleUIActionDeleteCloudSync(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	id := strings.TrimSpace(r.FormValue("id"))
	if id == "" {
		id = strings.TrimSpace(r.URL.Query().Get("id"))
	}
	if id == "" {
		s.setHXTrigger(w, "Task ID is required", "error")
		s.handleUIPartialCloudSync(w, r)
		return
	}

	if err := s.cloudsync.DeleteJob(id); err != nil {
		s.setHXTrigger(w, "Failed to delete Cloud Sync task: "+err.Error(), "error")
	} else {
		s.setHXTrigger(w, "Cloud Sync task deleted", "info", "cloudSyncUpdate")
	}
	s.handleUIPartialCloudSync(w, r)
}

func (s *Server) handleUIActionSaveCloudSync(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	id := strings.TrimSpace(r.FormValue("id"))
	desc := strings.TrimSpace(r.FormValue("description"))
	name := strings.TrimSpace(r.FormValue("name"))
	if desc == "" && name != "" {
		desc = name
	}
	if name == "" && desc != "" {
		name = desc
	}
	if name == "" {
		s.setHXTrigger(w, "Task description/name is required", "error")
		s.handleUIPartialCloudSync(w, r)
		return
	}

	sourcePath := strings.TrimSpace(r.FormValue("source_path"))
	remoteTarget := strings.TrimSpace(r.FormValue("remote_target"))
	direction := cloudsync.SyncDirection(strings.ToUpper(strings.TrimSpace(r.FormValue("direction"))))
	if direction != cloudsync.DirectionPull {
		direction = cloudsync.DirectionPush
	}

	schedule := strings.TrimSpace(r.FormValue("schedule"))
	if schedule == "" {
		schedule = "0 2 * * *"
	}
	freqText := strings.TrimSpace(r.FormValue("frequency_text"))
	bw := strings.TrimSpace(r.FormValue("bandwidth_limit"))
	enabled := r.FormValue("enabled") == "true" || r.FormValue("enabled") == "on" || r.FormValue("enabled") == "1"

	job := &cloudsync.SyncJob{
		ID:             id,
		Name:           name,
		Description:    desc,
		SourcePath:     sourcePath,
		RemoteTarget:   remoteTarget,
		Direction:      direction,
		Schedule:       schedule,
		FrequencyText:  freqText,
		BandwidthLimit: bw,
		Status:         cloudsync.StatusIdle,
		Enabled:        enabled,
	}

	if err := s.cloudsync.SaveJob(job); err != nil {
		s.setHXTrigger(w, "Failed to save Cloud Sync task: "+err.Error(), "error")
	} else {
		s.setHXTrigger(w, fmt.Sprintf("Cloud Sync task '%s' saved", name), "success", "cloudSyncUpdate")
	}
	s.handleUIPartialCloudSync(w, r)
}

func (s *Server) handleUIPartialSnapshotTasks(w http.ResponseWriter, r *http.Request) {
	s.renderUIPartial(w, "partials/snapshot_tasks.html", s.buildPartialViewData(viewFetchMask{
		snapshotTasks: true,
	}))
}

func (s *Server) handleUIActionSaveSnapshotTask(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	lifetimeVal, err := strconv.Atoi(strings.TrimSpace(r.FormValue("lifetime_value")))
	if err != nil || lifetimeVal <= 0 {
		lifetimeVal = 2
	}
	task := zfs.SnapshotTask{
		ID:            strings.TrimSpace(r.FormValue("id")),
		Dataset:       strings.TrimSpace(r.FormValue("dataset")),
		Recursive:     r.FormValue("recursive") == "true" || r.FormValue("recursive") == "on",
		LifetimeValue: lifetimeVal,
		LifetimeUnit:  strings.TrimSpace(r.FormValue("lifetime_unit")),
		NamingSchema:  strings.TrimSpace(r.FormValue("naming_schema")),
		Schedule:      strings.TrimSpace(r.FormValue("schedule")),
		FrequencyText: strings.TrimSpace(r.FormValue("frequency_text")),
		Enabled:       r.FormValue("enabled") == "true" || r.FormValue("enabled") == "on",
	}

	saved, err := s.zfs.SaveSnapshotTask(task)
	if err != nil {
		s.setHXTrigger(w, "Failed to save Periodic Snapshot Task: "+err.Error(), "error")
	} else {
		s.setHXTrigger(w, fmt.Sprintf("Periodic Snapshot Task for '%s' saved", saved.Dataset), "success", "snapshotTaskUpdate")
	}
	s.handleUIPartialSnapshotTasks(w, r)
}

func (s *Server) handleUIActionToggleSnapshotTask(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	id := strings.TrimSpace(r.FormValue("id"))
	t, err := s.zfs.ToggleSnapshotTask(id, nil)
	if err != nil {
		s.setHXTrigger(w, "Failed to toggle snapshot task: "+err.Error(), "error")
	} else {
		state := "disabled"
		if t.Enabled {
			state = "enabled"
		}
		s.setHXTrigger(w, fmt.Sprintf("Snapshot task for '%s' %s", t.Dataset, state), "info", "snapshotTaskUpdate")
	}
	s.handleUIPartialSnapshotTasks(w, r)
}

func (s *Server) handleUIActionRunSnapshotTask(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	id := strings.TrimSpace(r.FormValue("id"))
	t, snapName, err := s.zfs.RunSnapshotTask(id)
	if err != nil {
		s.setHXTrigger(w, "Snapshot task execution failed: "+err.Error(), "error", "snapshotTaskUpdate")
	} else {
		s.setHXTrigger(w, fmt.Sprintf("Snapshot '%s@%s' created", t.Dataset, snapName), "success", "snapshotTaskUpdate", "snapshotUpdate", "datasetUpdate")
	}
	s.handleUIPartialSnapshotTasks(w, r)
}

func (s *Server) handleUIActionDeleteSnapshotTask(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	id := strings.TrimSpace(r.FormValue("id"))
	if err := s.zfs.DeleteSnapshotTask(id); err != nil {
		s.setHXTrigger(w, "Failed to delete snapshot task: "+err.Error(), "error")
	} else {
		s.setHXTrigger(w, "Periodic Snapshot Task deleted", "success", "snapshotTaskUpdate")
	}
	s.handleUIPartialSnapshotTasks(w, r)
}
