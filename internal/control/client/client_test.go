package client

import (
	"context"
	"net"
	"sync/atomic"
	"testing"
	"time"

	pb "github.com/SShogun/GateHouse/gen/go/gatehouse/v1"
	"github.com/SShogun/GateHouse/internal/config"
	"github.com/SShogun/GateHouse/internal/gateway"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

type scriptedControlServer struct {
	pb.UnimplementedDataPlaneServiceServer
	candidate *pb.GatewayConfig
	hash      string
	started   chan int
	calls     atomic.Int64
	acks      chan *pb.ConfigAck
	nacks     chan *pb.ConfigNack
}

func (s *scriptedControlServer) Connect(stream grpc.BidiStreamingServer[pb.DataPlaneServiceConnectRequest, pb.DataPlaneServiceConnectResponse]) error {
	registration, err := stream.Recv()
	if err != nil {
		return err
	}
	call := int(s.calls.Add(1))
	s.started <- call
	if registration.GetRegistration() == nil || registration.GetInstanceId() != "dp-a" || registration.GetBootId() != "boot-a" {
		return status.Error(codes.InvalidArgument, "missing registration")
	}
	if call == 1 {
		if err := stream.Send(&pb.DataPlaneServiceConnectResponse{Revision: &pb.Revision{Revision: 1, ContentHash: s.hash, Config: s.candidate}}); err != nil {
			return err
		}
		message, err := stream.Recv()
		if err != nil {
			return err
		}
		s.acks <- message.GetAck()
		bad := &pb.GatewayConfig{SchemaVersion: 1, Listener: &pb.Listener{Address: "127.0.0.1:8080"}}
		if err := stream.Send(&pb.DataPlaneServiceConnectResponse{Revision: &pb.Revision{Revision: 2, ContentHash: "0000000000000000000000000000000000000000000000000000000000000000", Config: bad}}); err != nil {
			return err
		}
		message, err = stream.Recv()
		if err != nil {
			return err
		}
		s.nacks <- message.GetNack()
		return status.Error(codes.Unavailable, "simulate control-plane interruption")
	}
	if registration.GetRegistration().ActiveRevision != 1 || registration.GetRegistration().ActiveHash != s.hash {
		return status.Error(codes.DataLoss, "reconnect did not report active revision")
	}
	if err := stream.Send(&pb.DataPlaneServiceConnectResponse{Revision: &pb.Revision{Revision: 1, ContentHash: s.hash, Config: s.candidate}}); err != nil {
		return err
	}
	message, err := stream.Recv()
	if err != nil {
		return err
	}
	s.acks <- message.GetAck()
	_, err = stream.Recv()
	return err
}

func TestControlClientNacksInvalidSnapshotKeepsLastGoodAndReconnects(t *testing.T) {
	listener := bufconn.Listen(1 << 20)
	candidate := &pb.GatewayConfig{
		SchemaVersion: 1,
		Listener:      &pb.Listener{Address: "127.0.0.1:8080"},
		Routes:        []*pb.Route{{Id: "root", Path: "/", PathType: pb.PathType_PATH_TYPE_PREFIX, ClusterId: "backend"}},
		Clusters:      []*pb.Cluster{{Id: "backend", Policy: pb.SelectionPolicy_SELECTION_POLICY_ROUND_ROBIN, Endpoints: []*pb.Endpoint{{Id: "one", Address: "http://127.0.0.1:8081", Weight: 1}}}},
	}
	_, hash, err := config.EncodeCandidate(candidate)
	if err != nil {
		t.Fatal(err)
	}
	control := &scriptedControlServer{candidate: candidate, hash: hash, started: make(chan int, 2), acks: make(chan *pb.ConfigAck, 2), nacks: make(chan *pb.ConfigNack, 1)}
	server := grpc.NewServer()
	pb.RegisterDataPlaneServiceServer(server, control)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { server.Stop(); _ = listener.Close() })
	manager, err := gateway.NewManager(config.Listener{Address: "127.0.0.1:8080"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	client, err := NewControlClient(ControlClientConfig{
		Endpoint: "127.0.0.1:9090", InstanceID: "dp-a", BootID: "boot-a", Build: "test", Manager: manager,
		HeartbeatInterval: time.Hour, ReconnectMin: time.Millisecond, ReconnectMax: 5 * time.Millisecond,
		DialOptions: []grpc.DialOption{grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() })},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- client.Run(ctx) }()
	select {
	case ack := <-control.acks:
		if ack == nil || ack.Revision != 1 || ack.ContentHash != hash {
			t.Fatalf("first ACK=%+v", ack)
		}
	case <-time.After(time.Second):
		t.Fatal("client did not ACK valid snapshot")
	}
	select {
	case nack := <-control.nacks:
		if nack == nil || nack.Revision != 2 || nack.Reason == "" || nack.ActiveRevision != 1 || nack.ActiveHash != hash {
			t.Fatalf("NACK=%+v", nack)
		}
	case <-time.After(time.Second):
		t.Fatal("client did not NACK invalid snapshot")
	}
	select {
	case call := <-control.started:
		if call != 1 {
			t.Fatalf("first connection ordinal=%d", call)
		}
	case <-time.After(time.Second):
		t.Fatal("first stream did not connect")
	}
	select {
	case ack := <-control.acks:
		if ack == nil || ack.Revision != 1 || ack.ContentHash != hash {
			t.Fatalf("reconnect ACK=%+v", ack)
		}
	case <-time.After(time.Second):
		t.Fatal("client did not reconnect and ACK the replayed snapshot")
	}
	select {
	case call := <-control.started:
		if call != 2 {
			t.Fatalf("second connection ordinal=%d", call)
		}
	case <-time.After(time.Second):
		t.Fatal("client did not reconnect")
	}
	if status := manager.Status(); !status.Ready || status.Revision != 1 {
		t.Fatalf("invalid snapshot replaced last-known-good: %+v", status)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("client Run() error=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("client did not stop after context cancellation")
	}
	if status := manager.Status(); !status.Ready || !status.Stale || status.Connected {
		t.Fatalf("disconnected data plane lost its last-known-good status: %+v", status)
	}
}
