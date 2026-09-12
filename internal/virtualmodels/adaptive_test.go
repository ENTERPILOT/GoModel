package virtualmodels

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/enterpilot/gomodel/ext"
	"github.com/stretchr/testify/require"
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
	err := svc.Upsert(context.Background(), VirtualModel{
		Source:   "smart",
		Strategy: StrategyAdaptive,
		Targets: []Target{
			{Provider: "openai", Model: "gpt-4o"},
			{Provider: "anthropic", Model: "claude"},
			{Provider: "groq", Model: "llama"},
		},
		Enabled: true,
	})
	require.NoError(t, err)
}

func TestBalancer_AdaptiveDelegatesToSelector(t *testing.T) {
	t.Parallel()
	svc := newBalancingService(t)
	selector := &scriptedSelector{answer: "groq/llama"}
	svc.SetRouteSelector(selector)
	upsertAdaptive(t, svc)

	for i, got := range resolvedModels(t, svc, "smart", 4) {
		require.Equal(t, "groq/llama", got, "resolution[%d]: want selector's choice groq/llama", i)
	}

	requests := selector.seen()
	require.Len(t, requests, 4)

	req := requests[0]
	require.Equal(t, "smart", req.Source)

	want := []ext.RouteCandidate{
		{Provider: "openai", Model: "gpt-4o", Qualified: "openai/gpt-4o", InputPerMtok: new(2.5), OutputPerMtok: new(10.0)},
		{Provider: "anthropic", Model: "claude", Qualified: "anthropic/claude", InputPerMtok: new(3.0), OutputPerMtok: new(15.0)},
		{Provider: "groq", Model: "llama", Qualified: "groq/llama", InputPerMtok: new(0.5), OutputPerMtok: new(0.8)},
	}
	require.Equal(t, want, req.Candidates)
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
		require.True(t, ok)
		require.NotNil(t, model.Metadata.Pricing.InputPerMtok)
		require.NotNil(t, model.Metadata.Pricing.OutputPerMtok, "catalog lost the priced model %s", qualified)

		require.Equal(t, prices[0], *model.Metadata.Pricing.InputPerMtok, "catalog input price for %s changed after selector mutation (defensive copies)", qualified)
		require.Equal(t, prices[1], *model.Metadata.Pricing.OutputPerMtok, "catalog output price for %s changed after selector mutation (defensive copies)", qualified)
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
			require.Equal(t, want, got, "fallback must follow round-robin order")
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
		require.Equal(t, "anthropic/claude", got, "resolution[%d]: want the only target with capacity (anthropic/claude)", i)
	}
	seen := selector.seen()
	require.Empty(t, seen)
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
		got := resolveSession(t, svc, "smart", "sess-a")
		require.Equal(t, "groq/llama", got, "resolution %d: want selector's choice groq/llama", i)
	}
	got := len(selector.seen())
	require.Equal(t, 5, got)
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
	require.Len(t, requests, 3)
	require.Empty(t, requests[0].SessionTarget)

	for i, req := range requests[1:] {
		require.Equal(t, "anthropic/claude", req.SessionTarget, "request %d SessionTarget: want the recorded pin anthropic/claude", i+1)
		require.Equal(t, "sess-a", req.SessionID, "request %d SessionID: want sess-a", i+1)
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
	require.Equal(t, "openai/gpt-4o", first)
	got := resolveSession(t, svc, "smart", "sess-a")
	require.Equal(t, first, got)

	selector.fail(first)
	for i := range 3 {
		got := resolveSession(t, svc, "smart", "sess-a")
		require.NotEqual(t, first, got, "resolution %d stayed on failing target %q", i, got)
		require.Equal(t, "anthropic/claude", got, "resolution %d: want the selector's replacement anthropic/claude", i)
	}

	// The session re-pinned to the replacement, so the selector sees the new
	// target as the pin rather than the one it took out of service.
	requests := selector.seen()
	last := requests[len(requests)-1].SessionTarget
	require.Equal(t, "anthropic/claude", last)
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
		got := resolveSession(t, svc, "smart", "sess-a")
		require.Equal(t, first, got, "resolution %d: want pinned %q despite the decline", i, first)
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
		got := resolveSession(t, svc, "smart", "sess-a")
		require.Equal(t, first, got, "resolution %d: want pinned %q", i, first)
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
		require.Empty(t, req.SessionTarget, "request %d SessionTarget: want empty with affinity disabled", i)
	}
}

// racingSelector holds every concurrent call until they have all arrived, so
// each observes the same pin, then hands each a different valid answer.
type racingSelector struct {
	mu      sync.Mutex
	calls   int
	pins    []string
	answers []string
	arrive  chan struct{}
	release chan struct{}
}

func newRacingSelector(answers ...string) *racingSelector {
	return &racingSelector{
		answers: answers,
		arrive:  make(chan struct{}, len(answers)),
		release: make(chan struct{}),
	}
}

func (s *racingSelector) Name() string { return "racing" }

func (s *racingSelector) Select(req ext.RouteRequest) (string, bool) {
	s.mu.Lock()
	i := s.calls
	s.calls++
	s.pins = append(s.pins, req.SessionTarget)
	s.mu.Unlock()

	if i < len(s.answers) {
		s.arrive <- struct{}{}
		<-s.release
		return s.answers[i], true
	}
	// Later, unraced calls keep the session where it was committed.
	if req.SessionTarget != "" {
		return req.SessionTarget, true
	}
	return s.answers[len(s.answers)-1], true
}

func (s *racingSelector) OnAttemptStart(ext.RouteTarget) {}
func (s *racingSelector) OnAttemptEnd(ext.RouteOutcome)  {}

func (s *racingSelector) observedPins() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.pins...)
}

// Concurrent requests of one session must agree on a target. Two overlapping
// requests both see no pin, get different valid answers, and would each
// commit their own — splitting one session across two providers and leaving
// it pinned to whichever wrote last.
func TestSticky_AdaptiveConcurrentFirstRequestsAgree(t *testing.T) {
	t.Parallel()
	svc := newBalancingService(t)
	selector := newRacingSelector("openai/gpt-4o", "anthropic/claude")
	svc.SetRouteSelector(selector)
	upsertAdaptive(t, svc)

	results := make([]string, 2)
	var wg sync.WaitGroup
	for i := range results {
		wg.Go(func() {
			results[i] = resolveSession(t, svc, "smart", "sess-race")
		})
	}
	// Let both requests observe the same (absent) pin before either commits.
	<-selector.arrive
	<-selector.arrive
	close(selector.release)
	wg.Wait()

	require.Equal(t, results[1], results[0])
	// And the committed pin is the one both requests actually used.
	got := resolveSession(t, svc, "smart", "sess-race")
	require.Equal(t, results[0], got)
}

// The same race, but against an existing pin the selector deliberately
// replaces: both overlapping requests must still land on one target.
func TestSticky_AdaptiveConcurrentRepinsAgree(t *testing.T) {
	t.Parallel()
	svc := newBalancingService(t)
	steering := newSteeringSelector("openai/gpt-4o", "anthropic/claude", "groq/llama")
	svc.SetRouteSelector(steering)
	upsertAdaptive(t, svc)
	got := resolveSession(t, svc, "smart", "sess-race")
	require.Equal(t, "openai/gpt-4o", got)

	racing := newRacingSelector("anthropic/claude", "groq/llama")
	svc.SetRouteSelector(racing)

	results := make([]string, 2)
	var wg sync.WaitGroup
	for i := range results {
		wg.Go(func() {
			results[i] = resolveSession(t, svc, "smart", "sess-race")
		})
	}
	<-racing.arrive
	<-racing.arrive
	close(racing.release)
	wg.Wait()

	for i, pin := range racing.observedPins() {
		require.Equal(t, "openai/gpt-4o", pin, "observed pin %d", i)
	}
	require.Equal(t, results[1], results[0])
}

// counterFor reads the round-robin position for a source, or -1 when the
// source has never advanced it.
func counterFor(svc *Service, source string) int64 {
	value, ok := svc.balancer.counters.Load(source)
	if !ok {
		return -1
	}
	return int64(value.(*atomic.Uint64).Load())
}

// A pinned session whose selector declines keeps its pin — but it must not
// consume a rotation slot on the way, or an unusable selector silently
// shifts which target the next new session receives.
func TestSticky_AdaptiveDeclineDoesNotAdvanceRoundRobin(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		selector ext.RouteSelector
	}{
		{"declines", &scriptedSelector{decline: true}},
		{"answers outside the pool", &scriptedSelector{answer: "nowhere/model"}},
		{"panics", &scriptedSelector{panicking: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			svc := newBalancingService(t)
			upsertAdaptive(t, svc)
			// Pin the session while no selector is installed, so the pin is
			// core's own and the fallback path is what gets exercised.
			pinned := resolveSession(t, svc, "smart", "sess-a")
			svc.SetRouteSelector(tc.selector)

			before := counterFor(svc, "smart")
			for i := range 4 {
				got := resolveSession(t, svc, "smart", "sess-a")
				require.Equal(t, pinned, got, "resolution %d: want the pin %q kept", i, pinned)
			}
			after := counterFor(svc, "smart")
			require.Equal(t, before, after)
		})
	}
}
