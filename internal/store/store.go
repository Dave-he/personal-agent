// Package store keeps the whole agent state in memory and snapshots it to disk.
//
// The store is intentionally dumb: it holds records and guarantees ordering,
// but it never decides anything. Every decision lives in a dedicated layer so
// that behaviour stays testable without a database.
package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"personal-agent/internal/domain"
)

// ErrNotFound is returned whenever a lookup misses.
var ErrNotFound = errors.New("store: not found")

type state struct {
	Users         map[string]*domain.User         `json:"users"`
	Constraints   map[string]*domain.Constraint   `json:"constraints"`
	Goals         map[string]*domain.Goal         `json:"goals"`
	Tasks         map[string]*domain.Task         `json:"tasks"`
	Runs          map[string]*domain.Run          `json:"runs"`
	Intents       map[string]*domain.Intent       `json:"intents"`
	Approvals     map[string]*domain.Approval     `json:"approvals"`
	Evidence      map[string]*domain.Evidence     `json:"evidence"`
	Verifications map[string]*domain.Verification `json:"verifications"`
	Audit         []*domain.AuditEvent            `json:"audit"`
	Incidents     map[string]*domain.Incident     `json:"incidents"`
}

// Store is a mutex-guarded collection of records with append-only audit chaining.
type Store struct {
	mu    sync.RWMutex
	s     state
	path  string
	dirty bool
}

// Open loads an existing snapshot when path is readable, otherwise starts empty.
// path may be empty to keep everything in memory (useful in tests).
func Open(path string) (*Store, error) {
	st := &Store{path: path, s: newState()}
	if path == "" {
		return st, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return st, nil
		}
		return nil, err
	}
	if len(b) == 0 {
		return st, nil
	}
	if err := json.Unmarshal(b, &st.s); err != nil {
		return nil, fmt.Errorf("store: corrupt snapshot: %w", err)
	}
	st.fill()
	return st, nil
}

func newState() state {
	return state{
		Users:         map[string]*domain.User{},
		Constraints:   map[string]*domain.Constraint{},
		Goals:         map[string]*domain.Goal{},
		Tasks:         map[string]*domain.Task{},
		Runs:          map[string]*domain.Run{},
		Intents:       map[string]*domain.Intent{},
		Approvals:     map[string]*domain.Approval{},
		Evidence:      map[string]*domain.Evidence{},
		Verifications: map[string]*domain.Verification{},
		Incidents:     map[string]*domain.Incident{},
	}
}

// fill guards against nil maps after an older or hand-edited snapshot.
func (st *Store) fill() {
	if st.s.Users == nil {
		st.s.Users = map[string]*domain.User{}
	}
	if st.s.Constraints == nil {
		st.s.Constraints = map[string]*domain.Constraint{}
	}
	if st.s.Goals == nil {
		st.s.Goals = map[string]*domain.Goal{}
	}
	if st.s.Tasks == nil {
		st.s.Tasks = map[string]*domain.Task{}
	}
	if st.s.Runs == nil {
		st.s.Runs = map[string]*domain.Run{}
	}
	if st.s.Intents == nil {
		st.s.Intents = map[string]*domain.Intent{}
	}
	if st.s.Approvals == nil {
		st.s.Approvals = map[string]*domain.Approval{}
	}
	if st.s.Evidence == nil {
		st.s.Evidence = map[string]*domain.Evidence{}
	}
	if st.s.Verifications == nil {
		st.s.Verifications = map[string]*domain.Verification{}
	}
	if st.s.Incidents == nil {
		st.s.Incidents = map[string]*domain.Incident{}
	}
}

// Save writes the current snapshot atomically. It is safe to call concurrently.
func (st *Store) Save() error {
	if st.path == "" {
		return nil
	}
	st.mu.Lock()
	dirty := st.dirty
	var raw []byte
	var err error
	if dirty {
		raw, err = json.MarshalIndent(st.s, "", "  ")
	}
	st.mu.Unlock()
	if err != nil || !dirty {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(st.path), 0o755); err != nil {
		return err
	}
	tmp := st.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, st.path); err != nil {
		return err
	}
	st.mu.Lock()
	st.dirty = false
	st.mu.Unlock()
	return nil
}

// ---- users ----

func (st *Store) UpsertUser(u *domain.User) {
	st.mu.Lock()
	st.s.Users[u.ID] = u
	st.dirty = true
	st.mu.Unlock()
}

func (st *Store) User(id string) (*domain.User, error) {
	st.mu.RLock()
	defer st.mu.RUnlock()
	u, ok := st.s.Users[id]
	if !ok {
		return nil, ErrNotFound
	}
	cp := *u
	return &cp, nil
}

// ---- constraints ----

func (st *Store) AddConstraint(c *domain.Constraint) {
	st.mu.Lock()
	st.s.Constraints[c.ID] = c
	st.dirty = true
	st.mu.Unlock()
}

// ActiveConstraints returns every constraint that currently participates in
// enforcement. Advice-state (unconfirmed) constraints are excluded by design.
func (st *Store) ActiveConstraints(userID string) []domain.Constraint {
	st.mu.RLock()
	defer st.mu.RUnlock()
	out := make([]domain.Constraint, 0, len(st.s.Constraints))
	for _, c := range st.s.Constraints {
		if c.UserID == userID && c.Active {
			out = append(out, *c)
		}
	}
	return out
}

func (st *Store) AllConstraints(userID string) []domain.Constraint {
	st.mu.RLock()
	defer st.mu.RUnlock()
	out := make([]domain.Constraint, 0, len(st.s.Constraints))
	for _, c := range st.s.Constraints {
		if c.UserID == userID {
			out = append(out, *c)
		}
	}
	return out
}

func (st *Store) DeleteConstraint(id string) {
	st.mu.Lock()
	delete(st.s.Constraints, id)
	st.dirty = true
	st.mu.Unlock()
}

// ---- goals ----

func (st *Store) AddGoal(g *domain.Goal) {
	st.mu.Lock()
	st.s.Goals[g.ID] = g
	st.dirty = true
	st.mu.Unlock()
}

func (st *Store) Goal(id string) (*domain.Goal, error) {
	st.mu.RLock()
	defer st.mu.RUnlock()
	g, ok := st.s.Goals[id]
	if !ok {
		return nil, ErrNotFound
	}
	cp := *g
	return &cp, nil
}

// GoalByFingerprint prevents the same objective from being scheduled twice.
func (st *Store) GoalByFingerprint(userID, fp string) (*domain.Goal, error) {
	st.mu.RLock()
	defer st.mu.RUnlock()
	for _, g := range st.s.Goals {
		if g.UserID == userID && g.Fingerprint == fp {
			cp := *g
			return &cp, nil
		}
	}
	return nil, ErrNotFound
}

// ---- tasks ----

func (st *Store) AddTask(t *domain.Task) {
	st.mu.Lock()
	st.s.Tasks[t.ID] = t
	st.dirty = true
	st.mu.Unlock()
}

func (st *Store) Task(id string) (*domain.Task, error) {
	st.mu.RLock()
	defer st.mu.RUnlock()
	t, ok := st.s.Tasks[id]
	if !ok {
		return nil, ErrNotFound
	}
	cp := *t
	return &cp, nil
}

func (st *Store) UpdateTask(t *domain.Task) {
	st.mu.Lock()
	st.s.Tasks[t.ID] = t
	st.dirty = true
	st.mu.Unlock()
}

func (st *Store) TasksByUser(userID string) []domain.Task {
	st.mu.RLock()
	defer st.mu.RUnlock()
	out := make([]domain.Task, 0, len(st.s.Tasks))
	for _, t := range st.s.Tasks {
		if t.UserID == userID {
			out = append(out, *t)
		}
	}
	return out
}

// DueTasks returns tasks whose next run time has arrived. Terminal tasks are skipped.
func (st *Store) DueTasks(now time.Time) []domain.Task {
	st.mu.RLock()
	defer st.mu.RUnlock()
	out := make([]domain.Task, 0)
	for _, t := range st.s.Tasks {
		if t.Status == domain.StatusTerminated || t.Status == domain.StatusDone {
			continue
		}
		if !t.NextRun.After(now) {
			out = append(out, *t)
		}
	}
	return out
}

// ---- runs ----

func (st *Store) AddRun(r *domain.Run) {
	st.mu.Lock()
	st.s.Runs[r.ID] = r
	st.dirty = true
	st.mu.Unlock()
}

func (st *Store) UpdateRun(r *domain.Run) {
	st.mu.Lock()
	st.s.Runs[r.ID] = r
	st.dirty = true
	st.mu.Unlock()
}

func (st *Store) Run(id string) (*domain.Run, error) {
	st.mu.RLock()
	defer st.mu.RUnlock()
	r, ok := st.s.Runs[id]
	if !ok {
		return nil, ErrNotFound
	}
	cp := *r
	return &cp, nil
}

func (st *Store) RunsByTask(taskID string) []domain.Run {
	st.mu.RLock()
	defer st.mu.RUnlock()
	out := make([]domain.Run, 0, len(st.s.Runs))
	for _, r := range st.s.Runs {
		if r.TaskID == taskID {
			out = append(out, *r)
		}
	}
	return out
}

func (st *Store) AllRuns() []domain.Run {
	st.mu.RLock()
	defer st.mu.RUnlock()
	out := make([]domain.Run, 0, len(st.s.Runs))
	for _, r := range st.s.Runs {
		out = append(out, *r)
	}
	return out
}

// ---- intents ----

func (st *Store) AddIntent(i *domain.Intent) {
	st.mu.Lock()
	st.s.Intents[i.ID] = i
	st.dirty = true
	st.mu.Unlock()
}

func (st *Store) Intent(id string) (*domain.Intent, error) {
	st.mu.RLock()
	defer st.mu.RUnlock()
	i, ok := st.s.Intents[id]
	if !ok {
		return nil, ErrNotFound
	}
	cp := *i
	cp.Params = cloneMap(i.Params)
	return &cp, nil
}

func (st *Store) UpdateIntent(i *domain.Intent) {
	st.mu.Lock()
	st.s.Intents[i.ID] = i
	st.dirty = true
	st.mu.Unlock()
}

// ---- approvals ----

func (st *Store) AddApproval(a *domain.Approval) {
	st.mu.Lock()
	st.s.Approvals[a.ID] = a
	st.dirty = true
	st.mu.Unlock()
}

func (st *Store) UpdateApproval(a *domain.Approval) {
	st.mu.Lock()
	st.s.Approvals[a.ID] = a
	st.dirty = true
	st.mu.Unlock()
}

func (st *Store) Approval(id string) (*domain.Approval, error) {
	st.mu.RLock()
	defer st.mu.RUnlock()
	a, ok := st.s.Approvals[id]
	if !ok {
		return nil, ErrNotFound
	}
	cp := *a
	return &cp, nil
}

func (st *Store) ApprovalsByUser(userID string) []domain.Approval {
	st.mu.RLock()
	defer st.mu.RUnlock()
	out := make([]domain.Approval, 0, len(st.s.Approvals))
	for _, a := range st.s.Approvals {
		if a.UserID == userID {
			out = append(out, *a)
		}
	}
	return out
}

// ---- evidence ----

func (st *Store) AddEvidence(e *domain.Evidence) {
	st.mu.Lock()
	st.s.Evidence[e.ID] = e
	st.dirty = true
	st.mu.Unlock()
}

func (st *Store) EvidenceByRun(runID string) []domain.Evidence {
	st.mu.RLock()
	defer st.mu.RUnlock()
	out := make([]domain.Evidence, 0, len(st.s.Evidence))
	for _, e := range st.s.Evidence {
		if e.RunID == runID {
			cp := *e
			cp.Fields = cloneFloatMap(e.Fields)
			out = append(out, cp)
		}
	}
	return out
}

// ---- verification ----

func (st *Store) AddVerification(v *domain.Verification) {
	st.mu.Lock()
	st.s.Verifications[v.ID] = v
	st.dirty = true
	st.mu.Unlock()
}

func (st *Store) VerificationByRun(runID string) (*domain.Verification, error) {
	st.mu.RLock()
	defer st.mu.RUnlock()
	for _, v := range st.s.Verifications {
		if v.RunID == runID {
			cp := *v
			return &cp, nil
		}
	}
	return nil, ErrNotFound
}

// ---- incidents ----

func (st *Store) AddIncident(i *domain.Incident) {
	st.mu.Lock()
	st.s.Incidents[i.ID] = i
	st.dirty = true
	st.mu.Unlock()
}

func (st *Store) Incidents() []domain.Incident {
	st.mu.RLock()
	defer st.mu.RUnlock()
	out := make([]domain.Incident, 0, len(st.s.Incidents))
	for _, i := range st.s.Incidents {
		out = append(out, *i)
	}
	return out
}

// ---- audit ----

// AppendAudit adds an event and links it to the previous hash. Callers must treat
// an error as fatal for the operation that produced the event: the architecture
// requires "no execution without a recorded audit trail".
func (st *Store) AppendAudit(actor, typ, userID, taskID, intentID, payload string) *domain.AuditEvent {
	st.mu.Lock()
	defer st.mu.Unlock()
	var prev string
	seq := int64(len(st.s.Audit) + 1)
	if n := len(st.s.Audit); n > 0 {
		prev = st.s.Audit[n-1].Hash
	}
	e := &domain.AuditEvent{
		Seq:      seq,
		At:       time.Now().UTC(),
		Actor:    actor,
		Type:     typ,
		UserID:   userID,
		TaskID:   taskID,
		IntentID: intentID,
		Payload:  payload,
		PrevHash: prev,
	}
	e.Hash = auditHash(e)
	st.s.Audit = append(st.s.Audit, e)
	st.dirty = true
	cp := *e
	return &cp
}

func auditHash(e *domain.AuditEvent) string {
	h := sha256.New()
	fmt.Fprintf(h, "%d|%s|%s|%s|%s|%s|%s|%s|%s",
		e.Seq, e.At.Format(time.RFC3339Nano), e.Actor, e.Type, e.UserID, e.TaskID, e.IntentID, e.Payload, e.PrevHash)
	return hex.EncodeToString(h.Sum(nil))
}

// AuditAll returns the full chain in insertion order.
func (st *Store) AuditAll() []domain.AuditEvent {
	st.mu.RLock()
	defer st.mu.RUnlock()
	out := make([]domain.AuditEvent, 0, len(st.s.Audit))
	for _, e := range st.s.Audit {
		out = append(out, *e)
	}
	return out
}

// AuditByTask filters the chain for replay of a single task.
func (st *Store) AuditByTask(taskID string) []domain.AuditEvent {
	st.mu.RLock()
	defer st.mu.RUnlock()
	out := make([]domain.AuditEvent, 0)
	for _, e := range st.s.Audit {
		if e.TaskID == taskID {
			out = append(out, *e)
		}
	}
	return out
}

func cloneMap(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func cloneFloatMap(m map[string]float64) map[string]float64 {
	if m == nil {
		return nil
	}
	out := make(map[string]float64, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
