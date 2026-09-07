package plugins

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/pluginapi"
)

// RouteConfigSource returns the instance-scoped config of the route plugin
// registered under name: the config of a guardrail definition of the same
// name, when one exists. A missing definition (false) selects an empty
// config, which is validated against the plugin's instance-scoped fields.
type RouteConfigSource func(name string) (json.RawMessage, bool)

// RouteResolver builds one instance per routing-strategy plugin, lazily on
// first use, and validates virtual model strategy_config values against the
// plugin's route-scoped fields. Instances are rebuilt when their
// instance-scoped config changes. It is safe for concurrent use.
type RouteResolver struct {
	catalog *Catalog
	deps    HostDeps

	mu         sync.Mutex
	source     RouteConfigSource
	strategies map[string]*routeStrategy
}

// routeStrategy is one built (or failed) strategy instance together with the
// raw instance config it was built from.
type routeStrategy struct {
	inst *Instance
	raw  json.RawMessage
	err  error
}

// NewRouteResolver returns a resolver over the catalog's route plugins.
func NewRouteResolver(catalog *Catalog, deps HostDeps) *RouteResolver {
	return &RouteResolver{catalog: catalog, deps: deps, strategies: map[string]*routeStrategy{}}
}

// SetInstanceConfigs installs the source of instance-scoped configs. It may
// be called after strategies were built: the next Strategy call rebuilds an
// instance whose config changed.
func (r *RouteResolver) SetInstanceConfigs(source RouteConfigSource) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.source = source
}

// Names lists the loaded plugins that implement the route hook, sorted.
func (r *RouteResolver) Names() []string {
	if r == nil {
		return nil
	}
	return RoutePluginNames(r.catalog)
}

// RoutePluginNames lists the usable catalog entries implementing the route
// hook, sorted by name.
func RoutePluginNames(catalog *Catalog) []string {
	var names []string
	for _, entry := range catalog.Entries() {
		if entry.Err == nil && entry.HasKind(pluginapi.KindRoute) {
			names = append(names, entry.Name)
		}
	}
	sort.Strings(names)
	return names
}

// routeEntry looks up a usable route plugin.
func (r *RouteResolver) routeEntry(name string) (Entry, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Entry{}, fmt.Errorf("routing-strategy plugin name is required")
	}
	if r == nil || r.catalog == nil {
		return Entry{}, fmt.Errorf("routing-strategy plugin %q is not loaded", name)
	}
	entry, ok := r.catalog.Lookup(name)
	if !ok {
		return Entry{}, fmt.Errorf("routing-strategy plugin %q is not loaded", name)
	}
	if !entry.HasKind(pluginapi.KindRoute) {
		return Entry{}, fmt.Errorf("plugin %q is not a routing strategy (kinds: %s)", name, kindList(entry.Kinds))
	}
	return entry, nil
}

// ValidateRouteConfig checks cfg against the route-scoped fields of the named
// plugin and returns the canonical JSON: defaults applied, values coerced,
// keys sorted. Errors name the offending key.
func (r *RouteResolver) ValidateRouteConfig(name string, cfg map[string]any) (json.RawMessage, error) {
	entry, err := r.routeEntry(name)
	if err != nil {
		return nil, err
	}
	if cfg == nil {
		cfg = map[string]any{}
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return nil, fmt.Errorf("encode strategy_config: %w", err)
	}
	canonical, err := ValidateConfig(entry.Manifest.ConfigSchema, raw, pluginapi.ScopeRoute)
	if err != nil {
		return nil, fmt.Errorf("strategy_config: %w", err)
	}
	return canonical, nil
}

// Strategy returns the initialized strategy for the named plugin, building
// it on first use. It fails when the plugin is not loaded, does not implement
// the route hook, or its instance-scoped config (from the guardrail
// definition of the same name, or empty) is rejected by validation or Init;
// the failure is cached until that config changes, so a broken plugin costs
// one lookup per request rather than one Init.
func (r *RouteResolver) Strategy(name string) (pluginapi.RouteStrategy, *Instance, error) {
	entry, err := r.routeEntry(name)
	if err != nil {
		return nil, nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	raw := r.instanceConfigLocked(entry.Name)
	if current, ok := r.strategies[entry.Name]; ok && bytes.Equal(current.raw, raw) {
		return current.strategy()
	}
	built := r.buildLocked(entry, raw)
	if previous, ok := r.strategies[entry.Name]; ok && previous.inst != nil {
		if err := previous.inst.Close(context.Background()); err != nil {
			r.logger().Warn("closing replaced routing-strategy instance failed", "plugin", entry.Name, "error", err)
		}
	}
	r.strategies[entry.Name] = built
	return built.strategy()
}

func (s *routeStrategy) strategy() (pluginapi.RouteStrategy, *Instance, error) {
	if s.err != nil {
		return nil, nil, s.err
	}
	return s.inst.Plugin.(pluginapi.RouteStrategy), s.inst, nil
}

func (r *RouteResolver) instanceConfigLocked(name string) json.RawMessage {
	if r.source == nil {
		return json.RawMessage(`{}`)
	}
	raw, ok := r.source(name)
	if !ok || len(bytes.TrimSpace(raw)) == 0 {
		return json.RawMessage(`{}`)
	}
	return raw
}

func (r *RouteResolver) buildLocked(entry Entry, raw json.RawMessage) *routeStrategy {
	host := NewHost(r.deps, HostInfo{PluginName: entry.Name, InstanceName: entry.Name})
	inst, err := NewInstance(context.Background(), entry, InstanceSpec{Name: entry.Name, Config: raw}, host)
	if err != nil {
		return &routeStrategy{raw: raw, err: err}
	}
	return &routeStrategy{inst: inst, raw: raw}
}

// ReportOutcome hands one upstream attempt to every built strategy, each call
// recovered from panics, so strategies learn from traffic they did not steer.
func (r *RouteResolver) ReportOutcome(outcome pluginapi.RouteOutcome) {
	if r == nil {
		return
	}
	r.mu.Lock()
	strategies := make([]*routeStrategy, 0, len(r.strategies))
	for _, s := range r.strategies {
		if s.inst != nil {
			strategies = append(strategies, s)
		}
	}
	r.mu.Unlock()
	for _, s := range strategies {
		r.report(s, outcome)
	}
}

func (r *RouteResolver) report(s *routeStrategy, outcome pluginapi.RouteOutcome) {
	defer func() {
		if rec := recover(); rec != nil {
			r.logger().Error("routing-strategy plugin panicked in OnAttemptEnd", "plugin", s.inst.Name)
		}
	}()
	s.inst.Plugin.(pluginapi.RouteStrategy).OnAttemptEnd(outcome)
}

// Close releases every built strategy instance.
func (r *RouteResolver) Close(ctx context.Context) error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	var closeErr error
	for name, s := range r.strategies {
		if s.inst != nil {
			if err := s.inst.Close(ctx); err != nil && closeErr == nil {
				closeErr = fmt.Errorf("close routing-strategy %q: %w", name, err)
			}
		}
		delete(r.strategies, name)
	}
	return closeErr
}

func (r *RouteResolver) logger() *slog.Logger {
	if r.deps.Logger != nil {
		return r.deps.Logger
	}
	return slog.Default()
}

func kindList(kinds []pluginapi.Kind) string {
	names := make([]string, 0, len(kinds))
	for _, kind := range kinds {
		names = append(names, string(kind))
	}
	if len(names) == 0 {
		return "none"
	}
	return strings.Join(names, ", ")
}
