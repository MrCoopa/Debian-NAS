//go:build !linux

package zfs

func terminateMountHolders(mountpoint string) {
	// Only supported on Linux via /proc/<pid> inspection.
}
