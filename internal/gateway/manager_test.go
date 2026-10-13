package gateway

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	pb "github.com/SShogun/GateHouse/gen/go/gatehouse/v1"
	"github.com/SShogun/GateHouse/internal/config"
	"google.golang.org/protobuf/proto"
)

func TestManagerWaitsForInitialSnapshotAndKeepsLastGood(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "active") }))
	defer upstream.Close()
	manager, err := NewManager(config.Listener{Address: "127.0.0.1:8080"}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()

	response := httptest.NewRecorder()
	manager.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "http://gateway/", nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("without a snapshot status=%d, want 503", response.Code)
	}

	config := managerCandidate("127.0.0.1:8080", upstream.URL)
	if err := manager.Activate(1, candidateHash(t, config), config); err != nil {
		t.Fatal(err)
	}
	bad := managerCandidate("127.0.0.1:8080", "not a URL")
	if err := manager.Activate(2, "bad-hash", bad); err == nil {
		t.Fatal("invalid candidate activated")
	}
	response = httptest.NewRecorder()
	manager.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "http://gateway/", nil))
	if response.Code != http.StatusOK || response.Body.String() != "active" {
		t.Fatalf("last-known-good response=%d %q", response.Code, response.Body.String())
	}
}

func TestManagerPinsSnapshotForInFlightRequest(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	upstreamA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		_, _ = io.WriteString(w, "revision-a")
	}))
	defer upstreamA.Close()
	upstreamB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "revision-b") }))
	defer upstreamB.Close()
	manager, err := NewManager(config.Listener{Address: "127.0.0.1:8080"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	configA := managerCandidate("127.0.0.1:8080", upstreamA.URL)
	if err := manager.Activate(1, candidateHash(t, configA), configA); err != nil {
		t.Fatal(err)
	}

	oldResponse := httptest.NewRecorder()
	oldDone := make(chan struct{})
	go func() {
		defer close(oldDone)
		manager.ServeHTTP(oldResponse, httptest.NewRequest(http.MethodGet, "http://gateway/", nil))
	}()
	<-started
	configB := managerCandidate("127.0.0.1:8080", upstreamB.URL)
	if err := manager.Activate(2, candidateHash(t, configB), configB); err != nil {
		t.Fatal(err)
	}
	close(release)
	<-oldDone
	if oldResponse.Body.String() != "revision-a" {
		t.Fatalf("in-flight request returned %q, want revision-a", oldResponse.Body.String())
	}
	newResponse := httptest.NewRecorder()
	manager.ServeHTTP(newResponse, httptest.NewRequest(http.MethodGet, "http://gateway/", nil))
	if newResponse.Body.String() != "revision-b" {
		t.Fatalf("new request returned %q, want revision-b", newResponse.Body.String())
	}
}

func TestManagerRejectsStaleConflictingAndListenerChangingSnapshots(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "active") }))
	defer upstream.Close()
	manager, err := NewManager(config.Listener{Address: "127.0.0.1:8080"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	first := managerCandidate("127.0.0.1:8080", upstream.URL)
	firstHash := candidateHash(t, first)
	if err := manager.Activate(5, firstHash, first); err != nil {
		t.Fatal(err)
	}
	if err := manager.Activate(5, firstHash, first); err != nil {
		t.Fatalf("same revision and hash should be idempotent: %v", err)
	}
	changed := managerCandidate("127.0.0.1:8080", "http://127.0.0.1:8081")
	if err := manager.Activate(5, candidateHash(t, changed), changed); !errors.Is(err, ErrRevisionIntegrity) {
		t.Fatalf("same revision with different hash error=%v", err)
	}
	if err := manager.Activate(4, firstHash, first); !errors.Is(err, ErrStaleRevision) {
		t.Fatalf("stale revision error=%v", err)
	}
	mutatedListener := managerCandidate("127.0.0.1:8081", upstream.URL)
	if err := manager.Activate(6, candidateHash(t, mutatedListener), mutatedListener); !errors.Is(err, ErrListenerMutation) {
		t.Fatalf("listener mutation error=%v", err)
	}
	if status := manager.Status(); !status.Ready || status.Revision != 5 || status.Hash != firstHash {
		t.Fatalf("rejected snapshots changed active status: %+v", status)
	}
}

func TestManagerReportsStaleControlPlaneWithoutDroppingActiveSnapshot(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "active") }))
	defer upstream.Close()
	manager, err := NewManager(config.Listener{Address: "127.0.0.1:8080"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	candidate := managerCandidate("127.0.0.1:8080", upstream.URL)
	if err := manager.Activate(1, candidateHash(t, candidate), candidate); err != nil {
		t.Fatal(err)
	}
	manager.Heartbeat()
	if status := manager.Status(); !status.Ready || !status.Connected || status.Stale {
		t.Fatalf("connected status=%+v", status)
	}
	manager.SetConnected(false)
	if status := manager.Status(); !status.Ready || status.Connected || !status.Stale {
		t.Fatalf("disconnected status=%+v", status)
	}
	response := httptest.NewRecorder()
	manager.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "http://gateway/", nil))
	if response.Code != http.StatusOK || response.Body.String() != "active" {
		t.Fatalf("stale connection disrupted active snapshot: %d %q", response.Code, response.Body.String())
	}
}

func TestManagerRetainsClusterHealthAcrossABATransitionWithInflightRequest(t *testing.T) {
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	var healthyRequests atomic.Int32
	endpointOne := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "one") }))
	defer endpointOne.Close()
	endpointTwo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if healthyRequests.Add(1) == 1 {
			close(firstStarted)
			<-releaseFirst
		}
		_, _ = io.WriteString(w, "two")
	}))
	defer endpointTwo.Close()
	endpointB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "b") }))
	defer endpointB.Close()
	manager, err := NewManager(config.Listener{Address: "127.0.0.1:8080"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	a := managerCandidate("127.0.0.1:8080", endpointOne.URL)
	a.Clusters[0].Endpoints = append(a.Clusters[0].Endpoints, &pb.Endpoint{Id: "two", Address: endpointTwo.URL, Weight: 1})
	if err := manager.Activate(1, candidateHash(t, a), a); err != nil {
		t.Fatal(err)
	}
	if err := manager.active.gateway.MarkResult("backend", "one", false); err != nil {
		t.Fatal(err)
	}
	oldResponse := httptest.NewRecorder()
	oldDone := make(chan struct{})
	go func() {
		defer close(oldDone)
		manager.ServeHTTP(oldResponse, httptest.NewRequest(http.MethodGet, "http://gateway/", nil))
	}()
	<-firstStarted
	b := managerCandidate("127.0.0.1:8080", endpointB.URL)
	if err := manager.Activate(2, candidateHash(t, b), b); err != nil {
		t.Fatal(err)
	}
	if err := manager.Activate(3, candidateHash(t, a), a); err != nil {
		t.Fatal(err)
	}
	newResponse := httptest.NewRecorder()
	manager.ServeHTTP(newResponse, httptest.NewRequest(http.MethodGet, "http://gateway/", nil))
	if newResponse.Body.String() != "two" || healthyRequests.Load() != 2 {
		t.Fatalf("A→B→A lost retained health state: response=%q endpoint-two-requests=%d", newResponse.Body.String(), healthyRequests.Load())
	}
	close(releaseFirst)
	<-oldDone
	if oldResponse.Body.String() != "two" {
		t.Fatalf("in-flight A request returned %q", oldResponse.Body.String())
	}
}

func TestManagerBoundsHealthRuntimeAcross1000Revisions(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer upstream.Close()
	manager, err := NewManager(config.Listener{Address: "127.0.0.1:8080"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	candidate := managerCandidate("127.0.0.1:8080", upstream.URL)
	candidate.Clusters[0].HealthCheck = &pb.HealthCheck{Path: "/ready", IntervalNs: int64(time.Hour), TimeoutNs: int64(time.Second)}
	if err := manager.Activate(1, candidateHash(t, candidate), candidate); err != nil {
		_ = manager.Close()
		t.Fatal(err)
	}
	runtime.GC()
	var baseline runtime.MemStats
	runtime.ReadMemStats(&baseline)
	baselineGoroutines := runtime.NumGoroutine()
	manager.registry.mu.Lock()
	var retained *clusterRuntime
	for _, value := range manager.registry.entries {
		retained = value
	}
	manager.registry.mu.Unlock()
	if retained == nil || retained.scheduler == nil || retained.scheduler.Count() != 1 {
		_ = manager.Close()
		t.Fatalf("initial runtime has unexpected health scheduler: %#v", retained)
	}

	for revision := int64(2); revision <= 1000; revision++ {
		next := proto.Clone(candidate).(*pb.GatewayConfig)
		next.Routes[0].Path = fmt.Sprintf("/revision-%d", revision)
		if err := manager.Activate(revision, candidateHash(t, next), next); err != nil {
			_ = manager.Close()
			t.Fatalf("activate revision %d: %v", revision, err)
		}
		if revision%100 == 0 {
			manager.registry.mu.Lock()
			entries := len(manager.registry.entries)
			current := manager.registry.entries[retained.specHash]
			manager.registry.mu.Unlock()
			if entries != 1 || current != retained || retained.refs != 1 || retained.scheduler.Count() != 1 {
				_ = manager.Close()
				t.Fatalf("revision %d retained entries=%d same-runtime=%t refs=%d probes=%d", revision, entries, current == retained, retained.refs, retained.scheduler.Count())
			}
		}
	}
	runtime.GC()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	heapGrowth := int64(after.HeapAlloc) - int64(baseline.HeapAlloc)
	if heapGrowth > 24<<20 {
		_ = manager.Close()
		t.Fatalf("1000 health-enabled revisions grew live heap by %d bytes, limit is 25165824", heapGrowth)
	}
	goroutines := runtime.NumGoroutine()
	if goroutines > baselineGoroutines+8 {
		_ = manager.Close()
		t.Fatalf("1000 health-enabled revisions grew goroutines from %d to %d, limit is baseline +8", baselineGoroutines, goroutines)
	}
	t.Logf("1000 health-enabled revisions: live heap growth %d bytes; goroutines %d -> %d (limit baseline +8); retained clusters 1, probes 1", heapGrowth, baselineGoroutines, goroutines)
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestDynamicReadinessAndStatusEndpoints(t *testing.T) {
	manager, err := NewManager(config.Listener{Address: "127.0.0.1:8080"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	ready := httptest.NewRecorder()
	manager.ServeHTTP(ready, httptest.NewRequest(http.MethodGet, "http://gateway/_gatehouse/readyz", nil))
	if ready.Code != http.StatusServiceUnavailable {
		t.Fatalf("unconfigured readiness status=%d", ready.Code)
	}
	status := httptest.NewRecorder()
	manager.ServeHTTP(status, httptest.NewRequest(http.MethodGet, "http://gateway/_gatehouse/status", nil))
	if status.Code != http.StatusOK || !strings.Contains(status.Body.String(), `"ready":false`) || !strings.Contains(status.Body.String(), `"stale":true`) {
		t.Fatalf("unconfigured status response=%d %s", status.Code, status.Body.String())
	}
}

func managerCandidate(listener, upstream string) *pb.GatewayConfig {
	return &pb.GatewayConfig{
		SchemaVersion: 1,
		Listener:      &pb.Listener{Address: listener},
		Routes:        []*pb.Route{{Id: "root", Path: "/", PathType: pb.PathType_PATH_TYPE_PREFIX, ClusterId: "backend"}},
		Clusters:      []*pb.Cluster{{Id: "backend", Policy: pb.SelectionPolicy_SELECTION_POLICY_ROUND_ROBIN, Endpoints: []*pb.Endpoint{{Id: "one", Address: upstream, Weight: 1}}}},
	}
}

func candidateHash(t *testing.T, candidate *pb.GatewayConfig) string {
	t.Helper()
	_, hash, err := config.EncodeCandidate(candidate)
	if err != nil {
		t.Fatal(err)
	}
	return hash
}
