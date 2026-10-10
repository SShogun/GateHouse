package dataplane

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"net/textproto"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestProxyForwardsRequestAndResponse(t *testing.T) {
	type receivedRequest struct {
		method string
		path   string
		header string
		body   string
	}
	gotRequest := make(chan receivedRequest, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read upstream request body: %v", err)
		}
		gotRequest <- receivedRequest{
			method: r.Method,
			path:   r.URL.RequestURI(),
			header: r.Header.Get("X-Request-ID"),
			body:   string(body),
		}
		w.Header().Set("X-Upstream", "present")
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, "upstream response")
	}))
	defer upstream.Close()

	proxy, err := NewProxy(Config{UpstreamURL: upstream.URL}, nil)
	if err != nil {
		t.Fatal(err)
	}
	gateway := httptest.NewServer(proxy)
	defer gateway.Close()

	req, err := http.NewRequest(http.MethodPatch, gateway.URL+"/widgets/42?view=full", strings.NewReader("payload"))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Request-ID", "request-123")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}

	if resp.StatusCode != http.StatusCreated {
		t.Errorf("response status = %d, want %d", resp.StatusCode, http.StatusCreated)
	}
	if got := resp.Header.Get("X-Upstream"); got != "present" {
		t.Errorf("response X-Upstream = %q, want present", got)
	}
	if got := string(body); got != "upstream response" {
		t.Errorf("response body = %q, want upstream response", got)
	}

	got := <-gotRequest
	if got.method != http.MethodPatch || got.path != "/widgets/42?view=full" || got.header != "request-123" || got.body != "payload" {
		t.Errorf("upstream request = %+v, want PATCH /widgets/42?view=full, X-Request-ID request-123, body payload", got)
	}
}

func TestProxyAbortLogsFailureOutcome(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100")
		_, _ = io.WriteString(w, "x")
	}))
	defer upstream.Close()

	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	proxy, err := NewProxy(Config{UpstreamURL: upstream.URL}, logger)
	if err != nil {
		t.Fatal(err)
	}
	gateway := httptest.NewServer(proxy)
	defer gateway.Close()

	resp, requestErr := http.Get(gateway.URL + "/truncated")
	if resp != nil {
		_, requestErr = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}
	if err := waitForActiveRequests(proxy, 0, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	if requestErr == nil {
		t.Fatal("client read a complete response despite upstream truncation")
	}

	var record map[string]any
	if err := json.Unmarshal(logs.Bytes(), &record); err != nil {
		t.Fatalf("decode structured request outcome %q: %v", logs.String(), err)
	}
	if record["outcome"] != "failure" && record["outcome"] != "abort" {
		t.Fatalf("structured request outcome = %v, want failure or abort; log = %s", record["outcome"], logs.String())
	}
}

func TestProxyForwardsInformationalAndFinalResponses(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Link", "</app.css>; rel=preload")
		w.Header().Set("Connection", "X-Interim-Hop, Keep-Alive")
		w.Header().Set("X-Interim-Hop", "must-not-reach-client")
		w.Header().Set("Keep-Alive", "timeout=5")
		w.WriteHeader(http.StatusEarlyHints)
		w.Header().Set("X-Final", "present")
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, "created")
	}))
	defer upstream.Close()

	proxy, err := NewProxy(Config{UpstreamURL: upstream.URL}, nil)
	if err != nil {
		t.Fatal(err)
	}
	gateway := httptest.NewServer(proxy)
	defer gateway.Close()

	type interimResponse struct {
		link       string
		connection string
		hopHeader  string
		keepAlive  string
	}
	earlyHints := make(chan interimResponse, 1)
	trace := &httptrace.ClientTrace{
		Got1xxResponse: func(code int, header textproto.MIMEHeader) error {
			if code == http.StatusEarlyHints {
				earlyHints <- interimResponse{
					link:       header.Get("Link"),
					connection: header.Get("Connection"),
					hopHeader:  header.Get("X-Interim-Hop"),
					keepAlive:  header.Get("Keep-Alive"),
				}
			}
			return nil
		},
	}
	ctx := httptrace.WithClientTrace(context.Background(), trace)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, gateway.URL+"/early-hints", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Errorf("final status = %d, want %d", resp.StatusCode, http.StatusCreated)
	}
	if got := resp.Header.Get("X-Final"); got != "present" {
		t.Errorf("final X-Final header = %q, want present", got)
	}
	if string(body) != "created" {
		t.Errorf("final response body = %q, want created", body)
	}
	select {
	case got := <-earlyHints:
		if got.link != "</app.css>; rel=preload" {
			t.Errorf("103 Link header = %q", got.link)
		}
		if got.connection != "" || got.hopHeader != "" || got.keepAlive != "" {
			t.Errorf("103 contains hop-by-hop headers: %+v", got)
		}
	case <-time.After(2 * time.Second):
		t.Error("client did not receive upstream 103 Early Hints")
	}
}

func TestProxyPropagatesClientCancellation(t *testing.T) {
	started := make(chan struct{})
	canceled := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
		close(canceled)
	}))
	defer upstream.Close()

	proxy, err := NewProxy(Config{UpstreamURL: upstream.URL}, nil)
	if err != nil {
		t.Fatal(err)
	}
	gateway := httptest.NewServer(proxy)
	defer gateway.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, gateway.URL+"/blocked", nil)
	if err != nil {
		t.Fatal(err)
	}
	clientDone := make(chan struct{})
	go func() {
		defer close(clientDone)
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			_ = resp.Body.Close()
		}
	}()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("upstream did not receive request")
	}
	cancel()
	select {
	case <-canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("upstream request context was not canceled")
	}
	select {
	case <-clientDone:
	case <-time.After(2 * time.Second):
		t.Fatal("client request did not finish after cancellation")
	}
	if err := waitForActiveRequests(proxy, 0, 2*time.Second); err != nil {
		t.Error(err)
	}
}

func TestConcurrentCancellationsLeaveNoActiveRequestLeak(t *testing.T) {
	const requestCount = 16
	baselineGoroutines := runtime.NumGoroutine()
	started := make(chan struct{}, requestCount)
	canceled := make(chan struct{}, requestCount)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started <- struct{}{}
		<-r.Context().Done()
		canceled <- struct{}{}
	}))
	upstreamClosed := false
	defer func() {
		if !upstreamClosed {
			upstream.Close()
		}
	}()

	proxy, err := NewProxy(Config{UpstreamURL: upstream.URL}, nil)
	if err != nil {
		t.Fatal(err)
	}
	gateway := httptest.NewServer(proxy)
	gatewayClosed := false
	defer func() {
		if !gatewayClosed {
			gateway.Close()
		}
	}()
	clientTransport := &http.Transport{}
	client := &http.Client{Transport: clientTransport}
	defer clientTransport.CloseIdleConnections()
	defer proxy.CloseIdleConnections()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	clientResults := make(chan struct{}, requestCount)
	for range requestCount {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, gateway.URL+"/cancel", nil)
		if err != nil {
			t.Fatal(err)
		}
		go func() {
			resp, err := client.Do(req)
			if err == nil {
				_ = resp.Body.Close()
			}
			clientResults <- struct{}{}
		}()
	}
	for range requestCount {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("not all concurrent requests reached upstream")
		}
	}
	if err := waitForActiveRequests(proxy, requestCount, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	cancel()
	for range requestCount {
		select {
		case <-canceled:
		case <-time.After(2 * time.Second):
			t.Fatal("not all upstream requests observed cancellation")
		}
		select {
		case <-clientResults:
		case <-time.After(2 * time.Second):
			t.Fatal("not all canceled client requests finished")
		}
	}
	if err := waitForActiveRequests(proxy, 0, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	clientTransport.CloseIdleConnections()
	proxy.CloseIdleConnections()
	gateway.Close()
	gatewayClosed = true
	upstream.Close()
	upstreamClosed = true
	if err := waitForGoroutinesAtMost(baselineGoroutines+4, 3*time.Second); err != nil {
		t.Fatal(err)
	}
}

func TestProxyStreamsBeforeUpstreamCompletes(t *testing.T) {
	upstreamDone := make(chan struct{})
	allowFinish := make(chan struct{})
	var finishOnce sync.Once
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "first chunk\n")
		w.(http.Flusher).Flush()
		<-allowFinish
		_, _ = io.WriteString(w, "last chunk\n")
		close(upstreamDone)
	}))
	defer upstream.Close()
	defer finishOnce.Do(func() { close(allowFinish) })

	proxy, err := NewProxy(Config{UpstreamURL: upstream.URL}, nil)
	if err != nil {
		t.Fatal(err)
	}
	gateway := httptest.NewServer(proxy)
	defer gateway.Close()

	resp, err := http.Get(gateway.URL + "/stream")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	line, err := readLine(resp.Body)
	if err != nil {
		t.Fatalf("read first streamed chunk: %v", err)
	}
	if line != "first chunk\n" {
		t.Fatalf("first streamed chunk = %q, want %q", line, "first chunk\n")
	}
	select {
	case <-upstreamDone:
		t.Fatal("upstream finished before the first chunk was observed")
	default:
	}
	finishOnce.Do(func() { close(allowFinish) })
	_, _ = io.Copy(io.Discard, resp.Body)
}

func TestProxyLargeResponsePeakHeapDoesNotScaleWithBodySize(t *testing.T) {
	const chunkSize = 32 << 10
	chunk := strings.Repeat("x", chunkSize)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		size := int64(8 << 20)
		if r.URL.Path == "/large" {
			size = 128 << 20
		}
		for sent := int64(0); sent < size; sent += chunkSize {
			if _, err := io.WriteString(w, chunk); err != nil {
				return
			}
		}
	}))
	defer upstream.Close()

	proxy, err := NewProxy(Config{UpstreamURL: upstream.URL}, nil)
	if err != nil {
		t.Fatal(err)
	}
	gateway := httptest.NewServer(proxy)
	defer gateway.Close()

	measurePeak := func(path string, wantBytes int64) uint64 {
		t.Helper()
		runtime.GC()
		var before runtime.MemStats
		runtime.ReadMemStats(&before)
		peak := before.HeapAlloc
		stop := make(chan struct{})
		samplerStarted := make(chan struct{})
		samplerDone := make(chan struct{})
		go func() {
			defer close(samplerDone)
			ticker := time.NewTicker(time.Millisecond)
			defer ticker.Stop()
			close(samplerStarted)
			for {
				select {
				case <-stop:
					return
				case <-ticker.C:
					var current runtime.MemStats
					runtime.ReadMemStats(&current)
					if current.HeapAlloc > peak {
						peak = current.HeapAlloc
					}
				}
			}
		}()
		<-samplerStarted

		resp, err := http.Get(gateway.URL + path)
		if err != nil {
			close(stop)
			<-samplerDone
			t.Fatal(err)
		}
		count, copyErr := io.Copy(io.Discard, resp.Body)
		closeErr := resp.Body.Close()
		close(stop)
		<-samplerDone
		if copyErr != nil {
			t.Fatal(copyErr)
		}
		if closeErr != nil {
			t.Fatal(closeErr)
		}
		if count != wantBytes {
			t.Fatalf("streamed bytes for %s = %d, want %d", path, count, wantBytes)
		}
		var after runtime.MemStats
		runtime.ReadMemStats(&after)
		if after.HeapAlloc > peak {
			peak = after.HeapAlloc
		}
		return peak - before.HeapAlloc
	}

	smallPeak := measurePeak("/small", 8<<20)
	largePeak := measurePeak("/large", 128<<20)
	const allowedGrowth = 24 << 20
	t.Logf("peak heap above baseline: 8 MiB body=%d bytes, 128 MiB body=%d bytes", smallPeak, largePeak)
	if largePeak > smallPeak+allowedGrowth {
		t.Fatalf("peak heap grew from %d bytes for 8 MiB to %d bytes for 128 MiB; growth exceeds %d-byte allowance", smallPeak, largePeak, allowedGrowth)
	}
}

func TestProxyStripsHopByHopRequestHeaders(t *testing.T) {
	gotConnectionTokenHeader := make(chan string, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotConnectionTokenHeader <- r.Header.Get("X-Hop-By-Hop")
		w.Header().Set("Connection", "X-Upstream-Hop")
		w.Header().Set("X-Upstream-Hop", "must-not-reach-client")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()

	proxy, err := NewProxy(Config{UpstreamURL: upstream.URL}, nil)
	if err != nil {
		t.Fatal(err)
	}
	gateway := httptest.NewServer(proxy)
	defer gateway.Close()

	req, err := http.NewRequest(http.MethodGet, gateway.URL+"/headers", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Connection", "X-Hop-By-Hop")
	req.Header.Set("X-Hop-By-Hop", "must-not-forward")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if got := <-gotConnectionTokenHeader; got != "" {
		t.Errorf("upstream received hop-by-hop header value %q", got)
	}
	if got := resp.Header.Get("X-Upstream-Hop"); got != "" {
		t.Errorf("client received hop-by-hop response header value %q", got)
	}
}

func readLine(r io.Reader) (string, error) {
	var line strings.Builder
	var one [1]byte
	for {
		if _, err := r.Read(one[:]); err != nil {
			return line.String(), err
		}
		line.WriteByte(one[0])
		if one[0] == '\n' {
			return line.String(), nil
		}
	}
}

func waitForActiveRequests(proxy *Proxy, want int64, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for proxy.ActiveRequests() != want && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := proxy.ActiveRequests(); got != want {
		return fmt.Errorf("active requests = %d, want %d", got, want)
	}
	return nil
}

func waitForGoroutinesAtMost(limit int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		runtime.GC()
		if got := runtime.NumGoroutine(); got <= limit {
			return nil
		} else if time.Now().After(deadline) {
			return fmt.Errorf("goroutines after cleanup = %d, want at most %d", got, limit)
		}
		time.Sleep(25 * time.Millisecond)
	}
}
