package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/enterpilot/gomodel/pluginapi"
)

// fakeRoute is a test routing strategy whose schema and behaviour are shared
// across the instances its factory builds.
type fakeRoute struct {
	mu       sync.Mutex
	name     string
	schema   []pluginapi.Field
	initErr  error
	config   json.RawMessage
	outcomes []pluginapi.RouteOutcome
	panicEnd bool
	closed   int
	// initGate, when set, blocks Init until it is closed.
	initGate chan struct{}
}

func (f *fakeRoute) Manifest() pluginapi.Manifest {
	return pluginapi.Manifest{Name: f.name, Kinds: []pluginapi.Kind{pluginapi.KindRoute}, ConfigSchema: f.schema}
}

func (f *fakeRoute) Init(_ context.Context, config json.RawMessage, _ pluginapi.Host) error {
	if f.initGate != nil {
		<-f.initGate
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.config = config
	return f.initErr
}

func (f *fakeRoute) Close(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed++
	return nil
}

func (f *fakeRoute) Select(_ context.Context, req pluginapi.RouteRequest) (pluginapi.RouteChoice, error) {
	return pluginapi.RouteChoice{Qualified: req.Candidates[0].Qualified, Reason: "first"}, nil
}

func (f *fakeRoute) OnAttemptEnd(outcome pluginapi.RouteOutcome) {
	if f.panicEnd {
		panic("boom")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.outcomes = append(f.outcomes, outcome)
}

var routeSchema = []pluginapi.Field{
	{Key: "endpoint", Input: pluginapi.InputText, Required: true},
	{Key: "prefer", Input: pluginapi.InputSelect, Default: "cheapest", Scope: pluginapi.ScopeRoute,
		Options: []pluginapi.Option{{Value: "cheapest"}, {Value: "fastest"}}},
	{Key: "budget", Input: pluginapi.InputNumber, Scope: pluginapi.ScopeRoute},
}

func newRouteCatalog(t *testing.T, route *fakeRoute) *Catalog {
	t.Helper()
	catalog := NewCatalog()
	if err := catalog.Register(func() pluginapi.Plugin { return route }, SourceBuiltin); err != nil {
		t.Fatalf("Register(route): %v", err)
	}
	if err := catalog.Register(factoryOf(&fakePlugin{name: "prompt_only", kinds: []pluginapi.Kind{pluginapi.KindPrompt}}), SourceBuiltin); err != nil {
		t.Fatalf("Register(prompt): %v", err)
	}
	return catalog
}

func TestRouteResolver_ValidateRouteConfig(t *testing.T) {
	t.Parallel()
	resolver := NewRouteResolver(newRouteCatalog(t, &fakeRoute{name: "lat", schema: routeSchema}), HostDeps{})
	cases := []struct {
		name    string
		plugin  string
		cfg     map[string]any
		want    string
		wantErr string
	}{
		{name: "defaults applied", plugin: "lat", cfg: nil, want: `{"prefer":"cheapest"}`},
		{name: "values coerced and sorted", plugin: "lat", cfg: map[string]any{"prefer": "fastest", "budget": "3"}, want: `{"budget":3,"prefer":"fastest"}`},
		{name: "unknown key", plugin: "lat", cfg: map[string]any{"nope": 1}, wantErr: `unknown config key "nope"`},
		{name: "bad option", plugin: "lat", cfg: map[string]any{"prefer": "slowest"}, wantErr: `config key "prefer"`},
		{name: "instance key rejected in route scope", plugin: "lat", cfg: map[string]any{"endpoint": "x"}, wantErr: `unknown config key "endpoint"`},
		{name: "not loaded", plugin: "missing", wantErr: `not loaded`},
		{name: "not a route plugin", plugin: "prompt_only", wantErr: `not a routing strategy`},
		{name: "empty name", plugin: " ", wantErr: `name is required`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolver.ValidateRouteConfig(tc.plugin, tc.cfg)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error = %v, want containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ValidateRouteConfig: %v", err)
			}
			if string(got) != tc.want {
				t.Fatalf("canonical = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestRouteResolver_StrategyBuildsLazilyFromInstanceConfig(t *testing.T) {
	t.Parallel()
	route := &fakeRoute{name: "lat", schema: routeSchema}
	resolver := NewRouteResolver(newRouteCatalog(t, route), HostDeps{})

	if _, _, err := resolver.Strategy("lat"); err == nil || !strings.Contains(err.Error(), `"endpoint" is required`) {
		t.Fatalf("Strategy without instance config error = %v, want required-field error", err)
	}

	configs := map[string]json.RawMessage{"lat": json.RawMessage(`{"endpoint":"http://a"}`)}
	resolver.SetInstanceConfigs(func(name string) (json.RawMessage, bool) {
		raw, ok := configs[name]
		return raw, ok
	})
	strategy, inst, err := resolver.Strategy("lat")
	if err != nil {
		t.Fatalf("Strategy: %v", err)
	}
	if strategy == nil || inst == nil || inst.Name != "lat" || inst.Type != "lat" {
		t.Fatalf("Strategy returned %v / %+v", strategy, inst)
	}
	if string(route.config) != `{"endpoint":"http://a"}` {
		t.Fatalf("instance config = %s", route.config)
	}
	again, againInst, err := resolver.Strategy("lat")
	if err != nil || again != strategy {
		t.Fatalf("second Strategy = %v, %v; want the cached instance", again, err)
	}
	againInst.Release()

	// A changed definition rebuilds the instance. The previous one is still
	// held by the first caller, so it stays open until that caller releases
	// it and the resolver next looks.
	configs["lat"] = json.RawMessage(`{"endpoint":"http://b"}`)
	_, rebuilt, err := resolver.Strategy("lat")
	if err != nil {
		t.Fatalf("Strategy after change: %v", err)
	}
	if string(route.config) != `{"endpoint":"http://b"}` || route.closed != 0 || inst.Closed() {
		t.Fatalf("after change config = %s closed = %d, want the held instance kept open", route.config, route.closed)
	}
	inst.Release()
	resolver.ReportOutcome(pluginapi.RouteOutcome{Source: "smart"})
	if route.closed != 1 || !inst.Closed() || rebuilt.Closed() {
		t.Fatalf("after release closed = %d (previous closed %v, current closed %v), want the previous instance closed", route.closed, inst.Closed(), rebuilt.Closed())
	}
	rebuilt.Release()

	if names := resolver.Names(); len(names) != 1 || names[0] != "lat" {
		t.Fatalf("Names() = %v, want [lat]", names)
	}
	if err := resolver.Close(context.Background()); err != nil || route.closed != 2 {
		t.Fatalf("Close() error = %v closed = %d", err, route.closed)
	}
}

func TestRouteResolver_StrategyErrors(t *testing.T) {
	t.Parallel()
	failing := &fakeRoute{name: "bad", initErr: errors.New("nope")}
	resolver := NewRouteResolver(newRouteCatalog(t, failing), HostDeps{})
	cases := []struct{ name, plugin, wantErr string }{
		{"init failure", "bad", "init: nope"},
		{"unknown", "missing", "not loaded"},
		{"wrong kind", "prompt_only", "not a routing strategy"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := resolver.Strategy(tc.plugin)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Strategy(%q) error = %v, want containing %q", tc.plugin, err, tc.wantErr)
			}
		})
	}
	if _, _, err := resolver.Strategy("missing"); err == nil {
		t.Fatal("Strategy(missing) error = nil")
	}
	if len(resolver.strategies) != 1 {
		t.Fatalf("resolver cached %d strategies, want only the failed build", len(resolver.strategies))
	}
}

func TestRouteResolver_ReportOutcomeFansOutAndRecovers(t *testing.T) {
	t.Parallel()
	good := &fakeRoute{name: "good"}
	bad := &fakeRoute{name: "bad", panicEnd: true}
	catalog := NewCatalog()
	for _, route := range []*fakeRoute{good, bad} {
		if err := catalog.Register(func() pluginapi.Plugin { return route }, SourceBuiltin); err != nil {
			t.Fatalf("Register: %v", err)
		}
	}
	resolver := NewRouteResolver(catalog, HostDeps{})
	outcome := pluginapi.RouteOutcome{Source: "smart", Target: pluginapi.RouteTarget{Provider: "openai", Model: "gpt-4o"}, Success: true}

	// Nothing built yet: nothing to report to.
	resolver.ReportOutcome(outcome)
	if len(good.outcomes) != 0 {
		t.Fatalf("unbuilt strategy received %d outcomes", len(good.outcomes))
	}
	for _, name := range []string{"good", "bad"} {
		if _, _, err := resolver.Strategy(name); err != nil {
			t.Fatalf("Strategy(%s): %v", name, err)
		}
	}
	resolver.ReportOutcome(outcome)
	resolver.ReportOutcome(outcome)
	if len(good.outcomes) != 2 || good.outcomes[0].Target.Qualified() != "openai/gpt-4o" {
		t.Fatalf("good strategy outcomes = %+v, want 2 for openai/gpt-4o", good.outcomes)
	}
}

// A rebuild's Init must not hold the mutex ReportOutcome takes at the end of
// every upstream request.
func TestRouteResolver_ReportOutcomeNotBlockedByInit(t *testing.T) {
	t.Parallel()
	slow := &fakeRoute{name: "slow", initGate: make(chan struct{})}
	resolver := NewRouteResolver(newRouteCatalog(t, slow), HostDeps{})

	built := make(chan error, 1)
	go func() {
		_, inst, err := resolver.Strategy("slow")
		inst.Release()
		built <- err
	}()
	// Wait until the build has entered Init, which parks on the gate.
	deadline := time.Now().Add(2 * time.Second)
	for {
		resolver.mu.Lock()
		_, cached := resolver.strategies["slow"]
		resolver.mu.Unlock()
		if cached || time.Now().After(deadline) {
			break
		}
		if !resolver.buildMu.TryLock() {
			break
		}
		resolver.buildMu.Unlock()
		time.Sleep(time.Millisecond)
	}

	reported := make(chan struct{})
	go func() {
		resolver.ReportOutcome(pluginapi.RouteOutcome{Source: "smart"})
		close(reported)
	}()
	select {
	case <-reported:
	case <-time.After(time.Second):
		t.Fatal("ReportOutcome blocked behind a plugin Init")
	}
	close(slow.initGate)
	if err := <-built; err != nil {
		t.Fatalf("Strategy: %v", err)
	}
}
