// Package dockerapi is a deliberately tiny Docker Engine API client: it only
// knows how to list containers and read one stats sample. Talking HTTP to the
// socket directly keeps the binary small instead of pulling in the full Docker
// SDK and its dependency tree.
package dockerapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"runtime"
	"strings"
	"time"
)

// Client talks to one Docker daemon.
type Client struct {
	httpClient *http.Client
	// baseURL is what requests are addressed to. For socket transports the host
	// part is a placeholder; the dialer ignores it.
	baseURL string
	host    string
}

// DefaultHost is where the daemon listens when DOCKER_HOST is unset.
func DefaultHost() string {
	if runtime.GOOS == "windows" {
		return "npipe:////./pipe/docker_engine"
	}
	return "unix:///var/run/docker.sock"
}

// New builds a client for a DOCKER_HOST-style address: unix://, npipe:// or
// tcp:// (http). TLS-secured tcp daemons are out of scope for a home server.
func New(host string) (*Client, error) {
	if host == "" {
		host = DefaultHost()
	}
	u, err := url.Parse(host)
	if err != nil {
		return nil, fmt.Errorf("invalid docker host %q: %w", host, err)
	}
	transport := &http.Transport{
		// WHY: stats calls are short; idle sockets keep the daemon from paying
		// a new connection per container per second.
		MaxIdleConns:        16,
		IdleConnTimeout:     60 * time.Second,
		DisableCompression:  true,
		MaxIdleConnsPerHost: 16,
	}
	base := "http://docker"
	switch u.Scheme {
	case "unix":
		path := u.Path
		transport.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", path)
		}
	case "npipe":
		// url.Parse turns npipe:////./pipe/x into Host="" Path="//./pipe/x".
		pipe := strings.ReplaceAll(u.Path, "/", `\`)
		transport.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialPipe(ctx, pipe)
		}
	case "tcp", "http":
		base = "http://" + u.Host
	default:
		return nil, fmt.Errorf("unsupported docker host scheme %q (use unix://, npipe:// or tcp://)", u.Scheme)
	}
	return &Client{
		httpClient: &http.Client{Transport: transport},
		baseURL:    base,
		host:       host,
	}, nil
}

// Host returns the address this client was built for.
func (c *Client) Host() string { return c.host }

// Container is the subset of the /containers/json entry we display.
type Container struct {
	ID     string   `json:"Id"`
	Names  []string `json:"Names"`
	Image  string   `json:"Image"`
	State  string   `json:"State"`
	Status string   `json:"Status"`
}

// Name returns the primary name without Docker's leading slash.
func (c Container) Name() string {
	if len(c.Names) == 0 {
		return c.ID[:min(12, len(c.ID))]
	}
	return strings.TrimPrefix(c.Names[0], "/")
}

// Stats is the subset of one /containers/{id}/stats sample we need.
type Stats struct {
	Read     time.Time `json:"read"`
	CPUStats struct {
		CPUUsage struct {
			TotalUsage  uint64   `json:"total_usage"`
			PercpuUsage []uint64 `json:"percpu_usage"`
		} `json:"cpu_usage"`
		SystemCPUUsage uint64 `json:"system_cpu_usage"`
		OnlineCPUs     uint32 `json:"online_cpus"`
	} `json:"cpu_stats"`
	MemoryStats struct {
		Usage uint64            `json:"usage"`
		Limit uint64            `json:"limit"`
		Stats map[string]uint64 `json:"stats"`
		// Windows containers report memory here instead of Usage.
		PrivateWorkingSet uint64 `json:"privateworkingset"`
	} `json:"memory_stats"`
	Networks map[string]struct {
		RxBytes uint64 `json:"rx_bytes"`
		TxBytes uint64 `json:"tx_bytes"`
	} `json:"networks"`
	BlkioStats struct {
		IOServiceBytesRecursive []struct {
			Op    string `json:"op"`
			Value uint64 `json:"value"`
		} `json:"io_service_bytes_recursive"`
	} `json:"blkio_stats"`
	StorageStats struct {
		ReadSizeBytes  uint64 `json:"read_size_bytes"`
		WriteSizeBytes uint64 `json:"write_size_bytes"`
	} `json:"storage_stats"`
}

// Ping checks the daemon answers at all.
func (c *Client) Ping(ctx context.Context) error {
	return c.get(ctx, "/_ping", nil)
}

// ListContainers returns every container, running or not.
func (c *Client) ListContainers(ctx context.Context) ([]Container, error) {
	var out []Container
	if err := c.get(ctx, "/containers/json?all=1", &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ContainerStats reads a single non-blocking stats sample. With one-shot the
// daemon returns immediately and leaves precpu_stats empty, so the caller must
// keep its own previous sample to compute CPU percent.
func (c *Client) ContainerStats(ctx context.Context, id string) (*Stats, error) {
	var out Stats
	if err := c.get(ctx, "/containers/"+url.PathEscape(id)+"/stats?stream=false&one-shot=true", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("docker API %s: HTTP %d: %s", path, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if out == nil {
		_, err = io.Copy(io.Discard, resp.Body)
		return err
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
