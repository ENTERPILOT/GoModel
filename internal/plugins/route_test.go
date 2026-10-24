package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/enterpilot/gomodel/pluginapi"
	"github.com/stretchr/testify/require"
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
	// initGate, when set, blocks Init until it is closed; endGate does the
	// same for OnAttemptEnd.
	initGate chan struct{}
	endGate  chan struct{}
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
	if f.endGate != nil {
		<-f.endGate
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
	err := catalog.Register(func() pluginapi.Plugin { return route }, SourceBuiltin)
	require.NoError(t, err)
	err = catalog.Register(factoryOf(&fakePlugin{name: "prompt_only", kinds: []pluginapi.Kind{pluginapi.KindPrompt}}), SourceBuiltin)
	require.NoError(t, err)

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
				require.ErrorContains(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, string(got))
		})
	}
}

func TestRouteResolver_StrategyBuildsLazilyFromInstanceConfig(t *testing.T) {
	t.Parallel()
	route := &fakeRoute{name: "lat", schema: routeSchema}
	resolver := NewRouteResolver(newRouteCatalog(t, route), HostDeps{})
	_, _, err := resolver.Strategy("lat")
	require.ErrorContains(t, err, `"endpoint" is required`)

	configs := map[string]json.RawMessage{"lat": json.RawMessage(`{"endpoint":"http://a"}`)}
	resolver.SetInstanceConfigs(func(name string) (json.RawMessage, bool) {
		raw, ok := configs[name]
		return raw, ok
	})
	strategy, inst, err := resolver.Strategy("lat")
	require.NoError(t, err)
	require.NotNil(t, strategy)
	require.NotNil(t, inst)
	require.Equal(t, "lat", inst.Name)
	require.Equal(t, "lat", inst.Type)
	require.Equal(t, `{"endpoint":"http://a"}`, string(route.config), "instance config = %s", route.config)

	again, againInst, err := resolver.Strategy("lat")
	require.NoError(t, err)
	require.Equal(t, strategy, again)

	againInst.Release()

	// A changed definition rebuilds the instance. The previous one is still
	// held by the first caller, so it stays open until that caller releases
	// it and the resolver next looks.
	configs["lat"] = json.RawMessage(`{"endpoint":"http://b"}`)
	_, rebuilt, err := resolver.Strategy("lat")
	require.NoError(t, err)
	require.Equal(t, `{"endpoint":"http://b"}`, string(route.config))
	require.Equal(t, 0, route.closed)
	require.False(t, inst.Closed(), "after change config = %s closed = %d, want the held instance kept open", route.config, route.closed)

	inst.Release()
	resolver.ReportOutcome(pluginapi.RouteOutcome{Source: "smart"})
	require.Equal(t, 1, route.closed)
	require.True(t, inst.Closed())
	require.False(t, rebuilt.Closed())

	rebuilt.Release()
	names := resolver.Names()
	require.Len(t, names, 1)
	require.Equal(t, "lat", names[0])
	err = resolver.Close(context.Background())
	require.NoError(t, err)
	require.Equal(t, 2, route.closed)
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
			require.ErrorContains(t, err, tc.wantErr)
		})
	}
	_, _, err := resolver.Strategy("missing")
	require.Error(t, err)
	require.Len(t, resolver.strategies, 1)
}

func TestRouteResolver_ReportOutcomeFansOutAndRecovers(t *testing.T) {
	t.Parallel()
	good := &fakeRoute{name: "good"}
	bad := &fakeRoute{name: "bad", panicEnd: true}
	catalog := NewCatalog()
	for _, route := range []*fakeRoute{good, bad} {
		err := catalog.Register(func() pluginapi.Plugin { return route }, SourceBuiltin)
		require.NoError(t, err)
	}
	resolver := NewRouteResolver(catalog, HostDeps{})
	outcome := pluginapi.RouteOutcome{Source: "smart", Target: pluginapi.RouteTarget{Provider: "openai", Model: "gpt-4o"}, Success: true}

	// Nothing built yet: nothing to report to.
	resolver.ReportOutcome(outcome)
	require.Empty(t, good.outcomes)

	for _, name := range []string{"good", "bad"} {
		_, _, err := resolver.Strategy(name)
		require.NoError(t, err, "Strategy(%s): %v", name, err)
	}
	resolver.ReportOutcome(outcome)
	resolver.ReportOutcome(outcome)
	require.Len(t, good.outcomes, 2)
	require.Equal(t, "openai/gpt-4o", good.outcomes[0].Target.Qualified())
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
	waitForBuild(t, resolver)

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
	err := <-built
	require.NoError(t, err)
}

// waitForBuild returns once a rebuild holds buildMu, i.e. is inside Init.
func waitForBuild(t *testing.T, resolver *RouteResolver) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !resolver.buildMu.TryLock() {
			return
		}
		resolver.buildMu.Unlock()
		time.Sleep(time.Millisecond)
	}
	t.Fatal("no rebuild started")
}

// waitUntil polls cond for up to two seconds.
func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// Close during a rebuild waits for it and closes the instance it publishes;
// later lookups are refused.
func TestRouteResolver_CloseWaitsForRebuildAndRefusesAfter(t *testing.T) {
	t.Parallel()
	slow := &fakeRoute{name: "slow", initGate: make(chan struct{})}
	resolver := NewRouteResolver(newRouteCatalog(t, slow), HostDeps{})

	type built struct {
		inst *Instance
		err  error
	}
	strategy := make(chan built, 1)
	go func() {
		_, inst, err := resolver.Strategy("slow")
		strategy <- built{inst, err}
	}()
	waitForBuild(t, resolver)
	closeStarted := make(chan struct{})
	closed := make(chan error, 1)
	go func() {
		close(closeStarted)
		closed <- resolver.Close(context.Background())
	}()
	<-closeStarted
	// The rebuild still holds buildMu, so Close cannot have returned yet;
	// it must wait for the instance the rebuild is about to publish.
	select {
	case err := <-closed:
		t.Fatalf("Close returned (%v) before the rebuild finished", err)
	default:
	}
	close(slow.initGate)
	b := <-strategy
	err := <-closed
	require.NoError(t, err)
	require.NoError(t, b.err)
	require.NotNil(t, b.inst)
	require.True(t, b.inst.Closed())

	b.inst.Release()
	_, _, err = resolver.Strategy("slow")
	require.ErrorIs(t, err, ErrRouteResolverClosed)
}

// An instance stays open while its OnAttemptEnd runs, even when a rebuild
// replaces it meanwhile.
func TestRouteResolver_ReportOutcomeHoldsInstanceAcrossOnAttemptEnd(t *testing.T) {
	t.Parallel()
	route := &fakeRoute{name: "lat", schema: routeSchema, endGate: make(chan struct{})}
	resolver := NewRouteResolver(newRouteCatalog(t, route), HostDeps{})
	configs := map[string]json.RawMessage{"lat": json.RawMessage(`{"endpoint":"http://a"}`)}
	resolver.SetInstanceConfigs(func(name string) (json.RawMessage, bool) {
		raw, ok := configs[name]
		return raw, ok
	})
	_, inst, err := resolver.Strategy("lat")
	require.NoError(t, err)

	inst.Release()

	reported := make(chan struct{})
	go func() {
		resolver.ReportOutcome(pluginapi.RouteOutcome{Source: "smart"})
		close(reported)
	}()
	waitUntil(t, "OnAttemptEnd to hold the instance", inst.Held)

	configs["lat"] = json.RawMessage(`{"endpoint":"http://b"}`)
	_, rebuilt, err := resolver.Strategy("lat")
	require.NoError(t, err)

	rebuilt.Release()
	require.False(t, inst.Closed())

	close(route.endGate)
	<-reported
	require.True(t, inst.Closed())
	require.False(t, rebuilt.Closed())
}

// Handing an instance back closes it at once when a rebuild retired it
// meanwhile: no later report or rebuild is needed.
func TestRouteResolver_ReleaseClosesRetiredInstance(t *testing.T) {
	t.Parallel()
	route := &fakeRoute{name: "lat", schema: routeSchema}
	resolver := NewRouteResolver(newRouteCatalog(t, route), HostDeps{})
	configs := map[string]json.RawMessage{"lat": json.RawMessage(`{"endpoint":"http://a"}`)}
	resolver.SetInstanceConfigs(func(name string) (json.RawMessage, bool) {
		raw, ok := configs[name]
		return raw, ok
	})
	_, inst, err := resolver.Strategy("lat")
	require.NoError(t, err)

	configs["lat"] = json.RawMessage(`{"endpoint":"http://b"}`)
	_, rebuilt, err := resolver.Strategy("lat")
	require.NoError(t, err)
	require.False(t, inst.Closed())

	resolver.Release(inst)
	require.True(t, inst.Closed())
	require.False(t, rebuilt.Closed())

	resolver.Release(rebuilt)
}
