package admin

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	pb "github.com/SShogun/GateHouse/gen/go/gatehouse/v1"
	"github.com/SShogun/GateHouse/internal/control/revision"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

type memoryStore struct {
	mu             sync.Mutex
	active         revision.Record
	records        map[int64]revision.Record
	instances      []revision.Instance
	publishErr     error
	published      revision.Publish
	listBefore     int64
	listLimit      int
	instanceBefore string
	instanceLimit  int
}

func (s *memoryStore) Active(context.Context) (revision.Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active.Number == 0 {
		return revision.Record{}, revision.ErrNotFound
	}
	return s.active, nil
}
func (s *memoryStore) Get(_ context.Context, n int64) (revision.Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.records[n]
	if !ok {
		return revision.Record{}, revision.ErrNotFound
	}
	return r, nil
}
func (s *memoryStore) List(_ context.Context, before int64, limit int) ([]revision.Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.listBefore = before
	s.listLimit = limit
	if len(s.records) == 0 {
		return nil, nil
	}
	var out []revision.Record
	for n := before - 1; n > 0 && len(out) < limit; n-- {
		if r, ok := s.records[n]; ok {
			out = append(out, r)
		}
	}
	if before == 0 {
		for n := int64(len(s.records)); n > 0 && len(out) < limit; n-- {
			if r, ok := s.records[n]; ok {
				out = append(out, r)
			}
		}
	}
	return out, nil
}
func (s *memoryStore) Instances(_ context.Context, before string, limit int) ([]revision.Instance, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.instanceBefore = before
	s.instanceLimit = limit
	out := []revision.Instance{}
	for _, instance := range s.instances {
		if (before == "" || instance.ID > before) && len(out) < limit {
			out = append(out, instance)
		}
	}
	return out, nil
}
func (s *memoryStore) Publish(_ context.Context, p revision.Publish) (revision.Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.published = p
	if s.publishErr != nil {
		return revision.Record{}, s.publishErr
	}
	r := revision.Record{Number: p.ExpectedParent + 1, Parent: p.ExpectedParent, Hash: p.Hash, Config: proto.Clone(p.Config).(*pb.GatewayConfig), Actor: p.Actor, Reason: p.Reason, Operation: p.Operation, RollbackSource: p.RollbackSource, CreatedAt: time.Unix(10, 0)}
	if s.records == nil {
		s.records = map[int64]revision.Record{}
	}
	s.records[r.Number] = r
	s.active = r
	return r, nil
}

func (s *memoryStore) UpsertInstance(_ context.Context, instance revision.Instance) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for index := range s.instances {
		if s.instances[index].ID == instance.ID {
			s.instances[index] = instance
			return nil
		}
	}
	s.instances = append(s.instances, instance)
	return nil
}

func validCandidate() *pb.GatewayConfig {
	return &pb.GatewayConfig{SchemaVersion: 1, Listener: &pb.Listener{Address: "127.0.0.1:8080"}, Clusters: []*pb.Cluster{{Id: "backend", Policy: pb.SelectionPolicy_SELECTION_POLICY_ROUND_ROBIN, Endpoints: []*pb.Endpoint{{Id: "one", Address: "http://127.0.0.1:9000", Weight: 1}}}}, Routes: []*pb.Route{{Id: "root", Path: "/", PathType: pb.PathType_PATH_TYPE_PREFIX, ClusterId: "backend"}}}
}
func newService(t *testing.T, store *memoryStore) *Service {
	t.Helper()
	s, err := New(store)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestValidateConfig(t *testing.T) {
	s := newService(t, &memoryStore{})
	got, err := s.ValidateConfig(context.Background(), &pb.ValidateConfigRequest{Config: validCandidate()})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.ContentHash) != 64 {
		t.Fatalf("hash length=%d", len(got.ContentHash))
	}
	_, err = s.ValidateConfig(context.Background(), &pb.ValidateConfigRequest{Config: &pb.GatewayConfig{SchemaVersion: 9}})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("invalid config code=%s err=%v", status.Code(err), err)
	}
	_, err = s.ValidateConfig(context.Background(), nil)
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("nil request code=%s", status.Code(err))
	}
}

func TestPublishConfigChecksCandidateAndMapsConflict(t *testing.T) {
	st := &memoryStore{publishErr: revision.ErrConflict}
	s := newService(t, st)
	req := &pb.PublishConfigRequest{ExpectedParentRevision: 7, Config: validCandidate(), Actor: "operator", Reason: "change"}
	_, err := s.PublishConfig(context.Background(), req)
	if status.Code(err) != codes.Aborted {
		t.Fatalf("conflict code=%s err=%v", status.Code(err), err)
	}
	if st.published.ExpectedParent != 7 || st.published.Operation != "publish" || st.published.Hash == "" {
		t.Fatalf("unexpected publication: %#v", st.published)
	}
	req.Config.Listener.Address = "not-a-listener"
	_, err = s.PublishConfig(context.Background(), req)
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("invalid candidate code=%s", status.Code(err))
	}
	req.Config = validCandidate()
	req.Actor = " "
	_, err = s.PublishConfig(context.Background(), req)
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("blank actor code=%s", status.Code(err))
	}
}

func TestRevisionPaginationAndInstancePaginationAreBounded(t *testing.T) {
	records := map[int64]revision.Record{}
	for n := int64(1); n <= 3; n++ {
		records[n] = revision.Record{Number: n, CreatedAt: time.Unix(n, 0)}
	}
	st := &memoryStore{records: records, instances: []revision.Instance{{ID: "dp-a", BootID: "boot-a"}, {ID: "dp-b", BootID: "boot-b"}, {ID: "dp-c", BootID: "boot-c"}}}
	s := newService(t, st)
	revisions, err := s.ListRevisions(context.Background(), &pb.ListRevisionsRequest{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(revisions.Revisions) != 1 || revisions.Revisions[0].Revision != 3 || revisions.NextBeforeRevision != 3 || st.listLimit != 1 {
		t.Fatalf("unexpected revision page: %#v", revisions)
	}
	if _, err = s.ListRevisions(context.Background(), &pb.ListRevisionsRequest{Limit: 101}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("oversized revision page code=%s", status.Code(err))
	}
	instances, err := s.ListInstances(context.Background(), &pb.ListInstancesRequest{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(instances.Instances) != 2 || instances.NextBeforeInstanceId != "dp-b" || st.instanceLimit != 2 {
		t.Fatalf("unexpected instance page: %#v", instances)
	}
	next, err := s.ListInstances(context.Background(), &pb.ListInstancesRequest{BeforeInstanceId: instances.NextBeforeInstanceId, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(next.Instances) != 1 || next.Instances[0].InstanceId != "dp-c" || next.NextBeforeInstanceId != "" {
		t.Fatalf("unexpected final instance page: %#v", next)
	}
	if _, err = s.ListInstances(context.Background(), &pb.ListInstancesRequest{Limit: 101}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("oversized instance page code=%s", status.Code(err))
	}
}

func TestRollbackCreatesNewRevisionWithSourceConfig(t *testing.T) {
	source := revision.Record{Number: 2, Config: validCandidate()}
	st := &memoryStore{records: map[int64]revision.Record{2: source}}
	s := newService(t, st)
	resp, err := s.Rollback(context.Background(), &pb.RollbackRequest{ExpectedParentRevision: 4, SourceRevision: 2, Actor: "operator", Reason: "restore"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Revision.Revision != 5 || resp.Revision.ParentRevision != 4 || resp.Revision.RollbackSourceRevision != 2 || resp.Revision.Operation != "rollback" {
		t.Fatalf("unexpected rollback revision: %#v", resp.Revision)
	}
	if !proto.Equal(st.published.Config, source.Config) {
		t.Fatal("rollback did not publish source snapshot")
	}
}

func TestDiffRevisionsReportsSortedComponentChangesAndOrder(t *testing.T) {
	a := validCandidate()
	a.Routes = append(a.Routes, &pb.Route{Id: "old", Path: "/old", PathType: pb.PathType_PATH_TYPE_PREFIX, ClusterId: "backend"})
	a.Clusters = append(a.Clusters, &pb.Cluster{Id: "obsolete", Policy: pb.SelectionPolicy_SELECTION_POLICY_ROUND_ROBIN, Endpoints: []*pb.Endpoint{{Id: "one", Address: "http://127.0.0.1:9001", Weight: 1}}})
	b := proto.Clone(a).(*pb.GatewayConfig)
	b.Routes = []*pb.Route{{Id: "new", Path: "/new", PathType: pb.PathType_PATH_TYPE_PREFIX, ClusterId: "backend"}, proto.Clone(a.Routes[0]).(*pb.Route)}
	b.Clusters = b.Clusters[:1]
	b.Listener.IdleTimeoutNs = 123
	got := diff(a, b)
	if !got.ListenerChanged || !got.RouteOrderChanged || !got.ClusterOrderChanged || len(got.AddedRouteIds) != 1 || got.AddedRouteIds[0] != "new" || len(got.RemovedRouteIds) != 1 || got.RemovedRouteIds[0] != "old" || len(got.RemovedClusterIds) != 1 || got.RemovedClusterIds[0] != "obsolete" {
		t.Fatalf("unexpected diff: %#v", got)
	}
}

func TestAdminBoundaryValidationAndErrorMapping(t *testing.T) {
	for _, tc := range []struct {
		address string
		want    bool
	}{{"127.0.0.1:9090", true}, {"[::1]:9090", true}, {"0.0.0.0:9090", false}, {"localhost:9090", false}, {"192.0.2.1:9090", false}} {
		t.Run(tc.address, func(t *testing.T) {
			err := ValidateDevelopmentAddress(tc.address)
			if (err == nil) != tc.want {
				t.Fatalf("ValidateDevelopmentAddress(%q) error=%v", tc.address, err)
			}
		})
	}
	for input, want := range map[error]codes.Code{revision.ErrNotFound: codes.NotFound, revision.ErrConflict: codes.Aborted, revision.ErrCorrupt: codes.DataLoss, context.Canceled: codes.Canceled, context.DeadlineExceeded: codes.DeadlineExceeded, errors.New("db down"): codes.Unavailable} {
		if got := status.Code(apiError(input)); got != want {
			t.Errorf("apiError(%v) code=%s, want %s", input, got, want)
		}
	}
}
