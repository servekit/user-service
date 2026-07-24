package jobs_test

import (
	"testing"

	"github.com/servekit/go-common/cronx"

	"github.com/servekit/user-service/internal/jobs"
)

func TestScheduler_StartStop_Idempotent(t *testing.T) {
	s, err := jobs.New(&jobs.Deps{
		Config: &cronx.Config{Timezone: "UTC", WithSeconds: true},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := s.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := s.Start(); err != nil {
		t.Fatalf("Start (2nd): %v", err)
	}
	if err := s.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if err := s.Stop(); err != nil {
		t.Fatalf("Stop (2nd): %v", err)
	}
}

func TestScheduler_AddFunc_AfterStart_Fails(t *testing.T) {
	s, _ := jobs.New(&jobs.Deps{Config: &cronx.Config{Timezone: "UTC"}})
	_ = s.Start()
	defer s.Stop()

	if err := s.AddFunc("* * * * * *", func() {}); err == nil {
		t.Error("AddFunc after Start should fail")
	}
}

func TestScheduler_InjectedCron_NoOpStartStop(t *testing.T) {
	c, err := cronx.New(&cronx.Config{Timezone: "UTC"})
	if err != nil {
		t.Fatalf("cronx.New: %v", err)
	}
	s, err := jobs.New(&jobs.Deps{Cron: c})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// Injected cron — Start/Stop must be no-ops so the caller stays the
	// single lifecycle owner.
	if err := s.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := s.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

func TestNew_NilDeps_Errors(t *testing.T) {
	if _, err := jobs.New(nil); err == nil {
		t.Error("New(nil) should error")
	}
}

func TestNew_NilConfigAndCron_Errors(t *testing.T) {
	if _, err := jobs.New(&jobs.Deps{}); err == nil {
		t.Error("New with both Config and Cron nil should error")
	}
}
