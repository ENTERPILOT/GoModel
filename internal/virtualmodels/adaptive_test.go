package virtualmodels

import (
	"context"
	"sync"
	"testing"

	"github.com/enterpilot/gomodel/ext"
)

// scriptedSelector answers Select with a fixed qualified model (or declines)
// and records the requests it saw.
type scriptedSelector struct {
	mu        sync.Mutex
	answer    string
	decline   bool
	panicking bool
	requests  []ext.RouteRequest
}

func (s *scriptedSelector) Name() string { return "scripted" }

func (s *scriptedSelector) Select(req ext.RouteRequest) (string, bool) {
	s.mu.Lock()
	s.requests = append(s.requests, req)
	s.mu.Unlock()
	if s.panicking {
		panic("scripted panic")
	}
	if s.decline {
		return "", false
	}
	return s.answer, true
}

func (s *scriptedSelector) OnAttemptStart(ext.RouteTarget) {}
func (s *scriptedSelector) OnAttemptEnd(ext.RouteOutcome)  {}

func (s *scriptedSelector) seen() []ext.RouteRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requests
}

func upsertAdaptive(t *testing.T, svc *Service) {
	t.Helper()
	if err := svc.Upsert(context.Background(), VirtualModel{
		Source:   "smart",
		Strategy: StrategyAdaptive,
		Targets: []Target{
			{Provider: "openai", Model: "gpt-4o"},
			{Provider: "anthropic", Model: "claude"},
			{Provider: "groq", Model: "llama"},
		},
		Enabled: true,
	}); err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}
}

func TestBalancer_AdaptiveDelegatesToSelector(t *testing.T) {
	t.Parallel()
	svc := newBalancingService(t)
	selector := &scriptedSelector{answer: "groq/llama"}
	svc.SetRouteSelector(selector)
	upsertAdaptive(t, svc)

	for i, got := range resolvedModels(t, svc, "smart", 4) {
		if got != "groq/llama" {
			t.Fatalf("resolution[%d] = %q, want selector's choice groq/llama", i, got)
		}
	}

	requests := selector.seen()
	if len(requests) != 4 {
		t.Fatalf("selector saw %d requests, want 4", len(requests))
	}
	req := requests[0]
	if req.Source != "smart" || len(req.Candidates) != 3 {
		t.Fatalf("RouteRequest = %+v, want source smart with 3 candidates", req)
	}
	first := req.Candidates[0]
	if first.Qualified != "openai/gpt-4o" || first.Provider != "openai" || first.Model != "gpt-4o" {
		t.Fatalf("candidate[0] = %+v, want openai/gpt-4o split into provider and model", first)
	}
	if first.InputPerMtok == nil || *first.InputPerMtok != 2.5 {
		t.Fatalf("candidate[0] pricing = %+v, want registry input price 2.5", first.InputPerMtok)
	}
}

func TestBalancer_AdaptiveFallsBackToRoundRobin(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		selector *scriptedSelector
	}{
		{name: "no selector installed", selector: nil},
		{name: "selector declines", selector: &scriptedSelector{decline: true}},
		{name: "selector answers outside pool", selector: &scriptedSelector{answer: "nonexistent/model"}},
		{name: "selector panics", selector: &scriptedSelector{panicking: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			svc := newBalancingService(t)
			if tc.selector != nil {
				svc.SetRouteSelector(tc.selector)
			}
			upsertAdaptive(t, svc)

			got := resolvedModels(t, svc, "smart", 3)
			want := []string{"openai/gpt-4o", "anthropic/claude", "groq/llama"}
			for i := range want {
				if got[i] != want[i] {
					t.Fatalf("fallback[%d] = %q, want round-robin order %q (full: %v)", i, got[i], want[i], got)
				}
			}
		})
	}
}

func TestBalancer_AdaptiveSingleViableTargetBypassesSelector(t *testing.T) {
	t.Parallel()
	svc := newBalancingService(t)
	selector := &scriptedSelector{answer: "groq/llama"}
	svc.SetRouteSelector(selector)
	svc.SetTargetCapacity(func(qualified string) bool { return qualified == "anthropic/claude" })
	upsertAdaptive(t, svc)

	for i, got := range resolvedModels(t, svc, "smart", 2) {
		if got != "anthropic/claude" {
			t.Fatalf("resolution[%d] = %q, want the only target with capacity (anthropic/claude)", i, got)
		}
	}
	if seen := selector.seen(); len(seen) != 0 {
		t.Fatalf("selector saw %d requests, want 0 for a single-target pool", len(seen))
	}
}
