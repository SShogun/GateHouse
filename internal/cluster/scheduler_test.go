package cluster

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestSchedulerRemoveCancelsAndJoinsProbe(t *testing.T) {
	started := make(chan struct{})
	finished := make(chan struct{})
	scheduler := NewScheduler()
	if err := scheduler.Add("api", time.Hour, func(ctx context.Context) error {
		close(started)
		<-ctx.Done()
		close(finished)
		return ctx.Err()
	}); err != nil {
		t.Fatal(err)
	}
	<-started
	if err := scheduler.Remove("api"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-finished:
	default:
		t.Fatal("Remove returned before the probe stopped")
	}
	if got := scheduler.Count(); got != 0 {
		t.Fatalf("scheduler count = %d, want 0", got)
	}
	if err := scheduler.Remove("api"); !errors.Is(err, ErrUnknownSchedule) {
		t.Fatalf("second Remove error = %v, want ErrUnknownSchedule", err)
	}
}

func TestSchedulerStopCancelsAllProbes(t *testing.T) {
	started := make(chan struct{}, 2)
	finished := make(chan struct{}, 2)
	scheduler := NewScheduler()
	for _, id := range []string{"one", "two"} {
		if err := scheduler.Add(id, time.Hour, func(ctx context.Context) error {
			started <- struct{}{}
			<-ctx.Done()
			finished <- struct{}{}
			return ctx.Err()
		}); err != nil {
			t.Fatal(err)
		}
	}
	<-started
	<-started
	scheduler.Stop()
	if got := scheduler.Count(); got != 0 {
		t.Fatalf("scheduler count = %d, want 0", got)
	}
	if len(finished) != 2 {
		t.Fatalf("finished probes = %d, want 2", len(finished))
	}
}
