//go:build !windows

package disks

func (m *Manager) scanWindowsDisks() ([]*DiskInfo, error) {
	return nil, nil
}
