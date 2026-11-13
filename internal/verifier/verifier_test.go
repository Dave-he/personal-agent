package verifier

import (
	"testing"

	"personal-agent/internal/domain"
)

func goal() *domain.Goal {
	return &domain.Goal{Kind: "price_watch", Object: "headphone", Metric: "price", Op: "lte", Value: 800, Channel: "official"}
}

func evidence(price float64) *domain.Evidence {
	return &domain.Evidence{ID: "e1", RunID: "r1", TaskID: "t1", Source: "page", Fields: map[string]float64{"price": price}}
}

func TestThresholdMet(t *testing.T) {
	v := New().Verify(goal(), evidence(700), nil, nil, 0)
	if v.Verdict != domain.VerdictPassed {
		t.Fatalf("want passed, got %s (%v)", v.Verdict, v.Reasons)
	}
	if !hasPrefix(v.Reasons, "threshold_met") {
		t.Fatalf("want threshold_met reason, got %v", v.Reasons)
	}
}

func TestThresholdNotMetStillPassed(t *testing.T) {
	v := New().Verify(goal(), evidence(900), nil, nil, 0)
	if v.Verdict != domain.VerdictPassed {
		t.Fatalf("a valid observation must pass verification even when the threshold is missed, got %s", v.Verdict)
	}
	if !hasPrefix(v.Reasons, "threshold_not_met") {
		t.Fatalf("want threshold_not_met reason, got %v", v.Reasons)
	}
}

func TestMissingFieldIsUncertain(t *testing.T) {
	v := New().Verify(goal(), &domain.Evidence{ID: "e", RunID: "r", TaskID: "t", Source: "page"}, nil, nil, 0)
	if v.Verdict != domain.VerdictUncertain {
		t.Fatalf("missing field must be uncertain, got %s", v.Verdict)
	}
}

func TestNoSourceIsFailure(t *testing.T) {
	v := New().Verify(goal(), &domain.Evidence{ID: "e", RunID: "r", TaskID: "t", Fields: map[string]float64{"price": 10}}, nil, nil, 0)
	if v.Verdict != domain.VerdictFailed {
		t.Fatalf("evidence without a source cannot be trusted, got %s", v.Verdict)
	}
}

func TestBudgetBoundaryFails(t *testing.T) {
	cs := []domain.Constraint{{Kind: "budget", Value: "100", Active: true}}
	v := New().Verify(goal(), evidence(50), cs, nil, 200)
	if v.Verdict != domain.VerdictFailed {
		t.Fatalf("amount above budget must fail, got %s", v.Verdict)
	}
}

func TestChannelAllowList(t *testing.T) {
	cs := []domain.Constraint{{Kind: "channel_allow", Value: "self,official", Active: true}}
	v := New().Verify(goal(), evidence(50), cs, nil, 0)
	if v.Verdict != domain.VerdictPassed {
		t.Fatalf("channel in allow list must pass, got %s (%v)", v.Verdict, v.Reasons)
	}
	g := goal()
	g.Channel = "third-party"
	v = New().Verify(g, evidence(50), cs, nil, 0)
	if v.Verdict != domain.VerdictFailed {
		t.Fatalf("channel outside allow list must fail, got %s", v.Verdict)
	}
}

func TestAnomalyIsUncertain(t *testing.T) {
	prev := evidence(1000)
	v := New().Verify(goal(), evidence(300), nil, prev, 0)
	if v.Verdict != domain.VerdictUncertain {
		t.Fatalf("a 70%% drop is an anomaly, expected uncertain, got %s", v.Verdict)
	}
}

func hasPrefix(list []string, p string) bool {
	for _, s := range list {
		if len(s) >= len(p) && s[:len(p)] == p {
			return true
		}
	}
	return false
}
