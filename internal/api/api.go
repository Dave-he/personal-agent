// Package api exposes the HTTP surface of the agent.
//
// Every write endpoint is intentionally narrow: the API can create goals and
// approvals, but it can never bypass the gateway to execute an action.
package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"personal-agent/internal/audit"
	"personal-agent/internal/domain"
	"personal-agent/internal/gateway"
	"personal-agent/internal/observability"
	"personal-agent/internal/planner"
	"personal-agent/internal/scheduler"
	"personal-agent/internal/store"
	"personal-agent/web"
)

// Deps are the collaborators the API needs. Nothing else is reachable from here.
type Deps struct {
	Store     *store.Store
	Sched     *scheduler.Scheduler
	Gateway   *gateway.Sentinel
	Extractor planner.Extractor
	Metrics   *observability.Metrics
}

// New builds the router.
func New(d Deps) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "paused": d.Sched.Paused.Load()})
	})

	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = w.Write([]byte(d.Metrics.Prometheus()))
	})

	mux.HandleFunc("GET /api/state", func(w http.ResponseWriter, r *http.Request) {
		uid := userID(r)
		writeJSON(w, http.StatusOK, map[string]any{
			"user_id":     uid,
			"tasks":       d.Store.TasksByUser(uid),
			"approvals":   d.Store.ApprovalsByUser(uid),
			"constraints": d.Store.AllConstraints(uid),
			"incidents":   d.Store.Incidents(),
			"metrics":     d.Metrics.Snapshot(),
			"paused":      d.Sched.Paused.Load(),
		})
	})

	mux.HandleFunc("POST /api/goals", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Text      string `json:"text"`
			Schedule  string `json:"schedule"`
			IntervalS int    `json:"interval_s"`
			URL       string `json:"url"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid json")
			return
		}
		uid := userID(r)
		ensureUser(d.Store, uid)
		goal, err := d.Extractor.Parse(body.Text, uid)
		if err != nil {
			writeErr(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		goal.QueryURL = body.URL
		if prev, err := d.Store.GoalByFingerprint(uid, goal.Fingerprint); err == nil {
			writeJSON(w, http.StatusOK, map[string]any{"deduped": true, "goal": prev})
			return
		}
		d.Store.AddGoal(goal)

		sched := body.Schedule
		if sched == "" {
			sched = "interval"
		}
		interval := body.IntervalS
		if interval <= 0 {
			interval = 1800
		}
		task := &domain.Task{
			ID:        domain.NewID("tsk"),
			UserID:    uid,
			GoalID:    goal.ID,
			Schedule:  sched,
			IntervalS: interval,
			NextRun:   time.Now().UTC(),
			Status:    domain.StatusWaiting,
			CreatedAt: time.Now().UTC(),
			UpdatedAt: time.Now().UTC(),
		}
		d.Store.AddTask(task)
		d.Store.AppendAudit("user", "goal_created", uid, task.ID, "", goal.Object)
		writeJSON(w, http.StatusCreated, map[string]any{"goal": goal, "task": task})
	})

	mux.HandleFunc("GET /api/tasks", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"tasks": d.Store.TasksByUser(userID(r))})
	})

	mux.HandleFunc("GET /api/tasks/{id}", func(w http.ResponseWriter, r *http.Request) {
		t, err := d.Store.Task(r.PathValue("id"))
		if err != nil {
			writeErr(w, http.StatusNotFound, "task not found")
			return
		}
		runs := d.Store.RunsByTask(t.ID)
		writeJSON(w, http.StatusOK, map[string]any{"task": t, "runs": runs})
	})

	mux.HandleFunc("POST /api/tasks/{id}/runs", func(w http.ResponseWriter, r *http.Request) {
		run, err := d.Sched.RunTaskNow(r.Context(), r.PathValue("id"))
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, run)
	})

	mux.HandleFunc("POST /api/tasks/{id}/terminate", func(w http.ResponseWriter, r *http.Request) {
		t, err := d.Store.Task(r.PathValue("id"))
		if err != nil {
			writeErr(w, http.StatusNotFound, "task not found")
			return
		}
		t.Status = domain.StatusTerminated
		t.UpdatedAt = time.Now().UTC()
		d.Store.UpdateTask(t)
		d.Store.AppendAudit("user", "task_terminated", t.UserID, t.ID, "", "user terminated")
		writeJSON(w, http.StatusOK, t)
	})

	mux.HandleFunc("GET /api/approvals", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"approvals": d.Store.ApprovalsByUser(userID(r))})
	})

	mux.HandleFunc("POST /api/approvals/{id}/grant", func(w http.ResponseWriter, r *http.Request) {
		run, err := d.Sched.Approve(r.Context(), r.PathValue("id"), userID(r))
		if err != nil {
			writeErr(w, statusFor(err), err.Error())
			return
		}
		writeJSON(w, http.StatusOK, run)
	})

	mux.HandleFunc("POST /api/approvals/{id}/reject", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Reason string `json:"reason"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Reason == "" {
			body.Reason = "user rejected"
		}
		if err := d.Gateway.Reject(r.PathValue("id"), userID(r), body.Reason); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})

	mux.HandleFunc("GET /api/constraints", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"constraints": d.Store.AllConstraints(userID(r))})
	})

	mux.HandleFunc("POST /api/constraints", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Kind  string  `json:"kind"`
			Value string  `json:"value"`
			Infer bool    `json:"inferred"`
			Conf  float64 `json:"confidence"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid json")
			return
		}
		if body.Kind == "" || body.Value == "" {
			writeErr(w, http.StatusBadRequest, "kind and value are required")
			return
		}
		c := &domain.Constraint{
			ID:         domain.NewID("cst"),
			UserID:     userID(r),
			Kind:       body.Kind,
			Value:      body.Value,
			Source:     "user",
			Confidence: 1,
			Active:     true,
			CreatedAt:  time.Now().UTC(),
		}
		if body.Infer {
			// Inferred constraints start inactive: the model may propose bounds,
			// only the user may enforce them.
			c.Source = "inferred"
			c.Confidence = body.Conf
			c.Active = false
		}
		d.Store.AddConstraint(c)
		d.Store.AppendAudit("user", "constraint_added", c.UserID, "", "", c.Kind+"="+c.Value)
		writeJSON(w, http.StatusCreated, c)
	})

	mux.HandleFunc("GET /api/audit", func(w http.ResponseWriter, r *http.Request) {
		if tid := r.URL.Query().Get("task_id"); tid != "" {
			writeJSON(w, http.StatusOK, map[string]any{"events": d.Store.AuditByTask(tid)})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"events": d.Store.AuditAll()})
	})

	mux.HandleFunc("GET /api/audit/verify", func(w http.ResponseWriter, r *http.Request) {
		err := audit.Verify(d.Store.AuditAll())
		writeJSON(w, http.StatusOK, map[string]any{"ok": err == nil, "detail": errString(err)})
	})

	mux.HandleFunc("GET /api/incidents", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"incidents": d.Store.Incidents()})
	})

	mux.HandleFunc("POST /api/system/pause", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Paused bool `json:"paused"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		d.Sched.Paused.Store(body.Paused)
		actor := "user"
		d.Store.AppendAudit(actor, "kill_switch", "", "", "", fmt.Sprintf("paused=%v", body.Paused))
		writeJSON(w, http.StatusOK, map[string]any{"paused": body.Paused})
	})

	mux.Handle("/", http.FileServer(http.FS(web.Files)))
	return mux
}

func ensureUser(st *store.Store, uid string) {
	if _, err := st.User(uid); err == nil {
		return
	}
	st.UpsertUser(&domain.User{ID: uid, Name: uid, TrustLevel: "tier0", CreatedAt: time.Now().UTC()})
}

func userID(r *http.Request) string {
	if v := strings.TrimSpace(r.Header.Get("X-User-Id")); v != "" {
		return v
	}
	if v := strings.TrimSpace(r.URL.Query().Get("user_id")); v != "" {
		return v
	}
	return "u_demo"
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]any{"error": msg})
}

func statusFor(err error) int {
	switch {
	case errors.Is(err, gateway.ErrForbidden), errors.Is(err, gateway.ErrBadSignature),
		errors.Is(err, gateway.ErrReplay):
		return http.StatusForbidden
	case errors.Is(err, gateway.ErrExpired):
		return http.StatusGone
	default:
		return http.StatusBadRequest
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
