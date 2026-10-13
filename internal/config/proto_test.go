package config

import (
	"bytes"
	"strings"
	"testing"
	"time"

	pb "github.com/SShogun/GateHouse/gen/go/gatehouse/v1"
	"github.com/SShogun/GateHouse/internal/cluster"
	"github.com/SShogun/GateHouse/internal/dataplane"
	"github.com/SShogun/GateHouse/internal/router"
	"google.golang.org/protobuf/proto"
)

func TestProtoConversionPreservesM2Config(t *testing.T) {
	want := Config{
		Listener: dataplane.ServerConfig{Address: "127.0.0.1:8080", ReadHeaderTimeout: 3 * time.Second, IdleTimeout: 47 * time.Second, MaxHeaderBytes: 32768, MaxConcurrentRequests: 321, RequestBodyReadIdleTimeout: 19 * time.Second},
		Routes:   []router.Route{{ID: "api", Host: "*.example.com", Path: "/v1", PathType: router.PathPrefix, Methods: []string{"GET", "POST"}, Headers: map[string]string{"X-Region": "west", "X-Canary": "yes"}, ClusterID: "primary"}},
		Clusters: []Cluster{{ID: "primary", Policy: cluster.Weighted, Health: cluster.HealthThresholds{Healthy: 2, Unhealthy: 3}, HealthCheck: &HealthCheck{Path: "/ready", Interval: 7 * time.Second, Timeout: 800 * time.Millisecond}, Endpoints: []cluster.Endpoint{{ID: "a", Address: "http://127.0.0.1:9001", Weight: 4, Disabled: true}, {ID: "b", Address: "https://backend.example:9443", Weight: 9}}}},
	}
	encoded, err := ToProto(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := FromProto(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if !configEqual(got, want) {
		t.Fatalf("round trip did not preserve M2 config:\n got: %#v\nwant: %#v", got, want)
	}
}

func TestEncodeCandidateHashIsDeterministicAndTracksConfig(t *testing.T) {
	a := &pb.GatewayConfig{SchemaVersion: 1, Listener: &pb.Listener{Address: "127.0.0.1:8080"}, Clusters: []*pb.Cluster{{Id: "backend", Policy: pb.SelectionPolicy_SELECTION_POLICY_ROUND_ROBIN, Endpoints: []*pb.Endpoint{{Id: "one", Address: "http://127.0.0.1:9000", Weight: 1}}}}, Routes: []*pb.Route{{Id: "root", Path: "/", PathType: pb.PathType_PATH_TYPE_PREFIX, ClusterId: "backend", Headers: map[string]string{"X-Z": "z", "X-A": "a"}}}}
	b := proto.Clone(a).(*pb.GatewayConfig)
	b.Routes[0].Headers = map[string]string{"X-A": "a", "X-Z": "z"}
	bytesA, hashA, err := EncodeCandidate(a)
	if err != nil {
		t.Fatal(err)
	}
	bytesB, hashB, err := EncodeCandidate(b)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(bytesA, bytesB) || hashA != hashB {
		t.Fatal("map insertion order changed canonical snapshot hash")
	}
	b.Routes[0].Headers["X-A"] = "changed"
	_, hashChanged, err := EncodeCandidate(b)
	if err != nil {
		t.Fatal(err)
	}
	if hashChanged == hashA {
		t.Fatal("config change did not change content hash")
	}
}

func TestEncodeCandidateRejectsUnsupportedOrInvalidInput(t *testing.T) {
	valid := func() *pb.GatewayConfig {
		return &pb.GatewayConfig{SchemaVersion: 1, Listener: &pb.Listener{Address: "127.0.0.1:8080"}, Clusters: []*pb.Cluster{{Id: "backend", Policy: pb.SelectionPolicy_SELECTION_POLICY_ROUND_ROBIN, Endpoints: []*pb.Endpoint{{Id: "one", Address: "http://127.0.0.1:9000", Weight: 1}}}}, Routes: []*pb.Route{{Id: "root", Path: "/", PathType: pb.PathType_PATH_TYPE_PREFIX, ClusterId: "backend"}}}
	}
	cases := []struct {
		name   string
		mutate func(*pb.GatewayConfig)
	}{
		{"unsupported schema", func(c *pb.GatewayConfig) { c.SchemaVersion = 2 }},
		{"unknown fields", func(c *pb.GatewayConfig) { c.ProtoReflect().SetUnknown([]byte{0x98, 0x06, 0x01}) }},
		{"unknown nested fields", func(c *pb.GatewayConfig) {
			c.Clusters[0].Endpoints[0].ProtoReflect().SetUnknown([]byte{0x98, 0x06, 0x01})
		}},
		{"unspecified policy", func(c *pb.GatewayConfig) { c.Clusters[0].Policy = pb.SelectionPolicy_SELECTION_POLICY_UNSPECIFIED }},
		{"unknown route enum", func(c *pb.GatewayConfig) { c.Routes[0].PathType = pb.PathType(99) }},
		{"unknown cluster reference", func(c *pb.GatewayConfig) { c.Routes[0].ClusterId = "missing" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			candidate := valid()
			tc.mutate(candidate)
			if _, _, err := EncodeCandidate(candidate); err == nil {
				t.Fatal("EncodeCandidate() error=nil, want rejection")
			}
		})
	}
	if _, _, err := EncodeCandidate(nil); err == nil {
		t.Fatal("nil config accepted")
	}
	oversized := valid()
	oversized.Routes[0].Path = "/" + strings.Repeat("x", MaxSnapshotBytes)
	if _, _, err := EncodeCandidate(oversized); err == nil {
		t.Fatal("oversized config accepted")
	}
}

func FuzzConfigDecodeValidate(f *testing.F) {
	valid := &pb.GatewayConfig{
		SchemaVersion: 1,
		Listener:      &pb.Listener{Address: "127.0.0.1:8080"},
		Clusters: []*pb.Cluster{{
			Id: "backend", Policy: pb.SelectionPolicy_SELECTION_POLICY_ROUND_ROBIN,
			Endpoints: []*pb.Endpoint{{Id: "one", Address: "http://127.0.0.1:9000", Weight: 1}},
		}},
		Routes: []*pb.Route{{Id: "root", Path: "/", PathType: pb.PathType_PATH_TYPE_PREFIX, ClusterId: "backend"}},
	}
	validBytes, err := proto.Marshal(valid)
	if err != nil {
		f.Fatal(err)
	}
	f.Add(validBytes)
	f.Add([]byte{})
	f.Add([]byte{0xff})
	f.Add([]byte{0x98, 0x06, 0x01}) // unknown protobuf field

	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > MaxSnapshotBytes {
			return
		}
		candidate := new(pb.GatewayConfig)
		if err := proto.Unmarshal(data, candidate); err != nil {
			return
		}
		canonical, hash, err := EncodeCandidate(candidate)
		if err != nil {
			return
		}

		var decoded pb.GatewayConfig
		if err := proto.Unmarshal(canonical, &decoded); err != nil {
			t.Fatalf("EncodeCandidate returned invalid protobuf: %v", err)
		}
		canonicalAgain, hashAgain, err := EncodeCandidate(&decoded)
		if err != nil {
			t.Fatalf("EncodeCandidate rejected its canonical output: %v", err)
		}
		if !bytes.Equal(canonical, canonicalAgain) || hash != hashAgain {
			t.Fatal("canonical config bytes or hash changed after decode and revalidation")
		}
	})
}

func configEqual(a, b Config) bool {
	pa, ea := ToProto(a)
	pb, eb := ToProto(b)
	return ea == nil && eb == nil && proto.Equal(pa, pb)
}

var _ = bytes.Equal
