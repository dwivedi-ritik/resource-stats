package server

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/dwivedi-ritik/resource-stats/internal/collector"
)

type fakeSource struct {
	snap collector.Snapshot
	ok   bool
	ch   chan collector.Snapshot
}

func (f *fakeSource) Latest() (collector.Snapshot, bool) { return f.snap, f.ok }
func (f *fakeSource) Subscribe() (<-chan collector.Snapshot, func()) {
	return f.ch, func() {}
}

func newTestServer(src *fakeSource, auth string) *httptest.Server {
	static := fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("<html>hello</html>")}}
	return httptest.NewServer(New(src, static, Options{BasicAuth: auth, Version: "test"}))
}

func TestSnapshotEndpoint(t *testing.T) {
	src := &fakeSource{snap: collector.Snapshot{Host: collector.HostInfo{Hostname: "box"}}, ok: true, ch: make(chan collector.Snapshot, 1)}
	ts := newTestServer(src, "")
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/snapshot")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var got collector.Snapshot
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Host.Hostname != "box" {
		t.Fatalf("hostname = %q", got.Host.Hostname)
	}
}

func TestSnapshotBeforeFirstSampleIs503(t *testing.T) {
	ts := newTestServer(&fakeSource{ch: make(chan collector.Snapshot)}, "")
	defer ts.Close()
	for _, path := range []string{"/api/snapshot", "/healthz"} {
		resp, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Errorf("%s = %d, want 503 while warming up", path, resp.StatusCode)
		}
	}
}

func TestStreamSendsLatestThenUpdates(t *testing.T) {
	src := &fakeSource{snap: collector.Snapshot{Host: collector.HostInfo{Hostname: "first"}}, ok: true, ch: make(chan collector.Snapshot, 1)}
	ts := newTestServer(src, "")
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/stream")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type = %q", ct)
	}
	reader := bufio.NewReader(resp.Body)
	readEvent := func() collector.Snapshot {
		deadline := time.After(3 * time.Second)
		for {
			select {
			case <-deadline:
				t.Fatal("timed out waiting for an event")
			default:
			}
			line, err := reader.ReadString('\n')
			if err != nil {
				t.Fatal(err)
			}
			if strings.HasPrefix(line, "data: ") {
				var s collector.Snapshot
				if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &s); err != nil {
					t.Fatal(err)
				}
				return s
			}
		}
	}
	if got := readEvent(); got.Host.Hostname != "first" {
		t.Fatalf("first event = %q", got.Host.Hostname)
	}
	src.ch <- collector.Snapshot{Host: collector.HostInfo{Hostname: "second"}}
	if got := readEvent(); got.Host.Hostname != "second" {
		t.Fatalf("second event = %q", got.Host.Hostname)
	}
}

func TestBasicAuth(t *testing.T) {
	src := &fakeSource{ok: true, ch: make(chan collector.Snapshot)}
	ts := newTestServer(src, "admin:secret")
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no credentials = %d, want 401", resp.StatusCode)
	}
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/", nil)
	req.SetBasicAuth("admin", "wrong")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong password = %d, want 401", resp.StatusCode)
	}
	req.SetBasicAuth("admin", "secret")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("right password = %d, want 200", resp.StatusCode)
	}
}
