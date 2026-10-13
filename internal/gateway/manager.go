package gateway

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	pb "github.com/SShogun/GateHouse/gen/go/gatehouse/v1"
	"github.com/SShogun/GateHouse/internal/config"
	"github.com/SShogun/GateHouse/internal/dataplane"
	"google.golang.org/protobuf/proto"
)

var (
	ErrStaleRevision     = errors.New("configuration revision is stale")
	ErrRevisionIntegrity = errors.New("configuration revision/hash integrity failure")
	ErrListenerMutation  = errors.New("configuration changes the process-start listener")
	ErrManagerClosed     = errors.New("gateway manager is closed")
)

const heartbeatStaleAfter = 15 * time.Second

type activeSnapshot struct {
	revision int64
	hash     string
	gateway  *Gateway
	refs     int
	retired  bool
}

// RuntimeStatus is a read-only view of readiness and control-plane freshness.
type RuntimeStatus struct {
	Ready         bool          `json:"ready"`
	Connected     bool          `json:"connected"`
	Stale         bool          `json:"stale"`
	Revision      int64         `json:"active_revision"`
	Hash          string        `json:"active_hash"`
	LastHeartbeat time.Time     `json:"last_heartbeat"`
	Staleness     time.Duration `json:"staleness_ns"`
}

// Manager atomically switches complete, precompiled gateway snapshots while
// keeping retired snapshots alive until every request that acquired them ends.
type Manager struct {
	mu            sync.Mutex
	activateMu    sync.Mutex
	listener      config.Listener
	logger        *slog.Logger
	registry      *clusterRegistry
	active        *activeSnapshot
	closed        bool
	connected     bool
	lastHeartbeat time.Time
}

func NewManager(listener config.Listener, logger *slog.Logger) (*Manager, error) {
	normalized, err := dataplane.NormalizeServerConfig(listener)
	if err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Manager{listener: normalized, logger: logger, registry: newClusterRegistry()}, nil
}

// Activate validates the hash and listener contract, compiles the entire
// candidate, then makes it visible to new requests in one atomic transition.
func (m *Manager) Activate(revision int64, hash string, candidate *pb.GatewayConfig) error {
	m.activateMu.Lock()
	defer m.activateMu.Unlock()
	if revision <= 0 {
		return ErrStaleRevision
	}
	if candidate == nil {
		return errors.New("configuration is required")
	}
	candidate = proto.Clone(candidate).(*pb.GatewayConfig)
	_, actualHash, err := config.EncodeCandidate(candidate)
	if err != nil {
		return err
	}
	if actualHash != hash {
		return ErrRevisionIntegrity
	}
	compiled, err := config.FromProto(candidate)
	if err != nil {
		return err
	}
	normalized, err := dataplane.NormalizeServerConfig(compiled.Listener)
	if err != nil {
		return err
	}
	if normalized != m.listener {
		return ErrListenerMutation
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return ErrManagerClosed
	}
	if current := m.active; current != nil {
		switch {
		case revision < current.revision:
			m.mu.Unlock()
			return ErrStaleRevision
		case revision == current.revision:
			m.mu.Unlock()
			if hash == current.hash {
				return nil
			}
			return ErrRevisionIntegrity
		}
	}
	m.mu.Unlock()
	gateway, err := newWithRegistry(compiled, m.logger, Options{}, m.registry)
	if err != nil {
		return err
	}
	next := &activeSnapshot{revision: revision, hash: hash, gateway: gateway}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		_ = gateway.Close()
		return ErrManagerClosed
	}
	previous := m.active
	m.active = next
	if previous != nil {
		previous.retired = true
	}
	closePrevious := previous != nil && previous.refs == 0
	m.mu.Unlock()
	if closePrevious {
		_ = previous.gateway.Close()
	}
	return nil
}

func (m *Manager) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet && r.URL.Path == "/_gatehouse/readyz" {
		if m.Status().Ready {
			w.WriteHeader(http.StatusOK)
		} else {
			http.Error(w, "configuration not ready", http.StatusServiceUnavailable)
		}
		return
	}
	if r.Method == http.MethodGet && r.URL.Path == "/_gatehouse/status" {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(m.Status())
		return
	}
	m.mu.Lock()
	active := m.active
	if active != nil {
		active.refs++
	}
	m.mu.Unlock()
	if active == nil {
		http.Error(w, "configuration not ready", http.StatusServiceUnavailable)
		return
	}
	defer m.release(active)
	active.gateway.ServeHTTP(w, r)
}

func (m *Manager) release(snapshot *activeSnapshot) {
	m.mu.Lock()
	snapshot.refs--
	closeSnapshot := snapshot.retired && snapshot.refs == 0
	m.mu.Unlock()
	if closeSnapshot {
		_ = snapshot.gateway.Close()
	}
}

// SetConnected records stream state without changing the active runtime.
func (m *Manager) SetConnected(connected bool) {
	m.mu.Lock()
	m.connected = connected
	if connected {
		m.lastHeartbeat = time.Now()
	}
	m.mu.Unlock()
}

// Heartbeat refreshes control-plane freshness while the data plane keeps
// serving its last-known-good snapshot.
func (m *Manager) Heartbeat() {
	m.mu.Lock()
	m.connected = true
	m.lastHeartbeat = time.Now()
	m.mu.Unlock()
}

func (m *Manager) Status() RuntimeStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	status := RuntimeStatus{Ready: m.active != nil, Connected: m.connected, LastHeartbeat: m.lastHeartbeat}
	if m.active != nil {
		status.Revision = m.active.revision
		status.Hash = m.active.hash
	}
	if !m.lastHeartbeat.IsZero() {
		status.Staleness = time.Since(m.lastHeartbeat)
	}
	status.Stale = !m.connected || status.Staleness > heartbeatStaleAfter
	return status
}

func (m *Manager) Close() error {
	m.activateMu.Lock()
	defer m.activateMu.Unlock()
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	active := m.active
	m.active = nil
	if active != nil {
		active.retired = true
	}
	closeActive := active != nil && active.refs == 0
	m.mu.Unlock()
	if closeActive {
		return active.gateway.Close()
	}
	return nil
}

var _ http.Handler = (*Manager)(nil)
