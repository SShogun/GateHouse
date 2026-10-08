package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/SShogun/GateHouse/internal/lifecycle"
)

const shutdownTimeout = 10 * time.Second

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "gatehouse-control:", err)
		os.Exit(1)
	}
}

func run() error {
	return lifecycle.RunWithSignals(shutdownTimeout,
		func(ctx context.Context) error {
			<-ctx.Done()
			return nil
		},
		func(context.Context) error {
			// The control plane owns no listeners or pools until later milestones.
			return nil
		},
	)
}
