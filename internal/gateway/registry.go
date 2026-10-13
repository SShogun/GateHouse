package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/SShogun/GateHouse/internal/cluster"
	"github.com/SShogun/GateHouse/internal/config"
	"github.com/SShogun/GateHouse/internal/dataplane"
	"google.golang.org/protobuf/proto"
)

const (
	defaultProbeTimeout = 5 * time.Second
	maxProbeConcurrency = 16
)

type clusterRegistry struct {
	mu      sync.Mutex
	entries map[string]*clusterRuntime
}

func newClusterRegistry() *clusterRegistry {
	return &clusterRegistry{entries: make(map[string]*clusterRuntime)}
}

func (r *clusterRegistry) acquire(spec config.Cluster, logger *slog.Logger, probeHTTP *http.Client) (*clusterRuntime, error) {
	hash, err := clusterSpecHash(spec)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if existing := r.entries[hash]; existing != nil {
		existing.refs++
		return existing, nil
	}
	selection, err := cluster.New(cluster.Config{Endpoints: spec.Endpoints, Health: spec.Health})
	if err != nil {
		return nil, err
	}
	state := &clusterRuntime{config: cloneCluster(spec), selection: selection, endpoints: make(map[string]*endpointRuntime, len(spec.Endpoints)), specHash: hash, refs: 1}
	for _, endpoint := range spec.Endpoints {
		proxy, proxyErr := dataplane.NewProxy(dataplane.Config{UpstreamURL: endpoint.Address}, logger)
		if proxyErr != nil {
			for _, created := range state.endpoints {
				created.proxy.CloseIdleConnections()
			}
			return nil, fmt.Errorf("create proxy for endpoint %q: %w", endpoint.ID, proxyErr)
		}
		state.endpoints[endpoint.ID] = &endpointRuntime{proxy: proxy}
	}
	if spec.HealthCheck != nil {
		if probeHTTP == nil {
			probeHTTP = http.DefaultClient
		}
		probeTimeout := defaultProbeTimeout
		if spec.HealthCheck.Timeout > probeTimeout {
			probeTimeout = spec.HealthCheck.Timeout
		}
		state.scheduler = cluster.NewSchedulerWithOptions(cluster.SchedulerOptions{ProbeTimeout: probeTimeout, MaxConcurrent: maxProbeConcurrency})
		for _, endpoint := range spec.Endpoints {
			endpointID, address, health := endpoint.ID, endpoint.Address, *spec.HealthCheck
			if err := state.scheduler.Add(scheduleID(spec.ID, endpointID), health.Interval, func(parent context.Context) error {
				return state.checkEndpoint(parent, endpointID, address, health, probeHTTP)
			}); err != nil {
				state.scheduler.Stop()
				for _, created := range state.endpoints {
					created.proxy.CloseIdleConnections()
				}
				return nil, fmt.Errorf("schedule health check for endpoint %q: %w", endpointID, err)
			}
		}
	}
	r.entries[hash] = state
	return state, nil
}

func (r *clusterRegistry) release(state *clusterRuntime) {
	if state == nil {
		return
	}
	r.mu.Lock()
	state.refs--
	if state.refs > 0 {
		r.mu.Unlock()
		return
	}
	delete(r.entries, state.specHash)
	r.mu.Unlock()
	if state.scheduler != nil {
		state.scheduler.Stop()
	}
	for _, endpoint := range state.endpoints {
		endpoint.proxy.CloseIdleConnections()
	}
}

func (state *clusterRuntime) checkEndpoint(parent context.Context, endpointID, address string, health config.HealthCheck, probeHTTP *http.Client) error {
	ctx, cancel := context.WithTimeout(parent, health.Timeout)
	defer cancel()
	target, err := url.Parse(address)
	if err == nil {
		var probePath *url.URL
		probePath, err = url.ParseRequestURI(health.Path)
		if err == nil {
			target.Path = probePath.Path
			target.RawPath = probePath.RawPath
		}
		target.RawQuery = ""
		target.Fragment = ""
	}
	success := false
	if err == nil {
		request, requestErr := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
		if requestErr == nil {
			response, doErr := probeHTTP.Do(request)
			if doErr == nil {
				_, copyErr := io.Copy(io.Discard, response.Body)
				_ = response.Body.Close()
				success = copyErr == nil && ctx.Err() == nil && response.StatusCode >= 200 && response.StatusCode < 300
			}
		}
	}
	markErr := state.selection.MarkResult(endpointID, success)
	if err != nil {
		return err
	}
	if markErr != nil {
		return markErr
	}
	if !success {
		return errors.New("health probe failed")
	}
	return nil
}

func clusterSpecHash(spec config.Cluster) (string, error) {
	converted, err := config.ToProto(config.Config{
		Listener: config.Listener{Address: "127.0.0.1:0"}, Clusters: []config.Cluster{spec},
	})
	if err != nil {
		return "", err
	}
	data, err := (proto.MarshalOptions{Deterministic: true}).Marshal(converted.Clusters[0])
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func cloneCluster(spec config.Cluster) config.Cluster {
	copy := spec
	copy.Endpoints = append([]cluster.Endpoint(nil), spec.Endpoints...)
	if spec.HealthCheck != nil {
		health := *spec.HealthCheck
		copy.HealthCheck = &health
	}
	return copy
}
