// Package cluster provides endpoint health tracking and deterministic selection.
package cluster

import (
	"errors"
	"fmt"
	"sync"
)

var (
	ErrEmptyCluster       = errors.New("cluster must contain at least one endpoint")
	ErrNoHealthyEndpoints = errors.New("cluster has no healthy enabled endpoints")
	ErrUnknownEndpoint    = errors.New("unknown endpoint")
	ErrInvalidConfig      = errors.New("invalid cluster configuration")
	ErrSelectionSource    = errors.New("weighted selection requires a source")
)

// Policy identifies how an eligible endpoint is selected.
type Policy string

const (
	RoundRobin Policy = "round_robin"
	Weighted   Policy = "weighted_round_robin"
)

// HealthState is the local runtime state of an endpoint. Configured endpoints
// start Unknown; selection accepts Unknown until the unhealthy threshold is crossed.
type HealthState string

const (
	Unknown   HealthState = "unknown"
	Healthy   HealthState = "healthy"
	Unhealthy HealthState = "unhealthy"
)

// Endpoint is immutable cluster configuration; health is maintained separately.
type Endpoint struct {
	ID       string
	Address  string
	Weight   uint32
	Disabled bool
}

// HealthThresholds define consecutive probe results required to change state.
type HealthThresholds struct {
	Healthy   uint32
	Unhealthy uint32
}

// Config is validated once at construction. Endpoint configuration is copied.
type Config struct {
	Endpoints []Endpoint
	Health    HealthThresholds
}

// SelectionSource supplies a deterministic draw in [0,n) for weighted selection.
type SelectionSource interface {
	Uint64n(n uint64) uint64
}

type endpointHealth struct {
	state     HealthState
	successes uint32
	failures  uint32
}

// Cluster owns mutable health and selection cursor state for one config snapshot.
type Cluster struct {
	mu              sync.Mutex
	endpoints       []Endpoint
	health          map[string]*endpointHealth
	thresholds      HealthThresholds
	cursor          uint64
	weightedCurrent map[string]int64
	weightedKey     string
}

// New validates and copies endpoint configuration. Zero health thresholds default to 1.
func New(cfg Config) (*Cluster, error) {
	if len(cfg.Endpoints) == 0 {
		return nil, ErrEmptyCluster
	}
	if cfg.Health.Healthy == 0 {
		cfg.Health.Healthy = 1
	}
	if cfg.Health.Unhealthy == 0 {
		cfg.Health.Unhealthy = 1
	}
	seen := make(map[string]struct{}, len(cfg.Endpoints))
	endpoints := append([]Endpoint(nil), cfg.Endpoints...)
	for _, ep := range endpoints {
		if ep.ID == "" || ep.Weight == 0 {
			return nil, fmt.Errorf("%w: endpoint ID and positive weight are required", ErrInvalidConfig)
		}
		if _, ok := seen[ep.ID]; ok {
			return nil, fmt.Errorf("%w: duplicate endpoint ID %q", ErrInvalidConfig, ep.ID)
		}
		seen[ep.ID] = struct{}{}
	}
	health := make(map[string]*endpointHealth, len(endpoints))
	for _, ep := range endpoints {
		health[ep.ID] = &endpointHealth{state: Unknown}
	}
	return &Cluster{endpoints: endpoints, health: health, thresholds: cfg.Health}, nil
}

// Select returns one enabled endpoint that is not known Unhealthy. Unknown
// endpoints remain eligible until the unhealthy threshold is crossed. If all
// endpoints are unavailable, it returns ErrNoHealthyEndpoints. Weighted
// selection uses smooth weighted round robin. source breaks exact score ties,
// making tie outcomes deterministic and injectable by callers.
func (c *Cluster) Select(policy Policy, source SelectionSource) (Endpoint, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	eligible := make([]Endpoint, 0, len(c.endpoints))
	for _, ep := range c.endpoints {
		if !ep.Disabled && c.health[ep.ID].state != Unhealthy {
			eligible = append(eligible, ep)
		}
	}
	if len(eligible) == 0 {
		return Endpoint{}, ErrNoHealthyEndpoints
	}
	switch policy {
	case RoundRobin:
		ep := eligible[c.cursor%uint64(len(eligible))]
		c.cursor++
		return ep, nil
	case Weighted:
		if source == nil {
			return Endpoint{}, ErrSelectionSource
		}
		var total int64
		key := ""
		for _, ep := range eligible {
			total += int64(ep.Weight)
			key += fmt.Sprintf("%d:%s:%d;", len(ep.ID), ep.ID, ep.Weight)
		}
		if key != c.weightedKey {
			c.weightedKey = key
			c.weightedCurrent = make(map[string]int64, len(eligible))
		}
		var best int64
		candidates := make([]Endpoint, 0, len(eligible))
		for _, ep := range eligible {
			c.weightedCurrent[ep.ID] += int64(ep.Weight)
			if len(candidates) == 0 || c.weightedCurrent[ep.ID] > best {
				best = c.weightedCurrent[ep.ID]
				candidates = candidates[:0]
				candidates = append(candidates, ep)
			} else if c.weightedCurrent[ep.ID] == best {
				candidates = append(candidates, ep)
			}
		}
		selected := candidates[source.Uint64n(uint64(len(candidates)))%uint64(len(candidates))]
		c.weightedCurrent[selected.ID] -= total
		return selected, nil
	default:
		return Endpoint{}, fmt.Errorf("%w: unknown selection policy %q", ErrInvalidConfig, policy)
	}
}

// MarkResult applies one consecutive active health-check result.
func (c *Cluster) MarkResult(id string, success bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	h, ok := c.health[id]
	if !ok {
		return fmt.Errorf("%w: %q", ErrUnknownEndpoint, id)
	}
	if success {
		h.failures = 0
		if h.successes < c.thresholds.Healthy {
			h.successes++
		}
		if h.successes >= c.thresholds.Healthy {
			h.state = Healthy
		}
		return nil
	}
	h.successes = 0
	if h.failures < c.thresholds.Unhealthy {
		h.failures++
	}
	if h.failures >= c.thresholds.Unhealthy {
		h.state = Unhealthy
	}
	return nil
}

// Health returns the current health state for an endpoint.
func (c *Cluster) Health(id string) (HealthState, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	h, ok := c.health[id]
	if !ok {
		return "", fmt.Errorf("%w: %q", ErrUnknownEndpoint, id)
	}
	return h.state, nil
}
