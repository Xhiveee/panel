// Package agent implements the local node agent: instance processes, console,
// files and metrics, driven over the master connection.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"panel/agent/internal/client"
	"panel/agent/internal/config"
	"panel/agent/internal/fsmgr"
	"panel/agent/internal/metrics"
	"panel/agent/internal/runner"
	"panel/common/protocol"
)

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	cfg := config.Load()
	if err := cfg.Save(); err != nil {
		slog.Error("save agent config", "err", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Event sink: services emit events here; the client forwards them once a
	// session is live. This decouples the services from the connection lifetime.
	var sink sink
	r := runner.New(sink.emit)
	fr := fsmgr.NewRegistry()
	mc := metrics.NewCollector(r, sink.emit)
	c := client.New(cfg, r, fr, mc)
	sink.setClient(c)

	if err := c.Run(ctx); err != nil {
		slog.Error("agent exited", "err", err)
		os.Exit(1)
	}
}

// sink forwards service events to the connected client.
type sink struct {
	mu     sync.Mutex
	client *client.Client
}

func (s *sink) setClient(c *client.Client) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.client = c
}

func (s *sink) emit(msg protocol.Message) {
	s.mu.Lock()
	c := s.client
	s.mu.Unlock()
	if c != nil {
		c.Emit(msg)
	}
}
