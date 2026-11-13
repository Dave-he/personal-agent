// Package observability exposes counters that are independent of business logic.
// Operators should never have to ask the agent "how are you doing" - the numbers
// live here and are exported in Prometheus text format.
package observability

import (
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// Metrics is a small, dependency-free registry.
type Metrics struct {
	mu       sync.RWMutex
	counters map[string]*int64
	started  time.Time
}

// New creates an empty registry.
func New() *Metrics {
	return &Metrics{counters: map[string]*int64{}, started: time.Now()}
}

// Inc increments a counter by one.
func (m *Metrics) Inc(name string) { m.Add(name, 1) }

// Add increments a counter by n.
func (m *Metrics) Add(name string, n int64) {
	m.mu.RLock()
	c, ok := m.counters[name]
	m.mu.RUnlock()
	if ok {
		atomic.AddInt64(c, n)
		return
	}
	m.mu.Lock()
	if c, ok = m.counters[name]; !ok {
		var v int64
		c = &v
		m.counters[name] = c
	}
	m.mu.Unlock()
	atomic.AddInt64(c, n)
}

// Get reads a counter.
func (m *Metrics) Get(name string) int64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if c, ok := m.counters[name]; ok {
		return atomic.LoadInt64(c)
	}
	return 0
}

// Snapshot returns a stable copy for the API.
func (m *Metrics) Snapshot() map[string]int64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make(map[string]int64, len(m.counters))
	for k, c := range m.counters {
		out[k] = atomic.LoadInt64(c)
	}
	return out
}

// Prometheus renders the registry in exposition format.
func (m *Metrics) Prometheus() string {
	snap := m.Snapshot()
	keys := make([]string, 0, len(snap))
	for k := range snap {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := "# HELP agent_uptime_seconds Seconds since process start.\n# TYPE agent_uptime_seconds gauge\n"
	out += fmt.Sprintf("agent_uptime_seconds %.0f\n", time.Since(m.started).Seconds())
	for _, k := range keys {
		out += fmt.Sprintf("agent_%s %d\n", sanitize(k), snap[k])
	}
	return out
}

func sanitize(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
			out = append(out, r)
		default:
			out = append(out, '_')
		}
	}
	return string(out)
}
