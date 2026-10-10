package dataplane

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestServerGracefullyDrainsInFlightRequest(t *testing.T) {
	upstreamStarted := make(chan struct{})
	releaseUpstream := make(chan struct{})
	var releaseOnce sync.Once
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(upstreamStarted)
		<-releaseUpstream
		_, _ = io.WriteString(w, "drained response")
	}))
	defer upstream.Close()

	proxy, err := NewProxy(Config{UpstreamURL: upstream.URL}, nil)
	if err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(proxy, ServerConfig{Address: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		releaseOnce.Do(func() { close(releaseUpstream) })
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	}()

	clientResult := make(chan error, 1)
	go func() {
		resp, err := http.Get("http://" + server.Addr() + "/drain")
		if err != nil {
			clientResult <- err
			return
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err == nil && string(body) != "drained response" {
			err = fmt.Errorf("response body = %q, want drained response", body)
		}
		clientResult <- err
	}()

	select {
	case <-upstreamStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("upstream did not receive in-flight request")
	}
	shutdownResult := make(chan error, 1)
	listenerClosed := make(chan error, 1)
	go func() {
		_, err := server.listener.Accept()
		listenerClosed <- err
	}()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		shutdownResult <- server.Shutdown(ctx)
	}()
	select {
	case err := <-shutdownResult:
		t.Fatalf("server shutdown returned before in-flight request drained: %v", err)
	case err := <-listenerClosed:
		if err == nil {
			t.Fatal("listener accepted an unexpected connection while shutdown started")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server shutdown did not close listener")
	}
	releaseOnce.Do(func() { close(releaseUpstream) })

	select {
	case err := <-clientResult:
		if err != nil {
			t.Fatalf("in-flight client request failed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("in-flight client request did not finish")
	}
	select {
	case err := <-shutdownResult:
		if err != nil {
			t.Fatalf("server shutdown failed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server did not finish graceful shutdown")
	}
}

func TestServerShutdownHonorsDeadlineWhileRequestBlocked(t *testing.T) {
	upstreamStarted := make(chan struct{})
	releaseUpstream := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(upstreamStarted)
		<-releaseUpstream
		_, _ = io.WriteString(w, "late response")
	}))
	defer upstream.Close()

	proxy, err := NewProxy(Config{UpstreamURL: upstream.URL}, nil)
	if err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(proxy, ServerConfig{Address: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		close(releaseUpstream)
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	}()

	clientResult := make(chan error, 1)
	go func() {
		resp, err := http.Get("http://" + server.Addr() + "/deadline")
		if err == nil {
			_ = resp.Body.Close()
		}
		clientResult <- err
	}()
	select {
	case <-upstreamStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("upstream did not receive in-flight request")
	}

	listenerClosed := make(chan error, 1)
	go func() {
		_, err := server.listener.Accept()
		listenerClosed <- err
	}()
	shutdownResult := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()
		shutdownResult <- server.Shutdown(ctx)
	}()
	select {
	case err := <-listenerClosed:
		if err == nil {
			t.Fatal("listener accepted an unexpected connection while shutdown started")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server shutdown did not close listener")
	}

	select {
	case err := <-shutdownResult:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Shutdown() error = %v, want context deadline exceeded while request is blocked", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server shutdown did not return after its deadline")
	}
	select {
	case err := <-clientResult:
		if err == nil {
			t.Fatal("in-flight request unexpectedly completed before upstream was released")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("forced shutdown did not end the in-flight client request")
	}
}

func TestServerConfigRejectsMissingAddress(t *testing.T) {
	_, err := NewProxy(Config{UpstreamURL: "http://127.0.0.1:1"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), ServerConfig{})
	if err == nil || !strings.Contains(err.Error(), "address") {
		t.Fatalf("NewServer() error = %v, want missing address error", err)
	}
}
