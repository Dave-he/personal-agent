// Package planner turns a natural-language objective into a structured goal and a
// step plan. It is the only place allowed to interpret user language, and it never
// touches credentials or performs actions itself.
package planner

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"personal-agent/internal/domain"
	"personal-agent/internal/policy"
)

// ErrNeedsClarification is returned when the sentence lacks an enforceable field.
var ErrNeedsClarification = errors.New("planner: missing threshold, please ask the user")

// Action is one executable step the planner proposes. The gateway decides whether
// it is allowed; the planner's opinion about risk is irrelevant.
type Action struct {
	Name      string
	Params    map[string]string
	AmountCNY float64
}

// Plan is the ordered intent list for a single run.
type Plan struct {
	Steps   []string
	Actions []Action
}

// Planner is the interface an LLM-backed implementation would satisfy.
type Planner interface {
	Plan(g *domain.Goal, channel string) (*Plan, error)
}

// RulePlanner is a deterministic planner. It is used by default so that the whole
// loop is testable without a model; swap in an LLM implementation behind the same
// interface when a provider is configured.
type RulePlanner struct{}

// Plan implements Planner.
func (RulePlanner) Plan(g *domain.Goal, channel string) (*Plan, error) {
	steps := []string{
		"plan: resolve goal into one read action",
		"act: request signed intent from gateway",
		"observe: capture evidence snapshot",
		"verify: run consistency, boundary and anomaly rules",
		"commit: update task state and notify on threshold",
	}
	var a Action
	switch g.Kind {
	case "subscription_scan":
		a = Action{Name: policy.ActFetchOrders, Params: map[string]string{"object": g.Object, "metric": g.Metric, "url": g.QueryURL}}
	case "expiry_remind":
		a = Action{Name: policy.ActFetchPage, Params: map[string]string{"object": g.Object, "metric": g.Metric, "url": g.QueryURL}}
	default:
		a = Action{Name: policy.ActFetchPrice, Params: map[string]string{"object": g.Object, "metric": g.Metric, "url": g.QueryURL, "channel": channel}}
	}
	return &Plan{Steps: steps, Actions: []Action{a}}, nil
}

// Extractor converts Chinese/English sentences into a goal.
type Extractor struct{}

var numRe = regexp.MustCompile(`([0-9]+(?:\.[0-9]+)?)`)

// Parse builds a goal from free text. Anything that cannot be made enforceable is
// rejected instead of guessed: a goal without a threshold cannot be verified later.
func (Extractor) Parse(text, userID string) (*domain.Goal, error) {
	t := strings.TrimSpace(text)
	if t == "" {
		return nil, ErrNeedsClarification
	}
	low := strings.ToLower(t)

	kind := "price_watch"
	metric := "price"
	switch {
	case strings.Contains(t, "账单") || strings.Contains(t, "订阅") || strings.Contains(t, "扣费"):
		kind, metric = "subscription_scan", "amount"
	case strings.Contains(t, "到期") || strings.Contains(t, "续费提醒"):
		kind, metric = "expiry_remind", "days_left"
	}

	op := "lte"
	switch {
	case strings.Contains(t, "低于"), strings.Contains(t, "小于"), strings.Contains(t, "不超过"),
		strings.Contains(t, "降到"), strings.Contains(t, "少于"), strings.Contains(low, "below"), strings.Contains(low, "under"):
		op = "lte"
	case strings.Contains(t, "高于"), strings.Contains(t, "大于"), strings.Contains(t, "超过"),
		strings.Contains(low, "above"), strings.Contains(low, "over"):
		op = "gte"
	}

	nums := numRe.FindAllString(t, -1)
	if len(nums) == 0 {
		return nil, ErrNeedsClarification
	}
	value, err := strconv.ParseFloat(nums[0], 64)
	if err != nil {
		return nil, ErrNeedsClarification
	}

	channel := ""
	if strings.Contains(t, "官方") {
		channel = "official"
	} else if strings.Contains(t, "自营") {
		channel = "self"
	}

	object := cleanObject(numRe.ReplaceAllString(t, ""))
	if object == "" {
		object = "target"
	}

	g := &domain.Goal{
		ID:        domain.NewID("gol"),
		UserID:    userID,
		Kind:      kind,
		Object:    object,
		Metric:    metric,
		Op:        op,
		Value:     value,
		Channel:   channel,
		CreatedAt: time.Now().UTC(),
	}
	g.Acceptance = []string{
		fmt.Sprintf("%s %s %.2f", metric, op, value),
		"evidence source present",
		"no constraint violation",
	}
	g.Fingerprint = Fingerprint(g)
	return g, nil
}

// stopWords are filler fragments that carry no identity of the monitored object.
var stopWords = []string{
	"帮我", "盯着", "盯", "就提醒我", "提醒我", "降到", "低于", "高于", "小于", "大于",
	"不超过", "超过", "少于", "以内", "以上", "的价格", "价格", "官方店", "官方", "自营", "的",
}

// cleanObject strips filler words and punctuation so that two paraphrases of the
// same objective produce the same dedupe fingerprint.
func cleanObject(s string) string {
	for _, w := range stopWords {
		s = strings.ReplaceAll(s, w, "")
	}
	s = strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) || unicode.IsPunct(r) || unicode.IsSymbol(r) {
			return -1
		}
		return r
	}, s)
	return s
}

// Fingerprint is the dedupe key for goals.
func Fingerprint(g *domain.Goal) string {
	raw := strings.Join([]string{g.UserID, g.Kind, g.Object, g.Metric, g.Op,
		strconv.FormatFloat(g.Value, 'f', 2, 64), g.Channel}, "|")
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:8])
}
