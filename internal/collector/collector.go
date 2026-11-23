package collector

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
	"github.com/shirou/gopsutil/v4/net"

	"github.com/dwivedi-ritik/resource-stats/internal/dockerapi"
)

// Options configure a Collector.
type Options struct {
	// Interval between samples. One second matches what people expect from a
	// live monitor; the cost per sample is a few milliseconds.
	Interval time.Duration
	// Docker is nil when the integration is disabled.
	Docker *dockerapi.Client
	// HostRoot is where the host filesystem is mounted when running inside a
	// container ("" when running directly on the host).
	HostRoot string
	Logger   *slog.Logger
}

// Collector samples the machine on a timer and fans snapshots out to
// subscribers. Subscribers that fall behind skip samples rather than block
// the loop.
type Collector struct {
	opts    Options
	docker  *dockerPoller
	battery *batteryReader

	mu          sync.RWMutex
	latest      Snapshot
	hasLatest   bool
	subscribers map[chan Snapshot]struct{}

	// previous counters for rate computation
	prevAt   time.Time
	prevNet  map[string]net.IOCountersStat
	prevDisk map[string]disk.IOCountersStat

	staticHost HostInfo
}

// New prepares a collector; call Run to start sampling.
func New(opts Options) *Collector {
	if opts.Interval <= 0 {
		opts.Interval = time.Second
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	c := &Collector{opts: opts, subscribers: map[chan Snapshot]struct{}{}, battery: newBatteryReader()}
	if opts.Docker != nil {
		c.docker = newDockerPoller(opts.Docker)
	}
	return c
}

// Run samples until ctx is cancelled. The first sample is taken immediately
// so the UI has data as soon as it loads.
func (c *Collector) Run(ctx context.Context) {
	c.staticHost = c.readStaticHost()
	// WHY: cpu.Percent reports usage since its previous call, so prime it once
	// before the loop or the first reading is meaningless.
	_, _ = cpu.Percent(0, false)
	_, _ = cpu.Percent(0, true)
	c.sample(ctx)

	ticker := time.NewTicker(c.opts.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.sample(ctx)
		}
	}
}

// Latest returns the most recent snapshot and whether one exists yet.
func (c *Collector) Latest() (Snapshot, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.latest, c.hasLatest
}

// Subscribe returns a channel that receives every new snapshot. Call the
// returned function to unsubscribe.
func (c *Collector) Subscribe() (<-chan Snapshot, func()) {
	// WHY: buffer of 1 lets a slow reader miss intermediate samples instead of
	// stalling every other subscriber.
	ch := make(chan Snapshot, 1)
	c.mu.Lock()
	c.subscribers[ch] = struct{}{}
	c.mu.Unlock()
	return ch, func() {
		c.mu.Lock()
		delete(c.subscribers, ch)
		c.mu.Unlock()
	}
}

func (c *Collector) publish(s Snapshot) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.latest, c.hasLatest = s, true
	for ch := range c.subscribers {
		select {
		case ch <- s:
		default:
			// drop the stale one, deliver the fresh one
			select {
			case <-ch:
			default:
			}
			select {
			case ch <- s:
			default:
			}
		}
	}
}

func (c *Collector) sample(ctx context.Context) {
	now := time.Now()
	snap := Snapshot{Timestamp: now, Host: c.staticHost}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		snap.Docker = c.docker.poll(ctx)
	}()

	snap.Host.UptimeSeconds, snap.Host.Processes = readUptimeAndProcs(ctx)
	snap.Host.Load1, snap.Host.Load5, snap.Host.Load15 = readLoad(ctx)
	snap.CPU = c.readCPU(ctx)
	snap.Memory = readMemory(ctx)
	elapsed := now.Sub(c.prevAt)
	snap.Network = c.readNetwork(ctx, elapsed)
	snap.DiskIO = c.readDiskIO(ctx, elapsed)
	snap.Disks = c.readDisks(ctx)
	snap.Battery = c.battery.read(now)
	c.prevAt = now

	wg.Wait()
	c.publish(snap)
}

func (c *Collector) readStaticHost() HostInfo {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	h := HostInfo{Arch: runtime.GOARCH, LogicalCores: runtime.NumCPU()}
	if info, err := host.InfoWithContext(ctx); err == nil {
		h.Hostname = info.Hostname
		h.OS = info.OS
		h.Platform = info.Platform
		h.PlatformVersion = info.PlatformVersion
		if info.KernelArch != "" {
			h.Arch = info.KernelArch
		}
	} else {
		c.opts.Logger.Warn("host info unavailable", "error", err)
	}
	if n, err := cpu.CountsWithContext(ctx, true); err == nil && n > 0 {
		h.LogicalCores = n
	}
	if n, err := cpu.CountsWithContext(ctx, false); err == nil && n > 0 {
		h.PhysicalCores = n
	}
	if infos, err := cpu.InfoWithContext(ctx); err == nil && len(infos) > 0 {
		h.CPUModel = strings.TrimSpace(infos[0].ModelName)
	}
	return h
}

func readUptimeAndProcs(ctx context.Context) (uint64, uint64) {
	var procs uint64
	up, _ := host.UptimeWithContext(ctx)
	if info, err := host.InfoWithContext(ctx); err == nil {
		procs = info.Procs
	}
	return up, procs
}

func readLoad(ctx context.Context) (float64, float64, float64) {
	avg, err := load.AvgWithContext(ctx)
	if err != nil || avg == nil {
		return 0, 0, 0
	}
	return avg.Load1, avg.Load5, avg.Load15
}

func (c *Collector) readCPU(ctx context.Context) CPUStats {
	out := CPUStats{PerCore: []float64{}}
	if total, err := cpu.PercentWithContext(ctx, 0, false); err == nil && len(total) > 0 {
		out.TotalPercent = total[0]
	}
	if cores, err := cpu.PercentWithContext(ctx, 0, true); err == nil {
		out.PerCore = cores
	}
	return out
}

func readMemory(ctx context.Context) MemoryStats {
	var out MemoryStats
	if vm, err := mem.VirtualMemoryWithContext(ctx); err == nil {
		out.Total, out.Used, out.Available, out.UsedPercent = vm.Total, vm.Used, vm.Available, vm.UsedPercent
	}
	if sw, err := mem.SwapMemoryWithContext(ctx); err == nil {
		out.SwapTotal, out.SwapUsed, out.SwapPercent = sw.Total, sw.Used, sw.UsedPercent
	}
	return out
}

// hostNetDevPath returns the host's interface counters file when we are in a
// container that shares the host PID namespace (pid: host) and HOST_PROC
// points at the host's /proc. /proc/net is per network namespace, so the
// container's own view is useless; PID 1 is the host's init and its
// /proc/1/net/dev shows the real interfaces. This is what lets the compose
// file use ordinary port mapping instead of host networking.
func hostNetDevPath() string {
	if runtime.GOOS != "linux" {
		return ""
	}
	hostProc := os.Getenv("HOST_PROC")
	if hostProc == "" {
		return ""
	}
	path := filepath.Join(hostProc, "1", "net", "dev")
	if _, err := os.Stat(path); err != nil {
		return ""
	}
	return path
}

func readNetworkCounters(ctx context.Context) ([]net.IOCountersStat, error) {
	if path := hostNetDevPath(); path != "" {
		if counters, err := net.IOCountersByFileWithContext(ctx, true, path); err == nil {
			return counters, nil
		}
	}
	return net.IOCountersWithContext(ctx, true)
}

func isLoopback(name string) bool {
	return name == "lo" || strings.HasPrefix(name, "lo0") || strings.HasPrefix(name, "Loopback")
}

func (c *Collector) readNetwork(ctx context.Context, elapsed time.Duration) NetworkStats {
	out := NetworkStats{Interfaces: []NetworkInterface{}}
	counters, err := readNetworkCounters(ctx)
	if err != nil {
		return out
	}
	cur := make(map[string]net.IOCountersStat, len(counters))
	for _, nic := range counters {
		cur[nic.Name] = nic
		if isLoopback(nic.Name) {
			continue
		}
		iface := NetworkInterface{Name: nic.Name, RxTotal: nic.BytesRecv, TxTotal: nic.BytesSent}
		if prev, ok := c.prevNet[nic.Name]; ok {
			iface.RxBytesPerSec = ratePerSecond(prev.BytesRecv, nic.BytesRecv, elapsed)
			iface.TxBytesPerSec = ratePerSecond(prev.BytesSent, nic.BytesSent, elapsed)
		}
		out.RxBytesPerSec += iface.RxBytesPerSec
		out.TxBytesPerSec += iface.TxBytesPerSec
		out.RxTotal += iface.RxTotal
		out.TxTotal += iface.TxTotal
		if interfaceWorthShowing(iface) {
			out.Interfaces = append(out.Interfaces, iface)
		}
	}
	c.prevNet = cur
	sort.SliceStable(out.Interfaces, func(i, j int) bool {
		a, b := out.Interfaces[i], out.Interfaces[j]
		if ra, rb := a.RxBytesPerSec+a.TxBytesPerSec, b.RxBytesPerSec+b.TxBytesPerSec; ra != rb {
			return ra > rb
		}
		return a.RxTotal+a.TxTotal > b.RxTotal+b.TxTotal
	})
	return out
}

// interfaceWorthShowing hides interfaces that are effectively idle: dormant
// VPN tunnels and discovery links that have only ever exchanged a few
// kilobytes. The aggregate totals still include them. A megabyte is the
// threshold because a day of background chatter stays well under it.
func interfaceWorthShowing(i NetworkInterface) bool {
	const megabyte = 1 << 20
	return i.RxBytesPerSec > 0 || i.TxBytesPerSec > 0 || i.RxTotal+i.TxTotal >= megabyte
}

func (c *Collector) readDiskIO(ctx context.Context, elapsed time.Duration) DiskIOStats {
	counters, err := disk.IOCountersWithContext(ctx)
	if err != nil || len(counters) == 0 {
		return DiskIOStats{Available: false}
	}
	out := DiskIOStats{Available: true}
	for name, io := range counters {
		if prev, ok := c.prevDisk[name]; ok {
			out.ReadBytesPerSec += ratePerSecond(prev.ReadBytes, io.ReadBytes, elapsed)
			out.WriteBytesPerSec += ratePerSecond(prev.WriteBytes, io.WriteBytes, elapsed)
		}
	}
	c.prevDisk = counters
	return out
}

func (c *Collector) readDisks(ctx context.Context) []DiskUsage {
	out := []DiskUsage{}
	parts, err := disk.PartitionsWithContext(ctx, false)
	if err != nil {
		return out
	}
	for _, p := range selectPartitions(parts) {
		u, err := disk.UsageWithContext(ctx, usagePath(c.opts.HostRoot, p.Mountpoint))
		if err != nil || u.Total == 0 {
			continue
		}
		out = append(out, DiskUsage{
			Mountpoint: p.Mountpoint, Device: p.Device, Fstype: p.Fstype,
			Total: u.Total, Used: u.Used, Free: u.Free, UsedPercent: u.UsedPercent,
		})
	}
	return dedupeSharedVolumes(out)
}

// dedupeSharedVolumes collapses filesystems that are really one pool of space.
// macOS mounts the sealed system volume at / and the writable Data volume
// under /System/Volumes/Data, both reporting the same APFS container; showing
// both would double-count the disk. Two entries with identical total and free
// bytes are treated as one and the shortest mountpoint is kept.
func dedupeSharedVolumes(in []DiskUsage) []DiskUsage {
	type key struct{ total, free uint64 }
	best := map[key]int{}
	out := make([]DiskUsage, 0, len(in))
	for _, d := range in {
		k := key{d.Total, d.Free}
		if idx, ok := best[k]; ok {
			if len(d.Mountpoint) < len(out[idx].Mountpoint) {
				out[idx] = d
			}
			continue
		}
		best[k] = len(out)
		out = append(out, d)
	}
	return out
}
