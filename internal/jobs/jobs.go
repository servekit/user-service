// Package jobs provides a cron-driven task scheduler that integrates with
// lifecycle.Manager. The scheduler itself is task-agnostic — business code
// registers jobs via AddFunc.
package jobs

import (
	"errors"
	"fmt"
	"sync"

	"github.com/servekit/go-common/cronx"
	"github.com/servekit/go-common/lifecycle"
	"github.com/robfig/cron/v3"
)

// Scheduler wraps *cron.Cron and implements lifecycle.Service.
//
// When constructed with Deps.Config, the Scheduler owns the underlying cron and
// manages its lifecycle (Start/Stop). When constructed with Deps.Cron, the
// caller retains ownership and Start/Stop are no-ops — robfig/cron's Stop is
// not idempotent (each call spawns a goroutine waiting on jobWaiter), so a
// borrowed cron must be lifecycle-managed by exactly one owner.
type Scheduler struct {
	cron     *cron.Cron
	ownsCron bool
	mu       sync.Mutex
	started  bool
}

// Deps configures a Scheduler. Either Config (scheduler will create cron) or
// Cron (caller-managed) must be non-nil.
type Deps struct {
	Config *cronx.Config // used when Cron is nil
	Cron   *cron.Cron    // optional: caller-managed cron
}

// Compile-time assertion that *Scheduler satisfies lifecycle.Service.
var _ lifecycle.Service = (*Scheduler)(nil)

// New constructs a Scheduler. When Deps.Cron is nil, the scheduler creates its
// own cron from Config and manages its lifecycle.
func New(d *Deps) (*Scheduler, error) {
	if d == nil {
		return nil, errors.New("jobs: nil deps")
	}
	if d.Cron != nil {
		return &Scheduler{cron: d.Cron, ownsCron: false}, nil
	}
	if d.Config == nil {
		return nil, errors.New("jobs: nil config when cron not injected")
	}
	c, err := cronx.New(d.Config)
	if err != nil {
		return nil, fmt.Errorf("jobs: init cron: %w", err)
	}
	return &Scheduler{cron: c, ownsCron: true}, nil
}

// AddFunc registers a cron job. Must be called before Start.
func (s *Scheduler) AddFunc(spec string, cmd func()) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started {
		return errors.New("jobs: cannot AddFunc after Start")
	}
	if _, err := s.cron.AddFunc(spec, cmd); err != nil {
		return fmt.Errorf("jobs: add func %q: %w", spec, err)
	}
	return nil
}

// Start starts the underlying cron if owned; otherwise no-op.
func (s *Scheduler) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.ownsCron || s.started {
		return nil
	}
	s.cron.Start()
	s.started = true
	return nil
}

// Stop stops the underlying cron if owned; otherwise no-op.
func (s *Scheduler) Stop() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.ownsCron || !s.started {
		return nil
	}
	s.cron.Stop()
	s.started = false
	return nil
}
