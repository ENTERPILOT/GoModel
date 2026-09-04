package virtualmodels

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/plugins"
	"github.com/enterpilot/gomodel/pluginapi"
)

// RouteResolver serves routing-strategy plugins to redirects using the plugin
// strategy. plugins.RouteResolver implements it.
type RouteResolver interface {
	// Strategy returns the initialized strategy registered under name, or an
	// error when no such route plugin is loaded or its instance failed to
	// initialize.
	Strategy(name string) (pluginapi.RouteStrategy, *plugins.Instance, error)
	// ValidateRouteConfig checks a virtual model's strategy_config against
	// the plugin's route-scoped fields and returns the canonical JSON with
	// defaults applied.
	ValidateRouteConfig(name string, cfg map[string]any) (json.RawMessage, error)
}

// pluginSelectTimeout bounds one Select call: the plugin runs on the request
// path before the upstream call, so a slow one costs every request latency.
const pluginSelectTimeout = 250 * time.Millisecond

// SetRouteResolver installs the routing-strategy plugin resolver consulted by
// redirects using the plugin strategy. Must be called before the service
// starts resolving requests.
func (s *Service) SetRouteResolver(resolver RouteResolver) {
	if s == nil {
		return
	}
	s.routeResolver = resolver
}

// validateRouteStrategy checks a plugin-strategy redirect against the loaded
// plugins: the plugin must exist, implement the route hook, and accept the
// strategy_config. Other strategies pass.
func (s *Service) validateRouteStrategy(vm VirtualModel) error {
	if normalizeStrategy(vm.Strategy) != StrategyPlugin {
		return nil
	}
	if s.routeResolver == nil {
		return newValidationError("routing-strategy plugins are not available in this deployment", nil)
	}
	if _, err := s.routeResolver.ValidateRouteConfig(vm.StrategyPlugin, vm.StrategyConfig); err != nil {
		return newValidationError(fmt.Sprintf("strategy plugin %q: %v", vm.StrategyPlugin, err), err)
	}
	return nil
}

// routeConfigCache holds the validated strategy_config of one redirect entry.
// It is filled on first use, because validation needs the resolver, which
// the snapshot builder does not have.
type routeConfigCache struct {
	mu  sync.Mutex
	raw json.RawMessage
	ok  bool
}

// routeConfig returns the canonical strategy_config for entry, validating it
// through the resolver once and caching the result on success.
func (s *Service) routeConfig(entry *redirectEntry) (json.RawMessage, error) {
	cache := entry.route
	if cache == nil {
		return s.routeResolver.ValidateRouteConfig(entry.vm.StrategyPlugin, entry.vm.StrategyConfig)
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.ok {
		return cache.raw, nil
	}
	raw, err := s.routeResolver.ValidateRouteConfig(entry.vm.StrategyPlugin, entry.vm.StrategyConfig)
	if err != nil {
		return nil, err
	}
	cache.raw, cache.ok = raw, true
	return raw, nil
}

// pluginTarget delegates the choice among the viable pool to the redirect's
// routing-strategy plugin, with the same contract as adaptiveTarget: pinned
// is the target already serving the session (the plugin decides whether to
// keep it), and false sends the caller to weighted round robin. It reports
// false when no resolver is installed, the plugin is missing or failed to
// initialize, the strategy_config is invalid, Select errors, panics, times
// out, or answers with a model outside the pool. Plugin failures are logged
// once per redirect until the plugin answers again; per-request outcomes are
// logged at debug level.
func (s *Service) pluginTarget(ctx context.Context, entry *redirectEntry, sessionID, pinned string, pool []resolvedTarget) (resolvedTarget, bool) {
	source := entry.vm.Source
	name := entry.vm.StrategyPlugin
	if s.routeResolver == nil {
		s.warnPluginOnce(source, name, "routing-strategy plugins are not available; falling back to round robin", nil)
		return resolvedTarget{}, false
	}
	strategy, _, err := s.routeResolver.Strategy(name)
	if err != nil {
		s.warnPluginOnce(source, name, "routing-strategy plugin unavailable; falling back to round robin", err)
		return resolvedTarget{}, false
	}
	config, err := s.routeConfig(entry)
	if err != nil {
		s.warnPluginOnce(source, name, "routing-strategy config invalid; falling back to round robin", err)
		return resolvedTarget{}, false
	}
	s.pluginWarned.Delete(source)

	if ctx == nil {
		ctx = context.Background()
	}
	req := pluginapi.RouteRequest{
		Source:        source,
		SessionID:     sessionID,
		SessionTarget: pinned,
		Candidates:    s.routeCandidates(pool),
		Meta:          plugins.MetaFromContext(ctx, core.GetWorkflow(ctx)),
		Config:        config,
	}
	choice, err := callRouteStrategy(ctx, strategy, req)
	if err != nil {
		slog.Warn("routing-strategy plugin failed; falling back to round robin",
			"source", source, "plugin", name, "error", err)
		return resolvedTarget{}, false
	}
	target, found := poolTarget(pool, choice.Qualified)
	if !found {
		slog.Debug("routing-strategy plugin answered outside the viable pool; falling back to round robin",
			"source", source, "plugin", name, "answer", choice.Qualified, "reason", choice.Reason)
		return resolvedTarget{}, false
	}
	slog.Debug("routing-strategy plugin selected target",
		"source", source, "plugin", name, "target", target.qualified, "reason", choice.Reason)
	return target, true
}

// routeCandidates projects the viable pool into plugin candidates. Pricing
// is copied so a plugin writing through the pointers cannot change what the
// cost strategy later reads.
func (s *Service) routeCandidates(pool []resolvedTarget) []pluginapi.RouteCandidate {
	now := time.Now()
	candidates := make([]pluginapi.RouteCandidate, len(pool))
	for i, t := range pool {
		candidate := pluginapi.RouteCandidate{
			Provider:  t.selector.Provider,
			Model:     t.selector.Model,
			Qualified: t.qualified,
			Weight:    t.weight,
		}
		if model, found := s.catalog.LookupModel(t.qualified); found && model != nil && model.Metadata != nil && model.Metadata.Pricing != nil {
			pricing := model.Metadata.Pricing.AtTime(now)
			candidate.InputPerMtok = copyPrice(pricing.InputPerMtok)
			candidate.OutputPerMtok = copyPrice(pricing.OutputPerMtok)
		}
		candidates[i] = candidate
	}
	return candidates
}

type routeSelectResult struct {
	choice pluginapi.RouteChoice
	err    error
}

// callRouteStrategy runs Select on its own goroutine with panic recovery and
// the select timeout. A plugin that ignores the context keeps running after
// the timeout, but the request no longer waits for it.
func callRouteStrategy(ctx context.Context, strategy pluginapi.RouteStrategy, req pluginapi.RouteRequest) (pluginapi.RouteChoice, error) {
	selectCtx, cancel := context.WithTimeout(ctx, pluginSelectTimeout)
	defer cancel()
	results := make(chan routeSelectResult, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				results <- routeSelectResult{err: fmt.Errorf("select panicked: %v", r)}
			}
		}()
		choice, err := strategy.Select(selectCtx, req)
		results <- routeSelectResult{choice: choice, err: err}
	}()
	select {
	case result := <-results:
		if result.err != nil {
			return pluginapi.RouteChoice{}, result.err
		}
		return result.choice, nil
	case <-selectCtx.Done():
		return pluginapi.RouteChoice{}, fmt.Errorf("select timed out after %s", pluginSelectTimeout)
	}
}

// warnPluginOnce logs a plugin-strategy failure the first time it happens
// for source; pluginTarget clears the mark once the plugin works again.
func (s *Service) warnPluginOnce(source, plugin, message string, err error) {
	if _, logged := s.pluginWarned.LoadOrStore(source, struct{}{}); logged {
		return
	}
	attrs := []any{"source", source, "plugin", plugin}
	if err != nil {
		attrs = append(attrs, "error", err)
	}
	slog.Warn(message, attrs...)
}
