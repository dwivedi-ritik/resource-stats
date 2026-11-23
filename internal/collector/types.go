// Package collector samples system and Docker resource usage on a fixed
// interval and publishes immutable snapshots to subscribers.
package collector

import "time"

// Snapshot is one complete reading of the machine. It is what the HTTP API
// serves and what the browser renders.
type Snapshot struct {
	Timestamp time.Time    `json:"timestamp"`
	Host      HostInfo     `json:"host"`
	CPU       CPUStats     `json:"cpu"`
	Memory    MemoryStats  `json:"memory"`
	Network   NetworkStats `json:"network"`
	Disks     []DiskUsage  `json:"disks"`
	DiskIO    DiskIOStats  `json:"disk_io"`
	Battery   BatteryStats `json:"battery"`
	Docker    DockerStats  `json:"docker"`
}

// HostInfo describes the machine. Load averages are zero on Windows.
type HostInfo struct {
	Hostname        string  `json:"hostname"`
	OS              string  `json:"os"`
	Platform        string  `json:"platform"`
	PlatformVersion string  `json:"platform_version"`
	Arch            string  `json:"arch"`
	UptimeSeconds   uint64  `json:"uptime_seconds"`
	CPUModel        string  `json:"cpu_model"`
	LogicalCores    int     `json:"logical_cores"`
	PhysicalCores   int     `json:"physical_cores"`
	Processes       uint64  `json:"processes"`
	Load1           float64 `json:"load1"`
	Load5           float64 `json:"load5"`
	Load15          float64 `json:"load15"`
}

// CPUStats is utilisation since the previous sample.
type CPUStats struct {
	TotalPercent float64   `json:"total_percent"`
	PerCore      []float64 `json:"per_core"`
}

// MemoryStats is physical memory plus swap, in bytes.
type MemoryStats struct {
	Total       uint64  `json:"total"`
	Used        uint64  `json:"used"`
	Available   uint64  `json:"available"`
	UsedPercent float64 `json:"used_percent"`
	SwapTotal   uint64  `json:"swap_total"`
	SwapUsed    uint64  `json:"swap_used"`
	SwapPercent float64 `json:"swap_percent"`
}

// NetworkStats aggregates every non-loopback interface. Rates are bytes per
// second computed between consecutive samples.
type NetworkStats struct {
	RxBytesPerSec float64            `json:"rx_bytes_per_sec"`
	TxBytesPerSec float64            `json:"tx_bytes_per_sec"`
	RxTotal       uint64             `json:"rx_total"`
	TxTotal       uint64             `json:"tx_total"`
	Interfaces    []NetworkInterface `json:"interfaces"`
}

// NetworkInterface is one interface's current throughput.
type NetworkInterface struct {
	Name          string  `json:"name"`
	RxBytesPerSec float64 `json:"rx_bytes_per_sec"`
	TxBytesPerSec float64 `json:"tx_bytes_per_sec"`
	RxTotal       uint64  `json:"rx_total"`
	TxTotal       uint64  `json:"tx_total"`
}

// DiskUsage is the capacity of one mounted filesystem, in bytes.
type DiskUsage struct {
	Mountpoint  string  `json:"mountpoint"`
	Device      string  `json:"device"`
	Fstype      string  `json:"fstype"`
	Total       uint64  `json:"total"`
	Used        uint64  `json:"used"`
	Free        uint64  `json:"free"`
	UsedPercent float64 `json:"used_percent"`
}

// DiskIOStats is aggregate read/write throughput across physical disks.
// Available reports whether the platform exposed counters at all.
type DiskIOStats struct {
	Available        bool    `json:"available"`
	ReadBytesPerSec  float64 `json:"read_bytes_per_sec"`
	WriteBytesPerSec float64 `json:"write_bytes_per_sec"`
}

// DockerStats lists containers. When the daemon cannot be reached, Available
// is false and Error carries a short human-readable reason — the UI must be
// able to tell "no containers" from "could not ask".
type DockerStats struct {
	Available  bool             `json:"available"`
	Error      string           `json:"error,omitempty"`
	Containers []ContainerStats `json:"containers"`
}

// ContainerStats is one container's live usage.
type ContainerStats struct {
	ID            string  `json:"id"`
	Name          string  `json:"name"`
	Image         string  `json:"image"`
	State         string  `json:"state"`
	Status        string  `json:"status"`
	CPUPercent    float64 `json:"cpu_percent"`
	MemoryUsage   uint64  `json:"memory_usage"`
	MemoryLimit   uint64  `json:"memory_limit"`
	MemoryPercent float64 `json:"memory_percent"`
	RxBytesPerSec float64 `json:"rx_bytes_per_sec"`
	TxBytesPerSec float64 `json:"tx_bytes_per_sec"`
	RxTotal       uint64  `json:"rx_total"`
	TxTotal       uint64  `json:"tx_total"`
	BlockRead     uint64  `json:"block_read"`
	BlockWrite    uint64  `json:"block_write"`
}
