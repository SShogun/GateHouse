package main

import (
	"strings"
	"testing"
)

func TestRunRejectsExternallyReachableDevelopmentListenerBeforeStarting(t *testing.T) {
	t.Setenv("GATEHOUSE_LISTEN_ADDR", "0.0.0.0:8080")
	t.Setenv("GATEHOUSE_UPSTREAM_URL", "not a URL")
	err := run()
	if err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("run() error = %v, want loopback listener rejection before upstream setup", err)
	}
}
