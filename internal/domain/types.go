// Package domain holds the core data model shared by every layer.
// It has no dependencies on other internal packages so that any layer can import it.
package domain

import (
	"time"
)

// RiskLevel is the permission class attached to every outward action.
type RiskLevel string

const (
	// RiskRead only reads information and never mutates anything at the target.
	RiskRead RiskLevel = "read"
	// RiskConfirm mutates something at the target and therefore requires an approval.
	RiskConfirm RiskLevel = "confirm"
	// RiskForbidden must never be executed automatically, even with approval.
	RiskForbidden RiskLevel = "forbidden"
)

// TaskStatus is the lifecycle state of a task.
type TaskStatus string

const (
	StatusWaiting    TaskStatus = "waiting"
	StatusRunning    TaskStatus = "running"
	StatusSleeping   TaskStatus = "sleeping"
	StatusDone       TaskStatus = "done"
	StatusFailed     TaskStatus = "failed"
	StatusTerminated TaskStatus = "terminated"
)

// Verdict is the three-state outcome of verification. There is deliberately no
// "assume passed" state: uncertain must always fall through to a human.
type Verdict string

const (
	VerdictPassed    Verdict = "passed"
	VerdictFailed    Verdict = "failed"
	VerdictUncertain Verdict = "uncertain"
)

// ApprovalState tracks a single approval request.
type ApprovalState string

const (
	ApprovalPending  ApprovalState = "pending"
	ApprovalGranted  ApprovalState = "granted"
	ApprovalRejected ApprovalState = "rejected"
	ApprovalExpired  ApprovalState = "expired"
)

// User is the account holder. TrustLevel decides which capability tier is open.
type User struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	TrustLevel string    `json:"trust_level"` // tier0 read-only .. tier2 limited autonomy
	CreatedAt  time.Time `json:"created_at"`
}

// Constraint is a structured, executable rule. Inferred constraints stay in the
// advice state until the user confirms them, so the model can never widen its own bounds.
type Constraint struct {
	ID         string    `json:"id"`
	UserID     string    `json:"user_id"`
	Kind       string    `json:"kind"` // budget, channel_allow, channel_deny, notify_window, city
	Value      string    `json:"value"`
	Source     string    `json:"source"` // user | inferred
	Confidence float64   `json:"confidence,omitempty"`
	Active     bool      `json:"active"`
	CreatedAt  time.Time `json:"created_at"`
}

// Goal is a structured, checkable objective. Free text never becomes a goal on
// its own: it must be compiled into fields, including an acceptance condition.
type Goal struct {
	ID          string    `json:"id"`
	UserID      string    `json:"user_id"`
	Kind        string    `json:"kind"`
	Object      string    `json:"object"`
	Metric      string    `json:"metric"`
	Op          string    `json:"op"` // lte, gte, eq
	Value       float64   `json:"value"`
	Channel     string    `json:"channel,omitempty"`
	QueryURL    string    `json:"query_url,omitempty"`
	Acceptance  []string  `json:"acceptance"`
	Fingerprint string    `json:"fingerprint"`
	CreatedAt   time.Time `json:"created_at"`
}

// Task binds a goal to a schedule.
type Task struct {
	ID         string     `json:"id"`
	UserID     string     `json:"user_id"`
	GoalID     string     `json:"goal_id"`
	Schedule   string     `json:"schedule"` // once | interval
	IntervalS  int        `json:"interval_s"`
	NextRun    time.Time  `json:"next_run"`
	Status     TaskStatus `json:"status"`
	FailStreak int        `json:"fail_streak"`
	Runs       int        `json:"runs"`
	CostCNY    float64    `json:"cost_cny"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

// Step is one entry of the plan/act/observe trace kept for audit replay.
type Step struct {
	Name   string    `json:"name"`
	At     time.Time `json:"at"`
	Detail string    `json:"detail"`
}

// Run is a single execution of the five-step loop.
type Run struct {
	ID        string     `json:"id"`
	TaskID    string     `json:"task_id"`
	Token     string     `json:"token"`
	StartedAt time.Time  `json:"started_at"`
	EndedAt   *time.Time `json:"ended_at,omitempty"`
	Verdict   Verdict    `json:"verdict"`
	Reason    string     `json:"reason"`
	Steps     []Step     `json:"steps"`
	CostCNY   float64    `json:"cost_cny"`
}

// Intent is an authorisation request. The planner emits intents; it never emits
// actions. Only the gateway can sign an intent, and only an executor may verify it.
type Intent struct {
	ID         string            `json:"id"`
	TaskID     string            `json:"task_id"`
	RunID      string            `json:"run_id"`
	UserID     string            `json:"user_id"`
	Action     string            `json:"action"`
	Params     map[string]string `json:"params"`
	Risk       RiskLevel         `json:"risk"`
	Nonce      string            `json:"nonce"`
	Signature  string            `json:"signature,omitempty"`
	ExpiresAt  time.Time         `json:"expires_at"`
	ApprovalID string            `json:"approval_id,omitempty"`
	Reason     string            `json:"reason,omitempty"` // set when rejected
}

// Approval is the user-visible decision point for a confirm-level intent.
type Approval struct {
	ID          string        `json:"id"`
	IntentID    string        `json:"intent_id"`
	UserID      string        `json:"user_id"`
	Summary     string        `json:"summary"`
	AmountCNY   float64       `json:"amount_cny"`
	Object      string        `json:"object"`
	RejectRisk  string        `json:"reject_risk"` // what happens if the user says no
	State       ApprovalState `json:"state"`
	Token       string        `json:"token,omitempty"`
	ContentHash string        `json:"content_hash"`
	CreatedAt   time.Time     `json:"created_at"`
	DecidedAt   *time.Time    `json:"decided_at,omitempty"`
}

// Evidence is a captured fact. It backs verification and stays in the snapshot
// store for a bounded retention window; conclusions are never derived from it directly.
type Evidence struct {
	ID         string             `json:"id"`
	RunID      string             `json:"run_id"`
	TaskID     string             `json:"task_id"`
	Source     string             `json:"source"` // api | page | receipt
	URL        string             `json:"url"`
	CapturedAt time.Time          `json:"captured_at"`
	Fields     map[string]float64 `json:"fields"`
	Raw        string             `json:"raw,omitempty"`
}

// Verification records which rules were hit and what the engine concluded.
type Verification struct {
	ID      string    `json:"id"`
	RunID   string    `json:"run_id"`
	TaskID  string    `json:"task_id"`
	Verdict Verdict   `json:"verdict"`
	Reasons []string  `json:"reasons"`
	At      time.Time `json:"at"`
}

// AuditEvent is append-only. Hash chaining makes silent edits detectable.
type AuditEvent struct {
	Seq      int64     `json:"seq"`
	At       time.Time `json:"at"`
	Actor    string    `json:"actor"`
	Type     string    `json:"type"`
	UserID   string    `json:"user_id,omitempty"`
	TaskID   string    `json:"task_id,omitempty"`
	IntentID string    `json:"intent_id,omitempty"`
	Payload  string    `json:"payload,omitempty"`
	PrevHash string    `json:"prev_hash"`
	Hash     string    `json:"hash"`
}

// Incident is opened by the circuit breaker or by a user complaint.
type Incident struct {
	ID     string    `json:"id"`
	Level  string    `json:"level"` // task | scene | user | global
	UserID string    `json:"user_id,omitempty"`
	TaskID string    `json:"task_id,omitempty"`
	Scene  string    `json:"scene,omitempty"`
	Detail string    `json:"detail"`
	Open   bool      `json:"open"`
	At     time.Time `json:"at"`
}

// Decision is what the gateway returns to the caller.
type Decision struct {
	Allowed    bool
	NeedsHuman bool
	IntentID   string
	ApprovalID string
	Reason     string
}
