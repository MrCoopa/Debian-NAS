package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"debian-nas/internal/api"
	"debian-nas/internal/cloudsync"
	"debian-nas/internal/config"
	"debian-nas/internal/disks"
	"debian-nas/internal/shares"
	"debian-nas/internal/system"
	"debian-nas/internal/web"
	"debian-nas/internal/zfs"
)

func main() {
	port := flag.Int("port", 8080, "Web UI and API port")
	host := flag.String("host", "0.0.0.0", "Bind address")
	configPath := flag.String("config", "/etc/nas/config.json", "Path to config file")
	mock := flag.Bool("mock", false, "Force mock mode (simulated ZFS and Disks)")
	flag.Parse()

	log.Println("=====================================================")
	log.Println("  Debian NAS Daemon (Debian 13 Trixie + OpenZFS)     ")
	log.Println("=====================================================")

	cfg, err := config.LoadConfig(*configPath)
	if err != nil {
		log.Printf("[WARN] Could not load config at %s, using defaults: %v", *configPath, err)
		cfg = config.DefaultConfig()
	}

	if *port != 8080 {
		cfg.Port = *port
	}
	if *host != "0.0.0.0" {
		cfg.Host = *host
	}
	if *mock {
		cfg.MockMode = true
	}

	// Sub-fs for static files
	staticSub, err := web.GetFS()
	if err != nil {
		log.Fatalf("Embedding static assets failed: %v", err)
	}

	// Initialize core collectors & managers
	sysCollector := system.NewCollector(cfg.MockMode)
	diskMgr := disks.NewManager(cfg.MockMode, cfg.HdIdleConfig)
	zfsMgr := zfs.NewManager(cfg.MockMode)
	shareMgr := shares.NewManager(cfg.MockMode, cfg.SambaConfDir, cfg.NfsConfFile)
	cloudMgr := cloudsync.NewManager(cfg.MockMode, cfg.RcloneConfig)

	// API & Web Server
	srv := api.NewServer(cfg, sysCollector, diskMgr, zfsMgr, shareMgr, cloudMgr, staticSub)

	addr := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
	httpServer := &http.Server{
		Addr:         addr,
		Handler:      srv.Routes(),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	go func() {
		log.Printf("[INFO] Web UI & API active at: http://%s (or http://localhost:%d)", addr, cfg.Port)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("[ERROR] HTTP Server crashed: %v", err)
		}
	}()

	// Graceful shutdown
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop

	log.Println("\n[INFO] Shutting down Debian NAS daemon...")
}
