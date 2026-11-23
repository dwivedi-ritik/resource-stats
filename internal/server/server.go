// Package server exposes the collector over HTTP: the embedded web UI, a JSON
// snapshot endpoint, and a Server-Sent Events stream for live updates.
package server

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/ritik/stats-lite/internal/collector"
)

// Source is the part of the collector the server needs.
type Source interface {
	Latest() (collector.Snapshot, bool)
	Subscribe() (<-chan collector.Snapshot, func())
}

// Options configure the HTTP handler.
type Options struct {
	// BasicAuth is "user:password"; empty disables authentication.
	BasicAuth string
	Version   string
	Logger    *slog.Logger
}

// New builds the HTTP handler. static is the embedded web directory.
func New(src Source, static fs.FS, opts Options) http.Handler {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/snapshot", func(w http.ResponseWriter, r *http.Request) {
		snap, ok := src.Latest()
		if !ok {
			http.Error(w, `{"error":"no sample yet"}`, http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(snap)
	})
	mux.HandleFunc("GET /api/stream", func(w http.ResponseWriter, r *http.Request) {
		streamSnapshots(w, r, src)
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		_, ok := src.Latest()
		if !ok {
			http.Error(w, "warming up", http.StatusServiceUnavailable)
			return
		}
		fmt.Fprintln(w, "ok")
	})
	mux.HandleFunc("GET /version", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, opts.Version)
	})
	fileServer := http.FileServer(http.FS(static))
	mux.Handle("GET /", noCache(fileServer))

	var h http.Handler = mux
	if opts.BasicAuth != "" {
		h = basicAuth(opts.BasicAuth, h)
	}
	return h
}

// streamSnapshots writes each new snapshot as one SSE event until the client
// goes away. EventSource in the browser reconnects on its own.
func streamSnapshots(w http.ResponseWriter, r *http.Request, src Source) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	write := func(s collector.Snapshot) bool {
		data, err := json.Marshal(s)
		if err != nil {
			return false
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}
	if snap, ok := src.Latest(); ok && !write(snap) {
		return
	}
	ch, cancel := src.Subscribe()
	defer cancel()
	// WHY: a comment line every 20s keeps proxies and browsers from closing an
	// idle stream if the collector ever stalls.
	keepalive := time.NewTicker(20 * time.Second)
	defer keepalive.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case snap := <-ch:
			if !write(snap) {
				return
			}
		case <-keepalive.C:
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func noCache(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// WHY: the UI is tiny and served from one binary; no-cache means an
		// upgrade is visible on the next reload without cache-busting hashes.
		w.Header().Set("Cache-Control", "no-cache")
		next.ServeHTTP(w, r)
	})
}

func basicAuth(credentials string, next http.Handler) http.Handler {
	wantUser, wantPass, _ := strings.Cut(credentials, ":")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok ||
			subtle.ConstantTimeCompare([]byte(user), []byte(wantUser)) != 1 ||
			subtle.ConstantTimeCompare([]byte(pass), []byte(wantPass)) != 1 {
			w.Header().Set("WWW-Authenticate", `Basic realm="stats-lite", charset="UTF-8"`)
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}
