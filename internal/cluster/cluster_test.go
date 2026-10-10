package cluster

import (
	"errors"
	"testing"
)

type sequenceSource struct {
	values []uint64
	index  int
}

func (s *sequenceSource) Uint64n(n uint64) uint64 {
	v := s.values[s.index] % n
	s.index++
	return v
}

func TestNewRejectsEmptyCluster(t *testing.T) {
	if _, err := New(Config{}); !errors.Is(err, ErrEmptyCluster) {
		t.Fatalf("New(Config{}) error = %v, want ErrEmptyCluster", err)
	}
}

func TestRoundRobinCyclesEnabledUnknownEndpoints(t *testing.T) {
	c, err := New(Config{Endpoints: []Endpoint{
		{ID: "a", Weight: 1},
		{ID: "disabled", Weight: 1, Disabled: true},
		{ID: "b", Weight: 1},
	}})
	if err != nil {
		t.Fatal(err)
	}
	got := []string{}
	for range 4 {
		ep, err := c.Select(RoundRobin, nil)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, ep.ID)
	}
	want := []string{"a", "b", "a", "b"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("selection %v, want %v", got, want)
		}
	}
}

func TestWeightedSelectionUsesConfiguredWeights(t *testing.T) {
	c, err := New(Config{Endpoints: []Endpoint{
		{ID: "light", Weight: 1},
		{ID: "heavy", Weight: 3},
	}})
	if err != nil {
		t.Fatal(err)
	}
	source := &sequenceSource{values: []uint64{0, 1, 2, 3}}
	got := make([]string, 4)
	for i := range got {
		ep, err := c.Select(Weighted, source)
		if err != nil {
			t.Fatal(err)
		}
		got[i] = ep.ID
	}
	want := []string{"light", "heavy", "heavy", "heavy"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("selection %v, want %v", got, want)
		}
	}
}

func TestUnknownEndpointsRemainEligibleUntilUnhealthyThreshold(t *testing.T) {
	c, err := New(Config{
		Endpoints: []Endpoint{{ID: "api", Weight: 1}},
		Health:    HealthThresholds{Healthy: 2, Unhealthy: 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	state, err := c.Health("api")
	if err != nil || state != Unknown {
		t.Fatalf("initial health = %q, %v; want Unknown", state, err)
	}
	if ep, err := c.Select(RoundRobin, nil); err != nil || ep.ID != "api" {
		t.Fatalf("initial selection = %q, %v; want api", ep.ID, err)
	}
	if err := c.MarkResult("api", false); err != nil {
		t.Fatal(err)
	}
	if ep, err := c.Select(RoundRobin, nil); err != nil || ep.ID != "api" {
		t.Fatalf("selection before unhealthy threshold = %q, %v; want api", ep.ID, err)
	}
	if err := c.MarkResult("api", false); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Select(RoundRobin, nil); !errors.Is(err, ErrNoHealthyEndpoints) {
		t.Fatalf("selection after unhealthy threshold error = %v, want ErrNoHealthyEndpoints", err)
	}
}

func TestSelectionExcludesDisabledAndUnhealthyEndpoints(t *testing.T) {
	c, err := New(Config{Endpoints: []Endpoint{
		{ID: "down", Weight: 100},
		{ID: "disabled", Weight: 100, Disabled: true},
		{ID: "up", Weight: 1},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.MarkResult("down", false); err != nil {
		t.Fatal(err)
	}
	for _, policy := range []Policy{RoundRobin, Weighted} {
		for i := 0; i < 20; i++ {
			ep, err := c.Select(policy, &sequenceSource{values: []uint64{999}})
			if err != nil {
				t.Fatal(err)
			}
			if ep.ID != "up" {
				t.Fatalf("%s selected %q, want only non-unhealthy enabled endpoint up", policy, ep.ID)
			}
		}
	}
}

func TestHealthTransitionsRequireConfiguredThresholds(t *testing.T) {
	c, err := New(Config{Endpoints: []Endpoint{{ID: "api", Weight: 1}},
		Health: HealthThresholds{Unhealthy: 2, Healthy: 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	checkHealth := func(want HealthState) {
		t.Helper()
		got, err := c.Health("api")
		if err != nil || got != want {
			t.Fatalf("health = %q, %v; want %q", got, err, want)
		}
	}
	checkHealth(Unknown)
	_ = c.MarkResult("api", false)
	checkHealth(Unknown)
	_ = c.MarkResult("api", false)
	checkHealth(Unhealthy)
	_ = c.MarkResult("api", true)
	checkHealth(Unhealthy)
	_ = c.MarkResult("api", true)
	checkHealth(Healthy)
}
