package admin

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	pb "github.com/SShogun/GateHouse/gen/go/gatehouse/v1"
	"github.com/SShogun/GateHouse/internal/config"
	"github.com/SShogun/GateHouse/internal/control/revision"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"
)

func TestConnectStreamsLatestSnapshotsAndRecordsInstanceStatus(t *testing.T) {
	store := &memoryStore{}
	first := validCandidate()
	_, firstHash, err := config.EncodeCandidate(first)
	if err != nil {
		t.Fatal(err)
	}
	store.active = revision.Record{Number: 1, Hash: firstHash, Config: first, CreatedAt: time.Now()}
	store.records = map[int64]revision.Record{1: store.active}
	service := newService(t, store)
	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	pb.RegisterDataPlaneServiceServer(server, service)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { server.Stop(); _ = listener.Close() })
	connection, err := grpc.NewClient("passthrough:///gatehouse-test", grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	stream, err := pb.NewDataPlaneServiceClient(connection).Connect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(registrationMessage("dp-a", "boot-a", 0, "")); err != nil {
		t.Fatal(err)
	}
	update, err := stream.Recv()
	if err != nil || update.GetRevision().GetRevision() != 1 || update.GetRevision().GetContentHash() != firstHash {
		t.Fatalf("initial update=%+v err=%v", update, err)
	}
	if err := stream.Send(&pb.DataPlaneServiceConnectRequest{InstanceId: "dp-a", BootId: "boot-a", Event: &pb.DataPlaneServiceConnectRequest_Nack{Nack: &pb.ConfigNack{Revision: 1, ContentHash: firstHash, Reason: "candidate rejected", ActiveRevision: 0}}}); err != nil {
		t.Fatal(err)
	}
	waitForInstance(t, store, func(instance revision.Instance) bool {
		return instance.LastNACK == "candidate rejected" && instance.ActiveRevision == 0
	})

	second := validCandidate()
	second.Routes[0].Path = "/intermediate"
	_, _, err = config.EncodeCandidate(second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.PublishConfig(context.Background(), &pb.PublishConfigRequest{ExpectedParentRevision: 1, Config: second, Actor: "test", Reason: "stream test"}); err != nil {
		t.Fatal(err)
	}
	latest := validCandidate()
	latest.Routes[0].Path = "/latest"
	_, latestHash, err := config.EncodeCandidate(latest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.PublishConfig(context.Background(), &pb.PublishConfigRequest{ExpectedParentRevision: 2, Config: latest, Actor: "test", Reason: "coalesced stream test"}); err != nil {
		t.Fatal(err)
	}
	update, err = stream.Recv()
	if err != nil || (update.GetRevision().GetRevision() != 2 && update.GetRevision().GetRevision() != 3) {
		t.Fatalf("first published update=%+v err=%v", update, err)
	}
	if update.GetRevision().GetRevision() == 2 {
		secondHash := store.records[2].Hash
		if err := stream.Send(&pb.DataPlaneServiceConnectRequest{InstanceId: "dp-a", BootId: "boot-a", Event: &pb.DataPlaneServiceConnectRequest_Ack{Ack: &pb.ConfigAck{Revision: 2, ContentHash: secondHash}}}); err != nil {
			t.Fatal(err)
		}
		update, err = stream.Recv()
		if err != nil || update.GetRevision().GetRevision() != 3 {
			t.Fatalf("latest coalesced update after revision 2 ACK=%+v err=%v", update, err)
		}
	}
	if update.GetRevision().GetContentHash() != latestHash || update.GetRevision().GetConfig().GetRoutes()[0].GetPath() != "/latest" {
		t.Fatalf("latest update=%+v", update)
	}
	if err := stream.Send(&pb.DataPlaneServiceConnectRequest{InstanceId: "dp-a", BootId: "boot-a", Event: &pb.DataPlaneServiceConnectRequest_Ack{Ack: &pb.ConfigAck{Revision: 3, ContentHash: latestHash}}}); err != nil {
		t.Fatal(err)
	}
	waitForInstance(t, store, func(instance revision.Instance) bool {
		return instance.ActiveRevision == 3 && instance.ActiveHash == latestHash && instance.LastNACK == ""
	})
	heartbeatAfter := time.Now()
	if err := stream.Send(&pb.DataPlaneServiceConnectRequest{InstanceId: "dp-a", BootId: "boot-a", Event: &pb.DataPlaneServiceConnectRequest_Heartbeat{Heartbeat: &pb.DataPlaneHeartbeat{ActiveRevision: 3, ActiveHash: latestHash}}}); err != nil {
		t.Fatal(err)
	}
	waitForInstance(t, store, func(instance revision.Instance) bool {
		return instance.ActiveRevision == 3 && instance.ActiveHash == latestHash && instance.LastHeartbeat.After(heartbeatAfter)
	})

	// A new process connection always replays the current complete revision.
	reconnected, err := pb.NewDataPlaneServiceClient(connection).Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := reconnected.Send(registrationMessage("dp-a", "boot-b", 0, "")); err != nil {
		t.Fatal(err)
	}
	replayed, err := reconnected.Recv()
	if err != nil || replayed.GetRevision().GetRevision() != 3 || replayed.GetRevision().GetContentHash() != latestHash {
		t.Fatalf("reconnect replay=%+v err=%v", replayed, err)
	}
	cancel()
	_ = reconnected.CloseSend()
	for {
		_, recvErr := reconnected.Recv()
		if recvErr != nil {
			if !errors.Is(recvErr, context.Canceled) && !errors.Is(recvErr, io.EOF) {
				t.Fatalf("reconnect close error=%v", recvErr)
			}
			break
		}
	}
	waitForInstance(t, store, func(instance revision.Instance) bool {
		return instance.BootID == "boot-b" && !instance.Connected && instance.ActiveRevision == 0
	})
}

func TestConnectRequiresRegistrationAsFirstMessage(t *testing.T) {
	service := newService(t, &memoryStore{})
	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	pb.RegisterDataPlaneServiceServer(server, service)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { server.Stop(); _ = listener.Close() })
	connection, err := grpc.NewClient("passthrough:///gatehouse-test", grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	stream, err := pb.NewDataPlaneServiceClient(connection).Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(&pb.DataPlaneServiceConnectRequest{InstanceId: "dp-a", BootId: "boot-a", Event: &pb.DataPlaneServiceConnectRequest_Heartbeat{Heartbeat: &pb.DataPlaneHeartbeat{}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Recv(); err == nil {
		t.Fatal("server accepted heartbeat before registration")
	}
}

func TestConnectRejectsAckForSkippedRevision(t *testing.T) {
	candidates := make([]*pb.GatewayConfig, 3)
	records := make(map[int64]revision.Record, len(candidates))
	var active revision.Record
	for i := range candidates {
		candidates[i] = validCandidate()
		candidates[i].Routes[0].Path = []string{"/one", "/two", "/three"}[i]
		_, hash, err := config.EncodeCandidate(candidates[i])
		if err != nil {
			t.Fatal(err)
		}
		active = revision.Record{Number: int64(i + 1), Hash: hash, Config: candidates[i], CreatedAt: time.Now()}
		records[active.Number] = active
	}
	store := &memoryStore{active: active, records: records}
	service := newService(t, store)
	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	pb.RegisterDataPlaneServiceServer(server, service)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { server.Stop(); _ = listener.Close() })
	connection, err := grpc.NewClient("passthrough:///gatehouse-test", grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	stream, err := pb.NewDataPlaneServiceClient(connection).Connect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(registrationMessage("dp-skipped", "boot-a", 0, "")); err != nil {
		t.Fatal(err)
	}
	update, err := stream.Recv()
	if err != nil || update.GetRevision().GetRevision() != 3 {
		t.Fatalf("initial update=%+v err=%v, want only active revision 3", update, err)
	}
	if err := stream.Send(&pb.DataPlaneServiceConnectRequest{InstanceId: "dp-skipped", BootId: "boot-a", Event: &pb.DataPlaneServiceConnectRequest_Ack{Ack: &pb.ConfigAck{Revision: 2, ContentHash: records[2].Hash}}}); err != nil {
		t.Fatal(err)
	}
	_, err = stream.Recv()
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("ACK for stored but undelivered revision 2 error=%v, want InvalidArgument", err)
	}
}

func TestConnectSlowSubscriberCoalesces1000Revisions(t *testing.T) {
	initial := validCandidate()
	initial.Clusters[0].HealthCheck = &pb.HealthCheck{Path: "/health", IntervalNs: int64(time.Hour), TimeoutNs: int64(time.Second)}
	_, initialHash, err := config.EncodeCandidate(initial)
	if err != nil {
		t.Fatal(err)
	}
	store := &memoryStore{active: revision.Record{Number: 1, Hash: initialHash, Config: initial}, records: map[int64]revision.Record{1: {Number: 1, Hash: initialHash, Config: initial}}}
	service := newService(t, store)
	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	pb.RegisterDataPlaneServiceServer(server, service)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { server.Stop(); _ = listener.Close() })
	connection, err := grpc.NewClient("passthrough:///gatehouse-test", grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	stream, err := pb.NewDataPlaneServiceClient(connection).Connect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(registrationMessage("dp-slow", "boot-a", 0, "")); err != nil {
		t.Fatal(err)
	}
	first, err := stream.Recv()
	if err != nil || first.GetRevision().GetRevision() != 1 {
		t.Fatalf("initial update=%+v err=%v", first, err)
	}

	latest := initial
	for number := int64(2); number <= 1001; number++ {
		latest = proto.Clone(latest).(*pb.GatewayConfig)
		latest.Routes[0].Path = fmt.Sprintf("/revision-%d", number)
		if _, err := service.PublishConfig(ctx, &pb.PublishConfigRequest{ExpectedParentRevision: number - 1, Config: latest, Actor: "churn-test", Reason: "slow subscriber"}); err != nil {
			t.Fatalf("publish revision %d: %v", number, err)
		}
	}
	_, latestHash, err := config.EncodeCandidate(latest)
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(&pb.DataPlaneServiceConnectRequest{InstanceId: "dp-slow", BootId: "boot-a", Event: &pb.DataPlaneServiceConnectRequest_Ack{Ack: &pb.ConfigAck{Revision: 1, ContentHash: initialHash}}}); err != nil {
		t.Fatal(err)
	}
	update, err := stream.Recv()
	if err != nil || update.GetRevision().GetRevision() != 1001 || update.GetRevision().GetContentHash() != latestHash || update.GetRevision().GetConfig().GetRoutes()[0].GetPath() != "/revision-1001" {
		t.Fatalf("slow subscriber update=%+v err=%v, want latest revision 1001", update, err)
	}
}

func TestStreamRejectsAckForUndeliveredOrMismatchedRevision(t *testing.T) {
	candidate := validCandidate()
	_, hash, err := config.EncodeCandidate(candidate)
	if err != nil {
		t.Fatal(err)
	}
	store := &memoryStore{records: map[int64]revision.Record{1: {Number: 1, Hash: hash, Config: candidate}}}
	instance := &revision.Instance{ID: "dp-a", BootID: "boot-a"}
	lastNACK := ""
	state := &configStreamState{pendingRevision: 1, pendingHash: hash}
	for _, ack := range []*pb.ConfigAck{
		{Revision: 2, ContentHash: hash},
		{Revision: 1, ContentHash: "0000000000000000000000000000000000000000000000000000000000000000"},
	} {
		message := &pb.DataPlaneServiceConnectRequest{InstanceId: "dp-a", BootId: "boot-a", Event: &pb.DataPlaneServiceConnectRequest_Ack{Ack: ack}}
		if err := applyClientEvent(context.Background(), store, message, instance, &lastNACK, state); status.Code(err) != codes.InvalidArgument {
			t.Fatalf("ACK %+v error=%v, want invalid argument", ack, err)
		}
	}
}

func registrationMessage(instance, boot string, revisionNumber int64, hash string) *pb.DataPlaneServiceConnectRequest {
	return &pb.DataPlaneServiceConnectRequest{InstanceId: instance, BootId: boot, Event: &pb.DataPlaneServiceConnectRequest_Registration{Registration: &pb.DataPlaneRegistration{Build: "test", ActiveRevision: revisionNumber, ActiveHash: hash}}}
}

func waitForInstance(t *testing.T, store *memoryStore, predicate func(revision.Instance) bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	var latest []revision.Instance
	for time.Now().Before(deadline) {
		instances, err := store.Instances(context.Background(), "", 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(instances) == 1 && predicate(instances[0]) {
			return
		}
		latest = instances
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("instance did not reach expected state: %+v", latest)
}
