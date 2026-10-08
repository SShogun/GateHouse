package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/gatehouse/gatehouse/internal/lifecycle"
)

const shutdownTimeout = 10 * time.Second

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "gatehouse-data:", err)
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
			// The data plane owns no listener or upstream pool until later milestones.
			return nil
		},
	)
}
