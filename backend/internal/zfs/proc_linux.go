//go:build linux

package zfs

import (
	"syscall"
	"time"
)

// terminateMountHolders cleanly signals processes holding open file descriptors or working directories
// under mountpoint before ZFS unmounts, avoiding lazy umount (MNT_DETACH) mount-namespace desync.
func terminateMountHolders(mountpoint string) {
	pids := findMountHoldingPIDs(mountpoint)
	if len(pids) == 0 {
		return
	}
	for _, pid := range pids {
		_ = syscall.Kill(pid, syscall.SIGTERM)
	}
	time.Sleep(150 * time.Millisecond)

	remaining := findMountHoldingPIDs(mountpoint)
	for _, pid := range remaining {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
	if len(remaining) > 0 {
		time.Sleep(100 * time.Millisecond)
	}
}
