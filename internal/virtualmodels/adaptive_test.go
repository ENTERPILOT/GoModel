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

// steeringSelector answers with whatever target is currently healthy,
// standing in for a selector that tracks upstream health: it declines a
// target once it is marked failing, exactly as the adaptive selector's
// cooldowns do.
type steeringSelector struct {
	mu       sync.Mutex
	failing  map[string]bool
	order    []string
	requests []ext.RouteRequest
}

func newSteeringSelector(order ...string) *steeringSelector {
	return &steeringSelector{failing: map[string]bool{}, order: order}
}

func (s *steeringSelector) Name() string { return "steering" }

func (s *steeringSelector) Select(req ext.RouteRequest) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = append(s.requests, req)
	// Keep the session where it is unless that target has started failing —
	// the behaviour core must not pre-empt in either direction.
	if req.SessionTarget != "" && !s.failing[req.SessionTarget] {
		return req.SessionTarget, true
	}
	for _, qualified := range s.order {
		if !s.failing[qualified] {
			return qualified, true
		}
	}
	return "", false
}

func (s *steeringSelector) OnAttemptStart(ext.RouteTarget) {}
func (s *steeringSelector) OnAttemptEnd(ext.RouteOutcome)  {}

func (s *steeringSelector) fail(qualified string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failing[qualified] = true
}

func (s *steeringSelector) seen() []ext.RouteRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]ext.RouteRequest(nil), s.requests...)
}

// A session-affine redirect must still consult the selector on every request:
// core's pin only tests candidate membership, which a target that is timing
// out or serving 429s keeps passing, so skipping the selector while a pin
// exists left an agent session riding a failing target for the pin's whole
// lifetime.
func TestSticky_AdaptiveConsultsSelectorOnEveryRequest(t *testing.T) {
	t.Parallel()
	svc := newBalancingService(t)
	selector := &scriptedSelector{answer: "groq/llama"}
	svc.SetRouteSelector(selector)
	upsertAdaptive(t, svc)

	for i := range 5 {
		if got := resolveSession(t, svc, "smart", "sess-a"); got != "groq/llama" {
			t.Fatalf("resolution %d = %q, want selector's choice groq/llama", i, got)
		}
	}
	if got := len(selector.seen()); got != 5 {
		t.Fatalf("selector saw %d requests, want one per request (5)", got)
	}
}

// The pin reaches the selector as SessionTarget, so it can weigh cache
// warmth against health rather than guessing at the session's history.
func TestSticky_AdaptiveSelectorReceivesThePin(t *testing.T) {
	t.Parallel()
	svc := newBalancingService(t)
	selector := &scriptedSelector{answer: "anthropic/claude"}
	svc.SetRouteSelector(selector)
	upsertAdaptive(t, svc)

	for range 3 {
		resolveSession(t, svc, "smart", "sess-a")
	}
	requests := selector.seen()
	if len(requests) != 3 {
		t.Fatalf("selector saw %d requests, want 3", len(requests))
	}
	if requests[0].SessionTarget != "" {
		t.Fatalf("first request SessionTarget = %q, want empty for a new session", requests[0].SessionTarget)
	}
	for i, req := range requests[1:] {
		if req.SessionTarget != "anthropic/claude" {
			t.Fatalf("request %d SessionTarget = %q, want the recorded pin anthropic/claude", i+1, req.SessionTarget)
		}
		if req.SessionID != "sess-a" {
			t.Fatalf("request %d SessionID = %q, want sess-a", i+1, req.SessionID)
		}
	}
}

// The regression that matters: once the selector takes the pinned target out
// of service the session moves, instead of being held there by core's pin.
func TestSticky_AdaptiveSelectorMovesSessionOffFailingTarget(t *testing.T) {
	t.Parallel()
	svc := newBalancingService(t)
	selector := newSteeringSelector("openai/gpt-4o", "anthropic/claude", "groq/llama")
	svc.SetRouteSelector(selector)
	upsertAdaptive(t, svc)

	first := resolveSession(t, svc, "smart", "sess-a")
	if first != "openai/gpt-4o" {
		t.Fatalf("first resolution = %q, want openai/gpt-4o", first)
	}
	if got := resolveSession(t, svc, "smart", "sess-a"); got != first {
		t.Fatalf("healthy session moved to %q, want to stay on %q", got, first)
	}

	selector.fail(first)
	for i := range 3 {
		got := resolveSession(t, svc, "smart", "sess-a")
		if got == first {
			t.Fatalf("resolution %d stayed on failing target %q", i, got)
		}
		if got != "anthropic/claude" {
			t.Fatalf("resolution %d = %q, want the selector's replacement anthropic/claude", i, got)
		}
	}

	// The session re-pinned to the replacement, so the selector sees the new
	// target as the pin rather than the one it took out of service.
	requests := selector.seen()
	if last := requests[len(requests)-1].SessionTarget; last != "anthropic/claude" {
		t.Fatalf("final SessionTarget = %q, want the re-pinned anthropic/claude", last)
	}
}

// A selector that declines must not cost a session its affinity: core's own
// pin still governs on the round-robin fallback path.
func TestSticky_AdaptiveDeclineKeepsCoreAffinity(t *testing.T) {
	t.Parallel()
	svc := newBalancingService(t)
	selector := &scriptedSelector{decline: true}
	svc.SetRouteSelector(selector)
	upsertAdaptive(t, svc)

	first := resolveSession(t, svc, "smart", "sess-a")
	for i := range 5 {
		if got := resolveSession(t, svc, "smart", "sess-a"); got != first {
			t.Fatalf("resolution %d = %q, want pinned %q despite the decline", i, got, first)
		}
	}
}

// With no selector installed the adaptive strategy is plain weighted round
// robin, and session affinity behaves exactly as it does for the other
// strategies.
func TestSticky_AdaptiveWithoutSelectorPinsLikeRoundRobin(t *testing.T) {
	t.Parallel()
	svc := newBalancingService(t)
	upsertAdaptive(t, svc)

	first := resolveSession(t, svc, "smart", "sess-a")
	for i := range 5 {
		if got := resolveSession(t, svc, "smart", "sess-a"); got != first {
			t.Fatalf("resolution %d = %q, want pinned %q", i, got, first)
		}
	}
}

// Affinity turned off means no pin reaches the selector at all.
func TestSticky_AdaptiveAffinityDisabledSendsNoPin(t *testing.T) {
	t.Parallel()
	svc := newBalancingService(t)
	selector := &scriptedSelector{answer: "groq/llama"}
	svc.SetRouteSelector(selector)
	off := false
	upsertBalancedVM(t, svc, StrategyAdaptive, &off)

	for range 3 {
		resolveSession(t, svc, "smart", "sess-a")
	}
	for i, req := range selector.seen() {
		if req.SessionTarget != "" {
			t.Fatalf("request %d SessionTarget = %q, want empty with affinity disabled", i, req.SessionTarget)
		}
	}
}
