package config

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"time"

	pb "github.com/SShogun/GateHouse/gen/go/gatehouse/v1"
	"github.com/SShogun/GateHouse/internal/cluster"
	"github.com/SShogun/GateHouse/internal/router"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

const MaxSnapshotBytes = 1 << 20

// EncodeCandidate validates and dry-compiles a cloned candidate without starting
// runtime workers. Its deterministic wire bytes are the durable hash input.
func EncodeCandidate(candidate *pb.GatewayConfig) ([]byte, string, error) {
	if candidate == nil {
		return nil, "", errors.New("configuration is required")
	}
	if proto.Size(candidate) > MaxSnapshotBytes {
		return nil, "", errors.New("configuration exceeds 1 MiB")
	}
	candidate = proto.Clone(candidate).(*pb.GatewayConfig)
	if hasUnknown(candidate.ProtoReflect()) {
		return nil, "", errors.New("unsupported configuration fields")
	}
	cfg, err := FromProto(candidate)
	if err != nil {
		return nil, "", err
	}
	if err := Validate(cfg); err != nil {
		return nil, "", err
	}
	data, err := (proto.MarshalOptions{Deterministic: true}).Marshal(candidate)
	if err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(data)
	return data, hex.EncodeToString(sum[:]), nil
}

func hasUnknown(m protoreflect.Message) bool {
	if len(m.GetUnknown()) != 0 {
		return true
	}
	found := false
	m.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		if fd.IsMap() {
			if fd.MapValue().Message() != nil {
				v.Map().Range(func(_ protoreflect.MapKey, x protoreflect.Value) bool { found = hasUnknown(x.Message()); return !found })
			}
		} else if fd.IsList() && fd.Message() != nil {
			for i := 0; i < v.List().Len(); i++ {
				if hasUnknown(v.List().Get(i).Message()) {
					found = true
					break
				}
			}
		} else if fd.Message() != nil {
			found = hasUnknown(v.Message())
		}
		return !found
	})
	return found
}

// FromProto preserves M2 configuration fields and rejects unsupported versions/enums.
func FromProto(p *pb.GatewayConfig) (Config, error) {
	if p == nil || p.SchemaVersion != 1 {
		return Config{}, errors.New("schema_version must be 1")
	}
	if p.Listener == nil {
		return Config{}, errors.New("listener is required")
	}
	if len(p.Routes) > 1000 || len(p.Clusters) > 1000 {
		return Config{}, errors.New("at most 1000 routes and clusters are supported")
	}
	l := p.Listener
	if int64(int(l.MaxHeaderBytes)) != l.MaxHeaderBytes || int64(int(l.MaxConcurrentRequests)) != l.MaxConcurrentRequests {
		return Config{}, errors.New("listener limit overflows platform integer")
	}
	out := Config{Listener: Listener{Address: l.Address, ReadHeaderTimeout: time.Duration(l.ReadHeaderTimeoutNs), IdleTimeout: time.Duration(l.IdleTimeoutNs), MaxHeaderBytes: int(l.MaxHeaderBytes), MaxConcurrentRequests: int(l.MaxConcurrentRequests), RequestBodyReadIdleTimeout: time.Duration(l.RequestBodyReadIdleTimeoutNs)}}
	for _, r := range p.Routes {
		if r == nil {
			return Config{}, errors.New("nil route")
		}
		var pathType router.PathType
		switch r.PathType {
		case pb.PathType_PATH_TYPE_EXACT:
			pathType = router.PathExact
		case pb.PathType_PATH_TYPE_PREFIX:
			pathType = router.PathPrefix
		default:
			return Config{}, errors.New("unsupported path type")
		}
		out.Routes = append(out.Routes, router.Route{ID: r.Id, Host: r.Host, Path: r.Path, PathType: pathType, Methods: append([]string(nil), r.Methods...), Headers: maps.Clone(r.Headers), ClusterID: r.ClusterId})
	}
	for _, c := range p.Clusters {
		if c == nil {
			return Config{}, errors.New("nil cluster")
		}
		var policy cluster.Policy
		switch c.Policy {
		case pb.SelectionPolicy_SELECTION_POLICY_ROUND_ROBIN:
			policy = cluster.RoundRobin
		case pb.SelectionPolicy_SELECTION_POLICY_WEIGHTED_ROUND_ROBIN:
			policy = cluster.Weighted
		default:
			return Config{}, errors.New("unsupported selection policy")
		}
		dst := Cluster{ID: c.Id, Policy: policy, Health: cluster.HealthThresholds{Healthy: c.HealthyThreshold, Unhealthy: c.UnhealthyThreshold}}
		if len(c.Endpoints) > 1000 {
			return Config{}, fmt.Errorf("cluster %q exceeds 1000 endpoints", c.Id)
		}
		for _, e := range c.Endpoints {
			if e == nil {
				return Config{}, errors.New("nil endpoint")
			}
			dst.Endpoints = append(dst.Endpoints, cluster.Endpoint{ID: e.Id, Address: e.Address, Weight: e.Weight, Disabled: e.Disabled})
		}
		if h := c.HealthCheck; h != nil {
			dst.HealthCheck = &HealthCheck{Path: h.Path, Interval: time.Duration(h.IntervalNs), Timeout: time.Duration(h.TimeoutNs)}
		}
		out.Clusters = append(out.Clusters, dst)
	}
	return out, nil
}

// ToProto represents existing static configuration as a schema-version-1 snapshot.
func ToProto(cfg Config) (*pb.GatewayConfig, error) {
	l := cfg.Listener
	p := &pb.GatewayConfig{SchemaVersion: 1, Listener: &pb.Listener{Address: l.Address, ReadHeaderTimeoutNs: int64(l.ReadHeaderTimeout), IdleTimeoutNs: int64(l.IdleTimeout), MaxHeaderBytes: int64(l.MaxHeaderBytes), MaxConcurrentRequests: int64(l.MaxConcurrentRequests), RequestBodyReadIdleTimeoutNs: int64(l.RequestBodyReadIdleTimeout)}}
	for _, r := range cfg.Routes {
		var t pb.PathType
		switch r.PathType {
		case router.PathExact:
			t = pb.PathType_PATH_TYPE_EXACT
		case router.PathPrefix:
			t = pb.PathType_PATH_TYPE_PREFIX
		default:
			return nil, errors.New("unsupported path type")
		}
		p.Routes = append(p.Routes, &pb.Route{Id: r.ID, Host: r.Host, Path: r.Path, PathType: t, Methods: append([]string(nil), r.Methods...), Headers: maps.Clone(r.Headers), ClusterId: r.ClusterID})
	}
	for _, c := range cfg.Clusters {
		var policy pb.SelectionPolicy
		switch c.Policy {
		case cluster.RoundRobin:
			policy = pb.SelectionPolicy_SELECTION_POLICY_ROUND_ROBIN
		case cluster.Weighted:
			policy = pb.SelectionPolicy_SELECTION_POLICY_WEIGHTED_ROUND_ROBIN
		default:
			return nil, errors.New("unsupported selection policy")
		}
		dst := &pb.Cluster{Id: c.ID, Policy: policy, HealthyThreshold: c.Health.Healthy, UnhealthyThreshold: c.Health.Unhealthy}
		for _, e := range c.Endpoints {
			dst.Endpoints = append(dst.Endpoints, &pb.Endpoint{Id: e.ID, Address: e.Address, Weight: e.Weight, Disabled: e.Disabled})
		}
		if h := c.HealthCheck; h != nil {
			dst.HealthCheck = &pb.HealthCheck{Path: h.Path, IntervalNs: int64(h.Interval), TimeoutNs: int64(h.Timeout)}
		}
		p.Clusters = append(p.Clusters, dst)
	}
	return p, nil
}
