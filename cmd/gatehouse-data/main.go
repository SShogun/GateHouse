package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/SShogun/GateHouse/internal/dataplane"
	"github.com/SShogun/GateHouse/internal/lifecycle"
)

const shutdownTimeout = 10 * time.Second

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "gatehouse-data:", err)
		os.Exit(1)
	}
}

func run() error {
	signalCtx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	proxy, err := dataplane.NewProxy(dataplane.Config{
		UpstreamURL: envOrDefault("GATEHOUSE_UPSTREAM_URL", "http://127.0.0.1:8081"),
	}, logger)
	if err != nil {
		return fmt.Errorf("configure data-plane proxy: %w", err)
	}
	server, err := dataplane.NewServer(proxy, dataplane.ServerConfig{
		Address: envOrDefault("GATEHOUSE_LISTEN_ADDR", "127.0.0.1:8080"),
	})
	if err != nil {
		return fmt.Errorf("configure data-plane listener: %w", err)
	}
	if err := server.Start(); err != nil {
		return err
	}
	logger.Info("data plane listening", "address", server.Addr(), "upstream", envOrDefault("GATEHOUSE_UPSTREAM_URL", "http://127.0.0.1:8081"))
	return lifecycle.Run(signalCtx, shutdownTimeout, server.Wait, server.Shutdown)
}

func envOrDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
