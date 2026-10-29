package web

import (
	"embed"
	"io/fs"
)

//go:embed static/*
var staticFiles embed.FS

// GetFS returns the fs.FS pointing to the static assets
func GetFS() (fs.FS, error) {
	return fs.Sub(staticFiles, "static")
}
