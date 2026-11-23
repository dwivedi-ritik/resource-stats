package collector

import (
	"context"
	"runtime"
	"sort"
	"sync"
	"time"

	"github.com/ritik/stats-lite/internal/dockerapi"
)

// containerSample is what we remember between ticks to turn cumulative
// counters into rates.
type containerSample struct {
	at          time.Time
	cpuTotal    uint64
	systemTotal uint64
	rx, tx      uint64
}

// dockerPoller reads container stats from the daemon and keeps the previous
// sample per container so CPU and network rates can be computed.
type dockerPoller struct {
	client *dockerapi.Client
	// WHY: 8 concurrent stats calls keeps a host with dozens of containers
	// responsive without hammering the daemon; stats calls are cheap but the
	// daemon serialises some cgroup reads.
	concurrency int
	mu          sync.Mutex
	previous    map[string]containerSample
}

func newDockerPoller(client *dockerapi.Client) *dockerPoller {
	return &dockerPoller{client: client, concurrency: 8, previous: map[string]containerSample{}}
}

// poll returns the current Docker view. It never returns an error: failures
// become DockerStats.Error so the UI can show them.
func (p *dockerPoller) poll(ctx context.Context) DockerStats {
	if p == nil || p.client == nil {
		return DockerStats{Available: false, Error: "Docker integration disabled", Containers: []ContainerStats{}}
	}
	containers, err := p.client.ListContainers(ctx)
	if err != nil {
		return DockerStats{Available: false, Error: humanDockerError(err), Containers: []ContainerStats{}}
	}
	results := make([]ContainerStats, len(containers))
	sem := make(chan struct{}, p.concurrency)
	var wg sync.WaitGroup
	now := time.Now()
	for i, c := range containers {
		results[i] = ContainerStats{ID: c.ID, Name: c.Name(), Image: c.Image, State: c.State, Status: c.Status}
		if c.State != "running" {
			continue
		}
		wg.Add(1)
		go func(i int, id string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			// WHY: one-shot stats normally answer in milliseconds; 3s bounds a
			// wedged container so the whole snapshot is never held hostage.
			sctx, cancel := context.WithTimeout(ctx, 3*time.Second)
			defer cancel()
			s, err := p.client.ContainerStats(sctx, id)
			if err != nil {
				return
			}
			p.apply(&results[i], s, now)
		}(i, c.ID)
	}
	wg.Wait()
	p.forget(containers)
	sort.SliceStable(results, func(a, b int) bool {
		ra, rb := results[a].State == "running", results[b].State == "running"
		if ra != rb {
			return ra
		}
		if results[a].CPUPercent != results[b].CPUPercent {
			return results[a].CPUPercent > results[b].CPUPercent
		}
		return results[a].Name < results[b].Name
	})
	return DockerStats{Available: true, Containers: results}
}

// apply fills a ContainerStats from a raw sample and the remembered previous one.
func (p *dockerPoller) apply(out *ContainerStats, s *dockerapi.Stats, now time.Time) {
	var rx, tx uint64
	for _, n := range s.Networks {
		rx += n.RxBytes
		tx += n.TxBytes
	}
	cur := containerSample{at: now, cpuTotal: s.CPUStats.CPUUsage.TotalUsage, systemTotal: s.CPUStats.SystemCPUUsage, rx: rx, tx: tx}

	p.mu.Lock()
	prev, ok := p.previous[out.ID]
	p.previous[out.ID] = cur
	p.mu.Unlock()

	if ok {
		out.CPUPercent = containerCPUPercent(prev, cur, onlineCPUs(s))
		elapsed := cur.at.Sub(prev.at)
		out.RxBytesPerSec = ratePerSecond(prev.rx, cur.rx, elapsed)
		out.TxBytesPerSec = ratePerSecond(prev.tx, cur.tx, elapsed)
	}
	out.RxTotal, out.TxTotal = rx, tx
	out.MemoryUsage = containerMemoryUsage(s)
	out.MemoryLimit = s.MemoryStats.Limit
	out.MemoryPercent = percent(out.MemoryUsage, out.MemoryLimit)
	out.BlockRead, out.BlockWrite = blockIO(s)
}

// forget drops remembered samples for containers that no longer exist.
func (p *dockerPoller) forget(live []dockerapi.Container) {
	alive := make(map[string]bool, len(live))
	for _, c := range live {
		alive[c.ID] = true
	}
	p.mu.Lock()
	for id := range p.previous {
		if !alive[id] {
			delete(p.previous, id)
		}
	}
	p.mu.Unlock()
}

func onlineCPUs(s *dockerapi.Stats) int {
	if n := int(s.CPUStats.OnlineCPUs); n > 0 {
		return n
	}
	if n := len(s.CPUStats.CPUUsage.PercpuUsage); n > 0 {
		return n
	}
	return runtime.NumCPU()
}

// containerCPUPercent mirrors `docker stats`: the container's share of the
// host's CPU time between two samples, scaled by core count so a container
// saturating two cores reads 200%. On Windows (no system_cpu_usage) it falls
// back to wall-clock time, where cpu_total is in 100ns ticks.
func containerCPUPercent(prev, cur containerSample, cpus int) float64 {
	if cur.cpuTotal < prev.cpuTotal || cpus <= 0 {
		return 0
	}
	cpuDelta := float64(cur.cpuTotal - prev.cpuTotal)
	if cur.systemTotal > prev.systemTotal {
		systemDelta := float64(cur.systemTotal - prev.systemTotal)
		return cpuDelta / systemDelta * float64(cpus) * 100
	}
	wall := cur.at.Sub(prev.at)
	if wall <= 0 {
		return 0
	}
	// WHY: Windows reports CPU time in 100-nanosecond units; divide the
	// container's ticks by the ticks available across all cores in the window.
	available := float64(wall.Nanoseconds()/100) * float64(cpus)
	return cpuDelta / available * 100
}

// containerMemoryUsage mirrors `docker stats`: page cache is not "used", so it
// is subtracted (cgroup v2 reports inactive_file; v1 reports total_inactive_file
// or cache). Windows reports a private working set instead.
func containerMemoryUsage(s *dockerapi.Stats) uint64 {
	usage := s.MemoryStats.Usage
	if usage == 0 && s.MemoryStats.PrivateWorkingSet > 0 {
		return s.MemoryStats.PrivateWorkingSet
	}
	for _, key := range []string{"inactive_file", "total_inactive_file", "cache"} {
		if v, ok := s.MemoryStats.Stats[key]; ok {
			if v < usage {
				return usage - v
			}
			return 0
		}
	}
	return usage
}

// blockIO sums bytes read and written across all block devices.
func blockIO(s *dockerapi.Stats) (read, write uint64) {
	for _, e := range s.BlkioStats.IOServiceBytesRecursive {
		switch e.Op {
		case "read", "Read":
			read += e.Value
		case "write", "Write":
			write += e.Value
		}
	}
	if read == 0 && write == 0 {
		read, write = s.StorageStats.ReadSizeBytes, s.StorageStats.WriteSizeBytes
	}
	return read, write
}
