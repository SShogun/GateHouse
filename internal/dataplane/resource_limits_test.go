package dataplane

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestDevelopmentListenAddressRequiresLiteralLoopbackAndNumericPort(t *testing.T) {
	for _, address := range []string{"127.0.0.1:0", "127.200.1.2:65535", "[::1]:8080"} {
		if err := ValidateDevelopmentListenAddress(address); err != nil {
			t.Errorf("ValidateDevelopmentListenAddress(%q) = %v, want success", address, err)
		}
	}
	for _, address := range []string{"0.0.0.0:8080", "[::]:8080", "192.0.2.1:8080", "localhost:8080", "127.0.0.1:http", "127.0.0.1:-1", "127.0.0.1:65536", "127.0.0.1", "127.0.0.1:80:90"} {
		if err := ValidateDevelopmentListenAddress(address); err == nil {
			t.Errorf("ValidateDevelopmentListenAddress(%q) succeeded, want rejection", address)
		}
	}
}

func TestServerResourceLimitDefaultsAndRejectsNegativeValues(t *testing.T) {
	server, err := NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), ServerConfig{Address: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	if server.cfg.MaxConcurrentRequests != 128 {
		t.Errorf("MaxConcurrentRequests = %d, want 128", server.cfg.MaxConcurrentRequests)
	}
	if server.cfg.RequestBodyReadIdleTimeout != 30*time.Second {
		t.Errorf("RequestBodyReadIdleTimeout = %s, want 30s", server.cfg.RequestBodyReadIdleTimeout)
	}
	for _, cfg := range []ServerConfig{
		{Address: "127.0.0.1:0", MaxConcurrentRequests: -1},
		{Address: "127.0.0.1:0", RequestBodyReadIdleTimeout: -time.Second},
	} {
		if _, err := NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), cfg); err == nil {
			t.Errorf("NewServer(%+v) succeeded, want negative resource limit rejection", cfg)
		}
	}
}

func TestProxyHeaderLimitDefaultsAndRejectsNegativeValues(t *testing.T) {
	proxy, err := NewProxy(Config{UpstreamURL: "http://127.0.0.1:1"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if proxy.transport.ResponseHeaderTimeout != 30*time.Second {
		t.Errorf("ResponseHeaderTimeout = %s, want 30s", proxy.transport.ResponseHeaderTimeout)
	}
	if proxy.transport.MaxResponseHeaderBytes != 1<<20 {
		t.Errorf("MaxResponseHeaderBytes = %d, want 1MiB", proxy.transport.MaxResponseHeaderBytes)
	}
	for _, cfg := range []Config{
		{UpstreamURL: "http://127.0.0.1:1", ResponseHeaderTimeout: -time.Second},
		{UpstreamURL: "http://127.0.0.1:1", MaxResponseHeaderBytes: -1},
	} {
		if _, err := NewProxy(cfg, nil); err == nil {
			t.Errorf("NewProxy(%+v) succeeded, want negative limit rejection", cfg)
		}
	}
}

func TestServerTimesOutAnIdleRequestBodyRead(t *testing.T) {
	server, client := startResourceLimitServer(t, ServerConfig{RequestBodyReadIdleTimeout: 100 * time.Millisecond}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "body read failed", http.StatusRequestTimeout)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer client.CloseIdleConnections()
	conn, err := net.Dial("tcp", server.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.WriteString(conn, "POST / HTTP/1.1\r\nHost: local\r\nContent-Length: 5\r\n\r\nx"); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(line, "408 Request Timeout") {
		t.Fatalf("response status line = %q, want HTTP 408 after idle body read", line)
	}
}

func TestServerAllowsProgressiveBodyLongerThanOneIdleInterval(t *testing.T) {
	server, client := startResourceLimitServer(t, ServerConfig{RequestBodyReadIdleTimeout: 120 * time.Millisecond}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		_, _ = w.Write(body)
	}))
	defer client.CloseIdleConnections()
	pipeReader, pipeWriter := io.Pipe()
	request, err := http.NewRequest(http.MethodPost, "http://"+server.Addr()+"/", pipeReader)
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		response, err := client.Do(request)
		if err != nil {
			result <- err
			return
		}
		defer response.Body.Close()
		got, err := io.ReadAll(response.Body)
		if err == nil && string(got) != "progressive" {
			err = fmt.Errorf("response body = %q", got)
		}
		result <- err
	}()
	for _, part := range []string{"pro", "gre", "ssi", "ve"} {
		time.Sleep(70 * time.Millisecond)
		if _, err := io.WriteString(pipeWriter, part); err != nil {
			t.Fatal(err)
		}
	}
	_ = pipeWriter.Close()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("progressive upload failed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("progressive upload did not finish")
	}
}

func TestServerRejectsConcurrentRequestImmediatelyAndRecoversSlot(t *testing.T) {
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	server, client := startResourceLimitServer(t, ServerConfig{MaxConcurrentRequests: 1}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entered <- struct{}{}
		if r.URL.Path == "/error" {
			http.Error(w, "failed", http.StatusInternalServerError)
			return
		}
		<-release
		_, _ = io.WriteString(w, "ok")
	}))
	defer client.CloseIdleConnections()
	first := make(chan *http.Response, 1)
	firstErr := make(chan error, 1)
	go func() {
		resp, err := client.Get("http://" + server.Addr() + "/hold")
		if err != nil {
			firstErr <- err
			return
		}
		first <- resp
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("first request did not enter handler")
	}
	started := time.Now()
	second, err := client.Get("http://" + server.Addr() + "/second")
	if err != nil {
		t.Fatal(err)
	}
	_ = second.Body.Close()
	if second.StatusCode != http.StatusServiceUnavailable || time.Since(started) > 300*time.Millisecond {
		t.Fatalf("second request status=%d elapsed=%s, want immediate 503", second.StatusCode, time.Since(started))
	}
	close(release)
	select {
	case err := <-firstErr:
		t.Fatal(err)
	case resp := <-first:
		_ = resp.Body.Close()
	case <-time.After(time.Second):
		t.Fatal("first request did not finish")
	}
	resp, err := client.Get("http://" + server.Addr() + "/error")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("error request status=%d, want 500", resp.StatusCode)
	}
	resp, err = client.Get("http://" + server.Addr() + "/after-error")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("request after handler error status=%d, want 200", resp.StatusCode)
	}
}

func TestServerReleasesConcurrentSlotAfterCancellationAndPanic(t *testing.T) {
	started := make(chan struct{}, 1)
	finished := make(chan struct{}, 1)
	server, client := startResourceLimitServer(t, ServerConfig{MaxConcurrentRequests: 1}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/cancel":
			started <- struct{}{}
			<-r.Context().Done()
			finished <- struct{}{}
		case "/panic":
			panic("test panic")
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer client.CloseIdleConnections()
	ctx, cancel := context.WithCancel(context.Background())
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+server.Addr()+"/cancel", nil)
	if err != nil {
		t.Fatal(err)
	}
	canceled := make(chan struct{})
	go func() {
		_, _ = client.Do(request)
		close(canceled)
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("cancel handler did not start")
	}
	cancel()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("cancel handler did not exit")
	}
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("canceled client request did not return")
	}
	resp, err := client.Get("http://" + server.Addr() + "/panic")
	if err == nil {
		_ = resp.Body.Close()
	}
	resp, err = client.Get("http://" + server.Addr() + "/after-panic")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status after panic = %d, want 204", resp.StatusCode)
	}
}

func TestServerReleasesConcurrentSlotWhenUpgradeHandlerReturns(t *testing.T) {
	server, client := startResourceLimitServer(t, ServerConfig{MaxConcurrentRequests: 1}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/upgrade" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			t.Error("response writer does not support hijacking")
			return
		}
		conn, rw, err := hijacker.Hijack()
		if err != nil {
			t.Errorf("hijack connection: %v", err)
			return
		}
		_, _ = io.WriteString(rw, "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: test\r\n\r\n")
		_ = rw.Flush()
		_ = conn.Close()
	}))
	defer client.CloseIdleConnections()
	conn, err := net.Dial("tcp", server.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := io.WriteString(conn, "GET /upgrade HTTP/1.1\r\nHost: local\r\nConnection: Upgrade\r\nUpgrade: test\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	status, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil || !strings.Contains(status, "101 Switching Protocols") {
		t.Fatalf("upgrade status line=%q err=%v", status, err)
	}
	resp, err := client.Get("http://" + server.Addr() + "/after-upgrade")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status after upgrade = %d, want 204", resp.StatusCode)
	}
}

func TestServerResourceLimitsPreserveProxyUpgradeTunnel(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			t.Error("upstream response writer does not support hijacking")
			return
		}
		conn, rw, err := hijacker.Hijack()
		if err != nil {
			t.Errorf("upstream hijack: %v", err)
			return
		}
		defer conn.Close()
		if _, err := io.WriteString(rw, "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: echo\r\n\r\nready"); err != nil {
			t.Errorf("write upgrade response: %v", err)
			return
		}
		if err := rw.Flush(); err != nil {
			t.Errorf("flush upgrade response: %v", err)
			return
		}
		message := make([]byte, len("ping"))
		if _, err := io.ReadFull(rw, message); err != nil {
			t.Errorf("read upgraded request data: %v", err)
			return
		}
		_, _ = conn.Write(message)
	}))
	defer upstream.Close()
	proxy, err := NewProxy(Config{UpstreamURL: upstream.URL}, nil)
	if err != nil {
		t.Fatal(err)
	}
	server, client := startResourceLimitServer(t, ServerConfig{MaxConcurrentRequests: 1}, proxy)
	defer client.CloseIdleConnections()
	conn, err := net.Dial("tcp", server.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.WriteString(conn, "GET /upgrade HTTP/1.1\r\nHost: local\r\nConnection: Upgrade\r\nUpgrade: echo\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(conn)
	status, err := reader.ReadString('\n')
	if err != nil || !strings.Contains(status, "101 Switching Protocols") {
		t.Fatalf("upgrade response status=%q err=%v", status, err)
	}
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if line == "\r\n" {
			break
		}
	}
	ready := make([]byte, len("ready"))
	if _, err := io.ReadFull(reader, ready); err != nil || string(ready) != "ready" {
		t.Fatalf("upstream greeting=%q err=%v", ready, err)
	}
	if _, err := io.WriteString(conn, "ping"); err != nil {
		t.Fatal(err)
	}
	echo := make([]byte, len("ping"))
	if _, err := io.ReadFull(reader, echo); err != nil || string(echo) != "ping" {
		t.Fatalf("upgrade echo=%q err=%v", echo, err)
	}
}

func TestBodyBearingHandlerRetainsDirectHijackConnection(t *testing.T) {
	retained := make(chan net.Conn, 1)
	server, client := startResourceLimitServer(t, ServerConfig{}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			http.Error(w, "hijacker unavailable", http.StatusInternalServerError)
			return
		}
		conn, rw, err := hijacker.Hijack()
		if err != nil {
			return
		}
		if _, err := io.WriteString(rw, "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: body\r\n\r\n"); err != nil {
			_ = conn.Close()
			return
		}
		_ = rw.Flush()
		retained <- conn
		go echoHijackedMessage(conn, rw)
	}))
	defer client.CloseIdleConnections()
	conn, err := dialBodyBearingUpgrade(t, server.Addr())
	if err != nil {
		if conn != nil {
			_ = conn.Close()
		}
		t.Fatal(err)
	}
	defer conn.Close()
	retainedConn := awaitHijackedConn(t, retained)
	defer retainedConn.Close()
	assertHijackedEcho(t, conn)
}

func TestResponseControllerHijackDoesNotInheritBodyDeadline(t *testing.T) {
	retained := make(chan net.Conn, 1)
	server, client := startResourceLimitServer(t, ServerConfig{RequestBodyReadIdleTimeout: 100 * time.Millisecond}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, rw, err := http.NewResponseController(w).Hijack()
		if err != nil {
			t.Errorf("ResponseController.Hijack: %v", err)
			return
		}
		if _, err := io.WriteString(rw, "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: body\r\n\r\n"); err != nil {
			_ = conn.Close()
			t.Errorf("write upgrade response: %v", err)
			return
		}
		_ = rw.Flush()
		retained <- conn
		go echoHijackedMessage(conn, rw)
	}))
	defer client.CloseIdleConnections()
	conn, err := dialBodyBearingUpgrade(t, server.Addr())
	if err != nil {
		if conn != nil {
			_ = conn.Close()
		}
		t.Fatal(err)
	}
	defer conn.Close()
	retainedConn := awaitHijackedConn(t, retained)
	defer retainedConn.Close()
	assertHijackedEcho(t, conn)
}

func TestRepeatedHijackKeepsTransferredConnectionOwned(t *testing.T) {
	retained := make(chan net.Conn, 1)
	secondHijack := make(chan error, 1)
	server, client := startResourceLimitServer(t, ServerConfig{RequestBodyReadIdleTimeout: 100 * time.Millisecond}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		controller := http.NewResponseController(w)
		conn, rw, err := controller.Hijack()
		if err != nil {
			t.Errorf("first ResponseController.Hijack: %v", err)
			return
		}
		_, _, err = controller.Hijack()
		secondHijack <- err
		if _, err := io.WriteString(rw, "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: body\r\n\r\n"); err != nil {
			_ = conn.Close()
			return
		}
		_ = rw.Flush()
		retained <- conn
		go echoHijackedMessage(conn, rw)
	}))
	defer client.CloseIdleConnections()
	conn, err := dialBodyBearingUpgrade(t, server.Addr())
	if err != nil {
		if conn != nil {
			_ = conn.Close()
		}
		t.Fatal(err)
	}
	defer conn.Close()
	retainedConn := awaitHijackedConn(t, retained)
	defer retainedConn.Close()
	select {
	case err := <-secondHijack:
		if !errors.Is(err, http.ErrHijacked) {
			t.Fatalf("second Hijack error = %v, want http.ErrHijacked", err)
		}
	case <-time.After(time.Second):
		t.Fatal("second Hijack did not return")
	}
	assertHijackedEcho(t, conn)
}

func TestFailedHijackRestoresDeadlineForBlockedBodyRead(t *testing.T) {
	const idleTimeout = 150 * time.Millisecond
	readResult := make(chan error, 1)
	readObserved := make(chan error, 1)
	readDeadlineInstalled := make(chan struct{}, 1)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		go func() {
			_, err := io.ReadFull(r.Body, make([]byte, 4))
			readResult <- err
		}()
		select {
		case <-readDeadlineInstalled:
		case <-time.After(time.Second):
			t.Error("body read did not install its idle deadline")
			return
		}
		_, _, err := http.NewResponseController(w).Hijack()
		if err == nil {
			t.Error("Hijack succeeded through failing test writer")
		}
		select {
		case readErr := <-readResult:
			readObserved <- readErr
		case <-time.After(500 * time.Millisecond):
			readObserved <- errors.New("blocked body read did not retain its idle deadline after failed Hijack")
		}
	})
	limited := newResourceLimitHandler(handler, 1, idleTimeout)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		limited.ServeHTTP(failingHijackResponseWriter{ResponseWriter: w, readDeadlineInstalled: readDeadlineInstalled}, r)
	}))
	defer server.Close()
	conn, err := net.Dial("tcp", strings.TrimPrefix(server.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.WriteString(conn, "POST / HTTP/1.1\r\nHost: local\r\nContent-Length: 4\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-readObserved:
		var netErr net.Error
		if !errors.As(err, &netErr) || !netErr.Timeout() {
			t.Fatalf("blocked body read error = %v, want idle timeout", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not observe stalled body read timeout")
	}
}

type failingHijackResponseWriter struct {
	http.ResponseWriter
	readDeadlineInstalled chan struct{}
}

func (w failingHijackResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w failingHijackResponseWriter) SetReadDeadline(deadline time.Time) error {
	err := http.NewResponseController(w.ResponseWriter).SetReadDeadline(deadline)
	if err == nil && !deadline.IsZero() {
		select {
		case w.readDeadlineInstalled <- struct{}{}:
		default:
		}
	}
	return err
}

func (w failingHijackResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return nil, nil, errors.New("injected Hijack failure")
}

func dialBodyBearingUpgrade(t *testing.T, address string) (net.Conn, error) {
	t.Helper()
	conn, err := net.Dial("tcp", address)
	if err != nil {
		return nil, err
	}
	_ = conn.SetDeadline(time.Now().Add(time.Second))
	if _, err := io.WriteString(conn, "POST /upgrade HTTP/1.1\r\nHost: local\r\nContent-Length: 4\r\nConnection: Upgrade\r\nUpgrade: body\r\n\r\n"); err != nil {
		_ = conn.Close()
		return nil, err
	}
	reader := bufio.NewReader(conn)
	status, err := reader.ReadString('\n')
	if err != nil {
		return conn, err
	}
	if !strings.Contains(status, "101 Switching Protocols") {
		return conn, fmt.Errorf("upgrade status line = %q, want 101 Switching Protocols", status)
	}
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return conn, err
		}
		if line == "\r\n" {
			break
		}
	}
	return conn, nil
}

func awaitHijackedConn(t *testing.T, retained <-chan net.Conn) net.Conn {
	t.Helper()
	select {
	case conn := <-retained:
		return conn
	case <-time.After(time.Second):
		t.Fatal("handler did not retain hijacked connection")
		return nil
	}
}

func assertHijackedEcho(t *testing.T, conn net.Conn) {
	t.Helper()
	if _, err := io.WriteString(conn, "ping"); err != nil {
		t.Fatalf("write to retained hijacked connection: %v", err)
	}
	echo := make([]byte, len("ping"))
	if _, err := io.ReadFull(conn, echo); err != nil || string(echo) != "ping" {
		t.Fatalf("retained hijack echo=%q err=%v", echo, err)
	}
}

func echoHijackedMessage(conn net.Conn, rw *bufio.ReadWriter) {
	message := make([]byte, len("ping"))
	if _, err := io.ReadFull(rw, message); err != nil {
		return
	}
	_, _ = conn.Write(message)
}

func TestRejectedRequestBodyDoesNotDelayOverloadResponse(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	server, client := startResourceLimitServer(t, ServerConfig{
		MaxConcurrentRequests:      1,
		RequestBodyReadIdleTimeout: 5 * time.Second,
	}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/hold" {
			close(entered)
			<-release
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer client.CloseIdleConnections()
	defer releaseOnce.Do(func() { close(release) })
	firstResponse := make(chan error, 1)
	go func() {
		resp, err := client.Get("http://" + server.Addr() + "/hold")
		if resp != nil {
			_ = resp.Body.Close()
		}
		firstResponse <- err
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("first request did not occupy the only slot")
	}
	conn, err := net.Dial("tcp", server.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(time.Second))
	if _, err := io.WriteString(conn, "POST /overload HTTP/1.1\r\nHost: local\r\nContent-Length: 4\r\n\r\nx"); err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: http.MethodPost})
	if err != nil {
		t.Fatalf("read overload response before sending remaining body: %v", err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("overload status = %d, want 503", response.StatusCode)
	}
	if _, err := conn.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("overload connection read error = %v, want EOF after incomplete body", err)
	}
	releaseOnce.Do(func() { close(release) })
	select {
	case err := <-firstResponse:
		if err != nil {
			t.Fatalf("first request failed: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("first request did not finish after release")
	}
}

func TestEarlyResponseWithUnreadStalledBodyIsBounded(t *testing.T) {
	server, client := startResourceLimitServer(t, ServerConfig{RequestBodyReadIdleTimeout: 150 * time.Millisecond}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer client.CloseIdleConnections()
	conn, err := net.Dial("tcp", server.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(time.Second))
	started := time.Now()
	if _, err := io.WriteString(conn, "POST /early HTTP/1.1\r\nHost: local\r\nContent-Length: 4\r\n\r\nx"); err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: http.MethodPost})
	if err != nil {
		t.Fatalf("read early response with stalled body: %v", err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("early response status = %d, want 204", response.StatusCode)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("early response took %s with unread stalled body, want under 500ms", elapsed)
	}
	if _, err := conn.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("early-response connection read error = %v, want EOF after incomplete body", err)
	}
}

func TestEarlyResponseKeepsDeadlineWhileConcurrentBodyReadFinishes(t *testing.T) {
	readStarted := make(chan struct{})
	readFinished := make(chan error, 1)
	server, client := startResourceLimitServer(t, ServerConfig{RequestBodyReadIdleTimeout: 5 * time.Second}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		go func() {
			close(readStarted)
			_, err := io.Copy(io.Discard, r.Body)
			readFinished <- err
		}()
		<-readStarted
		time.Sleep(50 * time.Millisecond)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer client.CloseIdleConnections()
	conn, err := net.Dial("tcp", server.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(time.Second))
	if _, err := io.WriteString(conn, "POST /early HTTP/1.1\r\nHost: local\r\nContent-Length: 4\r\n\r\nx"); err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: http.MethodPost})
	if err != nil {
		t.Fatalf("read early response while body Read is blocked: %v", err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("early response status = %d, want 204", response.StatusCode)
	}
	select {
	case <-readFinished:
	case <-time.After(time.Second):
		t.Fatal("concurrent request-body read did not stop after response")
	}
}

func TestProxyHeaderTimeoutReturnsBadGatewayAndReleasesRequest(t *testing.T) {
	started := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
	}))
	defer upstream.Close()
	proxy, err := NewProxy(Config{UpstreamURL: upstream.URL, ResponseHeaderTimeout: 100 * time.Millisecond}, nil)
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	proxy.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "http://gateway/slow", nil))
	if response.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", response.Code)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("upstream request did not start")
	}
	if got := proxy.ActiveRequests(); got != 0 {
		t.Fatalf("active requests = %d, want 0 after header timeout", got)
	}
}

func TestProxyHeaderTimeoutDoesNotLimitResponseBodyAfterHeaders(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		time.Sleep(180 * time.Millisecond)
		_, _ = io.WriteString(w, "late body")
	}))
	defer upstream.Close()
	proxy, err := NewProxy(Config{UpstreamURL: upstream.URL, ResponseHeaderTimeout: 100 * time.Millisecond}, nil)
	if err != nil {
		t.Fatal(err)
	}
	gateway := httptest.NewServer(proxy)
	defer gateway.Close()
	resp, err := http.Get(gateway.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil || string(body) != "late body" {
		t.Fatalf("body=%q err=%v, want late body after headers", body, err)
	}
}

func TestProxyRejectsOversizedUpstreamResponseHeaders(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Large", strings.Repeat("x", (1<<20)+1))
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()
	proxy, err := NewProxy(Config{UpstreamURL: upstream.URL}, nil)
	if err != nil {
		t.Fatal(err)
	}
	gateway := httptest.NewServer(proxy)
	defer gateway.Close()
	resp, err := http.Get(gateway.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 for oversized upstream headers", resp.StatusCode)
	}
}

func startResourceLimitServer(t *testing.T, overrides ServerConfig, handler http.Handler) (*Server, *http.Client) {
	t.Helper()
	overrides.Address = "127.0.0.1:0"
	server, err := NewServer(handler, overrides)
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	})
	return server, &http.Client{Timeout: 3 * time.Second}
}
