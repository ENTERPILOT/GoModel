package virtualmodels

import (
	"context"
	"fmt"
	"reflect"
	"strings"
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
	panicName bool
	requests  []ext.RouteRequest
}

func (s *scriptedSelector) Name() string {
	if s.panicName {
		panic("scripted Name panic")
	}
	return "scripted"
}

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
	if req.Source != "smart" {
		t.Fatalf("RouteRequest source = %q, want smart", req.Source)
	}
	want := []ext.RouteCandidate{
		{Provider: "openai", Model: "gpt-4o", Qualified: "openai/gpt-4o", InputPerMtok: new(2.5), OutputPerMtok: new(10.0)},
		{Provider: "anthropic", Model: "claude", Qualified: "anthropic/claude", InputPerMtok: new(3.0), OutputPerMtok: new(15.0)},
		{Provider: "groq", Model: "llama", Qualified: "groq/llama", InputPerMtok: new(0.5), OutputPerMtok: new(0.8)},
	}
	if !reflect.DeepEqual(req.Candidates, want) {
		t.Fatalf("candidates = %s, want %s", formatCandidates(req.Candidates), formatCandidates(want))
	}
}

func formatCandidates(candidates []ext.RouteCandidate) string {
	out := make([]string, 0, len(candidates))
	for _, c := range candidates {
		in, priced := "nil", "nil"
		if c.InputPerMtok != nil {
			in = fmt.Sprintf("%v", *c.InputPerMtok)
		}
		if c.OutputPerMtok != nil {
			priced = fmt.Sprintf("%v", *c.OutputPerMtok)
		}
		out = append(out, fmt.Sprintf("{%s w=%v in=%s out=%s}", c.Qualified, c.Weight, in, priced))
	}
	return strings.Join(out, " ")
}

// The catalog's pricing must not be reachable through candidates: a selector
// writing through the pointers it receives must not change what the cost
// strategy later reads.
func TestBalancer_AdaptiveCandidatePricingIsCopied(t *testing.T) {
	t.Parallel()
	svc := newBalancingService(t)
	selector := &scriptedSelector{answer: "groq/llama"}
	svc.SetRouteSelector(selector)
	upsertAdaptive(t, svc)

	resolvedModels(t, svc, "smart", 1)
	for _, candidate := range selector.seen()[0].Candidates {
		if candidate.InputPerMtok != nil {
			*candidate.InputPerMtok = 999
		}
		if candidate.OutputPerMtok != nil {
			*candidate.OutputPerMtok = 999
		}
	}

	want := map[string][2]float64{
		"openai/gpt-4o":    {2.5, 10},
		"anthropic/claude": {3, 15},
		"groq/llama":       {0.5, 0.8},
	}
	for qualified, prices := range want {
		model, ok := svc.catalog.LookupModel(qualified)
		if !ok || model.Metadata.Pricing.InputPerMtok == nil || model.Metadata.Pricing.OutputPerMtok == nil {
			t.Fatalf("catalog lost the priced model %s", qualified)
		}
		if in, out := *model.Metadata.Pricing.InputPerMtok, *model.Metadata.Pricing.OutputPerMtok; in != prices[0] || out != prices[1] {
			t.Fatalf("catalog prices for %s = %v/%v after selector mutation, want %v/%v (defensive copies)",
				qualified, in, out, prices[0], prices[1])
		}
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
		{name: "selector and its Name both panic", selector: &scriptedSelector{panicking: true, panicName: true}},
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
