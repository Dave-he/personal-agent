// Package scheduler drives the five-step loop: plan, act, observe, verify, commit.
//
// It owns pacing, quotas and circuit breaking. It does not decide permissions and
// it does not decide correctness: those belong to the gateway and the verifier.
package scheduler

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"personal-agent/internal/domain"
	"personal-agent/internal/executor"
	"personal-agent/internal/gateway"
	"personal-agent/internal/observability"
	"personal-agent/internal/planner"
	"personal-agent/internal/store"
	"personal-agent/internal/verifier"
)

// Config holds the pacing and safety limits.
type Config struct {
	BaseInterval  time.Duration // first poll interval
	MaxInterval   time.Duration // ceiling for adaptive backoff
	FailStreak    int           // consecutive failures before the task is stopped
	RunTimeout    time.Duration
	TaskCostLimit float64 // CNY per task; crossing it stops the task
	ReadCost      float64 // accounted cost of one read action
}

// DefaultConfig returns conservative MVP values.
func DefaultConfig() Config {
	return Config{
		BaseInterval:  30 * time.Minute,
		MaxInterval:   6 * time.Hour,
		FailStreak:    3,
		RunTimeout:    30 * time.Second,
		TaskCostLimit: 5,
		ReadCost:      0.0025,
	}
}

// Notifier is how the agent tells the user something happened.
type Notifier func(userID, title, body string)

// Scheduler wires every layer together.
type Scheduler struct {
	st  *store.Store
	pl  planner.Planner
	ex  executor.Executor
	gw  *gateway.Sentinel
	ve  *verifier.Engine
	met *observability.Metrics
	cfg Config

	mu      sync.Mutex
	running map[string]bool

	// Paused is the global kill switch. When set, no run starts.
	Paused atomic.Bool
	Notify Notifier
}

// New builds a scheduler.
func New(st *store.Store, pl planner.Planner, ex executor.Executor, gw *gateway.Sentinel,
	ve *verifier.Engine, met *observability.Metrics, cfg Config) *Scheduler {
	if cfg.BaseInterval == 0 {
		cfg = DefaultConfig()
	}
	return &Scheduler{st: st, pl: pl, ex: ex, gw: gw, ve: ve, met: met, cfg: cfg, running: map[string]bool{}}
}

// Tick starts a run for every due task. Runs execute concurrently but a single
// task is never executed twice at the same time.
func (s *Scheduler) Tick(ctx context.Context) {
	if s.Paused.Load() {
		return
	}
	for _, t := range s.st.DueTasks(time.Now().UTC()) {
		t := t
		s.mu.Lock()
		if s.running[t.ID] {
			s.mu.Unlock()
			continue
		}
		s.running[t.ID] = true
		s.mu.Unlock()

		go func() {
			defer func() {
				s.mu.Lock()
				delete(s.running, t.ID)
				s.mu.Unlock()
			}()
			if _, err := s.RunOnce(ctx, t.ID); err != nil {
				s.st.AppendAudit("scheduler", "run_error", t.UserID, t.ID, "", err.Error())
			}
		}()
	}
}

// RunTaskNow executes a task immediately, ignoring the schedule but not the limits.
func (s *Scheduler) RunTaskNow(ctx context.Context, taskID string) (*domain.Run, error) {
	return s.RunOnce(ctx, taskID)
}

// RunOnce executes the loop exactly once.
func (s *Scheduler) RunOnce(ctx context.Context, taskID string) (*domain.Run, error) {
	task, err := s.st.Task(taskID)
	if err != nil {
		return nil, err
	}
	if task.Status == domain.StatusTerminated || task.Status == domain.StatusDone {
		return nil, fmt.Errorf("scheduler: task %s is closed", taskID)
	}
	if task.CostCNY >= s.cfg.TaskCostLimit {
		s.stopTask(task, "cost limit reached")
		return nil, fmt.Errorf("scheduler: cost limit reached for task %s", taskID)
	}
	goal, err := s.st.Goal(task.GoalID)
	if err != nil {
		return nil, err
	}
	user, err := s.st.User(task.UserID)
	if err != nil {
		return nil, err
	}
	cons := s.st.ActiveConstraints(task.UserID)

	run := &domain.Run{
		ID:        domain.NewID("run"),
		TaskID:    task.ID,
		Token:     domain.NewID("rtk"),
		StartedAt: time.Now().UTC(),
		Steps:     []domain.Step{},
	}
	s.st.AddRun(run)
	s.markRunning(task)

	plan, err := s.pl.Plan(goal, goal.Channel)
	if err != nil {
		s.finishRun(run, domain.VerdictFailed, "plan failed: "+err.Error())
		s.failTask(task, "plan failed")
		return run, nil
	}
	run.Steps = append(run.Steps, domain.Step{Name: "plan", At: time.Now().UTC(), Detail: plan.Steps[0]})

	var evidence *domain.Evidence
	for _, a := range plan.Actions {
		intent := &domain.Intent{
			ID:     domain.NewID("itn"),
			TaskID: task.ID,
			RunID:  run.ID,
			UserID: task.UserID,
			Action: a.Name,
			Params: a.Params,
		}
		dec, err := s.gw.Authorize(intent, user.TrustLevel, a.AmountCNY)
		if err != nil {
			s.met.Inc("intents_denied")
			s.finishRun(run, domain.VerdictFailed, "gateway refused: "+err.Error())
			s.failTask(task, "gateway refused")
			return run, nil
		}
		run.Steps = append(run.Steps, domain.Step{Name: "act", At: time.Now().UTC(), Detail: intent.Action + " " + dec.Reason})
		if dec.NeedsHuman {
			// Nothing is signed, so nothing can execute. The run stays open until
			// the user decides; the task is put to sleep meanwhile.
			s.met.Inc("approvals_pending")
			run.Verdict = domain.VerdictUncertain
			run.Reason = "awaiting approval: " + dec.ApprovalID
			s.st.UpdateRun(run)
			task.Status = domain.StatusSleeping
			task.NextRun = time.Now().UTC().Add(s.cfg.BaseInterval)
			task.UpdatedAt = time.Now().UTC()
			s.st.UpdateTask(task)
			s.st.AppendAudit("scheduler", "run_awaiting_approval", task.UserID, task.ID, intent.ID, dec.ApprovalID)
			return run, nil
		}

		ctxRun, cancel := context.WithTimeout(ctx, s.cfg.RunTimeout)
		ev, err := s.ex.Execute(ctxRun, intent)
		cancel()
		if err != nil {
			s.finishRun(run, domain.VerdictFailed, "executor: "+err.Error())
			s.failTask(task, "executor failed")
			return run, nil
		}
		evidence = ev
		s.st.AddEvidence(ev)
		run.CostCNY += s.cfg.ReadCost
		run.Steps = append(run.Steps, domain.Step{Name: "observe", At: time.Now().UTC(), Detail: fmt.Sprintf("%d fields from %s", len(ev.Fields), ev.Source)})
	}

	if evidence == nil {
		s.finishRun(run, domain.VerdictUncertain, "no evidence captured")
		s.settleTask(task, domain.VerdictUncertain)
		return run, nil
	}

	prev := s.previousEvidence(task.ID, run.ID)
	ver := s.ve.Verify(goal, evidence, cons, prev, 0)
	s.st.AddVerification(ver)
	run.Steps = append(run.Steps, domain.Step{Name: "verify", At: time.Now().UTC(), Detail: string(ver.Verdict) + ": " + join(ver.Reasons)})
	s.finishRun(run, ver.Verdict, join(ver.Reasons))

	triggered := ver.Verdict == domain.VerdictPassed && hasPrefix(ver.Reasons, "threshold_met")
	if triggered {
		s.notify(task.UserID, "目标达成", fmt.Sprintf("%s: %s", goal.Object, join(ver.Reasons)))
	}
	s.settleTask(task, ver.Verdict)
	return run, nil
}

// Approve finishes a run that was waiting for a human. The intent is signed only
// after the approval content hash still matches, then executed and verified.
func (s *Scheduler) Approve(ctx context.Context, approvalID, userID string) (*domain.Run, error) {
	intent, err := s.gw.Grant(approvalID, userID)
	if err != nil {
		return nil, err
	}
	run, err := s.st.Run(intent.RunID)
	if err != nil {
		return nil, err
	}
	task, err := s.st.Task(intent.TaskID)
	if err != nil {
		return nil, err
	}
	goal, err := s.st.Goal(task.GoalID)
	if err != nil {
		return nil, err
	}
	user, err := s.st.User(task.UserID)
	if err != nil {
		return nil, err
	}
	if user.ID != userID {
		return nil, fmt.Errorf("scheduler: user mismatch")
	}
	ctxRun, cancel := context.WithTimeout(ctx, s.cfg.RunTimeout)
	defer cancel()
	ev, err := s.ex.Execute(ctxRun, intent)
	if err != nil {
		s.finishRun(run, domain.VerdictFailed, "executor after approval: "+err.Error())
		s.failTask(task, "executor failed after approval")
		return run, nil
	}
	s.st.AddEvidence(ev)
	ver := s.ve.Verify(goal, ev, s.st.ActiveConstraints(task.UserID), s.previousEvidence(task.ID, run.ID), 0)
	s.st.AddVerification(ver)
	run.Steps = append(run.Steps, domain.Step{Name: "observe", At: time.Now().UTC(), Detail: "approved action executed"})
	run.Steps = append(run.Steps, domain.Step{Name: "verify", At: time.Now().UTC(), Detail: string(ver.Verdict)})
	s.finishRun(run, ver.Verdict, join(ver.Reasons))
	s.settleTask(task, ver.Verdict)
	return run, nil
}

// ---- helpers ----

func (s *Scheduler) finishRun(run *domain.Run, v domain.Verdict, reason string) {
	now := time.Now().UTC()
	run.Verdict = v
	run.Reason = reason
	run.EndedAt = &now
	s.st.UpdateRun(run)
	s.met.Inc("runs_total")
	switch v {
	case domain.VerdictPassed:
		s.met.Inc("runs_passed")
	case domain.VerdictFailed:
		s.met.Inc("runs_failed")
	default:
		s.met.Inc("runs_uncertain")
	}
}

// settleTask updates state, cost and the next interval after a completed run.
func (s *Scheduler) settleTask(task *domain.Task, v domain.Verdict) {
	task.Runs++
	task.CostCNY += s.cfg.ReadCost
	task.FailStreak = 0
	if v == domain.VerdictFailed {
		task.FailStreak++
	}
	task.UpdatedAt = time.Now().UTC()

	if task.FailStreak >= s.cfg.FailStreak {
		s.stopTask(task, "circuit breaker: consecutive failures")
		return
	}
	if task.Schedule == "once" || task.Status == domain.StatusDone {
		task.Status = domain.StatusDone
		s.st.UpdateTask(task)
		return
	}
	next := s.nextInterval(task, v)
	task.NextRun = time.Now().UTC().Add(next)
	task.Status = domain.StatusSleeping
	s.st.UpdateTask(task)
	s.st.AppendAudit("scheduler", "run_committed", task.UserID, task.ID, "", string(v))
}

func (s *Scheduler) nextInterval(task *domain.Task, v domain.Verdict) time.Duration {
	base := time.Duration(task.IntervalS) * time.Second
	if base <= 0 {
		base = s.cfg.BaseInterval
	}
	if v == domain.VerdictUncertain || v == domain.VerdictFailed {
		// Back off on trouble instead of hammering the target.
		d := time.Duration(task.FailStreak+1) * base
		if d > s.cfg.MaxInterval {
			return s.cfg.MaxInterval
		}
		return d
	}
	// Quiet runs stretch the interval, up to the ceiling.
	d := time.Duration(task.Runs) * base / 2
	if d < base {
		d = base
	}
	if d > s.cfg.MaxInterval {
		return s.cfg.MaxInterval
	}
	return d
}

func (s *Scheduler) failTask(task *domain.Task, reason string) {
	task.FailStreak++
	task.UpdatedAt = time.Now().UTC()
	if task.FailStreak >= s.cfg.FailStreak {
		s.stopTask(task, reason)
		return
	}
	task.NextRun = time.Now().UTC().Add(s.cfg.BaseInterval)
	task.Status = domain.StatusSleeping
	s.st.UpdateTask(task)
}

func (s *Scheduler) stopTask(task *domain.Task, reason string) {
	task.Status = domain.StatusFailed
	task.UpdatedAt = time.Now().UTC()
	s.st.UpdateTask(task)
	s.met.Inc("tasks_stopped")
	s.st.AddIncident(&domain.Incident{
		ID:     domain.NewID("inc"),
		Level:  "task",
		UserID: task.UserID,
		TaskID: task.ID,
		Detail: reason,
		Open:   true,
		At:     time.Now().UTC(),
	})
	s.st.AppendAudit("scheduler", "task_stopped", task.UserID, task.ID, "", reason)
}

func (s *Scheduler) notify(userID, title, body string) {
	s.met.Inc("notifications")
	if s.Notify != nil {
		s.Notify(userID, title, body)
	}
}

// previousEvidence returns the latest evidence from an earlier run of the task.
func (s *Scheduler) previousEvidence(taskID, currentRun string) *domain.Evidence {
	runs := s.st.RunsByTask(taskID)
	sort.Slice(runs, func(i, j int) bool { return runs[i].StartedAt.Before(runs[j].StartedAt) })
	for i := len(runs) - 1; i >= 0; i-- {
		if runs[i].ID == currentRun {
			continue
		}
		evs := s.st.EvidenceByRun(runs[i].ID)
		if len(evs) > 0 {
			return &evs[0]
		}
	}
	return nil
}

func (s *Scheduler) markRunning(t *domain.Task) {
	t.Status = domain.StatusRunning
	t.UpdatedAt = time.Now().UTC()
	s.st.UpdateTask(t)
}

func join(in []string) string {
	out := ""
	for i, s := range in {
		if i > 0 {
			out += "; "
		}
		out += s
	}
	return out
}

func hasPrefix(list []string, p string) bool {
	for _, s := range list {
		if len(s) >= len(p) && s[:len(p)] == p {
			return true
		}
	}
	return false
}
