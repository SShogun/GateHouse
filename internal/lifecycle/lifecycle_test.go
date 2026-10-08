package lifecycle

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"testing/synctest"
	"time"
)

func TestRunWithSignalsHandlesSIGTERM(t *testing.T) {
	if os.Getenv("GATEHOUSE_LIFECYCLE_SIGNAL_HELPER") == "1" {
		err := RunWithSignals(2*time.Second,
			func(root context.Context) error {
				fmt.Println("serve-ready")
				<-root.Done()
				fmt.Println("root-canceled")
				return nil
			},
			func(context.Context) error {
				fmt.Println("shutdown-called")
				return nil
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		return
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestRunWithSignalsHandlesSIGTERM$")
	cmd.Env = append(os.Environ(), "GATEHOUSE_LIFECYCLE_SIGNAL_HELPER=1")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	lines := make(chan string, 16)
	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
	}()

	var output []string
	ready := false
	for !ready {
		select {
		case line, open := <-lines:
			if !open {
				_ = cmd.Wait()
				t.Fatalf("signal helper exited before ready; output: %v; stderr: %s", output, stderr.String())
			}
			output = append(output, line)
			ready = line == "serve-ready"
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			t.Fatalf("signal helper did not become ready; output: %v", output)
		}
	}

	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatal(err)
	}
	wait := make(chan error, 1)
	go func() { wait <- cmd.Wait() }()
	select {
	case err := <-wait:
		if err != nil {
			t.Fatalf("signal helper exit error: %v; stderr: %s", err, stderr.String())
		}
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		<-wait
		t.Fatal("signal helper did not shut down after SIGTERM")
	}
	for line := range lines {
		output = append(output, line)
	}

	shutdownIndex := indexOf(output, "shutdown-called")
	rootCancelIndex := indexOf(output, "root-canceled")
	if shutdownIndex < 0 || rootCancelIndex < 0 || shutdownIndex >= rootCancelIndex {
		t.Fatalf("expected shutdown before root cancellation, got output: %v", output)
	}
}

func TestRunDrainsBeforeCancelingRoot(t *testing.T) {
	signalCtx, sendSignal := context.WithCancel(context.Background())
	defer sendSignal()

	rootCanceled := make(chan struct{})
	serveDone := make(chan struct{})
	shutdownCalled := make(chan struct{})
	serve := func(root context.Context) error {
		<-root.Done()
		close(rootCanceled)
		close(serveDone)
		return nil
	}
	shutdown := func(context.Context) error {
		select {
		case <-rootCanceled:
			t.Error("root context canceled before graceful shutdown")
		default:
		}
		close(shutdownCalled)
		return nil
	}

	finished := make(chan error, 1)
	go func() {
		finished <- Run(signalCtx, time.Second, serve, shutdown)
	}()
	sendSignal()

	select {
	case err := <-finished:
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run() did not finish after cancellation")
	}

	select {
	case <-shutdownCalled:
	default:
		t.Fatal("graceful shutdown was not called")
	}
	select {
	case <-serveDone:
	default:
		t.Fatal("serve function did not observe root cancellation")
	}
}

func TestRunReturnsShutdownErrorAndCancelsRoot(t *testing.T) {
	signalCtx, sendSignal := context.WithCancel(context.Background())
	defer sendSignal()
	wantErr := errors.New("resource shutdown failed")
	serve := func(root context.Context) error {
		<-root.Done()
		return nil
	}
	shutdown := func(context.Context) error { return wantErr }

	finished := make(chan error, 1)
	go func() { finished <- Run(signalCtx, time.Second, serve, shutdown) }()
	sendSignal()

	select {
	case err := <-finished:
		if !errors.Is(err, wantErr) {
			t.Fatalf("Run() error = %v, want wrapped %v", err, wantErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run() did not return after shutdown error")
	}
}

func TestRunBoundsShutdownAndServeWait(t *testing.T) {
	signalCtx, sendSignal := context.WithCancel(context.Background())
	defer sendSignal()
	serve := func(root context.Context) error {
		<-root.Done()
		return nil
	}
	shutdown := func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	}

	started := time.Now()
	sendSignal()
	err := Run(signalCtx, 50*time.Millisecond, serve, shutdown)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run() error = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("Run() exceeded bounded shutdown: %s", elapsed)
	}
}

func TestRunPreservesServeErrorWhenShutdownTimesOut(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		signalCtx, cancelSignal := context.WithCancel(context.Background())
		cancelSignal()

		serveError := errors.New("serve failed during drain")
		drainStarted := make(chan struct{})
		allowServeExit := make(chan struct{})
		allowShutdownReturn := make(chan struct{})
		finished := make(chan error, 1)

		go func() {
			finished <- Run(signalCtx, time.Second,
				func(context.Context) error {
					<-drainStarted
					<-allowServeExit
					return serveError
				},
				func(ctx context.Context) error {
					close(drainStarted)
					<-ctx.Done()
					<-allowShutdownReturn
					return ctx.Err()
				},
			)
		}()

		close(allowServeExit)
		synctest.Wait()
		time.Sleep(time.Second)
		err := <-finished
		close(allowShutdownReturn)

		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Run() error = %v, want shutdown deadline error", err)
		}
		if !errors.Is(err, serveError) {
			t.Fatalf("Run() error = %v, want preserved serve error %v", err, serveError)
		}
	})
}

func TestRunCleansUpAfterServeFailure(t *testing.T) {
	signalCtx, sendSignal := context.WithCancel(context.Background())
	defer sendSignal()
	wantServeErr := errors.New("startup failed")
	shutdownCalled := false
	err := Run(signalCtx, time.Second,
		func(context.Context) error { return wantServeErr },
		func(context.Context) error {
			shutdownCalled = true
			return nil
		},
	)
	if !errors.Is(err, wantServeErr) {
		t.Fatalf("Run() error = %v, want %v", err, wantServeErr)
	}
	if !shutdownCalled {
		t.Fatal("shutdown was not called after serve failure")
	}
}

func indexOf(lines []string, target string) int {
	for i, line := range lines {
		if line == target {
			return i
		}
	}
	return -1
}
