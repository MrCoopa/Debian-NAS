package api

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"debian-nas/internal/cloudsync"
	"debian-nas/internal/config"
	"debian-nas/internal/disks"
	"debian-nas/internal/shares"
	"debian-nas/internal/system"
	"debian-nas/internal/web"
	"debian-nas/internal/zfs"
)

type Server struct {
	cfg         *config.Config
	sys         *system.Collector
	disks       *disks.Manager
	zfs         *zfs.Manager
	shares      *shares.Manager
	cloudsync   *cloudsync.Manager
	staticFS    fs.FS
	web         *web.Engine
	startTime   time.Time
	snapPageMu  sync.RWMutex
	snapPages   map[string]int
	snapFilters map[string]string
}

func NewServer(
	cfg *config.Config,
	sys *system.Collector,
	dm *disks.Manager,
	zm *zfs.Manager,
	sm *shares.Manager,
	cm *cloudsync.Manager,
	staticFS fs.FS,
) *Server {
	webEngine, err := web.NewEngine()
	if err != nil {
		fmt.Printf("[WEB] Warning: could not initialize web template engine: %v\n", err)
	}

	return &Server{
		cfg:       cfg,
		sys:       sys,
		disks:     dm,
		zfs:       zm,
		shares:    sm,
		cloudsync:   cm,
		staticFS:    staticFS,
		web:         webEngine,
		startTime:   time.Now(),
		snapPages:   make(map[string]int),
		snapFilters: make(map[string]string),
	}
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()

	// 0. Authentication & Security
	mux.HandleFunc("GET /api/v1/auth/status", s.handleAuthStatus)
	mux.HandleFunc("POST /api/v1/auth/login", s.handleAuthLogin)
	mux.HandleFunc("POST /api/v1/auth/logout", s.handleAuthLogout)

	// 1. System Telemetry
	mux.HandleFunc("GET /api/v1/system", s.handleSystemStatus)
	mux.HandleFunc("GET /api/v1/system/arc", s.handleGetArcConfig)
	mux.HandleFunc("POST /api/v1/system/arc", s.handleSetArcConfig)

	// 2. Disks & Standby Control (45Drives Houston Chassis & SMART)
	mux.HandleFunc("GET /api/v1/disks", s.handleListDisks)
	mux.HandleFunc("POST /api/v1/disks/spindown", s.handleSetSpindown)
	mux.HandleFunc("POST /api/v1/disks/apm", s.handleSetDiskAPM)
	mux.HandleFunc("POST /api/v1/disks/standby", s.handleForceStandby)
	mux.HandleFunc("POST /api/v1/disks/locate", s.handleToggleLocate)
	mux.HandleFunc("POST /api/v1/disks/smart-test", s.handleTriggerSmartTest)
	mux.HandleFunc("POST /api/v1/disks/wipe", s.handleWipeDisk)

	// 3. OpenZFS Pools, Datasets & Snapshots
	mux.HandleFunc("GET /api/v1/zfs/pools", s.handleListPools)
	mux.HandleFunc("GET /api/v1/zfs/pools/importable", s.handleListImportablePools)
	mux.HandleFunc("POST /api/v1/zfs/pools/import", s.handleImportPool)
	mux.HandleFunc("POST /api/v1/zfs/pools/create", s.handleCreatePool)
	mux.HandleFunc("DELETE /api/v1/zfs/pools", s.handleDestroyPool)
	mux.HandleFunc("POST /api/v1/zfs/pools/export", s.handleExportPool)
	mux.HandleFunc("POST /api/v1/zfs/pools/trim", s.handleTrimPool)
	mux.HandleFunc("POST /api/v1/zfs/pools/clear", s.handleClearPool)
	mux.HandleFunc("POST /api/v1/zfs/pools/vdev", s.handleAddVdev)
	mux.HandleFunc("POST /api/v1/zfs/pools/disk/replace", s.handleReplaceDisk)
	mux.HandleFunc("POST /api/v1/zfs/pools/disk/attach", s.handleAttachDisk)
	mux.HandleFunc("POST /api/v1/zfs/pools/disk/detach", s.handleDetachDisk)
	mux.HandleFunc("POST /api/v1/zfs/pools/disk/offline", s.handleOfflineDisk)
	mux.HandleFunc("POST /api/v1/zfs/pools/disk/online", s.handleOnlineDisk)
	mux.HandleFunc("POST /api/v1/zfs/pools/property", s.handleSetPoolProperty)
	mux.HandleFunc("POST /api/v1/zfs/scrub", s.handleScrub)
	mux.HandleFunc("GET /api/v1/zfs/datasets", s.handleListDatasets)
	mux.HandleFunc("POST /api/v1/zfs/datasets", s.handleCreateDataset)
	mux.HandleFunc("DELETE /api/v1/zfs/datasets", s.handleDestroyDataset)
	mux.HandleFunc("POST /api/v1/zfs/datasets/property", s.handleSetDatasetProperty)
	mux.HandleFunc("POST /api/v1/zfs/zvols", s.handleCreateZvol)
	mux.HandleFunc("GET /api/v1/zfs/snapshots", s.handleListSnapshots)
	mux.HandleFunc("POST /api/v1/zfs/snapshots", s.handleCreateSnapshot)
	mux.HandleFunc("POST /api/v1/zfs/snapshots/rollback", s.handleRollbackSnapshot)
	mux.HandleFunc("POST /api/v1/zfs/snapshots/clone", s.handleCloneSnapshot)
	mux.HandleFunc("DELETE /api/v1/zfs/snapshots", s.handleDestroySnapshot)
	mux.HandleFunc("GET /api/v1/zfs/snapshot-tasks", s.handleListSnapshotTasks)
	mux.HandleFunc("POST /api/v1/zfs/snapshot-tasks", s.handleSaveSnapshotTask)
	mux.HandleFunc("POST /api/v1/zfs/snapshot-tasks/toggle", s.handleToggleSnapshotTask)
	mux.HandleFunc("POST /api/v1/zfs/snapshot-tasks/run", s.handleRunSnapshotTask)
	mux.HandleFunc("DELETE /api/v1/zfs/snapshot-tasks", s.handleDeleteSnapshotTask)

	// Storage Benchmark Engine (FIO Storage Testing)
	mux.HandleFunc("POST /api/v1/storage/benchmark", s.handleStorageBenchmark)

	// 4. File Shares (SMB & NFS) & Storage Users
	mux.HandleFunc("GET /api/v1/shares/smb", s.handleListSmb)
	mux.HandleFunc("POST /api/v1/shares/smb", s.handleSaveSmb)
	mux.HandleFunc("DELETE /api/v1/shares/smb", s.handleDeleteSmb)
	mux.HandleFunc("GET /api/v1/shares/nfs", s.handleListNfs)
	mux.HandleFunc("POST /api/v1/shares/nfs", s.handleSaveNfs)
	mux.HandleFunc("DELETE /api/v1/shares/nfs", s.handleDeleteNfs)
	mux.HandleFunc("GET /api/v1/shares/users", s.handleListUsers)
	mux.HandleFunc("POST /api/v1/shares/users", s.handleCreateUser)
	mux.HandleFunc("DELETE /api/v1/shares/users", s.handleDeleteUser)

	// 5. Cloud Sync (Rclone)
	mux.HandleFunc("GET /api/v1/cloudsync/remotes", s.handleListCloudRemotes)
	mux.HandleFunc("POST /api/v1/cloudsync/remotes", s.handleCreateCloudRemote)
	mux.HandleFunc("DELETE /api/v1/cloudsync/remotes", s.handleDeleteCloudRemote)
	mux.HandleFunc("POST /api/v1/cloudsync/remotes/test", s.handleTestCloudRemote)
	mux.HandleFunc("GET /api/v1/cloudsync/jobs", s.handleListCloudJobs)
	mux.HandleFunc("POST /api/v1/cloudsync/jobs", s.handleSaveCloudJob)
	mux.HandleFunc("DELETE /api/v1/cloudsync/jobs", s.handleDeleteCloudJob)
	mux.HandleFunc("POST /api/v1/cloudsync/trigger", s.handleTriggerCloudJob)

	// 6. UI Endpoints (Go Templates + HTMX + Idiomorph)
	if s.web != nil {
		mux.HandleFunc("GET /ui/partials/system", s.handleUIPartialSystem)
		mux.HandleFunc("GET /ui/partials/topbar", s.handleUIPartialTop)
		mux.HandleFunc("GET /ui/partials/pools", s.handleUIPartialPools)
		mux.HandleFunc("GET /ui/partials/disks", s.handleUIPartialDisks)
		mux.HandleFunc("GET /ui/partials/datasets", s.handleUIPartialDatasets)
		mux.HandleFunc("GET /ui/partials/shares", s.handleUIPartialShares)
		mux.HandleFunc("GET /ui/partials/users", s.handleUIPartialUsers)
		mux.HandleFunc("GET /ui/partials/snapshots", s.handleUIPartialSnapshots)
		mux.HandleFunc("GET /ui/partials/snapshot-tasks", s.handleUIPartialSnapshotTasks)
		mux.HandleFunc("GET /ui/partials/network", s.handleUIPartialNetwork)
		mux.HandleFunc("GET /ui/partials/arc", s.handleUIPartialArc)
		mux.HandleFunc("GET /ui/partials/arc-stats", s.handleUIPartialArcStats)
		mux.HandleFunc("GET /ui/partials/services", s.handleUIPartialServices)
		mux.HandleFunc("GET /ui/partials/reporting", s.handleUIPartialReporting)
		mux.HandleFunc("GET /ui/partials/cloudsync", s.handleUIPartialCloudSync)
		mux.HandleFunc("GET /ui/partials/modals/create_share", s.handleUIPartialCreateShareModal)
		mux.HandleFunc("GET /ui/partials/modals/share_users", s.handleUIPartialShareModalUsers)
		mux.HandleFunc("GET /ui/partials/modals/snap_datasets", s.handleUIPartialModalSnapDatasets)
		mux.HandleFunc("GET /ui/partials/modals/parent_datasets", s.handleUIPartialModalParentDatasets)
		mux.HandleFunc("GET /ui/partials/modals/pool_disks", s.handleUIPartialModalPoolDisks)
		mux.HandleFunc("GET /ui/partials/modals/share_datasets", s.handleUIPartialModalShareDatasets)
		mux.HandleFunc("GET /ui/partials/modals/available_disks", s.handleUIPartialModalAvailableDisks)
		mux.HandleFunc("GET /ui/partials/modals/importable_pools", s.handleUIPartialModalImportablePools)
		mux.HandleFunc("GET /ui/partials/modals/cloudsync_sources", s.handleUIPartialModalCloudSyncSources)
		mux.HandleFunc("GET /ui/partials/modals/cloudsync_remotes", s.handleUIPartialModalCloudSyncRemotes)

		mux.HandleFunc("POST /ui/actions/system/arc", s.handleUIActionSetArc)
		mux.HandleFunc("POST /ui/actions/zfs/snapshot-tasks/save", s.handleUIActionSaveSnapshotTask)
		mux.HandleFunc("POST /ui/actions/zfs/snapshot-tasks/toggle", s.handleUIActionToggleSnapshotTask)
		mux.HandleFunc("POST /ui/actions/zfs/snapshot-tasks/run", s.handleUIActionRunSnapshotTask)
		mux.HandleFunc("POST /ui/actions/zfs/snapshot-tasks/delete", s.handleUIActionDeleteSnapshotTask)

		mux.HandleFunc("POST /ui/actions/cloudsync/remotes/save", s.handleUIActionSaveCloudRemote)
		mux.HandleFunc("DELETE /ui/actions/cloudsync/remotes/delete", s.handleUIActionDeleteCloudRemote)
		mux.HandleFunc("POST /ui/actions/cloudsync/remotes/test", s.handleUIActionTestCloudRemote)
		mux.HandleFunc("POST /ui/actions/cloudsync/save", s.handleUIActionSaveCloudSync)
		mux.HandleFunc("DELETE /ui/actions/cloudsync/delete", s.handleUIActionDeleteCloudSync)
		mux.HandleFunc("POST /ui/actions/cloudsync/toggle", s.handleUIActionToggleCloudSync)
		mux.HandleFunc("POST /ui/actions/cloudsync/run", s.handleUIActionRunCloudSync)
		mux.HandleFunc("POST /ui/actions/cloudsync/dryrun", s.handleUIActionDryRunCloudSync)

		mux.HandleFunc("POST /ui/actions/shares/save", s.handleUIActionSaveSmb)
		mux.HandleFunc("POST /ui/actions/shares/smb", s.handleUIActionSaveSmb)
		mux.HandleFunc("DELETE /ui/actions/shares/smb", s.handleUIActionDeleteSmb)
		mux.HandleFunc("DELETE /ui/actions/shares/nfs", s.handleUIActionDeleteNfs)
		mux.HandleFunc("POST /ui/actions/shares/users", s.handleUIActionCreateUser)
		mux.HandleFunc("DELETE /ui/actions/shares/users", s.handleUIActionDeleteUser)
		mux.HandleFunc("POST /ui/actions/shares/users/password", s.handleUIActionSetUserPassword)
		mux.HandleFunc("POST /ui/actions/shares/smb/fix-permissions", s.handleUIActionFixSharePermissions)

		mux.HandleFunc("POST /ui/actions/zfs/pools/create", s.handleUIActionCreatePool)
		mux.HandleFunc("POST /ui/actions/zfs/pools/import", s.handleUIActionImportPool)
		mux.HandleFunc("POST /ui/actions/zfs/pools/export", s.handleUIActionExportPool)
		mux.HandleFunc("POST /ui/actions/zfs/pools/clear", s.handleUIActionClearPool)
		mux.HandleFunc("POST /ui/actions/zfs/pools/trim", s.handleUIActionTrimPool)
		mux.HandleFunc("POST /ui/actions/zfs/pools/vdev", s.handleUIActionAddVdev)
		mux.HandleFunc("POST /ui/actions/zfs/pools/property", s.handleUIActionSetPoolProperty)
		mux.HandleFunc("POST /ui/actions/zfs/pools/disk/action", s.handleUIActionDiskAction)
		mux.HandleFunc("POST /ui/actions/zfs/pools/disk/replace", s.handleUIActionDiskAction)
		mux.HandleFunc("POST /ui/actions/zfs/pools/disk/attach", s.handleUIActionDiskAction)
		mux.HandleFunc("POST /ui/actions/zfs/pools/disk/detach", s.handleUIActionDiskAction)
		mux.HandleFunc("POST /ui/actions/zfs/pools/disk/offline", s.handleUIActionDiskAction)
		mux.HandleFunc("POST /ui/actions/zfs/pools/disk/online", s.handleUIActionDiskAction)
		mux.HandleFunc("POST /ui/actions/zfs/scrub", s.handleUIActionScrub)

		mux.HandleFunc("POST /ui/actions/zfs/datasets", s.handleUIActionCreateDataset)
		mux.HandleFunc("POST /ui/actions/zfs/datasets/property", s.handleUIActionSetDatasetProperty)
		mux.HandleFunc("POST /ui/actions/zfs/zvols", s.handleUIActionCreateZvol)
		mux.HandleFunc("POST /ui/actions/zfs/snapshots", s.handleUIActionCreateSnapshot)
		mux.HandleFunc("POST /ui/actions/zfs/snapshots/rollback", s.handleUIActionRollbackSnapshot)
		mux.HandleFunc("POST /ui/actions/zfs/snapshots/clone", s.handleUIActionCloneSnapshot)
		mux.HandleFunc("DELETE /ui/actions/zfs/snapshots", s.handleUIActionDestroySnapshot)
		mux.HandleFunc("POST /ui/actions/zfs/destroy", s.handleUIActionDangerDestroy)

		mux.HandleFunc("POST /ui/actions/disks/spindown", s.handleUIActionSetSpindown)
		mux.HandleFunc("POST /ui/actions/disks/apm", s.handleUIActionSetAPM)
		mux.HandleFunc("POST /ui/actions/disks/standby", s.handleUIActionForceStandby)
		mux.HandleFunc("POST /ui/actions/disks/locate", s.handleUIActionToggleLocate)
		mux.HandleFunc("POST /ui/actions/disks/smart-test", s.handleUIActionTriggerSmartTest)
		mux.HandleFunc("POST /ui/actions/disks/wipe", s.handleUIActionWipeDisk)
	}

	// Static UI assets embedded in binary (served with no-cache to guarantee live updates)
	if s.staticFS != nil {
		fileServer := http.FileServer(http.FS(s.staticFS))
		mux.Handle("GET /static/", http.StripPrefix("/static/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
			fileServer.ServeHTTP(w, r)
		})))
		mux.HandleFunc("GET /favicon.ico", func(w http.ResponseWriter, r *http.Request) {
			if data, err := fs.ReadFile(s.staticFS, "favicon.ico"); err == nil {
				w.Header().Set("Content-Type", "image/x-icon")
				w.Header().Set("Cache-Control", "public, max-age=86400")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(data)
				return
			}
			http.NotFound(w, r)
		})
	}

	if s.web != nil {
		mux.HandleFunc("GET /login", s.handleUILogin)
		mux.HandleFunc("POST /login", s.handleUILoginSubmit)
		mux.HandleFunc("GET /logout", s.handleUILogout)
		mux.HandleFunc("POST /logout", s.handleUILogout)
		mux.HandleFunc("GET /{$}", s.handleUIRoot)
	} else if s.staticFS != nil {
		fileServer := http.FileServer(http.FS(s.staticFS))
		mux.Handle("GET /{$}", fileServer)
	}

	return s.securityMiddleware(mux)
}

func (s *Server) securityMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 1. Security Headers
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "SAMEORIGIN")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")

		origin := r.Header.Get("Origin")
		host := r.Host

		// 2. Strict Origin/Host checks - No wildcard CORS
		if origin != "" {
			originHost := origin
			if idx := strings.Index(origin, "://"); idx != -1 {
				originHost = origin[idx+3:]
			}
			if idx := strings.Index(originHost, "/"); idx != -1 {
				originHost = originHost[:idx]
			}

			if originHost == host || strings.HasPrefix(originHost, "localhost:") || strings.HasPrefix(originHost, "127.0.0.1:") || originHost == "localhost" || originHost == "127.0.0.1" {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Access-Control-Allow-Credentials", "true")
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Auth-Token")
			} else if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
				http.Error(w, "Cross-origin request forbidden", http.StatusForbidden)
				return
			}
		}

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}

		// 3. Authentication Check
		if s.cfg.AuthToken != "" && strings.HasPrefix(r.URL.Path, "/api/v1/") {
			if r.URL.Path != "/api/v1/auth/login" && r.URL.Path != "/api/v1/auth/status" {
				authenticated := false

				authHeader := r.Header.Get("Authorization")
				if authHeader != "" {
					token := strings.TrimPrefix(authHeader, "Bearer ")
					if token == s.cfg.AuthToken {
						authenticated = true
					}
				}

				if !authenticated && r.Header.Get("X-Auth-Token") == s.cfg.AuthToken {
					authenticated = true
				}

				if !authenticated {
					if cookie, err := r.Cookie("nas_session"); err == nil && cookie.Value == s.cfg.AuthToken {
						authenticated = true
					}
				}

				if !authenticated {
					s.errorResponse(w, http.StatusUnauthorized, "Authentication required")
					return
				}
			}
		}

		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleAuthStatus(w http.ResponseWriter, r *http.Request) {
	authenticated := true
	if s.cfg.AuthToken != "" {
		authenticated = false
		if cookie, err := r.Cookie("nas_session"); err == nil && cookie.Value == s.cfg.AuthToken {
			authenticated = true
		} else if r.Header.Get("X-Auth-Token") == s.cfg.AuthToken || strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ") == s.cfg.AuthToken {
			authenticated = true
		}
	}
	s.jsonResponse(w, http.StatusOK, map[string]any{
		"auth_enabled":  s.cfg.AuthToken != "",
		"authenticated": authenticated,
	})
}

func (s *Server) handleAuthLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid JSON payload")
		return
	}

	if s.cfg.AuthToken == "" || req.Token == s.cfg.AuthToken {
		http.SetCookie(w, &http.Cookie{
			Name:     "nas_session",
			Value:    req.Token,
			Path:     "/",
			HttpOnly: true,
			SameSite: http.SameSiteStrictMode,
			MaxAge:   86400 * 30, // 30 days
		})
		s.jsonResponse(w, http.StatusOK, map[string]string{"status": "authenticated"})
		return
	}

	s.errorResponse(w, http.StatusUnauthorized, "Invalid authentication token")
}

func (s *Server) handleAuthLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     "nas_session",
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   -1,
	})
	s.jsonResponse(w, http.StatusOK, map[string]string{"status": "logged out"})
}

func (s *Server) jsonResponse(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

func (s *Server) errorResponse(w http.ResponseWriter, status int, message string) {
	s.jsonResponse(w, status, map[string]string{"error": message})
}

// ---------------- 1. System Handlers ----------------

func (s *Server) handleSystemStatus(w http.ResponseWriter, r *http.Request) {
	status := s.sys.GetStatus()
	allDisks, _ := s.disks.ListDisks()

	spinningCount := 0
	standbyCount := 0
	for _, d := range allDisks {
		if d.IsRotational {
			if d.State == disks.StateStandby || d.State == disks.StateSleep {
				standbyCount++
			} else if d.State == disks.StateActive {
				spinningCount++
			}
		}
	}

	res := map[string]any{
		"hostname":      status.Hostname,
		"os":            status.OS,
		"kernel":        status.Kernel,
		"arch":          status.Arch,
		"uptime":        status.Uptime,
		"uptime_sec":    status.UptimeSec,
		"daemon_ram_mb": status.DaemonRamMB,
		"goroutines":    status.Goroutines,
		"disks": map[string]int{
			"total":    len(allDisks),
			"spinning": spinningCount,
			"standby":  standbyCount,
		},
		"cpu":     status.CPU,
		"memory":  status.Memory,
		"zfs_arc": status.ZfsArc,
		"network": status.Network,
		"disk_io": status.DiskIO,
	}
	s.jsonResponse(w, http.StatusOK, res)
}

func (s *Server) handleGetArcConfig(w http.ResponseWriter, r *http.Request) {
	s.jsonResponse(w, http.StatusOK, s.sys.GetArcConfig())
}

func (s *Server) handleSetArcConfig(w http.ResponseWriter, r *http.Request) {
	var req struct {
		MaxBytes  *uint64 `json:"max_bytes"`
		MinBytes  *uint64 `json:"min_bytes"`
		MaxValue  string  `json:"max_value"`
		MaxUnit   string  `json:"max_unit"`
		MinValue  string  `json:"min_value"`
		MinUnit   string  `json:"min_unit"`
		ResetAuto bool    `json:"reset_auto"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid JSON payload")
		return
	}

	var maxBytes, minBytes uint64
	if !req.ResetAuto {
		if req.MaxBytes != nil {
			maxBytes = *req.MaxBytes
		} else {
			parsed, err := system.ParseArcSizeInput(req.MaxValue, req.MaxUnit)
			if err != nil {
				s.errorResponse(w, http.StatusBadRequest, err.Error())
				return
			}
			maxBytes = parsed
		}
		if req.MinBytes != nil {
			minBytes = *req.MinBytes
		} else {
			parsed, err := system.ParseArcSizeInput(req.MinValue, req.MinUnit)
			if err != nil {
				s.errorResponse(w, http.StatusBadRequest, err.Error())
				return
			}
			minBytes = parsed
		}
	}

	updated, err := s.sys.SetArcConfig(maxBytes, minBytes)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	s.jsonResponse(w, http.StatusOK, updated)
}

// ---------------- 2. Disks & Standby Handlers ----------------

func (s *Server) syncDiskPoolMembershipFromPools(pools []*zfs.Pool) {
	if s.disks == nil {
		return
	}
	poolMap, roleMap := zfs.BuildDiskPoolMap(pools)
	s.disks.SyncPoolMembership(poolMap, roleMap)
}

func (s *Server) syncDiskPoolMembership() {
	if s.disks == nil || s.zfs == nil {
		return
	}
	pools, err := s.zfs.ListPools()
	if err != nil {
		return
	}
	s.syncDiskPoolMembershipFromPools(pools)
}

func (s *Server) handleListDisks(w http.ResponseWriter, r *http.Request) {
	var list []*disks.DiskInfo
	var err error
	if r.URL.Query().Get("rescan") == "true" {
		list, err = s.disks.RescanDisks()
	} else {
		list, err = s.disks.ListDisks()
	}
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, err.Error())
		return
	}

	s.syncDiskPoolMembership()
	if synced, syncErr := s.disks.ListDisks(); syncErr == nil {
		list = synced
	}
	s.jsonResponse(w, http.StatusOK, list)
}

func (s *Server) handleSetSpindown(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path           string `json:"path"`
		Device         string `json:"device"`
		Name           string `json:"name"`
		Seconds        *int   `json:"seconds"`
		TimeoutSeconds *int   `json:"timeout_seconds"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid JSON payload")
		return
	}
	target := strings.TrimSpace(req.Path)
	if target == "" {
		target = strings.TrimSpace(req.Device)
	}
	if target == "" {
		target = strings.TrimSpace(req.Name)
	}
	if target == "" {
		s.errorResponse(w, http.StatusBadRequest, "Disk device or path required")
		return
	}

	sec := 0
	if req.Seconds != nil {
		sec = *req.Seconds
	} else if req.TimeoutSeconds != nil {
		sec = *req.TimeoutSeconds
	}

	if err := s.disks.SetSpindown(target, sec); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.jsonResponse(w, http.StatusOK, map[string]any{"status": "ok", "device": target, "timeout_seconds": sec})
}

func (s *Server) handleSetDiskAPM(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path   string `json:"path"`
		Device string `json:"device"`
		Name   string `json:"name"`
		Level  string `json:"level"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid JSON payload")
		return
	}
	target := strings.TrimSpace(req.Path)
	if target == "" {
		target = strings.TrimSpace(req.Device)
	}
	if target == "" {
		target = strings.TrimSpace(req.Name)
	}
	if target == "" {
		s.errorResponse(w, http.StatusBadRequest, "Disk device or path required")
		return
	}
	level := strings.TrimSpace(req.Level)
	if level == "" {
		level = "Disabled"
	}
	if err := s.disks.SetAPM(target, level); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.jsonResponse(w, http.StatusOK, map[string]any{"status": "ok", "device": target, "apm_level": level})
}

func (s *Server) handleForceStandby(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path   string `json:"path"`
		Device string `json:"device"`
		Name   string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid JSON payload")
		return
	}
	target := strings.TrimSpace(req.Path)
	if target == "" {
		target = strings.TrimSpace(req.Device)
	}
	if target == "" {
		target = strings.TrimSpace(req.Name)
	}
	if target == "" {
		s.errorResponse(w, http.StatusBadRequest, "Disk device or path required")
		return
	}
	if err := s.disks.ForceStandby(target); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.jsonResponse(w, http.StatusOK, map[string]string{"status": "standby triggered", "device": target})
}

func (s *Server) handleToggleLocate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path   string `json:"path"`
		Device string `json:"device"`
		Name   string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid JSON payload")
		return
	}
	target := strings.TrimSpace(req.Path)
	if target == "" {
		target = strings.TrimSpace(req.Device)
	}
	if target == "" {
		target = strings.TrimSpace(req.Name)
	}
	if target == "" {
		s.errorResponse(w, http.StatusBadRequest, "Disk device or path required")
		return
	}
	active, err := s.disks.ToggleLocate(target)
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.jsonResponse(w, http.StatusOK, map[string]any{"status": "ok", "device": target, "locate_led": active})
}

func (s *Server) handleWipeDisk(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path   string `json:"path"`
		Device string `json:"device"`
		Name   string `json:"name"`
		Mode   string `json:"mode"` // "quick" or "zero"
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid JSON payload")
		return
	}
	target := strings.TrimSpace(req.Path)
	if target == "" {
		target = strings.TrimSpace(req.Device)
	}
	if target == "" {
		target = strings.TrimSpace(req.Name)
	}
	if target == "" {
		s.errorResponse(w, http.StatusBadRequest, "Disk device or path required")
		return
	}
	s.syncDiskPoolMembership()
	if err := s.disks.WipeDisk(target, req.Mode); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.jsonResponse(w, http.StatusOK, map[string]string{"status": "disk wiped", "device": target})
}

// ---------------- 3. ZFS Handlers ----------------

func (s *Server) handleListPools(w http.ResponseWriter, r *http.Request) {
	pools, err := s.zfs.ListPools()
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.jsonResponse(w, http.StatusOK, pools)
}

func (s *Server) handleCreatePool(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name        string   `json:"name"`
		VdevType    string   `json:"vdev_type"`
		Type        string   `json:"type"`
		Disks       []string `json:"disks"`
		Ashift      int      `json:"ashift"`
		Compression string   `json:"compression"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid JSON payload: "+err.Error())
		return
	}
	if req.Name == "" || len(req.Disks) == 0 {
		s.errorResponse(w, http.StatusBadRequest, "Pool name and at least one disk required")
		return
	}
	vdevType := strings.TrimSpace(req.VdevType)
	if vdevType == "" {
		vdevType = strings.TrimSpace(req.Type)
	}
	if req.Ashift <= 0 {
		req.Ashift = 12
	}
	if req.Compression == "" {
		req.Compression = "zstd"
	}
	if err := s.zfs.CreatePool(req.Name, vdevType, req.Disks, req.Ashift, req.Compression); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.syncDiskPoolMembership()
	s.jsonResponse(w, http.StatusOK, map[string]string{"status": "pool created", "name": req.Name})
}

func (s *Server) handleDestroyPool(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if name == "" {
		name = strings.TrimSpace(r.URL.Query().Get("pool"))
	}
	if r.Body != nil {
		var req struct {
			Name string `json:"name"`
			Pool string `json:"pool"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err == nil {
			if req.Name != "" {
				name = strings.TrimSpace(req.Name)
			} else if req.Pool != "" {
				name = strings.TrimSpace(req.Pool)
			}
		}
	}
	if name == "" {
		s.errorResponse(w, http.StatusBadRequest, "Pool name required")
		return
	}
	if err := s.zfs.DestroyPool(name); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.syncDiskPoolMembership()
	s.jsonResponse(w, http.StatusOK, map[string]string{"status": "pool destroyed", "name": name})
}

func (s *Server) handleExportPool(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if name == "" {
		name = strings.TrimSpace(r.URL.Query().Get("pool"))
	}
	if r.Body != nil {
		var req struct {
			Name string `json:"name"`
			Pool string `json:"pool"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err == nil {
			if req.Name != "" {
				name = strings.TrimSpace(req.Name)
			} else if req.Pool != "" {
				name = strings.TrimSpace(req.Pool)
			}
		}
	}
	if name == "" {
		s.errorResponse(w, http.StatusBadRequest, "Pool name required")
		return
	}
	if err := s.zfs.ExportPool(name); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.syncDiskPoolMembership()
	s.jsonResponse(w, http.StatusOK, map[string]string{"status": "pool exported", "name": name})
}

func (s *Server) handleListImportablePools(w http.ResponseWriter, r *http.Request) {
	pools, err := s.zfs.FindImportablePools()
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.jsonResponse(w, http.StatusOK, pools)
}

func (s *Server) handleImportPool(w http.ResponseWriter, r *http.Request) {
	var req struct {
		NameOrID string `json:"name_or_id"`
		NewName  string `json:"new_name"`
		Force    bool   `json:"force"`
		ReadOnly bool   `json:"readonly"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid JSON payload")
		return
	}
	if req.NameOrID == "" {
		s.errorResponse(w, http.StatusBadRequest, "name_or_id is required")
		return
	}

	if err := s.zfs.ImportPool(req.NameOrID, req.NewName, req.Force, req.ReadOnly); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.syncDiskPoolMembership()
	s.jsonResponse(w, http.StatusOK, map[string]string{"status": "pool imported", "pool": req.NameOrID})
}

func (s *Server) handleTrimPool(w http.ResponseWriter, r *http.Request) {
	pool := strings.TrimSpace(r.URL.Query().Get("pool"))
	if pool == "" {
		pool = strings.TrimSpace(r.URL.Query().Get("name"))
	}
	if r.Body != nil {
		var req struct {
			Pool string `json:"pool"`
			Name string `json:"name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err == nil {
			if req.Pool != "" {
				pool = strings.TrimSpace(req.Pool)
			} else if req.Name != "" {
				pool = strings.TrimSpace(req.Name)
			}
		}
	}
	if pool == "" {
		s.errorResponse(w, http.StatusBadRequest, "Pool name required")
		return
	}
	if err := s.zfs.TrimPool(pool); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.jsonResponse(w, http.StatusOK, map[string]string{"status": "trim started", "pool": pool})
}

func (s *Server) handleClearPool(w http.ResponseWriter, r *http.Request) {
	pool := strings.TrimSpace(r.URL.Query().Get("pool"))
	if pool == "" {
		pool = strings.TrimSpace(r.URL.Query().Get("name"))
	}
	if r.Body != nil {
		var req struct {
			Pool string `json:"pool"`
			Name string `json:"name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err == nil {
			if req.Pool != "" {
				pool = strings.TrimSpace(req.Pool)
			} else if req.Name != "" {
				pool = strings.TrimSpace(req.Name)
			}
		}
	}
	if pool == "" {
		s.errorResponse(w, http.StatusBadRequest, "Pool name required")
		return
	}
	if err := s.zfs.ClearPool(pool); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.jsonResponse(w, http.StatusOK, map[string]string{"status": "errors cleared", "pool": pool})
}

// Storage Benchmark Engine (FIO Storage Testing)
func (s *Server) handleStorageBenchmark(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Target     string `json:"target"`      // pool name or disk name
		TargetPath string `json:"target_path"` // frontend alias
		Profile    string `json:"profile"`     // "seq", "rand4k", "mixed"
		Mode       string `json:"mode"`        // frontend alias: "seq_read", "seq_write", "rand_read", "rand_write"
		SizeMB     int    `json:"size_mb"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid JSON payload")
		return
	}

	target := strings.TrimSpace(req.Target)
	if target == "" {
		target = strings.TrimSpace(req.TargetPath)
	}
	mode := strings.TrimSpace(req.Mode)
	if mode == "" {
		mode = strings.TrimSpace(req.Profile)
	}
	if req.SizeMB <= 0 {
		req.SizeMB = 1024
	}

	var readMB, writeMB float64
	var iops int
	var latMs float64

	// If running in production (not mock mode):
	if !s.cfg.MockMode {
		if runtime.GOOS != "linux" {
			s.errorResponse(w, http.StatusBadRequest, "Storage benchmarks via FIO are only supported on Linux")
			return
		}
		if _, err := exec.LookPath("fio"); err != nil {
			s.errorResponse(w, http.StatusNotFound, "fio benchmark tool is not installed (run 'apt install fio')")
			return
		}

		targetPath := "/tmp/.nas_bench.tmp"
		if target != "" && !strings.Contains(target, "..") {
			if strings.HasPrefix(target, "/") {
				targetPath = filepath.Join(filepath.Clean(target), ".nas_bench.tmp")
			} else {
				targetPath = filepath.Join("/mnt", filepath.Clean(target), ".nas_bench.tmp")
			}
		}

		fioArgs := []string{
			"--name=nas_bench",
			fmt.Sprintf("--filename=%s", targetPath),
			fmt.Sprintf("--size=%dM", req.SizeMB),
			"--direct=1",
			"--ioengine=libaio",
			"--output-format=json",
		}
		switch mode {
		case "rand4k", "rand_read", "rand_write":
			fioArgs = append(fioArgs, "--rw=randrw", "--bs=4k", "--iodepth=32")
		default:
			fioArgs = append(fioArgs, "--rw=readwrite", "--bs=1M", "--iodepth=8")
		}

		cmd := exec.Command("fio", fioArgs...)
		out, err := cmd.CombinedOutput()
		_ = os.Remove(targetPath)

		if err != nil {
			s.errorResponse(w, http.StatusInternalServerError, fmt.Sprintf("fio execution failed: %v (%s)", err, strings.TrimSpace(string(out))))
			return
		}

		var fioResult struct {
			Jobs []struct {
				Read struct {
					BwBytes uint64  `json:"bw_bytes"`
					Iops    float64 `json:"iops"`
					LatNs   struct {
						Mean float64 `json:"mean"`
					} `json:"lat_ns"`
				} `json:"read"`
				Write struct {
					BwBytes uint64  `json:"bw_bytes"`
					Iops    float64 `json:"iops"`
				} `json:"write"`
			} `json:"jobs"`
		}
		if err := json.Unmarshal(out, &fioResult); err != nil || len(fioResult.Jobs) == 0 {
			s.errorResponse(w, http.StatusInternalServerError, "failed to parse fio output")
			return
		}

		j := fioResult.Jobs[0]
		readMB = float64(j.Read.BwBytes) / (1024 * 1024)
		writeMB = float64(j.Write.BwBytes) / (1024 * 1024)
		iops = int(j.Read.Iops + j.Write.Iops)
		latMs = j.Read.LatNs.Mean / 1_000_000
	} else {
		// Mock calculation
		readMB = 542.4
		writeMB = 518.8
		iops = 72400
		latMs = 0.24

		if strings.Contains(strings.ToUpper(target), "NVME") || strings.Contains(strings.ToUpper(target), "SSD") {
			readMB = 3450.0
			writeMB = 2980.0
			iops = 485000
			latMs = 0.04
		} else if strings.HasPrefix(mode, "rand") {
			readMB = 48.2
			writeMB = 42.1
			iops = 12400
			latMs = 1.15
		} else if mode == "mixed" {
			readMB = 280.5
			writeMB = 260.0
			iops = 38000
			latMs = 0.45
		}
	}

	throughputMB := readMB
	if writeMB > throughputMB {
		throughputMB = writeMB
	}
	if mode == "seq_write" || mode == "rand_write" {
		throughputMB = writeMB
	}

	s.jsonResponse(w, http.StatusOK, map[string]any{
		"target":          target,
		"target_path":     target,
		"profile":         mode,
		"mode":            mode,
		"throughput_mb_s": throughputMB,
		"read_mb_s":       readMB,
		"write_mb_s":      writeMB,
		"iops":            iops,
		"read_iops":       iops,
		"latency_ms":      latMs,
		"timestamp":       time.Now().Format("2006-01-02 15:04:05"),
		"benchmark_msg":   "Benchmark completed via native asynchronous I/O engine",
	})
}

func (s *Server) handleScrub(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Pool   string `json:"pool"`
		Name   string `json:"name"`
		Action string `json:"action"` // "start" or "stop"
	}
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&req)
	}
	pool := strings.TrimSpace(req.Pool)
	if pool == "" {
		pool = strings.TrimSpace(req.Name)
	}
	if pool == "" {
		pool = strings.TrimSpace(r.URL.Query().Get("pool"))
	}
	if pool == "" {
		pool = strings.TrimSpace(r.URL.Query().Get("name"))
	}
	if pool == "" {
		s.errorResponse(w, http.StatusBadRequest, "Pool name required")
		return
	}
	var err error
	if req.Action == "stop" {
		err = s.zfs.StopScrub(pool)
	} else {
		err = s.zfs.StartScrub(pool)
	}
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.jsonResponse(w, http.StatusOK, map[string]string{"status": "ok", "pool": pool})
}

func (s *Server) handleListDatasets(w http.ResponseWriter, r *http.Request) {
	list, err := s.zfs.ListDatasets()
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.jsonResponse(w, http.StatusOK, list)
}

func (s *Server) handleCreateDataset(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name        string `json:"name"`
		Compression string `json:"compression"`
		RecordSize  string `json:"recordsize"`
		Atime       string `json:"atime"`
		Quota       string `json:"quota"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid JSON payload")
		return
	}
	if err := s.zfs.CreateDataset(req.Name, req.Compression, req.RecordSize); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, err.Error())
		return
	}
	if req.Atime != "" && req.Atime != "off" {
		_ = s.zfs.SetDatasetProperty(req.Name, "atime", req.Atime)
	}
	if req.Quota != "" && req.Quota != "none" {
		_ = s.zfs.SetDatasetProperty(req.Name, "quota", req.Quota)
	}
	s.jsonResponse(w, http.StatusOK, map[string]string{"status": "created", "atime": "off"})
}

func (s *Server) handleDestroyDataset(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if name == "" {
		name = strings.TrimSpace(r.URL.Query().Get("dataset"))
	}
	if r.Body != nil {
		var req struct {
			Name    string `json:"name"`
			Dataset string `json:"dataset"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err == nil {
			if req.Name != "" {
				name = strings.TrimSpace(req.Name)
			} else if req.Dataset != "" {
				name = strings.TrimSpace(req.Dataset)
			}
		}
	}
	if name == "" {
		s.errorResponse(w, http.StatusBadRequest, "Dataset name required")
		return
	}
	if err := s.zfs.DestroyDataset(name); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.jsonResponse(w, http.StatusOK, map[string]string{"status": "destroyed", "name": name})
}

func (s *Server) handleListSnapshots(w http.ResponseWriter, r *http.Request) {
	snaps, err := s.zfs.ListSnapshots()
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.jsonResponse(w, http.StatusOK, snaps)
}

func (s *Server) handleCreateSnapshot(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Dataset      string `json:"dataset"`
		Name         string `json:"name"`
		SnapshotName string `json:"snapshot_name"`
		Recursive    bool   `json:"recursive"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid JSON payload: "+err.Error())
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = strings.TrimSpace(req.SnapshotName)
	}
	dataset := strings.TrimSpace(req.Dataset)
	if dataset == "" || name == "" {
		s.errorResponse(w, http.StatusBadRequest, "Dataset and snapshot name required")
		return
	}
	if err := s.zfs.CreateSnapshotRecursive(dataset, name, req.Recursive); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.jsonResponse(w, http.StatusOK, map[string]string{"status": "created", "snapshot": dataset + "@" + name})
}

// rollbackSnapshotSynced cleanly releases overlapping Samba handles before ZFS rollback,
// executes the rollback (optionally including matching descendant snapshots), and reloads
// SMB/NFS shares so connected clients immediately see restored snapshot files.
func (s *Server) rollbackSnapshotSynced(snapshot string, recursive bool) ([]string, []string, error) {
	dsName := snapshot
	if at := strings.IndexByte(snapshot, '@'); at >= 0 {
		dsName = snapshot[:at]
	}

	var smbNames []string
	var hasNfs bool
	if s.zfs != nil && s.shares != nil {
		mps := s.zfs.CollectMountpointsForDataset(dsName)
		smbNames, hasNfs = s.shares.SharesMatchingMountpoints(mps)
		if len(smbNames) > 0 {
			s.shares.ReleaseShareConnections(smbNames)
		}
	}

	rolledBack, skippedChildren, err := s.zfs.RollbackSnapshotWithOptions(snapshot, recursive)
	if s.shares != nil && (len(smbNames) > 0 || hasNfs) {
		s.shares.RefreshSharesAfterRollback(smbNames, hasNfs)
	}
	return rolledBack, skippedChildren, err
}

func (s *Server) ensureSharePathMounted(sharePath string) {
	if s.zfs != nil && strings.TrimSpace(sharePath) != "" {
		_ = s.zfs.EnsurePathMounted(sharePath)
	}
}

func (s *Server) handleRollbackSnapshot(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Snapshot     string `json:"snapshot"`
		Dataset      string `json:"dataset"`
		SnapshotName string `json:"snapshot_name"`
		Recursive    bool   `json:"recursive"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&req)
	}
	target := strings.TrimSpace(req.Snapshot)
	if target == "" && req.Dataset != "" && req.SnapshotName != "" {
		snapTag := strings.TrimSpace(req.SnapshotName)
		if strings.Contains(snapTag, "@") {
			target = snapTag
		} else {
			target = fmt.Sprintf("%s@%s", strings.TrimSpace(req.Dataset), snapTag)
		}
	}
	if target == "" {
		target = strings.TrimSpace(r.URL.Query().Get("snapshot"))
	}
	recursive := req.Recursive || r.URL.Query().Get("recursive") == "true"
	if target == "" {
		s.errorResponse(w, http.StatusBadRequest, "Snapshot target required")
		return
	}
	if strings.Count(target, "@") > 1 {
		parts := strings.Split(target, "@")
		target = parts[0] + "@" + parts[len(parts)-1]
	}
	rolledBack, skippedChildren, err := s.rollbackSnapshotSynced(target, recursive)
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.jsonResponse(w, http.StatusOK, map[string]interface{}{
		"status":           "rolled back",
		"snapshot":         target,
		"rolled_back":      rolledBack,
		"skipped_children": skippedChildren,
	})
}

func (s *Server) handleDestroySnapshot(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if name == "" {
		name = strings.TrimSpace(r.URL.Query().Get("snapshot"))
	}
	if r.Body != nil {
		var req struct {
			Name     string `json:"name"`
			Snapshot string `json:"snapshot"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err == nil {
			if req.Snapshot != "" {
				name = strings.TrimSpace(req.Snapshot)
			} else if req.Name != "" {
				name = strings.TrimSpace(req.Name)
			}
		}
	}
	if name == "" {
		s.errorResponse(w, http.StatusBadRequest, "Snapshot name required")
		return
	}
	if strings.Count(name, "@") > 1 {
		parts := strings.Split(name, "@")
		name = parts[0] + "@" + parts[len(parts)-1]
	}
	if err := s.zfs.DestroySnapshot(name); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.jsonResponse(w, http.StatusOK, map[string]string{"status": "destroyed", "snapshot": name})
}

func (s *Server) handleSetDatasetProperty(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Dataset  string `json:"dataset"`
		Property string `json:"property"`
		Value    string `json:"value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid JSON payload")
		return
	}
	if err := s.zfs.SetDatasetProperty(req.Dataset, req.Property, req.Value); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.jsonResponse(w, http.StatusOK, map[string]string{"status": "property updated"})
}

func (s *Server) handleAddVdev(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Pool     string   `json:"pool"`
		VdevType string   `json:"vdev_type"`
		Disks    []string `json:"disks"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid JSON payload")
		return
	}
	if req.Pool == "" || len(req.Disks) == 0 {
		s.errorResponse(w, http.StatusBadRequest, "pool and disks are required")
		return
	}
	if err := s.zfs.AddVdev(req.Pool, req.VdevType, req.Disks); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.syncDiskPoolMembership()
	s.jsonResponse(w, http.StatusOK, map[string]string{"status": "vdev added", "pool": req.Pool})
}

func (s *Server) handleReplaceDisk(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Pool   string `json:"pool"`
		OldDev string `json:"old_dev"`
		NewDev string `json:"new_dev"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid JSON payload")
		return
	}
	if err := s.zfs.ReplaceDisk(req.Pool, req.OldDev, req.NewDev); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.syncDiskPoolMembership()
	s.jsonResponse(w, http.StatusOK, map[string]string{"status": "disk replacement initiated", "pool": req.Pool})
}

func (s *Server) handleAttachDisk(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Pool        string `json:"pool"`
		ExistingDev string `json:"existing_dev"`
		NewDev      string `json:"new_dev"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid JSON payload")
		return
	}
	if err := s.zfs.AttachDisk(req.Pool, req.ExistingDev, req.NewDev); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.syncDiskPoolMembership()
	s.jsonResponse(w, http.StatusOK, map[string]string{"status": "disk attached to vdev", "pool": req.Pool})
}

func (s *Server) handleDetachDisk(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Pool   string `json:"pool"`
		Device string `json:"device"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid JSON payload")
		return
	}
	if err := s.zfs.DetachDisk(req.Pool, req.Device); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.syncDiskPoolMembership()
	s.jsonResponse(w, http.StatusOK, map[string]string{"status": "disk detached", "pool": req.Pool})
}

func (s *Server) handleOfflineDisk(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Pool   string `json:"pool"`
		Device string `json:"device"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid JSON payload")
		return
	}
	if err := s.zfs.OfflineDisk(req.Pool, req.Device); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.jsonResponse(w, http.StatusOK, map[string]string{"status": "disk offline", "pool": req.Pool})
}

func (s *Server) handleOnlineDisk(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Pool   string `json:"pool"`
		Device string `json:"device"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid JSON payload")
		return
	}
	if err := s.zfs.OnlineDisk(req.Pool, req.Device); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.jsonResponse(w, http.StatusOK, map[string]string{"status": "disk online", "pool": req.Pool})
}

func (s *Server) handleSetPoolProperty(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Pool     string `json:"pool"`
		Property string `json:"property"`
		Value    string `json:"value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid JSON payload")
		return
	}
	if err := s.zfs.SetPoolProperty(req.Pool, req.Property, req.Value); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.jsonResponse(w, http.StatusOK, map[string]string{"status": "pool property updated", "pool": req.Pool})
}

func (s *Server) handleCreateZvol(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name         string `json:"name"`
		SizeBytes    uint64 `json:"size_bytes"`
		Volblocksize string `json:"volblocksize"`
		Sparse       bool   `json:"sparse"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid JSON payload")
		return
	}
	if req.Name == "" || req.SizeBytes == 0 {
		s.errorResponse(w, http.StatusBadRequest, "name and size_bytes are required")
		return
	}
	if err := s.zfs.CreateZvol(req.Name, req.SizeBytes, req.Volblocksize, req.Sparse); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.jsonResponse(w, http.StatusOK, map[string]string{"status": "zvol created", "name": req.Name})
}

func (s *Server) handleCloneSnapshot(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Snapshot      string `json:"snapshot"`
		TargetDataset string `json:"target_dataset"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid JSON payload")
		return
	}
	if req.Snapshot == "" || req.TargetDataset == "" {
		s.errorResponse(w, http.StatusBadRequest, "snapshot and target_dataset are required")
		return
	}
	snapshot := strings.TrimSpace(req.Snapshot)
	if strings.Count(snapshot, "@") > 1 {
		parts := strings.Split(snapshot, "@")
		snapshot = parts[0] + "@" + parts[len(parts)-1]
	}
	if err := s.zfs.CloneSnapshot(snapshot, req.TargetDataset); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.jsonResponse(w, http.StatusOK, map[string]string{"status": "snapshot cloned", "target": req.TargetDataset})
}

func (s *Server) handleTriggerSmartTest(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path     string `json:"path"`
		Device   string `json:"device"`
		Name     string `json:"name"`
		TestType string `json:"test_type"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid JSON payload")
		return
	}
	target := strings.TrimSpace(req.Path)
	if target == "" {
		target = strings.TrimSpace(req.Device)
	}
	if target == "" {
		target = strings.TrimSpace(req.Name)
	}
	if target == "" {
		s.errorResponse(w, http.StatusBadRequest, "Disk device or path required")
		return
	}
	if err := s.disks.TriggerSmartTest(target, req.TestType); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.jsonResponse(w, http.StatusOK, map[string]string{"status": "smart test started", "device": target})
}

// ---------------- 4. Shares Handlers (SMB & NFS) ----------------

func (s *Server) handleListSmb(w http.ResponseWriter, r *http.Request) {
	list, err := s.shares.ListSmb()
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.jsonResponse(w, http.StatusOK, list)
}

func (s *Server) handleSaveSmb(w http.ResponseWriter, r *http.Request) {
	var req shares.SmbShare
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid JSON payload")
		return
	}
	req.Enabled = true
	s.ensureSharePathMounted(req.Path)
	if err := s.shares.SaveSmb(&req); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.jsonResponse(w, http.StatusOK, map[string]string{"status": "saved"})
}

func (s *Server) handleDeleteSmb(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if r.Body != nil {
		var req struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err == nil && req.Name != "" {
			name = strings.TrimSpace(req.Name)
		}
	}
	if name == "" {
		s.errorResponse(w, http.StatusBadRequest, "Share name required")
		return
	}
	if err := s.shares.DeleteSmb(name); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.jsonResponse(w, http.StatusOK, map[string]string{"status": "deleted", "name": name})
}

func (s *Server) handleListNfs(w http.ResponseWriter, r *http.Request) {
	exports, err := s.shares.ListNfs()
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.jsonResponse(w, http.StatusOK, exports)
}

func (s *Server) handleSaveNfs(w http.ResponseWriter, r *http.Request) {
	var req shares.NfsExport
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid JSON payload")
		return
	}
	req.Enabled = true
	s.ensureSharePathMounted(req.Path)
	if err := s.shares.SaveNfs(&req); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.jsonResponse(w, http.StatusOK, map[string]string{"status": "saved"})
}

func (s *Server) handleDeleteNfs(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimSpace(r.URL.Query().Get("path"))
	if r.Body != nil {
		var req struct {
			Path string `json:"path"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err == nil && req.Path != "" {
			path = strings.TrimSpace(req.Path)
		}
	}
	if path == "" {
		s.errorResponse(w, http.StatusBadRequest, "Share path required")
		return
	}
	if err := s.shares.DeleteNfs(path); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.jsonResponse(w, http.StatusOK, map[string]string{"status": "deleted", "path": path})
}

func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.shares.ListUsers()
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.jsonResponse(w, http.StatusOK, users)
}

func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Role     string `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid JSON payload")
		return
	}
	if err := s.shares.CreateUser(req.Username, req.Password, req.Role); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.jsonResponse(w, http.StatusOK, map[string]string{"status": "user created", "username": req.Username})
}

func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	username := strings.TrimSpace(r.URL.Query().Get("username"))
	if r.Body != nil {
		var req struct {
			Username string `json:"username"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err == nil && req.Username != "" {
			username = strings.TrimSpace(req.Username)
		}
	}
	if username == "" {
		s.errorResponse(w, http.StatusBadRequest, "Username required")
		return
	}
	if err := s.shares.DeleteUser(username); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.jsonResponse(w, http.StatusOK, map[string]string{"status": "user deleted", "username": username})
}

// ---------------- 5. Cloud Sync Handlers ----------------

func (s *Server) handleListCloudRemotes(w http.ResponseWriter, r *http.Request) {
	remotes, err := s.cloudsync.ListRemotes()
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.jsonResponse(w, http.StatusOK, remotes)
}

func (s *Server) handleCreateCloudRemote(w http.ResponseWriter, r *http.Request) {
	var req cloudsync.CreateRemoteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid JSON payload: "+err.Error())
		return
	}
	if err := s.cloudsync.CreateRemote(req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	s.jsonResponse(w, http.StatusOK, map[string]string{"status": "created", "name": req.Name})
}

func (s *Server) handleDeleteCloudRemote(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if name == "" && r.Body != nil {
		var req struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err == nil {
			name = strings.TrimSpace(req.Name)
		}
	}
	if name == "" {
		s.errorResponse(w, http.StatusBadRequest, "Remote name required")
		return
	}
	if err := s.cloudsync.DeleteRemote(name); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.jsonResponse(w, http.StatusOK, map[string]string{"status": "deleted", "name": name})
}

func (s *Server) handleTestCloudRemote(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if name == "" && r.Body != nil {
		var req struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err == nil {
			name = strings.TrimSpace(req.Name)
		}
	}
	if name == "" {
		s.errorResponse(w, http.StatusBadRequest, "Remote name required")
		return
	}
	if err := s.cloudsync.TestRemote(name); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Connection test failed: "+err.Error())
		return
	}
	s.jsonResponse(w, http.StatusOK, map[string]string{"status": "connected", "name": name})
}

func (s *Server) handleListCloudJobs(w http.ResponseWriter, r *http.Request) {
	jobs, err := s.cloudsync.ListJobs()
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.jsonResponse(w, http.StatusOK, jobs)
}

func (s *Server) handleSaveCloudJob(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		cloudsync.SyncJob
		Enabled *bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid JSON payload")
		return
	}
	job := payload.SyncJob
	if payload.Enabled != nil {
		job.Enabled = *payload.Enabled
	} else {
		job.Enabled = true
	}
	if err := s.cloudsync.SaveJob(&job); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.jsonResponse(w, http.StatusOK, map[string]string{"status": "saved"})
}

func (s *Server) handleDeleteCloudJob(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if r.Body != nil {
		var req struct {
			ID string `json:"id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err == nil && req.ID != "" {
			id = strings.TrimSpace(req.ID)
		}
	}
	if id == "" {
		s.errorResponse(w, http.StatusBadRequest, "Job ID required")
		return
	}
	if err := s.cloudsync.DeleteJob(id); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.jsonResponse(w, http.StatusOK, map[string]string{"status": "deleted", "id": id})
}

func (s *Server) handleTriggerCloudJob(w http.ResponseWriter, r *http.Request) {
	var req struct {
		JobID string `json:"job_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid JSON payload")
		return
	}
	if err := s.cloudsync.TriggerJob(req.JobID); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.jsonResponse(w, http.StatusOK, map[string]string{"status": "started"})
}

func (s *Server) handleListSnapshotTasks(w http.ResponseWriter, r *http.Request) {
	s.jsonResponse(w, http.StatusOK, s.zfs.ListSnapshotTasks())
}

func (s *Server) handleSaveSnapshotTask(w http.ResponseWriter, r *http.Request) {
	var task zfs.SnapshotTask
	if err := json.NewDecoder(r.Body).Decode(&task); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid JSON payload")
		return
	}
	saved, err := s.zfs.SaveSnapshotTask(task)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	s.jsonResponse(w, http.StatusOK, saved)
}

func (s *Server) handleToggleSnapshotTask(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID      string `json:"id"`
		Enabled *bool  `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid JSON payload")
		return
	}
	t, err := s.zfs.ToggleSnapshotTask(req.ID, req.Enabled)
	if err != nil {
		s.errorResponse(w, http.StatusNotFound, err.Error())
		return
	}
	s.jsonResponse(w, http.StatusOK, t)
}

func (s *Server) handleRunSnapshotTask(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid JSON payload")
		return
	}
	t, snapName, err := s.zfs.RunSnapshotTask(req.ID)
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.jsonResponse(w, http.StatusOK, map[string]interface{}{
		"status":   "completed",
		"snapshot": t.Dataset + "@" + snapName,
		"task":     t,
	})
}

func (s *Server) handleDeleteSnapshotTask(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if id == "" && r.Body != nil {
		var req struct {
			ID string `json:"id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err == nil {
			id = strings.TrimSpace(req.ID)
		}
	}
	if id == "" {
		s.errorResponse(w, http.StatusBadRequest, "Task ID required")
		return
	}
	if err := s.zfs.DeleteSnapshotTask(id); err != nil {
		s.errorResponse(w, http.StatusNotFound, err.Error())
		return
	}
	s.jsonResponse(w, http.StatusOK, map[string]string{"status": "deleted", "id": id})
}
