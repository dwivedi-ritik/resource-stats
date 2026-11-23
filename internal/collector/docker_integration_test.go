//go:build !windows

package collector

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ritik/stats-lite/internal/dockerapi"
)

// fakeDaemon serves a two-container Docker API over a unix socket so the whole
// poll path (list -> per-container stats -> rates) runs without Docker.
func fakeDaemon(t *testing.T) *dockerapi.Client {
	t.Helper()
	// WHY: macOS limits unix socket paths to 104 bytes and t.TempDir() names
	// are long, so make a short directory of our own.
	dir, err := os.MkdirTemp("", "sl")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "d.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	calls := map[string]int{}
	mux := http.NewServeMux()
	mux.HandleFunc("/containers/json", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"Id": "aaa111", "Names": []string{"/web"}, "Image": "nginx:1.27", "State": "running", "Status": "Up 3 hours"},
			{"Id": "bbb222", "Names": []string{"/old"}, "Image": "alpine", "State": "exited", "Status": "Exited (0) 2 days ago"},
		})
	})
	mux.HandleFunc("/containers/aaa111/stats", func(w http.ResponseWriter, r *http.Request) {
		calls["aaa111"]++
		n := uint64(calls["aaa111"])
		_ = json.NewEncoder(w).Encode(map[string]any{
			"cpu_stats": map[string]any{
				"cpu_usage":        map[string]any{"total_usage": n * 1_000_000_000},
				"system_cpu_usage": n * 8_000_000_000,
				"online_cpus":      4,
			},
			"memory_stats": map[string]any{"usage": 600_000_000, "limit": 2_000_000_000, "stats": map[string]any{"inactive_file": 100_000_000}},
			"networks":     map[string]any{"eth0": map[string]any{"rx_bytes": n * 2048, "tx_bytes": n * 1024}},
			"blkio_stats":  map[string]any{"io_service_bytes_recursive": []map[string]any{{"op": "read", "value": 11}, {"op": "write", "value": 22}}},
		})
	})
	srv := httptest.NewUnstartedServer(mux)
	srv.Listener = ln
	srv.Start()
	t.Cleanup(srv.Close)
	client, err := dockerapi.New("unix://" + sock)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestDockerPollerComputesRates(t *testing.T) {
	p := newDockerPoller(fakeDaemon(t))
	ctx := context.Background()

	first := p.poll(ctx)
	if !first.Available || len(first.Containers) != 2 {
		t.Fatalf("first poll: %+v", first)
	}
	// Backdate the stored sample so the second poll sees one second elapsed.
	p.mu.Lock()
	s := p.previous["aaa111"]
	s.at = s.at.Add(-time.Second)
	p.previous["aaa111"] = s
	p.mu.Unlock()

	second := p.poll(ctx)
	web := second.Containers[0]
	if web.Name != "web" || web.State != "running" {
		t.Fatalf("running container should sort first: %+v", second.Containers)
	}
	// cpu delta 1e9 over system delta 8e9 on 4 cpus = 50%
	if web.CPUPercent < 49.9 || web.CPUPercent > 50.1 {
		t.Errorf("cpu%% = %v, want 50", web.CPUPercent)
	}
	if web.MemoryUsage != 500_000_000 || web.MemoryPercent != 25 {
		t.Errorf("memory = %d (%v%%), want 500000000 (25%%)", web.MemoryUsage, web.MemoryPercent)
	}
	if web.RxBytesPerSec < 2000 || web.RxBytesPerSec > 2100 || web.TxBytesPerSec < 1000 || web.TxBytesPerSec > 1050 {
		t.Errorf("network rates = %v/%v, want ~2048/~1024", web.RxBytesPerSec, web.TxBytesPerSec)
	}
	if web.BlockRead != 11 || web.BlockWrite != 22 {
		t.Errorf("block io = %d/%d", web.BlockRead, web.BlockWrite)
	}
	old := second.Containers[1]
	if old.State != "exited" || old.CPUPercent != 0 {
		t.Errorf("stopped container should carry no usage: %+v", old)
	}
}

func TestDockerPollerReportsUnreachableDaemon(t *testing.T) {
	client, err := dockerapi.New("unix:///nonexistent-stats-lite/missing.sock")
	if err != nil {
		t.Fatal(err)
	}
	got := newDockerPoller(client).poll(context.Background())
	if got.Available || got.Error == "" {
		t.Fatalf("missing socket must be reported, got %+v", got)
	}
	if got.Containers == nil {
		t.Fatal("containers must be an empty list, not null, so the UI can render")
	}
}
