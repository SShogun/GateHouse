// Package gateway connects the compiled route matcher to cluster selection and
// reusable endpoint proxies for one static data-plane snapshot.
package gateway

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/SShogun/GateHouse/internal/cluster"
	"github.com/SShogun/GateHouse/internal/config"
	"github.com/SShogun/GateHouse/internal/dataplane"
	"github.com/SShogun/GateHouse/internal/router"
)

// Options supplies dependencies that need deterministic control in runtime
// tests. Nil values select their standard library defaults.
type Options struct {
	ProbeClient *http.Client
}

type endpointRuntime struct {
	proxy *dataplane.Proxy
}

type clusterRuntime struct {
	config    config.Cluster
	selection *cluster.Cluster
	endpoints map[string]*endpointRuntime
}

// Gateway owns a compiled route set, cluster health state, endpoint proxies,
// and the active health-check scheduler.
type Gateway struct {
	routes    *router.Matcher
	clusters  map[string]*clusterRuntime
	logger    *slog.Logger
	scheduler *cluster.Scheduler
	probeHTTP *http.Client
	closeOnce sync.Once
}

const (
	defaultProbeTimeout = 5 * time.Second
	maxProbeConcurrency = 16
)

// New validates and constructs the data-plane runtime.
func New(cfg config.Config, logger *slog.Logger) (*Gateway, error) {
	return NewWithOptions(cfg, logger, Options{})
}

// NewWithOptions validates and constructs a gateway with injectable probe HTTP.
func NewWithOptions(cfg config.Config, logger *slog.Logger, options Options) (*Gateway, error) {
	if err := config.Validate(cfg); err != nil {
		return nil, fmt.Errorf("validate gateway config: %w", err)
	}
	if logger == nil {
		logger = slog.Default()
	}
	clusterIDs := make([]string, 0, len(cfg.Clusters))
	for _, item := range cfg.Clusters {
		clusterIDs = append(clusterIDs, item.ID)
	}
	routes, err := router.Compile(router.Config{Routes: cfg.Routes}, clusterIDs)
	if err != nil {
		return nil, fmt.Errorf("compile gateway routes: %w", err)
	}
	probeTimeout := defaultProbeTimeout
	for _, configured := range cfg.Clusters {
		if configured.HealthCheck != nil && configured.HealthCheck.Timeout > probeTimeout {
			probeTimeout = configured.HealthCheck.Timeout
		}
	}
	g := &Gateway{
		routes: routes, clusters: make(map[string]*clusterRuntime, len(cfg.Clusters)),
		logger: logger,
		scheduler: cluster.NewSchedulerWithOptions(cluster.SchedulerOptions{
			ProbeTimeout: probeTimeout, MaxConcurrent: maxProbeConcurrency,
		}),
		probeHTTP: options.ProbeClient,
	}
	if g.probeHTTP == nil {
		g.probeHTTP = http.DefaultClient
	}
	type scheduledHealthCheck struct {
		id       string
		interval time.Duration
		probe    cluster.Probe
	}
	var schedules []scheduledHealthCheck
	for _, configured := range cfg.Clusters {
		selection, err := cluster.New(cluster.Config{Endpoints: configured.Endpoints, Health: configured.Health})
		if err != nil {
			g.Close()
			return nil, fmt.Errorf("create cluster %q: %w", configured.ID, err)
		}
		state := &clusterRuntime{config: configured, selection: selection, endpoints: make(map[string]*endpointRuntime, len(configured.Endpoints))}
		g.clusters[configured.ID] = state
		for _, ep := range configured.Endpoints {
			proxy, err := dataplane.NewProxy(dataplane.Config{UpstreamURL: ep.Address}, logger)
			if err != nil {
				g.Close()
				return nil, fmt.Errorf("create proxy for cluster %q endpoint %q: %w", configured.ID, ep.ID, err)
			}
			state.endpoints[ep.ID] = &endpointRuntime{proxy: proxy}
			if configured.HealthCheck != nil {
				clusterID, endpointID, health := configured.ID, ep.ID, *configured.HealthCheck
				address := ep.Address
				schedules = append(schedules, scheduledHealthCheck{
					id: scheduleID(clusterID, endpointID), interval: health.Interval,
					probe: func(parent context.Context) error {
						return g.checkEndpoint(parent, clusterID, endpointID, address, health)
					},
				})
			}
		}
	}
	// Start workers only after the cluster map and every endpoint proxy are
	// complete; Add runs its first probe immediately on another goroutine.
	for _, schedule := range schedules {
		if err := g.scheduler.Add(schedule.id, schedule.interval, schedule.probe); err != nil {
			g.Close()
			return nil, fmt.Errorf("schedule health check %q: %w", schedule.id, err)
		}
	}
	return g, nil
}

func scheduleID(clusterID, endpointID string) string {
	return strconv.Itoa(len(clusterID)) + ":" + clusterID + strconv.Itoa(len(endpointID)) + ":" + endpointID
}

func (g *Gateway) checkEndpoint(parent context.Context, clusterID, endpointID, address string, health config.HealthCheck) error {
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
			response, doErr := g.probeHTTP.Do(request)
			if doErr == nil {
				_, copyErr := io.Copy(io.Discard, response.Body)
				_ = response.Body.Close()
				success = copyErr == nil && ctx.Err() == nil && response.StatusCode >= 200 && response.StatusCode < 300
			}
		}
	}
	markErr := g.MarkResult(clusterID, endpointID, success)
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

// MarkResult applies a probe outcome. It is also a deterministic seam for
// health integrations that deliver results from outside the HTTP scheduler.
func (g *Gateway) MarkResult(clusterID, endpointID string, success bool) error {
	state, ok := g.clusters[clusterID]
	if !ok {
		return fmt.Errorf("unknown cluster %q", clusterID)
	}
	return state.selection.MarkResult(endpointID, success)
}

// ServeHTTP matches a request, selects an eligible endpoint, and forwards it.
func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	route, matched := g.routes.Match(router.Request{Host: r.Host, Path: r.URL.EscapedPath(), Method: r.Method, Headers: r.Header})
	fields := []any{"method", r.Method, "route_id", "", "cluster_id", "", "endpoint_id", ""}
	if !matched {
		http.NotFound(w, r)
		g.logRequest(fields, http.StatusNotFound, started)
		return
	}
	fields[3] = route.ID
	fields[5] = route.ClusterID
	clusterState := g.clusters[route.ClusterID]
	endpoint, err := clusterState.selection.Select(clusterState.config.Policy, stableSelectionSource{})
	if err != nil {
		http.Error(w, "service unavailable", http.StatusServiceUnavailable)
		g.logRequest(fields, http.StatusServiceUnavailable, started)
		return
	}
	fields[7] = endpoint.ID
	response := &telemetryWriter{ResponseWriter: w}
	func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				status := response.status
				g.logRequest(fields, status, started)
				panic(recovered)
			}
		}()
		clusterState.endpoints[endpoint.ID].proxy.ServeHTTP(response, r)
	}()
	status := response.status
	if status == 0 {
		status = http.StatusOK
	}
	g.logRequest(fields, status, started)
}

func (g *Gateway) logRequest(fields []any, status int, started time.Time) {
	g.logger.Info("gateway request complete", append(fields,
		"status", status, "duration", time.Since(started),
	)...)
}

type stableSelectionSource struct{}

func (stableSelectionSource) Uint64n(n uint64) uint64 { return 0 }

type telemetryWriter struct {
	http.ResponseWriter
	status int
}

func (w *telemetryWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *telemetryWriter) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	if status >= 100 && status < 200 && status != http.StatusSwitchingProtocols {
		w.ResponseWriter.WriteHeader(status)
		return
	}
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *telemetryWriter) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(body)
}

// Close stops and joins health checks, then releases idle proxy connections.
func (g *Gateway) Close() error {
	g.closeOnce.Do(func() {
		g.scheduler.Stop()
		for _, state := range g.clusters {
			for _, endpoint := range state.endpoints {
				endpoint.proxy.CloseIdleConnections()
			}
		}
	})
	return nil
}
