package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
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
	controlclient "github.com/SShogun/GateHouse/internal/control/client"
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
	listenAddress := envOrDefault("GATEHOUSE_LISTEN_ADDR", "127.0.0.1:8080")
	if err := dataplane.ValidateDevelopmentListenAddress(listenAddress); err != nil {
		return fmt.Errorf("validate development listener: %w", err)
	}
	signalCtx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if controlEndpoint := os.Getenv("GATEHOUSE_CONTROL_ENDPOINT"); controlEndpoint != "" {
		return runDynamic(signalCtx, logger, listenAddress, controlEndpoint)
	}
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

func runDynamic(signalCtx context.Context, logger *slog.Logger, listenAddress, controlEndpoint string) error {
	listenerConfig := config.Listener{Address: listenAddress}
	manager, err := gateway.NewManager(listenerConfig, logger)
	if err != nil {
		return fmt.Errorf("configure dynamic gateway: %w", err)
	}
	instanceID := os.Getenv("GATEHOUSE_INSTANCE_ID")
	if instanceID == "" {
		_ = manager.Close()
		return errors.New("GATEHOUSE_INSTANCE_ID is required in dynamic mode")
	}
	bootID, err := newBootID()
	if err != nil {
		_ = manager.Close()
		return fmt.Errorf("create data-plane boot ID: %w", err)
	}
	client, err := controlclient.NewControlClient(controlclient.ControlClientConfig{
		Endpoint: controlEndpoint, InstanceID: instanceID, BootID: bootID,
		Build: envOrDefault("GATEHOUSE_BUILD_ID", "dev"), Manager: manager,
	})
	if err != nil {
		_ = manager.Close()
		return fmt.Errorf("configure control-plane stream: %w", err)
	}
	server, err := dataplane.NewServer(manager, listenerConfig)
	if err != nil {
		_ = client.Close()
		_ = manager.Close()
		return fmt.Errorf("configure data-plane listener: %w", err)
	}
	if err := server.Start(); err != nil {
		_ = client.Close()
		_ = manager.Close()
		return err
	}
	controlCtx, cancelControl := context.WithCancel(context.Background())
	controlDone := make(chan error, 1)
	go func() { controlDone <- client.Run(controlCtx) }()
	logger.Info("data plane listening in dynamic mode", "address", server.Addr(), "control_endpoint", controlEndpoint, "instance_id", instanceID)
	return lifecycle.Run(signalCtx, shutdownTimeout, server.Wait, func(ctx context.Context) error {
		serverErr := server.Shutdown(ctx)
		cancelControl()
		var controlErr error
		select {
		case controlErr = <-controlDone:
		case <-ctx.Done():
			controlErr = fmt.Errorf("control-plane stream did not stop: %w", ctx.Err())
		}
		managerErr := manager.Close()
		return errors.Join(serverErr, controlErr, managerErr)
	})
}

func newBootID() (string, error) {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes[:]), nil
}

func envOrDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
