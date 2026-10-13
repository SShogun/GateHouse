package main

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"
)

func TestRunEnforcesControlPlaneStartupGuards(t *testing.T) {
	t.Run("requires explicit development mode", func(t *testing.T) {
		t.Setenv("GATEHOUSE_CONTROL_DEV_MODE", "")
		t.Setenv("GATEHOUSE_CONTROL_LISTEN_ADDR", "127.0.0.1:0")
		t.Setenv("GATEHOUSE_DATABASE_URL", "")
		err := run()
		if err == nil || !strings.Contains(err.Error(), "GATEHOUSE_CONTROL_DEV_MODE") {
			t.Fatalf("run() error=%v, want explicit development-mode rejection", err)
		}
	})

	t.Run("requires literal loopback bind", func(t *testing.T) {
		t.Setenv("GATEHOUSE_CONTROL_DEV_MODE", "loopback")
		t.Setenv("GATEHOUSE_CONTROL_LISTEN_ADDR", "0.0.0.0:9090")
		t.Setenv("GATEHOUSE_DATABASE_URL", "")
		err := run()
		if err == nil || !strings.Contains(err.Error(), "literal loopback IP") {
			t.Fatalf("run() error=%v, want non-loopback listener rejection", err)
		}
	})

	t.Run("requires postgres URL", func(t *testing.T) {
		t.Setenv("GATEHOUSE_CONTROL_DEV_MODE", "loopback")
		t.Setenv("GATEHOUSE_CONTROL_LISTEN_ADDR", "127.0.0.1:0")
		t.Setenv("GATEHOUSE_DATABASE_URL", "http://127.0.0.1/test")
		err := run()
		if err == nil || !strings.Contains(err.Error(), "GATEHOUSE_DATABASE_URL") {
			t.Fatalf("run() error=%v, want invalid database URL rejection", err)
		}
	})
}

func TestRunChecksDatabaseBeforeOpeningListener(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()

	t.Setenv("GATEHOUSE_CONTROL_DEV_MODE", "loopback")
	t.Setenv("GATEHOUSE_CONTROL_LISTEN_ADDR", occupied.Addr().String())
	t.Setenv("GATEHOUSE_DATABASE_URL", "postgres://test:test@127.0.0.1:1/gatehouse_test?sslmode=disable&connect_timeout=1")
	err = run()
	if err == nil || !strings.Contains(err.Error(), "connect postgres") {
		t.Fatalf("run() error=%v, want database connectivity failure before listener setup", err)
	}
}

type testGRPCStopper struct {
	gracefulDone chan struct{}
	stopCalled   chan struct{}
	returnFast   bool
}

func (s *testGRPCStopper) GracefulStop() {
	if s.returnFast {
		close(s.gracefulDone)
		return
	}
	<-s.gracefulDone
}
func (s *testGRPCStopper) Stop() { close(s.stopCalled); close(s.gracefulDone) }

func TestShutdownGRPCUsesGracefulStopWhenItCompletes(t *testing.T) {
	server := &testGRPCStopper{gracefulDone: make(chan struct{}), stopCalled: make(chan struct{}), returnFast: true}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	shutdownGRPC(ctx, server)
	select {
	case <-server.stopCalled:
		t.Fatal("shutdownGRPC() forced stop despite graceful completion")
	default:
	}
}

func TestShutdownGRPCForcesStopAtDeadline(t *testing.T) {
	server := &testGRPCStopper{gracefulDone: make(chan struct{}), stopCalled: make(chan struct{})}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	shutdownGRPC(ctx, server)
	select {
	case <-server.stopCalled:
	default:
		t.Fatal("shutdownGRPC() did not force-stop after the deadline")
	}
}
