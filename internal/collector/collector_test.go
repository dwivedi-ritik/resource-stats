package collector

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/shirou/gopsutil/v4/disk"

	"github.com/ritik/stats-lite/internal/dockerapi"
)

func TestRatePerSecond(t *testing.T) {
	if got := ratePerSecond(1000, 3000, 2*time.Second); got != 1000 {
		t.Fatalf("rate = %v, want 1000", got)
	}
	if got := ratePerSecond(3000, 1000, time.Second); got != 0 {
		t.Fatalf("counter reset must read 0, got %v", got)
	}
	if got := ratePerSecond(0, 500, 0); got != 0 {
		t.Fatalf("zero elapsed must read 0, got %v", got)
	}
}

func TestPercent(t *testing.T) {
	if got := percent(50, 200); got != 25 {
		t.Fatalf("percent = %v, want 25", got)
	}
	if got := percent(5, 0); got != 0 {
		t.Fatalf("zero total must read 0, got %v", got)
	}
	if got := percent(300, 200); got != 100 {
		t.Fatalf("over 100 must clamp, got %v", got)
	}
}

func TestSelectPartitionsFiltersNoiseAndDuplicates(t *testing.T) {
	parts := []disk.PartitionStat{
		{Device: "/dev/sda1", Mountpoint: "/", Fstype: "ext4"},
		{Device: "tmpfs", Mountpoint: "/run", Fstype: "tmpfs"},
		{Device: "/dev/loop3", Mountpoint: "/snap/core/1", Fstype: "squashfs"},
		{Device: "/dev/sda1", Mountpoint: "/var/lib/docker/bind", Fstype: "ext4"},
		{Device: "/dev/disk3s5", Mountpoint: "/System/Volumes/Data", Fstype: "apfs"},
		{Device: "/dev/disk3s6", Mountpoint: "/System/Volumes/VM", Fstype: "apfs"},
		{Device: "//nas/media", Mountpoint: "/mnt/media", Fstype: "cifs"},
		{Device: "overlay", Mountpoint: "/var/lib/docker/overlay2/x/merged", Fstype: "overlay"},
	}
	got := selectPartitions(parts)
	want := []string{"/", "/System/Volumes/Data", "/mnt/media"}
	if len(got) != len(want) {
		t.Fatalf("kept %d partitions, want %d: %+v", len(got), len(want), got)
	}
	for i, p := range got {
		if p.Mountpoint != want[i] {
			t.Errorf("partition %d = %s, want %s", i, p.Mountpoint, want[i])
		}
	}
}

func TestUsagePath(t *testing.T) {
	if got := usagePath("", "/data"); got != "/data" {
		t.Fatalf("no host root: %s", got)
	}
	if got := usagePath("/host", "/"); got != "/host" {
		t.Fatalf("root under host root: %s", got)
	}
	if got := usagePath("/host", "/mnt/media"); got != "/host/mnt/media" {
		t.Fatalf("nested under host root: %s", got)
	}
}

func TestContainerCPUPercentLinux(t *testing.T) {
	// Container used 1s of CPU while the 4-core host accumulated 2s of system
	// time across all cores -> 50% of total = 200% in docker-stats terms.
	prev := containerSample{cpuTotal: 10_000_000_000, systemTotal: 100_000_000_000}
	cur := containerSample{cpuTotal: 11_000_000_000, systemTotal: 102_000_000_000}
	if got := containerCPUPercent(prev, cur, 4); got != 200 {
		t.Fatalf("cpu%% = %v, want 200", got)
	}
}

func TestContainerCPUPercentWindowsFallback(t *testing.T) {
	// No system_cpu_usage: 1s wall clock, 2 cores, container used 0.5s
	// (5,000,000 ticks of 100ns) -> 25%.
	now := time.Now()
	prev := containerSample{at: now.Add(-time.Second), cpuTotal: 0}
	cur := containerSample{at: now, cpuTotal: 5_000_000}
	got := containerCPUPercent(prev, cur, 2)
	if got < 24.9 || got > 25.1 {
		t.Fatalf("cpu%% = %v, want ~25", got)
	}
}

func TestContainerCPUPercentCounterReset(t *testing.T) {
	prev := containerSample{cpuTotal: 500, systemTotal: 1000}
	cur := containerSample{cpuTotal: 100, systemTotal: 2000}
	if got := containerCPUPercent(prev, cur, 1); got != 0 {
		t.Fatalf("restarted container must read 0, got %v", got)
	}
}

func TestContainerMemoryUsageSubtractsCache(t *testing.T) {
	var s dockerapi.Stats
	s.MemoryStats.Usage = 1000
	s.MemoryStats.Stats = map[string]uint64{"inactive_file": 300}
	if got := containerMemoryUsage(&s); got != 700 {
		t.Fatalf("cgroup v2 usage = %d, want 700", got)
	}
	s.MemoryStats.Stats = map[string]uint64{"cache": 200}
	if got := containerMemoryUsage(&s); got != 800 {
		t.Fatalf("cgroup v1 usage = %d, want 800", got)
	}
	s.MemoryStats.Stats = nil
	s.MemoryStats.Usage = 0
	s.MemoryStats.PrivateWorkingSet = 42
	if got := containerMemoryUsage(&s); got != 42 {
		t.Fatalf("windows usage = %d, want 42", got)
	}
}

func TestBlockIO(t *testing.T) {
	var s dockerapi.Stats
	s.BlkioStats.IOServiceBytesRecursive = []struct {
		Op    string `json:"op"`
		Value uint64 `json:"value"`
	}{{Op: "Read", Value: 10}, {Op: "Write", Value: 20}, {Op: "read", Value: 5}, {Op: "Total", Value: 35}}
	r, w := blockIO(&s)
	if r != 15 || w != 20 {
		t.Fatalf("blkio = %d/%d, want 15/20", r, w)
	}
	var win dockerapi.Stats
	win.StorageStats.ReadSizeBytes, win.StorageStats.WriteSizeBytes = 7, 9
	if r, w := blockIO(&win); r != 7 || w != 9 {
		t.Fatalf("windows storage = %d/%d, want 7/9", r, w)
	}
}

func TestSubscribeReceivesLatestAndDropsStale(t *testing.T) {
	c := New(Options{})
	ch, cancel := c.Subscribe()
	defer cancel()
	c.publish(Snapshot{Host: HostInfo{Hostname: "one"}})
	c.publish(Snapshot{Host: HostInfo{Hostname: "two"}})
	select {
	case s := <-ch:
		if s.Host.Hostname != "two" {
			t.Fatalf("subscriber got %q, want the freshest snapshot", s.Host.Hostname)
		}
	case <-time.After(time.Second):
		t.Fatal("no snapshot delivered")
	}
	if latest, ok := c.Latest(); !ok || latest.Host.Hostname != "two" {
		t.Fatalf("Latest = %+v %v", latest, ok)
	}
}

func TestDedupeSharedVolumes(t *testing.T) {
	in := []DiskUsage{
		{Mountpoint: "/System/Volumes/Data", Total: 1000, Free: 400},
		{Mountpoint: "/", Total: 1000, Free: 400},
		{Mountpoint: "/mnt/media", Total: 5000, Free: 100},
	}
	got := dedupeSharedVolumes(in)
	if len(got) != 2 || got[0].Mountpoint != "/" || got[1].Mountpoint != "/mnt/media" {
		t.Fatalf("dedupe = %+v", got)
	}
}

func TestInterfaceWorthShowing(t *testing.T) {
	if interfaceWorthShowing(NetworkInterface{Name: "utun0", RxTotal: 0, TxTotal: 7778}) {
		t.Fatal("an idle tunnel with a few KB of history should be hidden")
	}
	if !interfaceWorthShowing(NetworkInterface{Name: "en0", RxTotal: 5 << 30}) {
		t.Fatal("a busy interface must show")
	}
	if !interfaceWorthShowing(NetworkInterface{Name: "wg0", TxBytesPerSec: 12}) {
		t.Fatal("anything moving traffic right now must show")
	}
}

func TestHostNetDevPathUsesHostPIDOne(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("host /proc redirection only applies on Linux")
	}
	dir := t.TempDir()
	t.Setenv("HOST_PROC", dir)
	if got := hostNetDevPath(); got != "" {
		t.Fatalf("missing file should disable redirection, got %q", got)
	}
	want := filepath.Join(dir, "1", "net", "dev")
	if err := os.MkdirAll(filepath.Dir(want), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(want, []byte("Inter-|   Receive\n face |bytes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := hostNetDevPath(); got != want {
		t.Fatalf("hostNetDevPath = %q, want %q", got, want)
	}
}

func TestHostNetDevPathEmptyWithoutHostProc(t *testing.T) {
	t.Setenv("HOST_PROC", "")
	if got := hostNetDevPath(); got != "" {
		t.Fatalf("expected no redirection, got %q", got)
	}
}
