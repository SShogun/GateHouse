package gateway

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SShogun/GateHouse/internal/cluster"
	"github.com/SShogun/GateHouse/internal/config"
	"github.com/SShogun/GateHouse/internal/router"
)

func TestGatewayRoutesAndLogsStableIDs(t *testing.T) {
	upstreamA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "a") }))
	defer upstreamA.Close()
	upstreamB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "b") }))
	defer upstreamB.Close()
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	gw, err := New(config.Config{
		Listener: config.Listener{Address: "127.0.0.1:0"},
		Routes: []router.Route{
			{ID: "route-a", Path: "/a", PathType: router.PathPrefix, ClusterID: "cluster-a"},
			{ID: "route-b", Path: "/b", PathType: router.PathPrefix, ClusterID: "cluster-b"},
		},
		Clusters: []config.Cluster{
			{ID: "cluster-a", Policy: cluster.RoundRobin, Endpoints: []cluster.Endpoint{{ID: "endpoint-a", Address: upstreamA.URL, Weight: 1}}},
			{ID: "cluster-b", Policy: cluster.RoundRobin, Endpoints: []cluster.Endpoint{{ID: "endpoint-b", Address: upstreamB.URL, Weight: 1}}},
		},
	}, logger)
	if err != nil {
		t.Fatal(err)
	}
	defer gw.Close()

	rec := httptest.NewRecorder()
	gw.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://gateway/b/resource", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "b" {
		t.Fatalf("response = %d %q", rec.Code, rec.Body.String())
	}
	for _, id := range []string{"route-b", "cluster-b", "endpoint-b"} {
		if !strings.Contains(logs.String(), `"`+map[string]string{"route-b": "route_id", "cluster-b": "cluster_id", "endpoint-b": "endpoint_id"}[id]+`":"`+id+`"`) {
			t.Errorf("structured telemetry missing %s: %s", id, logs.String())
		}
	}
}

func TestValidUpstream502IsNotLabeledAsGatewayFailure(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "upstream error", http.StatusBadGateway)
	}))
	defer upstream.Close()
	var logs bytes.Buffer
	gw, err := New(config.Config{
		Listener: config.Listener{Address: "127.0.0.1:0"},
		Routes:   []router.Route{{ID: "r", Path: "/", PathType: router.PathPrefix, ClusterID: "c"}},
		Clusters: []config.Cluster{{ID: "c", Policy: cluster.RoundRobin, Endpoints: []cluster.Endpoint{{ID: "e", Address: upstream.URL, Weight: 1}}}},
	}, slog.New(slog.NewJSONHandler(&logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer gw.Close()

	rec := httptest.NewRecorder()
	gw.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://gateway/", nil))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
	for _, line := range strings.Split(logs.String(), "\n") {
		if strings.Contains(line, `"msg":"gateway request complete"`) && strings.Contains(line, `"outcome":"failure"`) {
			t.Fatalf("valid upstream status was mislabeled as a gateway failure: %s", line)
		}
	}
}

func TestAbortedRequestLogRetainsRouteAndClusterIDs(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100")
		_, _ = io.WriteString(w, "x")
	}))
	defer upstream.Close()
	var logs bytes.Buffer
	gw, err := New(config.Config{
		Listener: config.Listener{Address: "127.0.0.1:0"},
		Routes:   []router.Route{{ID: "route-abort", Path: "/", PathType: router.PathPrefix, ClusterID: "cluster-abort"}},
		Clusters: []config.Cluster{{ID: "cluster-abort", Policy: cluster.RoundRobin, Endpoints: []cluster.Endpoint{{ID: "endpoint-abort", Address: upstream.URL, Weight: 1}}}},
	}, slog.New(slog.NewJSONHandler(&logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer gw.Close()
	handlerDone := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(handlerDone)
		gw.ServeHTTP(w, r)
	}))
	defer server.Close()
	response, err := http.Get(server.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	_, bodyErr := io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	if bodyErr == nil {
		t.Fatal("truncated upstream body was read as a complete response")
	}
	select {
	case <-handlerDone:
	case <-time.After(time.Second):
		t.Fatal("gateway handler did not finish after abort")
	}
	for _, want := range []string{`"msg":"gateway request complete"`, `"route_id":"route-abort"`, `"cluster_id":"cluster-abort"`, `"endpoint_id":"endpoint-abort"`} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("abort telemetry missing %s: %s", want, logs.String())
		}
	}
}

func TestGatewayRejectsInvalidConfig(t *testing.T) {
	_, err := New(config.Config{}, nil)
	if err == nil {
		t.Fatal("New accepted config with no clusters")
	}
}

func TestHealthProbeSchedulerDoesNotPreemptConfiguredTimeout(t *testing.T) {
	observed := make(chan time.Duration, 1)
	client := &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		deadline, ok := r.Context().Deadline()
		if !ok {
			t.Error("probe request has no deadline")
		} else {
			observed <- time.Until(deadline)
		}
		return &http.Response{StatusCode: http.StatusNoContent, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
	})}
	gw, err := NewWithOptions(config.Config{
		Listener: config.Listener{Address: "127.0.0.1:0"},
		Clusters: []config.Cluster{{ID: "c", Policy: cluster.RoundRobin, Endpoints: []cluster.Endpoint{{ID: "e", Address: "http://upstream.test", Weight: 1}}, HealthCheck: &config.HealthCheck{Path: "/health", Interval: time.Hour, Timeout: 8 * time.Second}}},
	}, nil, Options{ProbeClient: client})
	if err != nil {
		t.Fatal(err)
	}
	defer gw.Close()
	select {
	case remaining := <-observed:
		if remaining <= 5*time.Second {
			t.Fatalf("scheduler deadline %s preempts configured 8s timeout", remaining)
		}
	case <-time.After(time.Second):
		t.Fatal("scheduled probe did not execute")
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (fn roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return fn(r) }

func TestGatewayCloseStopsHealthScheduler(t *testing.T) {
	probeStarted := make(chan struct{})
	probeDone := make(chan struct{})
	probe := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(probeStarted)
		<-r.Context().Done()
		close(probeDone)
	}))
	defer probe.Close()
	gw, err := NewWithOptions(config.Config{
		Listener: config.Listener{Address: "127.0.0.1:0"},
		Routes:   []router.Route{{ID: "r", Path: "/", PathType: router.PathPrefix, ClusterID: "c"}},
		Clusters: []config.Cluster{{ID: "c", Policy: cluster.RoundRobin, Endpoints: []cluster.Endpoint{{ID: "e", Address: probe.URL, Weight: 1}}, HealthCheck: &config.HealthCheck{Path: "/health", Interval: time.Hour, Timeout: time.Hour}}},
	}, nil, Options{ProbeClient: probe.Client()})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-probeStarted:
	case <-time.After(time.Second):
		t.Fatal("probe did not start")
	}
	if err := gw.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-probeDone:
	case <-time.After(time.Second):
		t.Fatal("scheduler did not cancel probe")
	}
}

func TestUnhealthyEndpointIsExcludedAfterProbe(t *testing.T) {
	probeStarted := make(chan struct{}, 1)
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			select {
			case probeStarted <- struct{}{}:
			default:
			}
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		_, _ = io.WriteString(w, "bad")
	}))
	defer bad.Close()
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "good") }))
	defer good.Close()
	gw, err := NewWithOptions(config.Config{
		Listener: config.Listener{Address: "127.0.0.1:0"},
		Routes:   []router.Route{{ID: "r", Path: "/", PathType: router.PathPrefix, ClusterID: "c"}},
		Clusters: []config.Cluster{{ID: "c", Policy: cluster.RoundRobin, Health: cluster.HealthThresholds{Unhealthy: 1}, Endpoints: []cluster.Endpoint{{ID: "bad", Address: bad.URL, Weight: 1}, {ID: "good", Address: good.URL, Weight: 1}}, HealthCheck: &config.HealthCheck{Path: "/health", Interval: time.Hour, Timeout: time.Second}}},
	}, nil, Options{ProbeClient: bad.Client()})
	if err != nil {
		t.Fatal(err)
	}
	defer gw.Close()
	select {
	case <-probeStarted:
	case <-time.After(time.Second):
		t.Fatal("health probe did not execute")
	}
	deadline := time.Now().Add(time.Second)
	for {
		state, err := gw.clusters["c"].selection.Health("bad")
		if err != nil {
			t.Fatal(err)
		}
		if state == cluster.Unhealthy {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("failing configured probe did not mark endpoint unhealthy; state=%s", state)
		}
		time.Sleep(time.Millisecond)
	}

	rec := httptest.NewRecorder()
	gw.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://gateway/", nil))
	if rec.Body.String() != "good" {
		t.Fatalf("selected unhealthy endpoint: response %q", rec.Body.String())
	}
}

func TestIncompleteSuccessfulHealthResponseMarksEndpointUnhealthy(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.Header().Set("Content-Length", "100")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, "short")
			return
		}
		_, _ = io.WriteString(w, "bad")
	}))
	defer bad.Close()
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		_, _ = io.WriteString(w, "good")
	}))
	defer good.Close()
	gw, err := New(config.Config{
		Listener: config.Listener{Address: "127.0.0.1:0"},
		Routes:   []router.Route{{ID: "r", Path: "/", PathType: router.PathPrefix, ClusterID: "c"}},
		Clusters: []config.Cluster{{ID: "c", Policy: cluster.RoundRobin, Health: cluster.HealthThresholds{Unhealthy: 1}, Endpoints: []cluster.Endpoint{
			{ID: "bad", Address: bad.URL, Weight: 1}, {ID: "good", Address: good.URL, Weight: 1},
		}, HealthCheck: &config.HealthCheck{Path: "/health", Interval: time.Hour, Timeout: time.Second}}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer gw.Close()

	deadline := time.Now().Add(time.Second)
	for {
		state, err := gw.clusters["c"].selection.Health("bad")
		if err != nil {
			t.Fatal(err)
		}
		if state == cluster.Unhealthy {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("incomplete 200 health response left endpoint %s", state)
		}
		time.Sleep(time.Millisecond)
	}
	recorder := httptest.NewRecorder()
	gw.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "http://gateway/", nil))
	if recorder.Body.String() != "good" {
		t.Fatalf("selected endpoint after incomplete probe = %q, want good", recorder.Body.String())
	}
}

func TestConcurrentRequestsAndHealthTransitions(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	defer upstream.Close()
	gw, err := New(config.Config{
		Listener: config.Listener{Address: "127.0.0.1:0"},
		Routes:   []router.Route{{ID: "r", Path: "/", PathType: router.PathPrefix, ClusterID: "c"}},
		Clusters: []config.Cluster{{ID: "c", Policy: cluster.RoundRobin, Health: cluster.HealthThresholds{Healthy: 1, Unhealthy: 1}, Endpoints: []cluster.Endpoint{
			{ID: "a", Address: upstream.URL, Weight: 1}, {ID: "b", Address: upstream.URL, Weight: 1},
		}}},
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer gw.Close()

	const requestCount = 200
	var wg sync.WaitGroup
	wg.Add(requestCount + 1)
	for i := 0; i < requestCount; i++ {
		go func() {
			defer wg.Done()
			response := httptest.NewRecorder()
			gw.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "http://gateway/", nil))
			if response.Code != http.StatusOK {
				t.Errorf("concurrent request status = %d, want 200", response.Code)
			}
		}()
	}
	go func() {
		defer wg.Done()
		for i := 0; i < requestCount; i++ {
			if err := gw.MarkResult("c", "a", i%2 == 0); err != nil {
				t.Errorf("concurrent health transition: %v", err)
				return
			}
		}
	}()
	wg.Wait()
}

func TestGatewayHealthProbeStartupUsesCompleteClusterMap(t *testing.T) {
	const clusterCount = 128
	checked := make(chan struct{}, clusterCount)
	client := &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		checked <- struct{}{}
		return &http.Response{StatusCode: http.StatusNoContent, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
	})}
	cfg := config.Config{Listener: config.Listener{Address: "127.0.0.1:0"}}
	for i := 0; i < clusterCount; i++ {
		clusterID := fmt.Sprintf("cluster-%03d", i)
		cfg.Clusters = append(cfg.Clusters, config.Cluster{
			ID: clusterID, Policy: cluster.RoundRobin,
			Endpoints:   []cluster.Endpoint{{ID: "endpoint", Address: "http://upstream.test", Weight: 1}},
			Health:      cluster.HealthThresholds{Healthy: 1, Unhealthy: 1},
			HealthCheck: &config.HealthCheck{Path: "/health", Interval: time.Hour, Timeout: time.Second},
		})
	}
	gw, err := NewWithOptions(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{ProbeClient: client})
	if err != nil {
		t.Fatal(err)
	}
	defer gw.Close()
	for i := 0; i < clusterCount; i++ {
		select {
		case <-checked:
		case <-time.After(3 * time.Second):
			t.Fatalf("only %d of %d initial health probes ran", i, clusterCount)
		}
	}
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		allHealthy := true
		for _, state := range gw.clusters {
			health, err := state.selection.Health("endpoint")
			if err != nil || health != cluster.Healthy {
				allHealthy = false
				break
			}
		}
		if allHealthy {
			break
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatal("not all clusters became healthy after their initial probes")
		}
	}
}

func TestGatewayHealthProbePreservesEscapedPath(t *testing.T) {
	requestURI := make(chan string, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestURI <- r.RequestURI
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()

	gw, err := New(config.Config{
		Listener: config.Listener{Address: "127.0.0.1:0"},
		Clusters: []config.Cluster{{
			ID: "c", Policy: cluster.RoundRobin,
			Endpoints:   []cluster.Endpoint{{ID: "e", Address: upstream.URL, Weight: 1}},
			HealthCheck: &config.HealthCheck{Path: "/ready%2Fdeep", Interval: time.Hour, Timeout: time.Second},
		}},
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer gw.Close()

	select {
	case got := <-requestURI:
		if got != "/ready%2Fdeep" {
			t.Fatalf("health probe RequestURI = %q, want %q", got, "/ready%2Fdeep")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("initial health probe did not reach the upstream")
	}
}
