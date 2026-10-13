package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	pb "github.com/SShogun/GateHouse/gen/go/gatehouse/v1"
	"github.com/SShogun/GateHouse/internal/config"
	controlclient "github.com/SShogun/GateHouse/internal/control/client"
	"github.com/SShogun/GateHouse/internal/gateway"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func TestControlPlaneRestartKeepsDataPlaneRequestAndRecoversRevision(t *testing.T) {
	rawURL := os.Getenv("GATEHOUSE_TEST_DATABASE_URL")
	if rawURL == "" {
		t.Skip("set GATEHOUSE_TEST_DATABASE_URL and GATEHOUSE_TEST_DB_CONFIRM=gatehouse_test to run PostgreSQL process integration tests")
	}
	if os.Getenv("GATEHOUSE_TEST_DB_CONFIRM") != "gatehouse_test" {
		t.Fatal("refusing PostgreSQL process test without GATEHOUSE_TEST_DB_CONFIRM=gatehouse_test")
	}
	baseConfig, err := pgxpool.ParseConfig(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	if baseConfig.ConnConfig.Database != "gatehouse_test" {
		t.Fatalf("refusing database %q; process test requires gatehouse_test", baseConfig.ConnConfig.Database)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	adminPool, err := pgxpool.NewWithConfig(ctx, baseConfig)
	if err != nil {
		t.Fatal(err)
	}
	if err := adminPool.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("gatehouse_restart_%d_%d", os.Getpid(), time.Now().UnixNano())
	if _, err := adminPool.Exec(ctx, "CREATE SCHEMA "+(pgx.Identifier{schema}).Sanitize()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		if _, err := adminPool.Exec(cleanupCtx, "DROP SCHEMA "+(pgx.Identifier{schema}).Sanitize()+" CASCADE"); err != nil {
			t.Errorf("drop isolated restart-test schema: %v", err)
		}
		adminPool.Close()
	})
	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	query := parsedURL.Query()
	query.Set("search_path", schema)
	parsedURL.RawQuery = query.Encode()
	databaseProxy := newPostgresProxy(t, net.JoinHostPort(baseConfig.ConnConfig.Host, strconv.Itoa(int(baseConfig.ConnConfig.Port))))
	parsedURL.Host = databaseProxy.listener.Addr().String()
	databaseURL := parsedURL.String()

	reserved, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := reserved.Addr().String()
	if err := reserved.Close(); err != nil {
		t.Fatal(err)
	}
	controlBinary := filepath.Join(t.TempDir(), "gatehouse-control")
	build := exec.CommandContext(ctx, "go", "build", "-o", controlBinary, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build control-plane executable: %v\n%s", err, output)
	}
	startControl := func() *exec.Cmd {
		cmd := exec.Command(controlBinary)
		cmd.Env = append(os.Environ(),
			"GATEHOUSE_CONTROL_DEV_MODE=loopback",
			"GATEHOUSE_CONTROL_LISTEN_ADDR="+address,
			"GATEHOUSE_DATABASE_URL="+databaseURL,
		)
		cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
		if err := cmd.Start(); err != nil {
			t.Fatalf("start control-plane executable: %v", err)
		}
		return cmd
	}
	stopControl := func(cmd *exec.Cmd) {
		if cmd == nil || cmd.Process == nil {
			return
		}
		_ = cmd.Process.Signal(syscall.SIGTERM)
		waited := make(chan error, 1)
		go func() { waited <- cmd.Wait() }()
		select {
		case err := <-waited:
			if err != nil {
				t.Errorf("control-plane graceful exit: %v", err)
			}
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
			<-waited
			t.Error("control-plane did not stop after SIGTERM")
		}
	}
	control := startControl()
	defer func() {
		if control != nil {
			_ = control.Process.Kill()
			_ = control.Wait()
		}
	}()
	connection, err := grpc.NewClient(address, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	adminClient := pb.NewAdminServiceClient(connection)

	upstreamStarted := make(chan struct{})
	releaseUpstream := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseUpstream) }) }
	var firstUpstreamRequest atomic.Bool
	upstreamA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if firstUpstreamRequest.CompareAndSwap(false, true) {
			close(upstreamStarted)
			<-releaseUpstream
		}
		_, _ = io.WriteString(w, "revision-a")
	}))
	defer upstreamA.Close()
	defer release()
	upstreamB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "revision-b") }))
	defer upstreamB.Close()
	manager, err := gateway.NewManager(config.Listener{Address: "127.0.0.1:8080"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	dataplaneClient, err := controlclient.NewControlClient(controlclient.ControlClientConfig{
		Endpoint: address, InstanceID: "restart-test-dp", BootID: "boot-restart-test", Build: "integration",
		Manager: manager, HeartbeatInterval: time.Second, ReconnectMin: 50 * time.Millisecond, ReconnectMax: 100 * time.Millisecond,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	clientCtx, cancelClient := context.WithCancel(context.Background())
	clientDone := make(chan struct{})
	go func() {
		defer close(clientDone)
		_ = dataplaneClient.Run(clientCtx)
	}()
	defer func() {
		cancelClient()
		select {
		case <-clientDone:
		case <-time.After(3 * time.Second):
			t.Error("data-plane client did not stop")
		}
	}()

	configA := restartCandidate(upstreamA.URL)
	responseA := publishWithRetry(t, ctx, adminClient, 0, configA)
	waitUntil(t, ctx, func() bool { return manager.Status().Ready && manager.Status().Revision == responseA.Revision.Revision })

	requestResult := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		recorder := httptest.NewRecorder()
		manager.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "http://gateway/", nil))
		requestResult <- recorder
	}()
	select {
	case <-upstreamStarted:
	case <-ctx.Done():
		t.Fatal("long request never reached upstream")
	}

	if err := control.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := control.Wait(); err == nil {
		t.Fatal("control-plane process unexpectedly exited successfully after forced restart")
	}
	restartAt := time.Now()
	control = startControl()
	waitUntil(t, ctx, func() bool {
		state := manager.Status()
		return state.Connected && state.Revision == responseA.Revision.Revision && state.LastHeartbeat.After(restartAt)
	})
	var instances *pb.ListInstancesResponse
	waitUntil(t, ctx, func() bool {
		requestCtx, requestCancel := context.WithTimeout(ctx, 250*time.Millisecond)
		defer requestCancel()
		response, listErr := adminClient.ListInstances(requestCtx, &pb.ListInstancesRequest{Limit: 10})
		if listErr != nil || len(response.Instances) != 1 {
			return false
		}
		instances = response
		return response.Instances[0].Connected && response.Instances[0].ActiveRevision == responseA.Revision.Revision && response.Instances[0].LastHeartbeatUnixMs >= restartAt.UnixMilli()
	})
	if len(instances.Instances) != 1 || !instances.Instances[0].Connected || instances.Instances[0].ActiveRevision != responseA.Revision.Revision {
		t.Fatalf("instance did not reconnect on durable revision: %+v", instances.Instances)
	}

	configB := restartCandidate(upstreamB.URL)
	databaseProxy.SetAvailable(false)
	outageCtx, cancelOutage := context.WithTimeout(ctx, 2*time.Second)
	_, outageErr := adminClient.PublishConfig(outageCtx, &pb.PublishConfigRequest{ExpectedParentRevision: responseA.Revision.Revision, Config: configB, Actor: "restart-integration", Reason: "database outage"})
	cancelOutage()
	if outageErr == nil {
		t.Fatal("publication unexpectedly succeeded while PostgreSQL was unavailable")
	}
	if manager.Status().Revision != responseA.Revision.Revision {
		t.Fatalf("database outage changed active data-plane revision: %+v", manager.Status())
	}
	duringOutage := httptest.NewRecorder()
	manager.ServeHTTP(duringOutage, httptest.NewRequest(http.MethodGet, "http://gateway/", nil))
	if duringOutage.Code != http.StatusOK || duringOutage.Body.String() != "revision-a" {
		t.Fatalf("database outage disrupted new requests: status=%d body=%q", duringOutage.Code, duringOutage.Body.String())
	}
	select {
	case <-requestResult:
		t.Fatal("in-flight request ended before the database outage was recovered")
	default:
	}
	databaseProxy.SetAvailable(true)
	responseB := publishWithRetry(t, ctx, adminClient, responseA.Revision.Revision, configB)
	waitUntil(t, ctx, func() bool { return manager.Status().Revision == responseB.Revision.Revision })
	newResponse := httptest.NewRecorder()
	manager.ServeHTTP(newResponse, httptest.NewRequest(http.MethodGet, "http://gateway/", nil))
	if newResponse.Code != http.StatusOK || newResponse.Body.String() != "revision-b" {
		t.Fatalf("post-restart publication response=%d %q", newResponse.Code, newResponse.Body.String())
	}
	release()
	select {
	case oldResponse := <-requestResult:
		if oldResponse.Code != http.StatusOK || oldResponse.Body.String() != "revision-a" {
			t.Fatalf("in-flight response changed across control-plane restart: status=%d body=%q", oldResponse.Code, oldResponse.Body.String())
		}
	case <-ctx.Done():
		t.Fatal("in-flight request did not finish after control-plane restart")
	}

	cancelClient()
	<-clientDone
	stopControl(control)
	control = nil
}

func restartCandidate(upstream string) *pb.GatewayConfig {
	return &pb.GatewayConfig{
		SchemaVersion: 1,
		Listener:      &pb.Listener{Address: "127.0.0.1:8080"},
		Routes:        []*pb.Route{{Id: "root", Path: "/", PathType: pb.PathType_PATH_TYPE_PREFIX, ClusterId: "backend"}},
		Clusters:      []*pb.Cluster{{Id: "backend", Policy: pb.SelectionPolicy_SELECTION_POLICY_ROUND_ROBIN, Endpoints: []*pb.Endpoint{{Id: "one", Address: upstream, Weight: 1}}}},
	}
}

func publishWithRetry(t *testing.T, ctx context.Context, client pb.AdminServiceClient, parent int64, candidate *pb.GatewayConfig) *pb.PublishConfigResponse {
	t.Helper()
	var latestErr error
	for {
		requestCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
		response, err := client.PublishConfig(requestCtx, &pb.PublishConfigRequest{ExpectedParentRevision: parent, Config: candidate, Actor: "restart-integration", Reason: "restart proof"})
		cancel()
		if err == nil {
			return response
		}
		latestErr = err
		select {
		case <-ctx.Done():
			t.Fatalf("publish after retries: %v (last error %v)", ctx.Err(), latestErr)
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func waitUntil(t *testing.T, ctx context.Context, condition func() bool) {
	t.Helper()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if condition() {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("condition was not reached before deadline")
		case <-ticker.C:
		}
	}
}

type postgresProxy struct {
	listener    net.Listener
	destination string
	mu          sync.Mutex
	available   bool
	connections map[net.Conn]struct{}
}

func newPostgresProxy(t *testing.T, destination string) *postgresProxy {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	proxy := &postgresProxy{listener: listener, destination: destination, available: true, connections: make(map[net.Conn]struct{})}
	t.Cleanup(proxy.Close)
	go proxy.accept()
	return proxy
}

func (p *postgresProxy) accept() {
	for {
		connection, err := p.listener.Accept()
		if err != nil {
			return
		}
		p.mu.Lock()
		if !p.available {
			p.mu.Unlock()
			_ = connection.Close()
			continue
		}
		p.connections[connection] = struct{}{}
		p.mu.Unlock()
		go p.forward(connection)
	}
}

func (p *postgresProxy) forward(client net.Conn) {
	backend, err := net.DialTimeout("tcp", p.destination, time.Second)
	if err != nil {
		_ = client.Close()
		p.remove(client)
		return
	}
	p.mu.Lock()
	p.connections[backend] = struct{}{}
	p.mu.Unlock()
	copyDone := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(backend, client); copyDone <- struct{}{} }()
	go func() { _, _ = io.Copy(client, backend); copyDone <- struct{}{} }()
	<-copyDone
	_ = client.Close()
	_ = backend.Close()
	p.remove(client)
	p.remove(backend)
}

func (p *postgresProxy) remove(connection net.Conn) {
	p.mu.Lock()
	delete(p.connections, connection)
	p.mu.Unlock()
}

func (p *postgresProxy) SetAvailable(available bool) {
	p.mu.Lock()
	p.available = available
	if !available {
		for connection := range p.connections {
			_ = connection.Close()
		}
	}
	p.mu.Unlock()
}

func (p *postgresProxy) Close() {
	_ = p.listener.Close()
	p.SetAvailable(false)
}
