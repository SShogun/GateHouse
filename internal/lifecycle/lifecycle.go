// Package lifecycle coordinates process cancellation and bounded shutdown.
package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// ServeFunc runs the process's long-lived work until its root context is
// canceled. It should return promptly after cancellation.
type ServeFunc func(context.Context) error

// ShutdownFunc stops accepting work and drains owned resources within ctx.
type ShutdownFunc func(context.Context) error

// Run starts serve and waits for either its completion or a process shutdown
// signal. On shutdown, it gives shutdown a bounded drain window, then cancels
// the root context and waits for serve to stop within the same deadline.
func Run(signalCtx context.Context, shutdownTimeout time.Duration, serve ServeFunc, shutdown ShutdownFunc) error {
	if signalCtx == nil {
		return errors.New("signal context is nil")
	}
	if shutdownTimeout <= 0 {
		return errors.New("shutdown timeout must be positive")
	}
	if serve == nil {
		return errors.New("serve function is nil")
	}
	if shutdown == nil {
		return errors.New("shutdown function is nil")
	}

	rootCtx, cancelRoot := context.WithCancel(context.Background())
	defer cancelRoot()

	serveResult := make(chan error, 1)
	go func() {
		serveResult <- serve(rootCtx)
	}()

	select {
	case serveErr := <-serveResult:
		cancelRoot()
		shutdownErr := runShutdown(shutdownTimeout, shutdown)
		return errors.Join(serveErr, shutdownErr)
	case <-signalCtx.Done():
	}

	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancelShutdown()
	shutdownResult := make(chan error, 1)
	go func() {
		shutdownResult <- shutdown(shutdownCtx)
	}()

	var shutdownErr error
	select {
	case shutdownErr = <-shutdownResult:
	case <-shutdownCtx.Done():
		shutdownErr = fmt.Errorf("graceful shutdown exceeded %s: %w", shutdownTimeout, shutdownCtx.Err())
		cancelRoot()
		select {
		case serveErr := <-serveResult:
			return errors.Join(shutdownErr, serveErr)
		default:
			return shutdownErr
		}
	}

	cancelRoot()
	select {
	case serveErr := <-serveResult:
		return errors.Join(shutdownErr, serveErr)
	case <-shutdownCtx.Done():
		return errors.Join(shutdownErr, fmt.Errorf("serve did not stop within %s: %w", shutdownTimeout, shutdownCtx.Err()))
	}
}

// RunWithSignals runs the process until SIGINT or SIGTERM, then performs the
// bounded shutdown sequence in Run.
func RunWithSignals(shutdownTimeout time.Duration, serve ServeFunc, shutdown ShutdownFunc) error {
	signalCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return Run(signalCtx, shutdownTimeout, serve, shutdown)
}

func runShutdown(timeout time.Duration, shutdown ShutdownFunc) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	result := make(chan error, 1)
	go func() {
		result <- shutdown(ctx)
	}()
	select {
	case err := <-result:
		return err
	case <-ctx.Done():
		return fmt.Errorf("shutdown after serve exit exceeded %s: %w", timeout, ctx.Err())
	}
}
