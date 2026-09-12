package virtualmodels

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/plugins"
	"github.com/enterpilot/gomodel/pluginapi"
)

// scriptedStrategy answers Select with a fixed target (or fails in a scripted
// way) and records the requests it saw.
type scriptedStrategy struct {
	mu     sync.Mutex
	answer string
	reason string
	err    error
	panics bool
	block  time.Duration
	// ignoreCtx makes a blocking Select sleep out its block regardless of
	// cancellation, like a plugin that does not honour its context.
	ignoreCtx bool
	requests  []pluginapi.RouteRequest
}

func (s *scriptedStrategy) Select(ctx context.Context, req pluginapi.RouteRequest) (pluginapi.RouteChoice, error) {
	s.mu.Lock()
	s.requests = append(s.requests, req)
	s.mu.Unlock()
	if s.panics {
		panic("scripted panic")
	}
	if s.block > 0 && s.ignoreCtx {
		time.Sleep(s.block)
	} else if s.block > 0 {
		select {
		case <-time.After(s.block):
		case <-ctx.Done():
			return pluginapi.RouteChoice{}, ctx.Err()
		}
	}
	if s.err != nil {
		return pluginapi.RouteChoice{}, s.err
	}
	return pluginapi.RouteChoice{Qualified: s.answer, Reason: s.reason}, nil
}

func (s *scriptedStrategy) OnAttemptEnd(pluginapi.RouteOutcome) {}

func (s *scriptedStrategy) seen() []pluginapi.RouteRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]pluginapi.RouteRequest(nil), s.requests...)
}

// fakeResolver serves one strategy under the name "lat" and validates a
// single route field "prefer".
type fakeResolver struct {
	strategy    pluginapi.RouteStrategy
	strategyErr error
	validated   []map[string]any
	// inst, when set, is handed out held like the real resolver does.
	inst *plugins.Instance
}

func (r *fakeResolver) Strategy(name string) (pluginapi.RouteStrategy, *plugins.Instance, error) {
	if name != "lat" {
		return nil, nil, errors.New("not loaded")
	}
	if r.strategyErr != nil {
		return nil, nil, r.strategyErr
	}
	r.inst.Acquire()
	return r.strategy, r.inst, nil
}

func (r *fakeResolver) Release(inst *plugins.Instance) { inst.Release() }

func (r *fakeResolver) ValidateRouteConfig(name string, cfg map[string]any) (json.RawMessage, error) {
	if name != "lat" {
		return nil, errors.New("routing-strategy plugin \"" + name + "\" is not loaded")
	}
	r.validated = append(r.validated, cfg)
	out := map[string]any{"prefer": "cheapest"}
	for key, value := range cfg {
		if key != "prefer" {
			return nil, errors.New("strategy_config: unknown config key \"" + key + "\"")
		}
		out[key] = value
	}
	return json.Marshal(out)
}

func pluginVM(config map[string]any) VirtualModel {
	return VirtualModel{
		Source:         "smart",
		Strategy:       StrategyPlugin,
		StrategyPlugin: "lat",
		StrategyConfig: config,
		Targets: []Target{
			{Provider: "openai", Model: "gpt-4o", Weight: 5},
			{Provider: "anthropic", Model: "claude"},
			{Provider: "groq", Model: "llama"},
		},
		Enabled: true,
	}
}

func newPluginService(t *testing.T, resolver RouteResolver, config map[string]any) *Service {
	t.Helper()
	svc := newBalancingService(t)
	svc.SetRouteResolver(resolver)
	err := svc.Upsert(context.Background(), pluginVM(config))
	require.NoError(t, err)

	return svc
}

func TestBalancer_PluginDelegatesToStrategy(t *testing.T) {
	t.Parallel()
	strategy := &scriptedStrategy{answer: "groq/llama", reason: "cheapest"}
	svc := newPluginService(t, &fakeResolver{strategy: strategy}, map[string]any{"prefer": "fastest"})

	ctx := core.WithRequestID(context.Background(), "req-1")
	for i := range 3 {
		sel, _, err := svc.ResolveModelForUserPath(ctx, core.NewRequestedModelSelector("smart", ""))
		require.NoError(t, err)
		got := sel.QualifiedModel()
		require.Equal(t, "groq/llama", got, "resolution[%d]: want the strategy's choice groq/llama", i)
	}
	requests := strategy.seen()
	require.Len(t, requests, 3)

	req := requests[0]
	require.Equal(t, "smart", req.Source)
	require.Equal(t, "req-1", req.Meta.RequestID)
	require.Nil(t, req.Prompt, "RouteRequest = %+v, want source smart, request id req-1, nil prompt", req)
	require.Equal(t, `{"prefer":"fastest"}`, string(req.Config), "want canonical strategy_config")

	want := []pluginapi.RouteCandidate{
		{Provider: "openai", Model: "gpt-4o", Qualified: "openai/gpt-4o", Weight: 5, InputPerMtok: new(2.5), OutputPerMtok: new(10.0)},
		{Provider: "anthropic", Model: "claude", Qualified: "anthropic/claude", InputPerMtok: new(3.0), OutputPerMtok: new(15.0)},
		{Provider: "groq", Model: "llama", Qualified: "groq/llama", InputPerMtok: new(0.5), OutputPerMtok: new(0.8)},
	}
	require.Equal(t, want, req.Candidates)

	// Pricing reaches the plugin as copies.
	*req.Candidates[2].InputPerMtok = 999
	model, _ := svc.catalog.LookupModel("groq/llama")
	require.Equal(t, 0.5, *model.Metadata.Pricing.InputPerMtok)
}

func TestBalancer_PluginFallsBackToRoundRobin(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		resolver RouteResolver
	}{
		{name: "no resolver installed", resolver: nil},
		{name: "strategy unavailable", resolver: &fakeResolver{strategyErr: errors.New("init failed")}},
		{name: "strategy errors", resolver: &fakeResolver{strategy: &scriptedStrategy{err: errors.New("no idea")}}},
		{name: "strategy panics", resolver: &fakeResolver{strategy: &scriptedStrategy{panics: true}}},
		{name: "strategy times out", resolver: &fakeResolver{strategy: &scriptedStrategy{answer: "groq/llama", block: 2 * pluginSelectTimeout}}},
		{name: "strategy answers outside pool", resolver: &fakeResolver{strategy: &scriptedStrategy{answer: "nonexistent/model"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			svc := newBalancingService(t)
			// Upsert validates through the resolver, so install a working one
			// for the write and swap in the scripted one for resolution.
			svc.SetRouteResolver(&fakeResolver{strategy: &scriptedStrategy{}})
			err := svc.Upsert(context.Background(), pluginVM(nil))
			require.NoError(t, err)

			svc.SetRouteResolver(tc.resolver)

			// Weight 5 on the first target: the fallback is weighted round robin.
			got := resolvedModels(t, svc, "smart", 7)
			want := []string{"openai/gpt-4o", "openai/gpt-4o", "openai/gpt-4o", "openai/gpt-4o", "openai/gpt-4o", "anthropic/claude", "groq/llama"}
			require.Equal(t, want, got)
		})
	}
}

func TestBalancer_PluginTimeoutDoesNotWaitForStrategy(t *testing.T) {
	t.Parallel()
	strategy := &scriptedStrategy{answer: "groq/llama", block: 5 * time.Second}
	svc := newPluginService(t, &fakeResolver{strategy: strategy}, nil)
	start := time.Now()
	resolvedModels(t, svc, "smart", 1)
	elapsed := time.Since(start)
	require.LessOrEqual(t, elapsed, 5*pluginSelectTimeout, "resolution took %s, want about the %s select timeout", elapsed, pluginSelectTimeout)
}

func TestBalancer_PluginSingleViableTargetBypassesStrategy(t *testing.T) {
	t.Parallel()
	strategy := &scriptedStrategy{answer: "groq/llama"}
	svc := newPluginService(t, &fakeResolver{strategy: strategy}, nil)
	svc.SetTargetCapacity(func(qualified string) bool { return qualified == "anthropic/claude" })
	for i, got := range resolvedModels(t, svc, "smart", 2) {
		require.Equal(t, "anthropic/claude", got, "resolution[%d]: want the only target with capacity", i)
	}
	seen := strategy.seen()
	require.Empty(t, seen)
}

// Like adaptive, the plugin is consulted on every request of a session and
// receives the pin, and its answer moves the session.
func TestSticky_PluginReceivesPinAndMovesSession(t *testing.T) {
	t.Parallel()
	strategy := &scriptedStrategy{answer: "anthropic/claude"}
	svc := newPluginService(t, &fakeResolver{strategy: strategy}, nil)

	for range 3 {
		got := resolveSession(t, svc, "smart", "sess-a")
		require.Equal(t, "anthropic/claude", got)
	}
	requests := strategy.seen()
	require.Len(t, requests, 3)
	require.Empty(t, requests[0].SessionTarget)
	require.Equal(t, "sess-a", requests[0].SessionID, "first request = %+v, want empty pin for a new session", requests[0])

	for i, req := range requests[1:] {
		require.Equal(t, "anthropic/claude", req.SessionTarget, "request %d SessionTarget: want the pin anthropic/claude", i+1)
	}

	strategy.mu.Lock()
	strategy.answer = "groq/llama"
	strategy.mu.Unlock()
	got := resolveSession(t, svc, "smart", "sess-a")
	require.Equal(t, "groq/llama", got)
	got = resolveSession(t, svc, "smart", "sess-a")
	require.Equal(t, "groq/llama", got)
}

// When the plugin declines (errors) under session affinity, core's own pin
// governs, exactly as for adaptive.
func TestSticky_PluginDeclineKeepsPin(t *testing.T) {
	t.Parallel()
	strategy := &scriptedStrategy{answer: "groq/llama"}
	svc := newPluginService(t, &fakeResolver{strategy: strategy}, nil)
	got := resolveSession(t, svc, "smart", "sess-a")
	require.Equal(t, "groq/llama", got)

	strategy.mu.Lock()
	strategy.err = errors.New("declining")
	strategy.mu.Unlock()
	for i := range 3 {
		got := resolveSession(t, svc, "smart", "sess-a")
		require.Equal(t, "groq/llama", got, "resolution %d: want the pin groq/llama while the plugin declines", i)
	}
}

func TestValidation_PluginStrategy(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		vm      VirtualModel
		wantErr string
	}{
		{name: "plugin name required", vm: VirtualModel{Source: "smart", Strategy: StrategyPlugin, Targets: pluginVM(nil).Targets}, wantErr: "strategy_plugin is required"},
		{name: "unknown plugin", vm: func() VirtualModel { vm := pluginVM(nil); vm.StrategyPlugin = "ghost"; return vm }(), wantErr: `strategy plugin "ghost": routing-strategy plugin "ghost" is not loaded`},
		{name: "invalid config key", vm: pluginVM(map[string]any{"nope": 1}), wantErr: `strategy plugin "lat": strategy_config: unknown config key "nope"`},
		{name: "valid", vm: pluginVM(map[string]any{"prefer": "fastest"})},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			svc := newBalancingService(t)
			svc.SetRouteResolver(&fakeResolver{strategy: &scriptedStrategy{}})
			err := svc.Upsert(context.Background(), tc.vm)
			if tc.wantErr == "" {
				require.NoError(t, err)

				stored, _ := svc.Get("smart")
				require.Equal(t, "lat", stored.StrategyPlugin)
				require.Equal(t, tc.vm.StrategyConfig, stored.StrategyConfig, "stored = %+v, want plugin fields kept", stored)
				return
			}
			require.ErrorContains(t, err, tc.wantErr)
			require.True(t, IsValidationError(err))
		})
	}
}

func TestValidation_PluginStrategyWithoutResolverIsRejected(t *testing.T) {
	t.Parallel()
	svc := newBalancingService(t)
	err := svc.Upsert(context.Background(), pluginVM(nil))
	require.ErrorContains(t, err, "not available")
}

func TestValidation_NonPluginStrategyDropsPluginFields(t *testing.T) {
	t.Parallel()
	svc := newBalancingService(t)
	vm := pluginVM(map[string]any{"prefer": "fastest"})
	vm.Strategy = StrategyCost
	err := svc.Upsert(context.Background(), vm)
	require.NoError(t, err)

	stored, _ := svc.Get("smart")
	require.Empty(t, stored.StrategyPlugin)
	require.Nil(t, stored.StrategyConfig, "stored = %+v, want plugin fields cleared under strategy cost", stored)
}

func TestValidateManagedConfig_PluginStrategy(t *testing.T) {
	t.Parallel()
	svc := newBalancingService(t)
	svc.SetRouteResolver(&fakeResolver{strategy: &scriptedStrategy{}})
	svc.SetConfigModels([]VirtualModel{pluginVM(map[string]any{"nope": true})})
	err := svc.Refresh(context.Background())
	require.NoError(t, err)

	err = svc.ValidateManagedConfig(nil)
	require.ErrorContains(t, err, `load virtual model "smart": strategy plugin "lat": strategy_config: unknown config key "nope"`)
}

func TestConfigModels_PluginStrategy(t *testing.T) {
	t.Parallel()
	vm := configModel(configEntryWithPlugin())
	require.Equal(t, StrategyPlugin, vm.Strategy)
	require.Equal(t, "cheapest_healthy", vm.StrategyPlugin, "configModel() = %+v", vm)
	require.Equal(t, map[string]any{"prefer": "fastest", "max_error_rate": 0.1}, vm.StrategyConfig)
}

func configEntryWithPlugin() config.VirtualModelConfig {
	return config.VirtualModelConfig{
		Source:         "smart",
		Strategy:       "plugin",
		StrategyPlugin: "cheapest_healthy",
		StrategyConfig: map[string]any{"prefer": "fastest", "max_error_rate": 0.1},
		Targets: []config.VirtualModelTargetConfig{
			{Model: "openai/gpt-4o"},
			{Model: "groq/llama"},
		},
	}
}

func TestStore_RoundTripPluginStrategy(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, store Store) {
		ctx := context.Background()
		vm := pluginVM(map[string]any{"prefer": "fastest", "max_error_rate": 0.1, "tags": []any{"a", "b"}})
		err := store.Upsert(ctx, vm)
		require.NoError(t, err)

		got, err := store.Get(ctx, "smart")
		require.NoError(t, err)
		require.Equal(t, StrategyPlugin, got.Strategy)
		require.Equal(t, "lat", got.StrategyPlugin, "Get() = %+v, want plugin strategy fields", got)
		require.Equal(t, vm.StrategyConfig, got.StrategyConfig)

		plain := VirtualModel{Source: "plain", Strategy: StrategyCost, Targets: vm.Targets, Enabled: true}
		err = store.Upsert(ctx, plain)
		require.NoError(t, err)

		gotPlain, err := store.Get(ctx, "plain")
		require.NoError(t, err)
		require.Empty(t, gotPlain.StrategyPlugin)
		require.Nil(t, gotPlain.StrategyConfig, "Get(plain) = %+v, want empty plugin fields", gotPlain)
	})
}

func TestClone_StrategyConfigIsDeepCopied(t *testing.T) {
	t.Parallel()
	vm := pluginVM(map[string]any{"nested": map[string]any{"k": "v"}, "list": []any{"a"}})
	cloned := vm.clone()
	cloned.StrategyConfig["nested"].(map[string]any)["k"] = "changed"
	cloned.StrategyConfig["list"].([]any)[0] = "changed"
	require.Equal(t, "v", vm.StrategyConfig["nested"].(map[string]any)["k"])
	require.Equal(t, "a", vm.StrategyConfig["list"].([]any)[0], "clone shared nested config with the original: %v", vm.StrategyConfig)
}

// The hold on the instance outlives the select timeout: it is released by
// the Select goroutine when Select returns, not when the request gives up.
func TestBalancer_PluginTimeoutKeepsInstanceHeldUntilSelectReturns(t *testing.T) {
	t.Parallel()
	inst := &plugins.Instance{Name: "lat"}
	strategy := &scriptedStrategy{answer: "groq/llama", block: 3 * pluginSelectTimeout, ignoreCtx: true}
	svc := newPluginService(t, &fakeResolver{strategy: strategy, inst: inst}, nil)
	resolvedModels(t, svc, "smart", 1)
	require.True(t, inst.Held())

	deadline := time.Now().Add(2 * time.Second)
	for inst.Held() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	require.False(t, inst.Held())
}
