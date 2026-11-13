// Package verifier decides whether an observation actually satisfies the goal.
//
// It deliberately does not call a model. Thresholds, boundaries and anomalies are
// deterministic checks so that a run can be replayed and regression-tested.
package verifier

import (
	"fmt"
	"math"
	"time"

	"personal-agent/internal/domain"
)

// Engine applies the three rule families: consistency, boundary, anomaly.
type Engine struct {
	// AnomalyDropRatio flags a price that moves more than this fraction between
	// two consecutive runs. Such a move is treated as uncertain, not as a win.
	AnomalyDropRatio float64
}

// New returns an engine with production defaults.
func New() *Engine {
	return &Engine{AnomalyDropRatio: 0.6}
}

// Verify returns a three-state verdict. "Uncertain" is never converted to "passed":
// it always routes to a human or to the next run.
func (e *Engine) Verify(g *domain.Goal, ev *domain.Evidence, cs []domain.Constraint, prev *domain.Evidence, amountCNY float64) *domain.Verification {
	reasons := make([]string, 0, 4)
	v := &domain.Verification{
		ID:     domain.NewID("ver"),
		RunID:  ev.RunID,
		TaskID: ev.TaskID,
		At:     time.Now().UTC(),
	}

	metric := g.Metric
	if metric == "" {
		metric = "price"
	}

	// consistency: an empty source or a missing field means we cannot conclude anything.
	if ev.Source == "" {
		v.Verdict = domain.VerdictFailed
		v.Reasons = append(reasons, "consistency: evidence has no source")
		v.Reasons = append(v.Reasons, reasons...)
		return v
	}
	observed, ok := ev.Fields[metric]
	if !ok {
		v.Verdict = domain.VerdictUncertain
		v.Reasons = append(reasons, fmt.Sprintf("consistency: field %q missing from %s evidence", metric, ev.Source))
		v.Reasons = append(v.Reasons, reasons...)
		return v
	}

	// boundary: hard constraints win over any threshold outcome.
	for _, c := range cs {
		switch c.Kind {
		case "channel_allow":
			if g.Channel != "" && !contains(splitList(c.Value), g.Channel) {
				v.Verdict = domain.VerdictFailed
				reasons = append(reasons, fmt.Sprintf("boundary: channel %q not in allow list", g.Channel))
			}
		case "channel_deny":
			if g.Channel != "" && contains(splitList(c.Value), g.Channel) {
				v.Verdict = domain.VerdictFailed
				reasons = append(reasons, fmt.Sprintf("boundary: channel %q is denied", g.Channel))
			}
		case "budget":
			limit := parseFloat(c.Value)
			if amountCNY > 0 && limit > 0 && amountCNY > limit {
				v.Verdict = domain.VerdictFailed
				reasons = append(reasons, fmt.Sprintf("boundary: amount %.2f exceeds budget %.2f", amountCNY, limit))
			}
		}
	}
	if v.Verdict == domain.VerdictFailed {
		v.Reasons = reasons
		return v
	}

	// anomaly: a violent move is suspicious and needs a human or a second source.
	if prev != nil {
		if p, ok := prev.Fields[metric]; ok && p > 0 {
			drop := (p - observed) / p
			if drop >= e.AnomalyDropRatio {
				v.Verdict = domain.VerdictUncertain
				reasons = append(reasons, fmt.Sprintf("anomaly: %s moved %.0f%% since last run (%.2f -> %.2f)", metric, drop*100, p, observed))
				v.Reasons = reasons
				return v
			}
		}
	}

	// threshold: reaching it or not is an outcome, not a correctness error.
	if e.Satisfied(g, observed) {
		reasons = append(reasons, fmt.Sprintf("threshold_met: %s %.2f %s %.2f", metric, observed, g.Op, g.Value))
	} else {
		reasons = append(reasons, fmt.Sprintf("threshold_not_met: %s %.2f vs %.2f", metric, observed, g.Value))
	}
	v.Verdict = domain.VerdictPassed
	v.Reasons = reasons
	return v
}

// Satisfied reports whether the observed value meets the goal threshold.
func (e *Engine) Satisfied(g *domain.Goal, observed float64) bool {
	switch g.Op {
	case "lte":
		return observed <= g.Value
	case "gte":
		return observed >= g.Value
	case "eq":
		return math.Abs(observed-g.Value) < 0.01
	case "":
		return true
	default:
		return false
	}
}

func splitList(s string) []string {
	out := []string{}
	cur := ""
	for _, r := range s {
		if r == ',' || r == '，' || r == '|' {
			if cur != "" {
				out = append(out, cur)
				cur = ""
			}
			continue
		}
		cur += string(r)
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

func parseFloat(s string) float64 {
	var f float64
	_, err := fmt.Sscanf(s, "%f", &f)
	if err != nil {
		return 0
	}
	return f
}
