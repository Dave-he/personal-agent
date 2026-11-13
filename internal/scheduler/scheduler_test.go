package scheduler

import (
	"context"
	"testing"
	"time"

	"personal-agent/internal/audit"
	"personal-agent/internal/domain"
	"personal-agent/internal/executor"
	"personal-agent/internal/gateway"
	"personal-agent/internal/observability"
	"personal-agent/internal/planner"
	"personal-agent/internal/store"
	"personal-agent/internal/verifier"
)

func setup(t *testing.T) (*Scheduler, *store.Store) {
	t.Helper()
	st, err := store.Open("")
	if err != nil {
		t.Fatal(err)
	}
	st.UpsertUser(&domain.User{ID: "u1", Name: "u1", TrustLevel: "tier0", CreatedAt: time.Now().UTC()})
	gw := gateway.New(st, []byte("k"), time.Minute)
	met := observability.New()
	cfg := Config{BaseInterval: time.Minute, MaxInterval: time.Hour, FailStreak: 3,
		RunTimeout: 5 * time.Second, TaskCostLimit: 1, ReadCost: 0.0025}
	ex := executor.SimExecutor{Base: executor.Base{Guard: gw}, BasePrice: 1200}
	s := New(st, planner.RulePlanner{}, ex, gw, verifier.New(), met, cfg)
	return s, st
}

func seed(t *testing.T, st *store.Store) *domain.Task {
	t.Helper()
	g := &domain.Goal{ID: "g1", UserID: "u1", Kind: "price_watch", Object: "headphone",
		Metric: "price", Op: "lte", Value: 2000, CreatedAt: time.Now().UTC()}
	st.AddGoal(g)
	task := &domain.Task{ID: "t1", UserID: "u1", GoalID: "g1", Schedule: "interval",
		IntervalS: 60, NextRun: time.Now().UTC(), Status: domain.StatusWaiting, CreatedAt: time.Now().UTC()}
	st.AddTask(task)
	return task
}

func TestRunOnceHappyPath(t *testing.T) {
	s, st := setup(t)
	seed(t, st)
	run, err := s.RunOnce(context.Background(), "t1")
	if err != nil {
		t.Fatal(err)
	}
	if run.Verdict != domain.VerdictPassed {
		t.Fatalf("expected passed, got %s (%s)", run.Verdict, run.Reason)
	}
	if run.EndedAt == nil {
		t.Fatal("finished run must have an end time")
	}
	if len(st.EvidenceByRun(run.ID)) != 1 {
		t.Fatal("exactly one evidence record expected")
	}
	if err := audit.Verify(st.AuditAll()); err != nil {
		t.Fatalf("audit chain broken: %v", err)
	}
}

func TestCircuitBreakerStopsTask(t *testing.T) {
	s, st := setup(t)
	seed(t, st)
	// A goal whose acceptance can never be observed makes the run uncertain, which
	// is not a failure; to exercise the breaker we drive failures directly.
	task, _ := st.Task("t1")
	for i := 0; i < 3; i++ {
		s.failTask(task, "simulated failure")
		task, _ = st.Task("t1")
	}
	final, _ := st.Task("t1")
	if final.Status != domain.StatusFailed {
		t.Fatalf("task should be stopped by the breaker, got %s", final.Status)
	}
	if len(st.Incidents()) == 0 {
		t.Fatal("an incident must be opened when the breaker trips")
	}
}

func TestCostCapStopsTask(t *testing.T) {
	s, st := setup(t)
	task := seed(t, st)
	task.CostCNY = 2
	st.UpdateTask(task)
	if _, err := s.RunOnce(context.Background(), "t1"); err == nil {
		t.Fatal("expected an error when the cost cap is reached")
	}
	final, _ := st.Task("t1")
	if final.Status != domain.StatusFailed {
		t.Fatalf("cost-capped task should be stopped, got %s", final.Status)
	}
}

func TestDedupeFingerprint(t *testing.T) {
	a, err := planner.Extractor{}.Parse("盯着这款耳机，降到 800 以内就提醒我", "u1")
	if err != nil {
		t.Fatal(err)
	}
	b, err := planner.Extractor{}.Parse("帮我盯着这款耳机 降到 800 以内就提醒我", "u1")
	if err != nil {
		t.Fatal(err)
	}
	if a.Fingerprint != b.Fingerprint {
		t.Fatalf("equivalent goals should share a fingerprint: %s != %s", a.Fingerprint, b.Fingerprint)
	}
}
