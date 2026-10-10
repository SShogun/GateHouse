package config

import (
	"testing"
	"time"

	"github.com/SShogun/GateHouse/internal/cluster"
	"github.com/SShogun/GateHouse/internal/dataplane"
	"github.com/SShogun/GateHouse/internal/router"
)

func validConfig() Config {
	return Config{
		Listener: dataplane.ServerConfig{Address: "127.0.0.1:8080"},
		Clusters: []Cluster{{
			ID: "backend", Policy: cluster.RoundRobin,
			Endpoints: []cluster.Endpoint{{ID: "primary", Address: "http://127.0.0.1:9000", Weight: 1}},
		}},
		Routes: []router.Route{{ID: "root", Path: "/", PathType: router.PathPrefix, ClusterID: "backend"}},
	}
}

func TestValidateAcceptsOptionalHealthCheck(t *testing.T) {
	cfg := validConfig()
	cfg.Clusters[0].HealthCheck = &HealthCheck{Path: "/ready", Interval: time.Second, Timeout: 250 * time.Millisecond}
	if err := Validate(cfg); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestValidateRejectsInvalidHealthCheck(t *testing.T) {
	tests := []struct {
		name  string
		check HealthCheck
	}{
		{"relative path", HealthCheck{Path: "ready", Interval: time.Second, Timeout: time.Second}},
		{"path with query", HealthCheck{Path: "/ready?x=1", Interval: time.Second, Timeout: time.Second}},
		{"path with fragment", HealthCheck{Path: "/ready#ok", Interval: time.Second, Timeout: time.Second}},
		{"empty path", HealthCheck{Path: "", Interval: time.Second, Timeout: time.Second}},
		{"zero interval", HealthCheck{Path: "/ready", Timeout: time.Second}},
		{"negative interval", HealthCheck{Path: "/ready", Interval: -time.Second, Timeout: time.Second}},
		{"zero timeout", HealthCheck{Path: "/ready", Interval: time.Second}},
		{"negative timeout", HealthCheck{Path: "/ready", Interval: time.Second, Timeout: -time.Second}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validConfig()
			cfg.Clusters[0].HealthCheck = &tt.check
			if err := Validate(cfg); err == nil {
				t.Fatal("Validate() error = nil, want health-check validation error")
			}
		})
	}
}

func TestValidateAcceptsMinimalCompleteConfig(t *testing.T) {
	if err := Validate(validConfig()); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestValidateRejectsInvalidSemantics(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{"empty listener address", func(c *Config) { c.Listener.Address = "" }},
		{"malformed listener address", func(c *Config) { c.Listener.Address = "not-an-address" }},
		{"listener port out of range", func(c *Config) { c.Listener.Address = "127.0.0.1:70000" }},
		{"no clusters", func(c *Config) { c.Clusters = nil }},
		{"empty cluster id", func(c *Config) { c.Clusters[0].ID = "" }},
		{"duplicate cluster id", func(c *Config) { c.Clusters = append(c.Clusters, c.Clusters[0]) }},
		{"empty endpoints", func(c *Config) { c.Clusters[0].Endpoints = nil }},
		{"empty endpoint id", func(c *Config) { c.Clusters[0].Endpoints[0].ID = "" }},
		{"empty endpoint address", func(c *Config) { c.Clusters[0].Endpoints[0].Address = "" }},
		{"non-absolute endpoint address", func(c *Config) { c.Clusters[0].Endpoints[0].Address = "backend:9000" }},
		{"endpoint base path", func(c *Config) { c.Clusters[0].Endpoints[0].Address = "http://backend:9000/api" }},
		{"zero endpoint weight", func(c *Config) { c.Clusters[0].Endpoints[0].Weight = 0 }},
		{"duplicate endpoint id", func(c *Config) { c.Clusters[0].Endpoints = append(c.Clusters[0].Endpoints, c.Clusters[0].Endpoints[0]) }},
		{"invalid policy", func(c *Config) { c.Clusters[0].Policy = "random" }},
		{"empty route id", func(c *Config) { c.Routes[0].ID = "" }},
		{"duplicate route id", func(c *Config) { c.Routes = append(c.Routes, c.Routes[0]) }},
		{"unresolved route cluster", func(c *Config) { c.Routes[0].ClusterID = "missing" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validConfig()
			tt.mutate(&cfg)
			if err := Validate(cfg); err == nil {
				t.Fatal("Validate() error = nil, want validation error")
			}
		})
	}
}
