//go:build !windows

package system

func (c *Collector) readWindowsUptime() (int64, string) {
	return c.readUptime()
}

func (c *Collector) readWindowsMemory() MemoryInfo {
	return c.readMemInfo()
}

func (c *Collector) readWindowsCPUUsage() float64 {
	currCPU := c.readCPUStatSnapshot()
	usage := c.calcCPUUsage(c.lastCPUStat, currCPU)
	c.lastCPUStat = currCPU
	return usage
}

func (c *Collector) readWindowsCPUModel() string {
	model, _, _ := c.readCPUInfo()
	return model
}

func (c *Collector) readWindowsDiskIO() (float64, float64, int, int, map[string]*DeviceIOInfo) {
	return 0, 0, 0, 0, make(map[string]*DeviceIOInfo)
}
