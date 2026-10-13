// Package admin implements the typed control-plane Admin API.
package admin

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	pb "github.com/SShogun/GateHouse/gen/go/gatehouse/v1"
	"github.com/SShogun/GateHouse/internal/config"
	"github.com/SShogun/GateHouse/internal/control/revision"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

const (
	defaultPageSize         = 20
	maxPageSize             = 100
	defaultInstancePageSize = 50
	maxInstancePageSize     = 100
	maxTextBytes            = 1024
)

type Service struct {
	pb.UnimplementedAdminServiceServer
	pb.UnimplementedDataPlaneServiceServer
	store          revision.Store
	instances      revision.InstanceStore
	notifyMu       sync.Mutex
	nextSubscriber uint64
	subscribers    map[uint64]chan struct{}
	sessionMu      sync.Mutex
	sessions       map[string]*dataPlaneSession
}

type dataPlaneSession struct {
	done   chan struct{}
	bootID string
}

func New(store revision.Store) (*Service, error) {
	if store == nil {
		return nil, errors.New("revision store is required")
	}
	svc := &Service{store: store, subscribers: make(map[uint64]chan struct{}), sessions: make(map[string]*dataPlaneSession)}
	if instanceStore, ok := store.(revision.InstanceStore); ok {
		svc.instances = instanceStore
	}
	return svc, nil
}
func (s *Service) GetActiveConfig(ctx context.Context, _ *pb.GetActiveConfigRequest) (*pb.GetActiveConfigResponse, error) {
	r, e := s.store.Active(ctx)
	if e != nil {
		return nil, apiError(e)
	}
	return &pb.GetActiveConfigResponse{Revision: toProto(r, true)}, nil
}
func (s *Service) ValidateConfig(_ context.Context, req *pb.ValidateConfigRequest) (*pb.ValidateConfigResponse, error) {
	if req == nil || req.Config == nil {
		return nil, status.Error(codes.InvalidArgument, "config is required")
	}
	_, h, e := config.EncodeCandidate(req.Config)
	if e != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid config: %v", e)
	}
	return &pb.ValidateConfigResponse{ContentHash: h}, nil
}
func (s *Service) PublishConfig(ctx context.Context, req *pb.PublishConfigRequest) (*pb.PublishConfigResponse, error) {
	if req == nil || req.Config == nil {
		return nil, status.Error(codes.InvalidArgument, "config is required")
	}
	if req.ExpectedParentRevision < 0 {
		return nil, status.Error(codes.InvalidArgument, "expected parent revision cannot be negative")
	}
	if err := validText(req.Actor, req.Reason); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	_, hash, err := config.EncodeCandidate(req.Config)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid config: %v", err)
	}
	r, err := s.store.Publish(ctx, revision.Publish{ExpectedParent: req.ExpectedParentRevision, Hash: hash, Config: req.Config, Actor: req.Actor, Reason: req.Reason, Operation: "publish"})
	if err != nil {
		return nil, apiError(err)
	}
	s.signalPublication()
	return &pb.PublishConfigResponse{Revision: toProto(r, true)}, nil
}
func (s *Service) GetRevision(ctx context.Context, req *pb.GetRevisionRequest) (*pb.GetRevisionResponse, error) {
	if req == nil || req.Revision <= 0 {
		return nil, status.Error(codes.InvalidArgument, "revision must be positive")
	}
	r, e := s.store.Get(ctx, req.Revision)
	if e != nil {
		return nil, apiError(e)
	}
	return &pb.GetRevisionResponse{Revision: toProto(r, true)}, nil
}
func (s *Service) ListRevisions(ctx context.Context, req *pb.ListRevisionsRequest) (*pb.ListRevisionsResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "request is required")
	}
	if req.BeforeRevision < 0 || req.Limit < 0 {
		return nil, status.Error(codes.InvalidArgument, "pagination values cannot be negative")
	}
	n := int(req.Limit)
	if n == 0 {
		n = defaultPageSize
	}
	if n > maxPageSize {
		return nil, status.Errorf(codes.InvalidArgument, "limit cannot exceed %d", maxPageSize)
	}
	items, e := s.store.List(ctx, req.BeforeRevision, n)
	if e != nil {
		return nil, apiError(e)
	}
	out := &pb.ListRevisionsResponse{}
	for _, r := range items {
		out.Revisions = append(out.Revisions, toProto(r, false))
	}
	if len(items) == n {
		out.NextBeforeRevision = items[len(items)-1].Number
	}
	return out, nil
}
func (s *Service) DiffRevisions(ctx context.Context, req *pb.DiffRevisionsRequest) (*pb.DiffRevisionsResponse, error) {
	if req == nil || req.FromRevision <= 0 || req.ToRevision <= 0 {
		return nil, status.Error(codes.InvalidArgument, "both revisions must be positive")
	}
	a, e := s.store.Get(ctx, req.FromRevision)
	if e != nil {
		return nil, apiError(e)
	}
	b, e := s.store.Get(ctx, req.ToRevision)
	if e != nil {
		return nil, apiError(e)
	}
	return diff(a.Config, b.Config), nil
}
func (s *Service) Rollback(ctx context.Context, req *pb.RollbackRequest) (*pb.RollbackResponse, error) {
	if req == nil || req.ExpectedParentRevision < 0 || req.SourceRevision <= 0 {
		return nil, status.Error(codes.InvalidArgument, "valid parent and source revisions are required")
	}
	if err := validText(req.Actor, req.Reason); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	source, e := s.store.Get(ctx, req.SourceRevision)
	if e != nil {
		return nil, apiError(e)
	}
	_, hash, e := config.EncodeCandidate(source.Config)
	if e != nil {
		return nil, status.Errorf(codes.DataLoss, "stored config is invalid: %v", e)
	}
	r, e := s.store.Publish(ctx, revision.Publish{ExpectedParent: req.ExpectedParentRevision, Hash: hash, Config: source.Config, Actor: req.Actor, Reason: req.Reason, Operation: "rollback", RollbackSource: req.SourceRevision})
	if e != nil {
		return nil, apiError(e)
	}
	s.signalPublication()
	return &pb.RollbackResponse{Revision: toProto(r, true)}, nil
}
func (s *Service) ListInstances(ctx context.Context, req *pb.ListInstancesRequest) (*pb.ListInstancesResponse, error) {
	if req == nil || len(req.BeforeInstanceId) > maxTextBytes || req.Limit < 0 {
		return nil, status.Error(codes.InvalidArgument, "invalid instance pagination")
	}
	limit := int(req.Limit)
	if limit == 0 {
		limit = defaultInstancePageSize
	}
	if limit > maxInstancePageSize {
		return nil, status.Errorf(codes.InvalidArgument, "limit cannot exceed %d", maxInstancePageSize)
	}
	instances, err := s.store.Instances(ctx, req.BeforeInstanceId, limit)
	if err != nil {
		return nil, apiError(err)
	}
	response := &pb.ListInstancesResponse{}
	s.sessionMu.Lock()
	defer s.sessionMu.Unlock()
	for _, instance := range instances {
		session := s.sessions[instance.ID]
		connected := session != nil && session.bootID == instance.BootID
		response.Instances = append(response.Instances, &pb.InstanceStatus{
			InstanceId: instance.ID, BootId: instance.BootID, Build: instance.Build,
			Connected: connected, ActiveRevision: instance.ActiveRevision,
			ActiveHash: instance.ActiveHash, LastHeartbeatUnixMs: instance.LastHeartbeat.UnixMilli(),
			LastNack: instance.LastNACK,
		})
	}
	if len(instances) == limit {
		response.NextBeforeInstanceId = instances[len(instances)-1].ID
	}
	return response, nil
}
func validText(actor, reason string) error {
	if strings.TrimSpace(actor) == "" || len(actor) > maxTextBytes {
		return errors.New("actor is required and must be at most 1024 bytes")
	}
	if len(reason) > maxTextBytes {
		return errors.New("reason must be at most 1024 bytes")
	}
	return nil
}
func apiError(err error) error {
	switch {
	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, "request canceled")
	case errors.Is(err, context.DeadlineExceeded):
		return status.Error(codes.DeadlineExceeded, "request deadline exceeded")
	case errors.Is(err, revision.ErrNotFound):
		return status.Error(codes.NotFound, "revision not found")
	case errors.Is(err, revision.ErrConflict):
		return status.Error(codes.Aborted, "active revision changed; reload and rebase")
	case errors.Is(err, revision.ErrCorrupt):
		return status.Error(codes.DataLoss, "stored revision integrity failure")
	default:
		return status.Error(codes.Unavailable, "control-plane storage unavailable")
	}
}
func toProto(r revision.Record, includeConfig bool) *pb.Revision {
	p := &pb.Revision{Revision: r.Number, ParentRevision: r.Parent, ContentHash: r.Hash, CreatedAtUnixMs: r.CreatedAt.UnixMilli(), Actor: r.Actor, Reason: r.Reason, Operation: r.Operation, RollbackSourceRevision: r.RollbackSource}
	if includeConfig && r.Config != nil {
		p.Config = proto.Clone(r.Config).(*pb.GatewayConfig)
	}
	return p
}
func diff(a, b *pb.GatewayConfig) *pb.DiffRevisionsResponse {
	out := &pb.DiffRevisionsResponse{}
	if a == nil || b == nil {
		return out
	}
	out.ListenerChanged = !proto.Equal(a.Listener, b.Listener)
	out.SchemaVersionChanged = a.SchemaVersion != b.SchemaVersion
	out.RouteOrderChanged = !routeOrder(a.Routes, b.Routes)
	out.ClusterOrderChanged = !clusterOrder(a.Clusters, b.Clusters)
	compare := func(left, right map[string]proto.Message) (added, removed, changed []string) {
		for id, x := range right {
			old, ok := left[id]
			if !ok {
				added = append(added, id)
			} else if !proto.Equal(old, x) {
				changed = append(changed, id)
			}
		}
		for id := range left {
			if _, ok := right[id]; !ok {
				removed = append(removed, id)
			}
		}
		sort.Strings(added)
		sort.Strings(removed)
		sort.Strings(changed)
		return
	}
	ar, br := map[string]proto.Message{}, map[string]proto.Message{}
	for _, v := range a.Routes {
		if v != nil {
			ar[v.Id] = v
		}
	}
	for _, v := range b.Routes {
		if v != nil {
			br[v.Id] = v
		}
	}
	out.AddedRouteIds, out.RemovedRouteIds, out.ChangedRouteIds = compare(ar, br)
	ac, bc := map[string]proto.Message{}, map[string]proto.Message{}
	for _, v := range a.Clusters {
		if v != nil {
			ac[v.Id] = v
		}
	}
	for _, v := range b.Clusters {
		if v != nil {
			bc[v.Id] = v
		}
	}
	out.AddedClusterIds, out.RemovedClusterIds, out.ChangedClusterIds = compare(ac, bc)
	return out
}
func routeOrder(a, b []*pb.Route) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].GetId() != b[i].GetId() {
			return false
		}
	}
	return true
}
func clusterOrder(a, b []*pb.Cluster) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].GetId() != b[i].GetId() {
			return false
		}
	}
	return true
}

// ValidateDevelopmentAddress enforces the temporary M6 authentication boundary.
func ValidateDevelopmentAddress(address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("admin address must be host:port: %w", err)
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return errors.New("unauthenticated admin API must bind to a literal loopback IP")
	}
	return nil
}
func ValidatePostgresURL(raw string) error {
	u, e := url.Parse(raw)
	if e != nil {
		return e
	}
	if u.Scheme != "postgres" && u.Scheme != "postgresql" {
		return errors.New("database URL must use postgres scheme")
	}
	if u.Host == "" {
		return errors.New("database URL requires host")
	}
	return nil
}

// RequestDeadline caps unauthenticated development Admin RPC execution time.
func RequestDeadline(limit time.Duration) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if limit <= 0 {
			return nil, status.Error(codes.Internal, "invalid admin request deadline")
		}
		bounded, cancel := context.WithTimeout(ctx, limit)
		defer cancel()
		return handler(bounded, req)
	}
}
