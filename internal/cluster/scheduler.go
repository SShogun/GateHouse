package cluster

import (
	"context"
	"errors"
	"math/rand"
	"sync"
	"time"
)

var (
	ErrInvalidSchedule   = errors.New("schedule requires an ID, positive interval, and probe")
	ErrDuplicateSchedule = errors.New("schedule already exists")
	ErrUnknownSchedule   = errors.New("unknown schedule")
	ErrSchedulerStopped  = errors.New("scheduler is stopped")
)

// Probe performs one health check and must return promptly when ctx is canceled.
type Probe func(ctx context.Context) error

type scheduledProbe struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// SchedulerOptions bounds active checks and controls probe timing. A nil Jitter
// selects a random delay within 10% of the configured interval on either side.
type SchedulerOptions struct {
	ProbeTimeout  time.Duration
	MaxConcurrent int
	Jitter        func(time.Duration) time.Duration
}

// Scheduler owns at most one periodic worker per registered ID.
type Scheduler struct {
	mu        sync.Mutex
	entries   map[string]*scheduledProbe
	stopped   bool
	stopDone  chan struct{}
	options   SchedulerOptions
	semaphore chan struct{}
}

func NewScheduler() *Scheduler {
	return NewSchedulerWithOptions(SchedulerOptions{})
}

// NewSchedulerWithOptions creates a scheduler with bounded, cancelable probes.
func NewSchedulerWithOptions(options SchedulerOptions) *Scheduler {
	if options.ProbeTimeout <= 0 {
		options.ProbeTimeout = 5 * time.Second
	}
	if options.MaxConcurrent <= 0 {
		options.MaxConcurrent = 16
	}
	if options.Jitter == nil {
		options.Jitter = func(interval time.Duration) time.Duration {
			span := int64(interval / 10)
			if span == 0 {
				return 0
			}
			return time.Duration(rand.Int63n(2*span+1) - span)
		}
	}
	return &Scheduler{entries: make(map[string]*scheduledProbe), stopDone: make(chan struct{}), options: options, semaphore: make(chan struct{}, options.MaxConcurrent)}
}

// Add starts an immediate probe followed by periodic probes. Duplicate IDs fail.
func (s *Scheduler) Add(id string, interval time.Duration, probe Probe) error {
	if id == "" || interval <= 0 || probe == nil {
		return ErrInvalidSchedule
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return ErrSchedulerStopped
	}
	if _, exists := s.entries[id]; exists {
		return ErrDuplicateSchedule
	}
	ctx, cancel := context.WithCancel(context.Background())
	entry := &scheduledProbe{cancel: cancel, done: make(chan struct{})}
	s.entries[id] = entry
	go func() {
		defer close(entry.done)
		for {
			select {
			case s.semaphore <- struct{}{}:
			case <-ctx.Done():
				return
			}
			probeCtx, probeCancel := context.WithTimeout(ctx, s.options.ProbeTimeout)
			_ = probe(probeCtx)
			probeCancel()
			<-s.semaphore
			delay := interval + s.options.Jitter(interval)
			if delay <= 0 {
				delay = interval
			}
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			if ctx.Err() != nil {
				return
			}
		}
	}()
	return nil
}

// Remove cancels a registered worker and waits for it to finish.
func (s *Scheduler) Remove(id string) error {
	s.mu.Lock()
	entry, ok := s.entries[id]
	s.mu.Unlock()
	if !ok {
		return ErrUnknownSchedule
	}
	entry.cancel()
	<-entry.done
	s.mu.Lock()
	if s.entries[id] == entry {
		delete(s.entries, id)
	}
	s.mu.Unlock()
	return nil
}

// Count reports registered schedules, including workers that are stopping.
func (s *Scheduler) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.entries)
}

// Stop cancels and joins every worker. It is safe to call more than once.
func (s *Scheduler) Stop() {
	s.mu.Lock()
	if s.stopped {
		done := s.stopDone
		s.mu.Unlock()
		<-done
		return
	}
	s.stopped = true
	entries := make([]*scheduledProbe, 0, len(s.entries))
	for _, entry := range s.entries {
		entries = append(entries, entry)
	}
	s.mu.Unlock()
	for _, entry := range entries {
		entry.cancel()
	}
	for _, entry := range entries {
		<-entry.done
	}
	s.mu.Lock()
	clear(s.entries)
	close(s.stopDone)
	s.mu.Unlock()
}
