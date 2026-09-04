// Package routeexample is the built-in cheapest_healthy routing strategy: it
// sends a virtual model's traffic to the cheapest (or fastest) target whose
// recent error rate is acceptable, learning target health from the attempt
// outcomes GoModel reports. It doubles as the reference implementation of a
// pluginapi.RouteStrategy plugin.
package routeexample

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/enterpilot/gomodel/pluginapi"
)

// Name is the manifest name of the plugin.
const Name = "cheapest_healthy"

const (
	// PreferCheapest picks the healthy candidate with the lowest input price.
	PreferCheapest = "cheapest"
	// PreferFastest picks the healthy candidate with the lowest median latency.
	PreferFastest = "fastest"

	// DefaultMaxErrorRate is the error-rate ceiling above which a target is
	// treated as unhealthy.
	DefaultMaxErrorRate = 0.2
	// WindowSize is how many recent outcomes per target are remembered.
	WindowSize = 50
)

// Plugin is one cheapest_healthy instance. Health is tracked per target
// across every virtual model that uses the instance.
type Plugin struct {
	mu      sync.Mutex
	windows map[string]*window
}

// New returns an unconfigured plugin; call Init before use.
func New() pluginapi.Plugin { return &Plugin{windows: map[string]*window{}} }

// Manifest describes the plugin and its per-virtual-model form.
func (p *Plugin) Manifest() pluginapi.Manifest {
	return pluginapi.Manifest{
		Name:        Name,
		Version:     "1.0.0",
		Description: "Routes to the cheapest or fastest target whose recent error rate is acceptable.",
		Kinds:       []pluginapi.Kind{pluginapi.KindRoute},
		ConfigSchema: []pluginapi.Field{
			{
				Key: "prefer", Label: "Prefer", Input: pluginapi.InputSelect, Default: PreferCheapest, Scope: pluginapi.ScopeRoute,
				Help: "Cheapest picks the healthy target with the lowest input price; fastest picks the one with the lowest median latency over its last 50 attempts.",
				Options: []pluginapi.Option{
					{Value: PreferCheapest, Label: "Cheapest"},
					{Value: PreferFastest, Label: "Fastest"},
				},
			},
			{
				Key: "max_error_rate", Label: "Max error rate", Input: pluginapi.InputNumber, Default: DefaultMaxErrorRate, Scope: pluginapi.ScopeRoute,
				Help:        "Share of failed attempts (0 to 1) over a target's last 50 attempts above which it is skipped while another healthy target exists.",
				Placeholder: fmt.Sprint(DefaultMaxErrorRate),
			},
		},
	}
}

// Init has no instance-scoped settings.
func (p *Plugin) Init(context.Context, json.RawMessage, pluginapi.Host) error { return nil }

// Close releases nothing.
func (p *Plugin) Close(context.Context) error { return nil }

// routeConfig is the per-virtual-model config, as GoModel validates it.
type routeConfig struct {
	Prefer       string   `json:"prefer"`
	MaxErrorRate *float64 `json:"max_error_rate"`
}

func parseConfig(raw json.RawMessage) routeConfig {
	cfg := routeConfig{Prefer: PreferCheapest}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &cfg)
	}
	if cfg.Prefer != PreferFastest {
		cfg.Prefer = PreferCheapest
	}
	if cfg.MaxErrorRate == nil || *cfg.MaxErrorRate < 0 {
		rate := DefaultMaxErrorRate
		cfg.MaxErrorRate = &rate
	}
	return cfg
}

// Select keeps a healthy session target, otherwise picks among the healthy
// candidates by price or latency. When every candidate is unhealthy it
// chooses among all of them, so the strategy fails open rather than declining.
func (p *Plugin) Select(_ context.Context, req pluginapi.RouteRequest) (pluginapi.RouteChoice, error) {
	if len(req.Candidates) == 0 {
		return pluginapi.RouteChoice{}, fmt.Errorf("no candidates")
	}
	cfg := parseConfig(req.Config)
	healthy := p.healthyCandidates(req.Candidates, *cfg.MaxErrorRate)
	if req.SessionTarget != "" {
		for _, c := range healthy {
			if c.Qualified == req.SessionTarget {
				return pluginapi.RouteChoice{Qualified: c.Qualified, Reason: "session target is healthy"}, nil
			}
		}
	}
	pool := healthy
	note := ""
	if len(pool) == 0 {
		pool = req.Candidates
		note = " (no healthy candidate)"
	}
	if cfg.Prefer == PreferFastest {
		best, median := p.fastest(pool)
		return pluginapi.RouteChoice{Qualified: best.Qualified, Reason: fmt.Sprintf("fastest median latency %s%s", median, note)}, nil
	}
	best, priced := cheapest(pool)
	if !priced {
		return pluginapi.RouteChoice{Qualified: best.Qualified, Reason: "no pricing; first candidate" + note}, nil
	}
	return pluginapi.RouteChoice{Qualified: best.Qualified, Reason: fmt.Sprintf("cheapest input price $%g/Mtok%s", *best.InputPerMtok, note)}, nil
}

// OnAttemptEnd records the outcome in the target's sliding window.
func (p *Plugin) OnAttemptEnd(outcome pluginapi.RouteOutcome) {
	p.mu.Lock()
	defer p.mu.Unlock()
	key := outcome.Target.Qualified()
	w := p.windows[key]
	if w == nil {
		w = &window{}
		p.windows[key] = w
	}
	w.add(outcome.Success && !outcome.Timeout, outcome.Latency)
}

func (p *Plugin) healthyCandidates(candidates []pluginapi.RouteCandidate, maxErrorRate float64) []pluginapi.RouteCandidate {
	p.mu.Lock()
	defer p.mu.Unlock()
	healthy := make([]pluginapi.RouteCandidate, 0, len(candidates))
	for _, c := range candidates {
		if w := p.windows[c.Qualified]; w == nil || w.errorRate() <= maxErrorRate {
			healthy = append(healthy, c)
		}
	}
	return healthy
}

// fastest returns the candidate with the lowest median latency; a candidate
// without samples counts as zero, so new targets are tried first.
func (p *Plugin) fastest(candidates []pluginapi.RouteCandidate) (pluginapi.RouteCandidate, time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	best, bestMedian := candidates[0], time.Duration(-1)
	for _, c := range candidates {
		var median time.Duration
		if w := p.windows[c.Qualified]; w != nil {
			median = w.medianLatency()
		}
		if bestMedian < 0 || median < bestMedian {
			best, bestMedian = c, median
		}
	}
	return best, bestMedian
}

// cheapest returns the candidate with the lowest input price among the priced
// ones, or the first candidate (and false) when none is priced.
func cheapest(candidates []pluginapi.RouteCandidate) (pluginapi.RouteCandidate, bool) {
	best, priced := candidates[0], false
	for _, c := range candidates {
		if c.InputPerMtok == nil {
			continue
		}
		if !priced || *c.InputPerMtok < *best.InputPerMtok {
			best, priced = c, true
		}
	}
	return best, priced
}

// window is a fixed-size ring of the most recent outcomes of one target.
type window struct {
	success   [WindowSize]bool
	latencies [WindowSize]time.Duration
	next, n   int
}

func (w *window) add(ok bool, latency time.Duration) {
	w.success[w.next] = ok
	w.latencies[w.next] = latency
	w.next = (w.next + 1) % WindowSize
	if w.n < WindowSize {
		w.n++
	}
}

func (w *window) errorRate() float64 {
	if w.n == 0 {
		return 0
	}
	failures := 0
	for i := range w.n {
		if !w.success[i] {
			failures++
		}
	}
	return float64(failures) / float64(w.n)
}

func (w *window) medianLatency() time.Duration {
	if w.n == 0 {
		return 0
	}
	sorted := append([]time.Duration(nil), w.latencies[:w.n]...)
	slices.Sort(sorted)
	return sorted[w.n/2]
}
