package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"time"

	pb "github.com/SShogun/GateHouse/gen/go/gatehouse/v1"
	"github.com/SShogun/GateHouse/internal/control/admin"
	"github.com/SShogun/GateHouse/internal/control/store"
	"github.com/SShogun/GateHouse/internal/lifecycle"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc"
)

const shutdownTimeout = 10 * time.Second

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "gatehouse-control:", err)
		os.Exit(1)
	}
}
func run() error {
	if os.Getenv("GATEHOUSE_CONTROL_DEV_MODE") != "loopback" {
		return errors.New("set GATEHOUSE_CONTROL_DEV_MODE=loopback to enable unauthenticated development API")
	}
	address := envOrDefault("GATEHOUSE_CONTROL_LISTEN_ADDR", "127.0.0.1:9090")
	if err := admin.ValidateDevelopmentAddress(address); err != nil {
		return fmt.Errorf("validate admin listener: %w", err)
	}
	databaseURL := os.Getenv("GATEHOUSE_DATABASE_URL")
	if err := admin.ValidatePostgresURL(databaseURL); err != nil {
		return fmt.Errorf("validate GATEHOUSE_DATABASE_URL: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		cancel()
		return fmt.Errorf("create postgres pool: %w", err)
	}
	if err = pool.Ping(ctx); err != nil {
		pool.Close()
		cancel()
		return fmt.Errorf("connect postgres: %w", err)
	}
	cancel()
	persistent, err := store.NewPostgres(pool)
	if err != nil {
		pool.Close()
		return err
	}
	migrateCtx, migrateCancel := context.WithTimeout(context.Background(), 30*time.Second)
	err = persistent.Migrate(migrateCtx)
	migrateCancel()
	if err != nil {
		pool.Close()
		return fmt.Errorf("migrate control-plane database: %w", err)
	}
	service, err := admin.New(persistent)
	if err != nil {
		pool.Close()
		return err
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		pool.Close()
		return fmt.Errorf("listen on %s: %w", address, err)
	}
	grpcServer := grpc.NewServer(grpc.UnaryInterceptor(admin.RequestDeadline(5 * time.Second)))
	pb.RegisterAdminServiceServer(grpcServer, service)
	pb.RegisterDataPlaneServiceServer(grpcServer, service)
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	logger.Info("control plane listening", "address", listener.Addr(), "mode", "loopback-development")
	runErr := lifecycle.RunWithSignals(shutdownTimeout, func(ctx context.Context) error {
		result := make(chan error, 1)
		go func() { result <- grpcServer.Serve(listener) }()
		select {
		case err := <-result:
			if errors.Is(err, grpc.ErrServerStopped) {
				return nil
			}
			return err
		case <-ctx.Done():
			return nil
		}
	}, func(ctx context.Context) error {
		shutdownGRPC(ctx, grpcServer)
		return nil
	})
	pool.Close()
	return runErr
}

type grpcStopper interface {
	GracefulStop()
	Stop()
}

func shutdownGRPC(ctx context.Context, server grpcStopper) {
	done := make(chan struct{})
	go func() {
		server.GracefulStop()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		server.Stop()
		<-done
	}
}

func envOrDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
