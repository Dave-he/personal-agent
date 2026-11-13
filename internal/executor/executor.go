// Package executor runs a signed intent inside an isolated context and returns
// evidence. It never decides whether the result is correct or whether to retry.
package executor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"personal-agent/internal/domain"
	"personal-agent/internal/gateway"
)

// ErrRejected is returned when the gateway refuses the intent.
var ErrRejected = errors.New("executor: intent rejected")

// Executor performs one action and returns raw evidence.
type Executor interface {
	Execute(ctx context.Context, in *domain.Intent) (*domain.Evidence, error)
}

// Guard verifies signatures before execution. In this build the guard is the
// Sentinel itself, which keeps signature logic in one place.
type Guard interface {
	Verify(in *domain.Intent) error
}

// Base holds the guard shared by all executors.
type Base struct {
	Guard Guard
}

func (b Base) check(in *domain.Intent) error {
	if b.Guard == nil {
		return errors.New("executor: no guard configured")
	}
	if err := b.Guard.Verify(in); err != nil {
		return fmt.Errorf("%w: %v", ErrRejected, err)
	}
	return nil
}

var _ Guard = (*gateway.Sentinel)(nil)

// SimExecutor is a deterministic-ish stand-in for a platform adapter. It exists so
// the full loop can be exercised without credentials. Prices come from params when
// present, otherwise a bounded random walk around a base value.
type SimExecutor struct {
	Base
	BasePrice float64
}

// Execute implements Executor.
func (s SimExecutor) Execute(ctx context.Context, in *domain.Intent) (*domain.Evidence, error) {
	if err := s.check(in); err != nil {
		return nil, err
	}
	metric := in.Params["metric"]
	if metric == "" {
		metric = "price"
	}
	base := s.BasePrice
	if base <= 0 {
		base = 1000
	}
	price := base
	if v, err := strconv.ParseFloat(in.Params["price"], 64); err == nil && v > 0 {
		price = v
	} else {
		price = base * (0.9 + rand.Float64()*0.2) // #nosec G404 - demo jitter, not cryptography
	}
	f := map[string]float64{metric: round2(price)}
	if in.Params["stock"] != "" {
		if v, err := strconv.ParseFloat(in.Params["stock"], 64); err == nil {
			f["stock"] = v
		}
	}
	return &domain.Evidence{
		ID:         domain.NewID("evd"),
		RunID:      in.RunID,
		TaskID:     in.TaskID,
		Source:     "sim",
		URL:        in.Params["url"],
		CapturedAt: time.Now().UTC(),
		Fields:     f,
		Raw:        fmt.Sprintf("simulated read for %s", in.Action),
	}, nil
}

// HTTPExecutor fetches a page and extracts a single numeric field. It is the
// fallback path described in the architecture: used only when no official API
// adapter exists, and it refuses obvious SSRF targets.
type HTTPExecutor struct {
	Base
	Client *http.Client
}

// Execute implements Executor.
func (h HTTPExecutor) Execute(ctx context.Context, in *domain.Intent) (*domain.Evidence, error) {
	if err := h.check(in); err != nil {
		return nil, err
	}
	raw := in.Params["url"]
	if err := safeURL(raw); err != nil {
		return nil, err
	}
	client := h.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "personal-agent/0.1 (read-only)")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("executor: upstream status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	metric := in.Params["metric"]
	if metric == "" {
		metric = "price"
	}
	pat := in.Params["regex"]
	if pat == "" {
		pat = `[0-9]+(?:\.[0-9]+)?`
	}
	re, err := regexp.Compile(pat)
	if err != nil {
		return nil, fmt.Errorf("executor: bad regex: %w", err)
	}
	hit := re.FindString(string(body))
	if hit == "" {
		// No number found is not a failure of the executor: it is an uncertain
		// observation, and the verifier will treat it as such.
		return &domain.Evidence{
			ID: domain.NewID("evd"), RunID: in.RunID, TaskID: in.TaskID,
			Source: "page", URL: raw, CapturedAt: time.Now().UTC(),
			Fields: map[string]float64{}, Raw: truncate(string(body), 200),
		}, nil
	}
	v, err := strconv.ParseFloat(hit, 64)
	if err != nil {
		return nil, err
	}
	return &domain.Evidence{
		ID: domain.NewID("evd"), RunID: in.RunID, TaskID: in.TaskID,
		Source: "page", URL: raw, CapturedAt: time.Now().UTC(),
		Fields: map[string]float64{metric: v}, Raw: truncate(string(body), 200),
	}, nil
}

func safeURL(raw string) error {
	if raw == "" {
		return errors.New("executor: empty url")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("executor: scheme %q not allowed", u.Scheme)
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return errors.New("executor: missing host")
	}
	if host == "localhost" || strings.HasSuffix(host, ".localhost") ||
		host == "127.0.0.1" || host == "::1" || strings.HasPrefix(host, "10.") ||
		strings.HasPrefix(host, "192.168.") || strings.HasPrefix(host, "169.254.") ||
		strings.HasPrefix(host, "172.16.") || strings.HasPrefix(host, "172.17.") {
		return fmt.Errorf("executor: host %q is not allowed", host)
	}
	return nil
}

func round2(f float64) float64 {
	return float64(int(f*100+0.5)) / 100
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
