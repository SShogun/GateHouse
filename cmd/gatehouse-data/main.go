package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/SShogun/GateHouse/internal/cluster"
	"github.com/SShogun/GateHouse/internal/config"
	"github.com/SShogun/GateHouse/internal/dataplane"
	"github.com/SShogun/GateHouse/internal/gateway"
	"github.com/SShogun/GateHouse/internal/lifecycle"
	"github.com/SShogun/GateHouse/internal/router"
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
	listenAddress := envOrDefault("GATEHOUSE_LISTEN_ADDR", "127.0.0.1:8080")
	upstreamAddress := envOrDefault("GATEHOUSE_UPSTREAM_URL", "http://127.0.0.1:8081")
	upstream, err := url.Parse(upstreamAddress)
	if err != nil {
		return fmt.Errorf("parse GATEHOUSE_UPSTREAM_URL: %w", err)
	}
	clusterID, endpointID, routeID := "default", "default", "default"
	runtime, err := gateway.New(config.Config{
		Listener: config.Listener{Address: listenAddress},
		Routes:   []router.Route{{ID: routeID, Path: "/", PathType: router.PathPrefix, ClusterID: clusterID}},
		Clusters: []config.Cluster{{ID: clusterID, Policy: cluster.RoundRobin, Endpoints: []cluster.Endpoint{{ID: endpointID, Address: upstream.String(), Weight: 1}}}},
	}, logger)
	if err != nil {
		return fmt.Errorf("configure data-plane gateway: %w", err)
	}
	server, err := dataplane.NewServer(runtime, config.Listener{Address: listenAddress})
	if err != nil {
		_ = runtime.Close()
		return fmt.Errorf("configure data-plane listener: %w", err)
	}
	if err := server.Start(); err != nil {
		_ = runtime.Close()
		return err
	}
	logger.Info("data plane listening", "address", server.Addr(), "upstream", upstreamAddress)
	return lifecycle.Run(signalCtx, shutdownTimeout, server.Wait, func(ctx context.Context) error {
		serverErr := server.Shutdown(ctx)
		gatewayErr := runtime.Close()
		return errors.Join(serverErr, gatewayErr)
	})
}

func envOrDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
