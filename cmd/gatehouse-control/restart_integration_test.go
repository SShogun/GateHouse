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
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
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
	// Delay the first ACK until an instance-status read completes. This makes
	// local activation happen before durable ACK processing without sleeps.
	ackBlocked, releaseACK := make(chan struct{}), make(chan struct{})
	var ackBlockedOnce, releaseACKOnce sync.Once
	connection, err := grpc.NewClient(address, grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithUnaryInterceptor(func(ctx context.Context, method string, request, reply any, connection *grpc.ClientConn, invoke grpc.UnaryInvoker, options ...grpc.CallOption) error {
			err := invoke(ctx, method, request, reply, connection, options...)
			if err == nil && method == pb.AdminService_ListInstances_FullMethodName {
				releaseACKOnce.Do(func() { close(releaseACK) })
			}
			return err
		}))
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
	startDataPlaneClient := func(bootID string) (context.CancelFunc, chan struct{}) {
		t.Helper()
		dataplaneClient, clientErr := controlclient.NewControlClient(controlclient.ControlClientConfig{
			Endpoint: address, InstanceID: "restart-test-dp", BootID: bootID, Build: "integration",
			Manager: manager, HeartbeatInterval: time.Second, ReconnectMin: 50 * time.Millisecond, ReconnectMax: 100 * time.Millisecond,
			Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
			DialOptions: []grpc.DialOption{grpc.WithStreamInterceptor(func(ctx context.Context, description *grpc.StreamDesc, connection *grpc.ClientConn, method string, create grpc.Streamer, options ...grpc.CallOption) (grpc.ClientStream, error) {
				stream, err := create(ctx, description, connection, method, options...)
				if err != nil {
					return nil, err
				}
				return &ackGateClientStream{ClientStream: stream, blocked: ackBlocked, release: releaseACK, once: &ackBlockedOnce}, nil
			})},
		})
		if clientErr != nil {
			t.Fatal(clientErr)
		}
		clientCtx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			defer close(done)
			_ = dataplaneClient.Run(clientCtx)
		}()
		return cancel, done
	}
	cancelClient, clientDone := startDataPlaneClient("boot-restart-test")
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
	waitForSignal(t, ctx, ackBlocked, "initial ACK waiting after local activation")
	// Activation is client-side; the crash proof requires the control plane
	// to have processed and persisted that revision's ACK first.
	waitUntil(t, ctx, func() bool {
		requestCtx, requestCancel := context.WithTimeout(ctx, 250*time.Millisecond)
		defer requestCancel()
		response, listErr := adminClient.ListInstances(requestCtx, &pb.ListInstancesRequest{Limit: 10})
		if listErr != nil {
			return false
		}
		instance := findInstanceStatus(response.Instances, "restart-test-dp")
		return instance != nil && instance.Connected && instance.BootId == "boot-restart-test" && instance.Build == "integration" && instance.ActiveRevision == responseA.Revision.Revision && instance.ActiveHash == responseA.Revision.ContentHash
	})

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
	// Stop the client while its control plane is down so this process remains
	// absent after startup and exercises the durable orphaned row.
	cancelClient()
	waitForSignal(t, ctx, clientDone, "data-plane client shutdown after control-plane crash")
	restartAt := time.Now()
	control = startControl()
	var instances *pb.ListInstancesResponse
	waitUntil(t, ctx, func() bool {
		requestCtx, requestCancel := context.WithTimeout(ctx, 250*time.Millisecond)
		defer requestCancel()
		response, listErr := adminClient.ListInstances(requestCtx, &pb.ListInstancesRequest{Limit: 10})
		if listErr != nil || len(response.Instances) != 1 {
			return false
		}
		instances = response
		return !response.Instances[0].Connected
	})
	if instances.Instances[0].BootId != "boot-restart-test" || instances.Instances[0].Build != "integration" || instances.Instances[0].ActiveRevision != responseA.Revision.Revision || instances.Instances[0].ActiveHash != responseA.Revision.ContentHash {
		t.Fatalf("offline instance metadata was not preserved after control-plane restart: %+v", instances.Instances[0])
	}
	cancelClient, clientDone = startDataPlaneClient("boot-reconnected")
	waitUntil(t, ctx, func() bool {
		requestCtx, requestCancel := context.WithTimeout(ctx, 250*time.Millisecond)
		defer requestCancel()
		response, listErr := adminClient.ListInstances(requestCtx, &pb.ListInstancesRequest{Limit: 10})
		if listErr != nil || len(response.Instances) != 1 {
			return false
		}
		instances = response
		return response.Instances[0].Connected && response.Instances[0].BootId == "boot-reconnected" && response.Instances[0].ActiveRevision == responseA.Revision.Revision && response.Instances[0].LastHeartbeatUnixMs >= restartAt.UnixMilli()
	})
	if len(instances.Instances) != 1 || !instances.Instances[0].Connected || instances.Instances[0].ActiveRevision != responseA.Revision.Revision {
		t.Fatalf("instance did not reconnect on durable revision: %+v", instances.Instances)
	}

	// End the primary session cleanly before testing an outage on a separate
	// stream, so the outage row is the only disconnect write under test.
	cancelClient()
	waitForSignal(t, ctx, clientDone, "data-plane client shutdown before database outage")
	waitUntil(t, ctx, func() bool {
		requestCtx, requestCancel := context.WithTimeout(ctx, 250*time.Millisecond)
		defer requestCancel()
		response, listErr := adminClient.ListInstances(requestCtx, &pb.ListInstancesRequest{Limit: 10})
		return listErr == nil && len(response.Instances) == 1 && !response.Instances[0].Connected
	})
	outageStreamCtx, cancelOutageStream := context.WithCancel(ctx)
	defer cancelOutageStream()
	outageStream := registerDataPlaneStream(t, outageStreamCtx, pb.NewDataPlaneServiceClient(connection), "outage-test-dp", "boot-outage-1", responseA.Revision.Revision, responseA.Revision.ContentHash)

	configB := restartCandidate(upstreamB.URL)
	databaseProxy.SetAvailable(false)
	outageCtx, cancelPublish := context.WithTimeout(ctx, 2*time.Second)
	_, outageErr := adminClient.PublishConfig(outageCtx, &pb.PublishConfigRequest{ExpectedParentRevision: responseA.Revision.Revision, Config: configB, Actor: "restart-integration", Reason: "database outage"})
	cancelPublish()
	if outageErr == nil {
		t.Fatal("publication unexpectedly succeeded while PostgreSQL was unavailable")
	}
	if manager.Status().Revision != responseA.Revision.Revision {
		t.Fatalf("database outage changed active data-plane revision: %+v", manager.Status())
	}
	streamEndCtx, cancelStreamEnd := context.WithTimeout(ctx, 5*time.Second)
	waitForStreamFailure(t, streamEndCtx, outageStream)
	cancelStreamEnd()
	var persistedConnected bool
	var persistedBoot string
	var persistedRevision int64
	instanceTable := (pgx.Identifier{schema, "data_plane_instances"}).Sanitize()
	if err := adminPool.QueryRow(ctx, fmt.Sprintf(`SELECT connected,boot_id,active_revision FROM %s WHERE instance_id=$1`, instanceTable), "outage-test-dp").Scan(&persistedConnected, &persistedBoot, &persistedRevision); err != nil {
		t.Fatal(err)
	}
	if !persistedConnected || persistedBoot != "boot-outage-1" || persistedRevision != responseA.Revision.Revision {
		t.Fatalf("failed outage disconnect unexpectedly changed durable status: connected=%t boot=%q revision=%d", persistedConnected, persistedBoot, persistedRevision)
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
	var recoveredInstance *pb.InstanceStatus
	waitUntil(t, ctx, func() bool {
		requestCtx, requestCancel := context.WithTimeout(ctx, 250*time.Millisecond)
		defer requestCancel()
		response, listErr := adminClient.ListInstances(requestCtx, &pb.ListInstancesRequest{Limit: 10})
		if listErr != nil || len(response.Instances) != 2 {
			return false
		}
		recoveredInstance = findInstanceStatus(response.Instances, "outage-test-dp")
		return recoveredInstance != nil && !recoveredInstance.Connected
	})
	if recoveredInstance.BootId != "boot-outage-1" || recoveredInstance.ActiveRevision != responseA.Revision.Revision {
		t.Fatalf("instance metadata changed while database outage disconnect write failed: %+v", recoveredInstance)
	}
	reconnectStreamCtx, cancelReconnectStream := context.WithCancel(ctx)
	defer cancelReconnectStream()
	registerDataPlaneStream(t, reconnectStreamCtx, pb.NewDataPlaneServiceClient(connection), "outage-test-dp", "boot-outage-2", responseA.Revision.Revision, responseA.Revision.ContentHash)
	waitUntil(t, ctx, func() bool {
		requestCtx, requestCancel := context.WithTimeout(ctx, 250*time.Millisecond)
		defer requestCancel()
		response, listErr := adminClient.ListInstances(requestCtx, &pb.ListInstancesRequest{Limit: 10})
		if listErr != nil || len(response.Instances) != 2 {
			return false
		}
		instance := findInstanceStatus(response.Instances, "outage-test-dp")
		return instance != nil && instance.Connected && instance.BootId == "boot-outage-2"
	})
	cancelClient, clientDone = startDataPlaneClient("boot-after-outage")
	waitUntil(t, ctx, func() bool {
		return manager.Status().Connected && manager.Status().Revision == responseA.Revision.Revision
	})
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

	cancelReconnectStream()
	waitUntil(t, ctx, func() bool {
		requestCtx, requestCancel := context.WithTimeout(ctx, 250*time.Millisecond)
		defer requestCancel()
		response, listErr := adminClient.ListInstances(requestCtx, &pb.ListInstancesRequest{Limit: 10})
		if listErr != nil || len(response.Instances) != 2 {
			return false
		}
		instance := findInstanceStatus(response.Instances, "outage-test-dp")
		return instance != nil && !instance.Connected
	})
	cancelClient()
	waitForSignal(t, ctx, clientDone, "final data-plane client shutdown")
	stopControl(control)
	control = nil
}

type ackGateClientStream struct {
	grpc.ClientStream
	blocked chan struct{}
	release <-chan struct{}
	once    *sync.Once
}

func (stream *ackGateClientStream) SendMsg(message any) error {
	if request, ok := message.(*pb.DataPlaneServiceConnectRequest); ok && request.GetAck() != nil {
		stream.once.Do(func() { close(stream.blocked) })
		select {
		case <-stream.release:
		case <-stream.Context().Done():
			return stream.Context().Err()
		}
	}
	return stream.ClientStream.SendMsg(message)
}

func registerDataPlaneStream(t *testing.T, ctx context.Context, client pb.DataPlaneServiceClient, instanceID, bootID string, activeRevision int64, activeHash string) grpc.BidiStreamingClient[pb.DataPlaneServiceConnectRequest, pb.DataPlaneServiceConnectResponse] {
	t.Helper()
	stream, err := client.Connect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	registration := &pb.DataPlaneServiceConnectRequest{
		InstanceId: instanceID,
		BootId:     bootID,
		Event: &pb.DataPlaneServiceConnectRequest_Registration{Registration: &pb.DataPlaneRegistration{
			Build: "integration", ActiveRevision: activeRevision, ActiveHash: activeHash,
		}},
	}
	if err := stream.Send(registration); err != nil {
		t.Fatal(err)
	}
	response, err := stream.Recv()
	if err != nil {
		t.Fatalf("receive initial config for %s/%s: %v", instanceID, bootID, err)
	}
	if response.GetRevision() == nil || response.GetRevision().GetRevision() != activeRevision || response.GetRevision().GetContentHash() != activeHash {
		t.Fatalf("unexpected initial config for %s/%s: %+v", instanceID, bootID, response)
	}
	if err := stream.Send(&pb.DataPlaneServiceConnectRequest{
		InstanceId: instanceID,
		BootId:     bootID,
		Event: &pb.DataPlaneServiceConnectRequest_Ack{Ack: &pb.ConfigAck{
			Revision: activeRevision, ContentHash: activeHash,
		}},
	}); err != nil {
		t.Fatalf("acknowledge initial config for %s/%s: %v", instanceID, bootID, err)
	}
	return stream
}

func waitForStreamFailure(t *testing.T, ctx context.Context, stream grpc.BidiStreamingClient[pb.DataPlaneServiceConnectRequest, pb.DataPlaneServiceConnectResponse]) {
	t.Helper()
	result := make(chan error, 1)
	go func() {
		_, err := stream.Recv()
		result <- err
	}()
	select {
	case err := <-result:
		if status.Code(err) != codes.Unavailable {
			t.Fatalf("control-plane stream ended with %v, want Unavailable for database failure", err)
		}
	case <-ctx.Done():
		t.Fatalf("control-plane stream did not terminate after database failure: %v", ctx.Err())
	}
}

func waitForSignal(t *testing.T, ctx context.Context, signal <-chan struct{}, description string) {
	t.Helper()
	select {
	case <-signal:
	case <-ctx.Done():
		t.Fatalf("%s did not complete before the test deadline: %v", description, ctx.Err())
	}
}

func findInstanceStatus(instances []*pb.InstanceStatus, id string) *pb.InstanceStatus {
	for _, instance := range instances {
		if instance.GetInstanceId() == id {
			return instance
		}
	}
	return nil
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
