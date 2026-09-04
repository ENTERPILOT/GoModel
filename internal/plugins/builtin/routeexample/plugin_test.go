package routeexample

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/enterpilot/gomodel/pluginapi"
)

func candidate(qualified string, inputPrice float64) pluginapi.RouteCandidate {
	provider, model, _ := strings.Cut(qualified, "/")
	c := pluginapi.RouteCandidate{Provider: provider, Model: model, Qualified: qualified}
	if inputPrice > 0 {
		c.InputPerMtok = &inputPrice
	}
	return c
}

func target(qualified string) pluginapi.RouteTarget {
	provider, model, _ := strings.Cut(qualified, "/")
	return pluginapi.RouteTarget{Provider: provider, Model: model}
}

// report feeds n outcomes for qualified with the given success flag and latency.
func report(p *Plugin, qualified string, n int, success bool, latency time.Duration) {
	for range n {
		p.OnAttemptEnd(pluginapi.RouteOutcome{Target: target(qualified), Success: success, Latency: latency})
	}
}

func newPlugin(t *testing.T) *Plugin {
	t.Helper()
	p := New()
	if err := p.Init(context.Background(), json.RawMessage(`{}`), nil); err != nil {
		t.Fatalf("Init: %v", err)
	}
	return p.(*Plugin)
}

func TestManifest(t *testing.T) {
	m := New().Manifest()
	if m.Name != Name || len(m.Kinds) != 1 || m.Kinds[0] != pluginapi.KindRoute {
		t.Fatalf("manifest = %+v", m)
	}
	for _, field := range m.ConfigSchema {
		if field.Scope != pluginapi.ScopeRoute {
			t.Fatalf("field %q scope = %q, want route", field.Key, field.Scope)
		}
	}
}

func TestSelect(t *testing.T) {
	cheap := candidate("groq/llama", 0.5)
	mid := candidate("openai/gpt-4o", 2.5)
	pricey := candidate("anthropic/claude", 3)
	unpriced := candidate("local/mistral", 0)

	cases := []struct {
		name       string
		config     string
		candidates []pluginapi.RouteCandidate
		session    string
		prepare    func(p *Plugin)
		want       string
		wantReason string
	}{
		{
			name: "cheapest by default", config: `{}`,
			candidates: []pluginapi.RouteCandidate{mid, cheap, pricey},
			want:       "groq/llama", wantReason: "cheapest input price $0.5/Mtok",
		},
		{
			name: "unhealthy cheapest is skipped", config: `{"max_error_rate":0.2,"prefer":"cheapest"}`,
			candidates: []pluginapi.RouteCandidate{mid, cheap, pricey},
			prepare: func(p *Plugin) {
				report(p, "groq/llama", 5, false, time.Second)
				report(p, "groq/llama", 5, true, time.Second)
			},
			want: "openai/gpt-4o", wantReason: "cheapest input price $2.5/Mtok",
		},
		{
			name: "error rate at the ceiling stays healthy", config: `{"max_error_rate":0.5}`,
			candidates: []pluginapi.RouteCandidate{mid, cheap},
			prepare: func(p *Plugin) {
				report(p, "groq/llama", 5, false, time.Second)
				report(p, "groq/llama", 5, true, time.Second)
			},
			want: "groq/llama",
		},
		{
			name: "all unhealthy fails open to cheapest", config: `{}`,
			candidates: []pluginapi.RouteCandidate{mid, cheap},
			prepare: func(p *Plugin) {
				report(p, "groq/llama", 10, false, time.Second)
				report(p, "openai/gpt-4o", 10, false, time.Second)
			},
			want: "groq/llama", wantReason: "(no healthy candidate)",
		},
		{
			name: "no pricing picks the first candidate", config: `{}`,
			candidates: []pluginapi.RouteCandidate{unpriced, candidate("local/other", 0)},
			want:       "local/mistral", wantReason: "no pricing",
		},
		{
			name: "unpriced candidates lose to priced ones", config: `{}`,
			candidates: []pluginapi.RouteCandidate{unpriced, pricey},
			want:       "anthropic/claude",
		},
		{
			name: "healthy session target is kept", config: `{}`,
			candidates: []pluginapi.RouteCandidate{mid, cheap, pricey}, session: "anthropic/claude",
			want: "anthropic/claude", wantReason: "session target is healthy",
		},
		{
			name: "unhealthy session target is left", config: `{}`,
			candidates: []pluginapi.RouteCandidate{mid, cheap, pricey}, session: "anthropic/claude",
			prepare: func(p *Plugin) { report(p, "anthropic/claude", 10, false, time.Second) },
			want:    "groq/llama",
		},
		{
			name: "fastest prefers the lowest median latency", config: `{"prefer":"fastest"}`,
			candidates: []pluginapi.RouteCandidate{mid, cheap, pricey},
			prepare: func(p *Plugin) {
				report(p, "openai/gpt-4o", 3, true, 300*time.Millisecond)
				report(p, "groq/llama", 3, true, 900*time.Millisecond)
				report(p, "anthropic/claude", 3, true, 600*time.Millisecond)
			},
			want: "openai/gpt-4o", wantReason: "fastest median latency 300ms",
		},
		{
			name: "fastest tries unmeasured targets first", config: `{"prefer":"fastest"}`,
			candidates: []pluginapi.RouteCandidate{mid, cheap},
			prepare:    func(p *Plugin) { report(p, "openai/gpt-4o", 3, true, 100*time.Millisecond) },
			want:       "groq/llama",
		},
		{
			name: "timeouts count as failures", config: `{}`,
			candidates: []pluginapi.RouteCandidate{mid, cheap},
			prepare: func(p *Plugin) {
				for range 10 {
					p.OnAttemptEnd(pluginapi.RouteOutcome{Target: target("groq/llama"), Success: true, Timeout: true})
				}
			},
			want: "openai/gpt-4o",
		},
		{
			name: "window forgets old failures", config: `{}`,
			candidates: []pluginapi.RouteCandidate{mid, cheap},
			prepare: func(p *Plugin) {
				report(p, "groq/llama", 20, false, time.Second)
				report(p, "groq/llama", WindowSize, true, time.Second)
			},
			want: "groq/llama",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := newPlugin(t)
			if tc.prepare != nil {
				tc.prepare(p)
			}
			choice, err := p.Select(context.Background(), pluginapi.RouteRequest{
				Source: "smart", SessionTarget: tc.session, Candidates: tc.candidates, Config: json.RawMessage(tc.config),
			})
			if err != nil {
				t.Fatalf("Select: %v", err)
			}
			if choice.Qualified != tc.want {
				t.Fatalf("Select = %q (%s), want %q", choice.Qualified, choice.Reason, tc.want)
			}
			if tc.wantReason != "" && !strings.Contains(choice.Reason, tc.wantReason) {
				t.Fatalf("reason = %q, want containing %q", choice.Reason, tc.wantReason)
			}
		})
	}
}

func TestSelectRejectsEmptyCandidates(t *testing.T) {
	if _, err := newPlugin(t).Select(context.Background(), pluginapi.RouteRequest{}); err == nil {
		t.Fatal("Select() error = nil, want error for no candidates")
	}
}
