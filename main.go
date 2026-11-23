// stats-lite is a single-binary, cross-platform system monitor with a web UI:
// CPU, memory, network, disk and Docker container usage, streamed live.
package main

import (
	"context"
	"embed"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/ritik/stats-lite/internal/collector"
	"github.com/ritik/stats-lite/internal/dockerapi"
	"github.com/ritik/stats-lite/internal/server"
)

//go:embed web
var webFS embed.FS

// version is overridden at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	addr := flag.String("addr", envOr("STATS_LITE_ADDR", ":8080"), "listen address (env STATS_LITE_ADDR)")
	interval := flag.Duration("interval", envDuration("STATS_LITE_INTERVAL", time.Second), "sampling interval (env STATS_LITE_INTERVAL)")
	dockerHost := flag.String("docker-host", envOr("DOCKER_HOST", dockerapi.DefaultHost()), "Docker daemon address (env DOCKER_HOST)")
	dockerEnabled := flag.Bool("docker", envBool("STATS_LITE_DOCKER", true), "enable Docker container stats (env STATS_LITE_DOCKER)")
	auth := flag.String("auth", envOr("STATS_LITE_AUTH", ""), "optional basic auth as user:password (env STATS_LITE_AUTH)")
	hostRoot := flag.String("host-root", envOr("STATS_LITE_HOST_ROOT", ""), "where the host filesystem is mounted when running in a container (env STATS_LITE_HOST_ROOT)")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	slog.SetDefault(logger)

	var dockerClient *dockerapi.Client
	if *dockerEnabled {
		c, err := dockerapi.New(*dockerHost)
		if err != nil {
			logger.Warn("docker integration disabled", "error", err)
		} else {
			dockerClient = c
		}
	}

	coll := collector.New(collector.Options{
		Interval: *interval,
		Docker:   dockerClient,
		HostRoot: *hostRoot,
		Logger:   logger,
	})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go coll.Run(ctx)

	static, err := fs.Sub(webFS, "web")
	if err != nil {
		logger.Error("embedded web assets missing", "error", err)
		os.Exit(1)
	}
	httpServer := &http.Server{
		Addr:              *addr,
		Handler:           server.New(coll, static, server.Options{BasicAuth: *auth, Version: version, Logger: logger}),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
	}()

	logger.Info("stats-lite listening", "addr", *addr, "version", version, "docker", dockerClient != nil, "interval", interval.String())
	if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("server failed", "error", err)
		os.Exit(1)
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envBool(key string, fallback bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return fallback
	}
	return b
}

func envDuration(key string, fallback time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return fallback
	}
	return d
}
