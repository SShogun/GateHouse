package cluster

import (
	"context"
	"errors"
	"sync/atomic"
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

func TestSchedulerCancelsProbeWhenRemovedDuringJitterDelay(t *testing.T) {
	started := make(chan struct{})
	finished := make(chan struct{})
	scheduler := NewScheduler()
	if err := scheduler.Add("api", time.Hour, func(ctx context.Context) error {
		select {
		case <-started:
		default:
			close(started)
		}
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
		t.Fatal("Remove returned before cancellation completed")
	}
	if scheduler.Count() != 0 {
		t.Fatalf("schedule leaked after Remove: count=%d", scheduler.Count())
	}
}

func TestSchedulerProbeReceivesConfiguredTimeout(t *testing.T) {
	deadlineSeen := make(chan time.Time, 1)
	canceled := make(chan struct{}, 1)
	scheduler := NewSchedulerWithOptions(SchedulerOptions{
		ProbeTimeout:  20 * time.Millisecond,
		MaxConcurrent: 1,
		Jitter:        func(time.Duration) time.Duration { return 0 },
	})
	if err := scheduler.Add("api", time.Hour, func(ctx context.Context) error {
		deadline, ok := ctx.Deadline()
		if !ok {
			return errors.New("probe context has no timeout")
		}
		deadlineSeen <- deadline
		<-ctx.Done()
		canceled <- struct{}{}
		return ctx.Err()
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-deadlineSeen:
	case <-time.After(time.Second):
		t.Fatal("probe did not start")
	}
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("probe context was not canceled at timeout")
	}
	if err := scheduler.Remove("api"); err != nil {
		t.Fatal(err)
	}
}

func TestSchedulerBoundsConcurrentProbes(t *testing.T) {
	const limit = 2
	var active atomic.Int32
	var maximum atomic.Int32
	started := make(chan struct{}, 4)
	release := make(chan struct{}, 4)
	scheduler := NewSchedulerWithOptions(SchedulerOptions{
		ProbeTimeout:  time.Minute,
		MaxConcurrent: limit,
		Jitter:        func(time.Duration) time.Duration { return 0 },
	})
	for _, id := range []string{"one", "two", "three", "four"} {
		if err := scheduler.Add(id, time.Hour, func(ctx context.Context) error {
			current := active.Add(1)
			for seen := maximum.Load(); current > seen && !maximum.CompareAndSwap(seen, current); seen = maximum.Load() {
			}
			started <- struct{}{}
			defer active.Add(-1)
			select {
			case <-release:
			case <-ctx.Done():
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < limit; i++ {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("expected two probes to start")
		}
	}
	select {
	case <-started:
		t.Fatal("scheduler exceeded configured probe concurrency")
	default:
	}
	for i := 0; i < limit; i++ {
		release <- struct{}{}
	}
	for i := 0; i < limit; i++ {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("queued probe did not start after capacity was released")
		}
	}
	scheduler.Stop()
	if got := maximum.Load(); got > limit {
		t.Fatalf("maximum concurrent probes = %d, want <= %d", got, limit)
	}
	if got := active.Load(); got != 0 {
		t.Fatalf("active probes after Stop = %d, want 0", got)
	}
}
