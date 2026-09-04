package virtualmodels

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/plugins"
	"github.com/enterpilot/gomodel/pluginapi"
)

// scriptedStrategy answers Select with a fixed target (or fails in a scripted
// way) and records the requests it saw.
type scriptedStrategy struct {
	mu       sync.Mutex
	answer   string
	reason   string
	err      error
	panics   bool
	block    time.Duration
	requests []pluginapi.RouteRequest
}

func (s *scriptedStrategy) Select(ctx context.Context, req pluginapi.RouteRequest) (pluginapi.RouteChoice, error) {
	s.mu.Lock()
	s.requests = append(s.requests, req)
	s.mu.Unlock()
	if s.panics {
		panic("scripted panic")
	}
	if s.block > 0 {
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
}

func (r *fakeResolver) Strategy(name string) (pluginapi.RouteStrategy, *plugins.Instance, error) {
	if name != "lat" {
		return nil, nil, errors.New("not loaded")
	}
	if r.strategyErr != nil {
		return nil, nil, r.strategyErr
	}
	return r.strategy, nil, nil
}

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
	if err := svc.Upsert(context.Background(), pluginVM(config)); err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}
	return svc
}

func TestBalancer_PluginDelegatesToStrategy(t *testing.T) {
	t.Parallel()
	strategy := &scriptedStrategy{answer: "groq/llama", reason: "cheapest"}
	svc := newPluginService(t, &fakeResolver{strategy: strategy}, map[string]any{"prefer": "fastest"})

	ctx := core.WithRequestID(context.Background(), "req-1")
	for i := range 3 {
		sel, _, err := svc.ResolveModelForUserPath(ctx, core.NewRequestedModelSelector("smart", ""))
		if err != nil {
			t.Fatalf("ResolveModelForUserPath() error = %v", err)
		}
		if got := sel.QualifiedModel(); got != "groq/llama" {
			t.Fatalf("resolution[%d] = %q, want the strategy's choice groq/llama", i, got)
		}
	}
	requests := strategy.seen()
	if len(requests) != 3 {
		t.Fatalf("strategy saw %d requests, want 3", len(requests))
	}
	req := requests[0]
	if req.Source != "smart" || req.Meta.RequestID != "req-1" || req.Prompt != nil {
		t.Fatalf("RouteRequest = %+v, want source smart, request id req-1, nil prompt", req)
	}
	if string(req.Config) != `{"prefer":"fastest"}` {
		t.Fatalf("RouteRequest.Config = %s, want canonical strategy_config", req.Config)
	}
	want := []pluginapi.RouteCandidate{
		{Provider: "openai", Model: "gpt-4o", Qualified: "openai/gpt-4o", Weight: 5, InputPerMtok: new(2.5), OutputPerMtok: new(10.0)},
		{Provider: "anthropic", Model: "claude", Qualified: "anthropic/claude", InputPerMtok: new(3.0), OutputPerMtok: new(15.0)},
		{Provider: "groq", Model: "llama", Qualified: "groq/llama", InputPerMtok: new(0.5), OutputPerMtok: new(0.8)},
	}
	if !reflect.DeepEqual(req.Candidates, want) {
		t.Fatalf("candidates = %+v, want %+v", req.Candidates, want)
	}

	// Pricing reaches the plugin as copies.
	*req.Candidates[2].InputPerMtok = 999
	if model, _ := svc.catalog.LookupModel("groq/llama"); *model.Metadata.Pricing.InputPerMtok != 0.5 {
		t.Fatalf("catalog pricing changed through the plugin's candidate copy")
	}
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
			if err := svc.Upsert(context.Background(), pluginVM(nil)); err != nil {
				t.Fatalf("Upsert() error = %v", err)
			}
			svc.SetRouteResolver(tc.resolver)

			// Weight 5 on the first target: the fallback is weighted round robin.
			got := resolvedModels(t, svc, "smart", 7)
			want := []string{"openai/gpt-4o", "openai/gpt-4o", "openai/gpt-4o", "openai/gpt-4o", "openai/gpt-4o", "anthropic/claude", "groq/llama"}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("fallback order = %v, want weighted round robin %v", got, want)
			}
		})
	}
}

func TestBalancer_PluginTimeoutDoesNotWaitForStrategy(t *testing.T) {
	t.Parallel()
	strategy := &scriptedStrategy{answer: "groq/llama", block: 5 * time.Second}
	svc := newPluginService(t, &fakeResolver{strategy: strategy}, nil)
	start := time.Now()
	resolvedModels(t, svc, "smart", 1)
	if elapsed := time.Since(start); elapsed > 5*pluginSelectTimeout {
		t.Fatalf("resolution took %s, want about the %s select timeout", elapsed, pluginSelectTimeout)
	}
}

func TestBalancer_PluginSingleViableTargetBypassesStrategy(t *testing.T) {
	t.Parallel()
	strategy := &scriptedStrategy{answer: "groq/llama"}
	svc := newPluginService(t, &fakeResolver{strategy: strategy}, nil)
	svc.SetTargetCapacity(func(qualified string) bool { return qualified == "anthropic/claude" })
	for i, got := range resolvedModels(t, svc, "smart", 2) {
		if got != "anthropic/claude" {
			t.Fatalf("resolution[%d] = %q, want the only target with capacity", i, got)
		}
	}
	if seen := strategy.seen(); len(seen) != 0 {
		t.Fatalf("strategy saw %d requests, want 0 for a single-target pool", len(seen))
	}
}

// Like adaptive, the plugin is consulted on every request of a session and
// receives the pin, and its answer moves the session.
func TestSticky_PluginReceivesPinAndMovesSession(t *testing.T) {
	t.Parallel()
	strategy := &scriptedStrategy{answer: "anthropic/claude"}
	svc := newPluginService(t, &fakeResolver{strategy: strategy}, nil)

	for range 3 {
		if got := resolveSession(t, svc, "smart", "sess-a"); got != "anthropic/claude" {
			t.Fatalf("resolution = %q, want anthropic/claude", got)
		}
	}
	requests := strategy.seen()
	if len(requests) != 3 {
		t.Fatalf("strategy saw %d requests, want one per request", len(requests))
	}
	if requests[0].SessionTarget != "" || requests[0].SessionID != "sess-a" {
		t.Fatalf("first request = %+v, want empty pin for a new session", requests[0])
	}
	for i, req := range requests[1:] {
		if req.SessionTarget != "anthropic/claude" {
			t.Fatalf("request %d SessionTarget = %q, want the pin anthropic/claude", i+1, req.SessionTarget)
		}
	}

	strategy.mu.Lock()
	strategy.answer = "groq/llama"
	strategy.mu.Unlock()
	if got := resolveSession(t, svc, "smart", "sess-a"); got != "groq/llama" {
		t.Fatalf("resolution after the plugin moved the session = %q, want groq/llama", got)
	}
	if got := resolveSession(t, svc, "smart", "sess-a"); got != "groq/llama" {
		t.Fatalf("pin after move = %q, want groq/llama", got)
	}
}

// When the plugin declines (errors) under session affinity, core's own pin
// governs, exactly as for adaptive.
func TestSticky_PluginDeclineKeepsPin(t *testing.T) {
	t.Parallel()
	strategy := &scriptedStrategy{answer: "groq/llama"}
	svc := newPluginService(t, &fakeResolver{strategy: strategy}, nil)
	if got := resolveSession(t, svc, "smart", "sess-a"); got != "groq/llama" {
		t.Fatalf("first resolution = %q, want groq/llama", got)
	}
	strategy.mu.Lock()
	strategy.err = errors.New("declining")
	strategy.mu.Unlock()
	for i := range 3 {
		if got := resolveSession(t, svc, "smart", "sess-a"); got != "groq/llama" {
			t.Fatalf("resolution %d = %q, want the pin groq/llama while the plugin declines", i, got)
		}
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
				if err != nil {
					t.Fatalf("Upsert() error = %v", err)
				}
				stored, _ := svc.Get("smart")
				if stored.StrategyPlugin != "lat" || !reflect.DeepEqual(stored.StrategyConfig, tc.vm.StrategyConfig) {
					t.Fatalf("stored = %+v, want plugin fields kept", stored)
				}
				return
			}
			if err == nil || !IsValidationError(err) || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Upsert() error = %v, want validation error containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestValidation_PluginStrategyWithoutResolverIsRejected(t *testing.T) {
	t.Parallel()
	svc := newBalancingService(t)
	err := svc.Upsert(context.Background(), pluginVM(nil))
	if err == nil || !strings.Contains(err.Error(), "not available") {
		t.Fatalf("Upsert() error = %v, want plugins-unavailable validation error", err)
	}
}

func TestValidation_NonPluginStrategyDropsPluginFields(t *testing.T) {
	t.Parallel()
	svc := newBalancingService(t)
	vm := pluginVM(map[string]any{"prefer": "fastest"})
	vm.Strategy = StrategyCost
	if err := svc.Upsert(context.Background(), vm); err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}
	stored, _ := svc.Get("smart")
	if stored.StrategyPlugin != "" || stored.StrategyConfig != nil {
		t.Fatalf("stored = %+v, want plugin fields cleared under strategy cost", stored)
	}
}

func TestValidateManagedConfig_PluginStrategy(t *testing.T) {
	t.Parallel()
	svc := newBalancingService(t)
	svc.SetRouteResolver(&fakeResolver{strategy: &scriptedStrategy{}})
	svc.SetConfigModels([]VirtualModel{pluginVM(map[string]any{"nope": true})})
	if err := svc.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	err := svc.ValidateManagedConfig(nil)
	if err == nil || !strings.Contains(err.Error(), `load virtual model "smart": strategy plugin "lat": strategy_config: unknown config key "nope"`) {
		t.Fatalf("ValidateManagedConfig() error = %v, want it to name the virtual model and key", err)
	}
}

func TestConfigModels_PluginStrategy(t *testing.T) {
	t.Parallel()
	vm := configModel(configEntryWithPlugin())
	if vm.Strategy != StrategyPlugin || vm.StrategyPlugin != "cheapest_healthy" {
		t.Fatalf("configModel() = %+v", vm)
	}
	if !reflect.DeepEqual(vm.StrategyConfig, map[string]any{"prefer": "fastest", "max_error_rate": 0.1}) {
		t.Fatalf("strategy config = %v", vm.StrategyConfig)
	}
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
		if err := store.Upsert(ctx, vm); err != nil {
			t.Fatalf("Upsert() error = %v", err)
		}
		got, err := store.Get(ctx, "smart")
		if err != nil {
			t.Fatalf("Get() error = %v", err)
		}
		if got.Strategy != StrategyPlugin || got.StrategyPlugin != "lat" {
			t.Fatalf("Get() = %+v, want plugin strategy fields", got)
		}
		if !reflect.DeepEqual(got.StrategyConfig, vm.StrategyConfig) {
			t.Fatalf("strategy config = %#v, want %#v", got.StrategyConfig, vm.StrategyConfig)
		}

		plain := VirtualModel{Source: "plain", Strategy: StrategyCost, Targets: vm.Targets, Enabled: true}
		if err := store.Upsert(ctx, plain); err != nil {
			t.Fatalf("Upsert(plain) error = %v", err)
		}
		gotPlain, err := store.Get(ctx, "plain")
		if err != nil {
			t.Fatalf("Get(plain) error = %v", err)
		}
		if gotPlain.StrategyPlugin != "" || gotPlain.StrategyConfig != nil {
			t.Fatalf("Get(plain) = %+v, want empty plugin fields", gotPlain)
		}
	})
}

func TestClone_StrategyConfigIsDeepCopied(t *testing.T) {
	t.Parallel()
	vm := pluginVM(map[string]any{"nested": map[string]any{"k": "v"}, "list": []any{"a"}})
	cloned := vm.clone()
	cloned.StrategyConfig["nested"].(map[string]any)["k"] = "changed"
	cloned.StrategyConfig["list"].([]any)[0] = "changed"
	if vm.StrategyConfig["nested"].(map[string]any)["k"] != "v" || vm.StrategyConfig["list"].([]any)[0] != "a" {
		t.Fatalf("clone shared nested config with the original: %v", vm.StrategyConfig)
	}
}
