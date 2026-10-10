// Package config defines the static listener, route, and cluster configuration
// accepted by Gatehouse.
package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/SShogun/GateHouse/internal/cluster"
	"github.com/SShogun/GateHouse/internal/dataplane"
	"github.com/SShogun/GateHouse/internal/router"
)

// Listener configures the HTTP listener and its server resource limits.
type Listener = dataplane.ServerConfig

// Cluster configures one named group of upstream endpoints.
type Cluster struct {
	ID          string
	Policy      cluster.Policy
	Endpoints   []cluster.Endpoint
	Health      cluster.HealthThresholds
	HealthCheck *HealthCheck
}

// HealthCheck enables scheduled HTTP health probes when attached to a cluster.
type HealthCheck struct {
	Path     string
	Interval time.Duration
	Timeout  time.Duration
}

// Config is one complete data-plane configuration snapshot.
type Config struct {
	Listener Listener
	Routes   []router.Route
	Clusters []Cluster
}

// Validate checks cross-reference and component-level configuration rules.
// It does not bind the listener or create runtime health-check resources.
func Validate(cfg Config) error {
	if cfg.Listener.Address == "" {
		return errors.New("listener address is required")
	}
	if err := validateListenerAddress(cfg.Listener.Address); err != nil {
		return fmt.Errorf("listener address: %w", err)
	}
	if len(cfg.Clusters) == 0 {
		return errors.New("at least one cluster is required")
	}

	clusterIDs := make([]string, 0, len(cfg.Clusters))
	seenClusters := make(map[string]struct{}, len(cfg.Clusters))
	for i, configured := range cfg.Clusters {
		if configured.ID == "" {
			return fmt.Errorf("cluster %d: ID must not be empty", i)
		}
		if _, exists := seenClusters[configured.ID]; exists {
			return fmt.Errorf("cluster %q: duplicate ID", configured.ID)
		}
		seenClusters[configured.ID] = struct{}{}
		clusterIDs = append(clusterIDs, configured.ID)

		if configured.Policy != cluster.RoundRobin && configured.Policy != cluster.Weighted {
			return fmt.Errorf("cluster %q: invalid selection policy %q", configured.ID, configured.Policy)
		}
		if len(configured.Endpoints) == 0 {
			return fmt.Errorf("cluster %q: at least one endpoint is required", configured.ID)
		}
		for j, endpoint := range configured.Endpoints {
			if endpoint.Address == "" {
				return fmt.Errorf("cluster %q endpoint %d: address is required", configured.ID, j)
			}
			if err := validateEndpointAddress(endpoint.Address); err != nil {
				return fmt.Errorf("cluster %q endpoint %q: %w", configured.ID, endpoint.ID, err)
			}
		}
		if _, err := cluster.New(cluster.Config{Endpoints: configured.Endpoints, Health: configured.Health}); err != nil {
			return fmt.Errorf("cluster %q: %w", configured.ID, err)
		}
		if check := configured.HealthCheck; check != nil {
			if err := validateHealthCheck(*check); err != nil {
				return fmt.Errorf("cluster %q health check: %w", configured.ID, err)
			}
		}
	}

	if _, err := router.Compile(router.Config{Routes: cfg.Routes}, clusterIDs); err != nil {
		return fmt.Errorf("routes: %w", err)
	}
	return nil
}

func validateEndpointAddress(address string) error {
	parsed, err := url.Parse(address)
	if err != nil {
		return fmt.Errorf("invalid endpoint address: %w", err)
	}
	if (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return errors.New("endpoint address must be an absolute http or https URL")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return errors.New("endpoint address must contain only a scheme and host")
	}
	return nil
}

func validateListenerAddress(address string) error {
	if strings.TrimSpace(address) != address {
		return errors.New("must not contain surrounding whitespace")
	}
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("must be host:port: %w", err)
	}
	if strings.ContainsAny(host, " \t\r\n") {
		return errors.New("host must not contain whitespace")
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 0 || portNumber > 65535 {
		return errors.New("port must be a number between 0 and 65535")
	}
	return nil
}

func validateHealthCheck(check HealthCheck) error {
	parsed, err := url.ParseRequestURI(check.Path)
	if err != nil || parsed.IsAbs() || parsed.Host != "" || parsed.Path == "" || parsed.Path[0] != '/' || parsed.RawQuery != "" || parsed.Fragment != "" || strings.ContainsAny(check.Path, "?#") {
		return errors.New("path must be an absolute URL path")
	}
	if check.Interval <= 0 {
		return errors.New("interval must be positive")
	}
	if check.Timeout <= 0 {
		return errors.New("timeout must be positive")
	}
	return nil
}
