package virtualmodels

import (
	"log/slog"
	"sort"
	"strings"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/core"
)

// ResolveFailovers returns the failover chain for a request that resolved
// through a redirect: the concrete models behind the redirect's remaining
// available targets, in declared order and descending chained virtual models,
// minus the model the request was sent to first. The redirect's strategy only
// chooses that first target; every other available target is a failover leg,
// so a load balancer and a priority list fail over the same way. Requests that
// did not go through a redirect have no chain.
func (s *Service) ResolveFailovers(resolution *core.RequestModelResolution, _ core.Operation) []core.ModelSelector {
	if s == nil || resolution == nil || !resolution.AliasApplied || resolution.Requested.ExplicitProvider {
		return nil
	}
	snap := s.snapshot()
	entry, ok := snap.redirects[strings.TrimSpace(resolution.Requested.Model)]
	if !ok || !entry.vm.Enabled || len(entry.targets) < 2 {
		return nil
	}
	seen := map[string]struct{}{resolution.ResolvedQualifiedModel(): {}}
	chain := make([]core.ModelSelector, 0, len(entry.targets))
	for _, leaf := range snap.leafTargets(entry, s.catalog) {
		if _, dup := seen[leaf.qualified]; dup {
			continue
		}
		seen[leaf.qualified] = struct{}{}
		chain = append(chain, leaf.selector)
	}
	return chain
}

// FailoverConfigModels translates the deprecated `failover` rules block
// (failover.rules, manual_rules_path, FAILOVER_RULES_JSON) into managed
// failover-strategy virtual models, so a configuration written for the
// standalone failover feature keeps routing the same way. Each rule becomes a
// redirect that shadows its primary model: the primary is the first target
// and the fallbacks follow in order. A primary listed in disabled_models is
// skipped. Sources already declared under virtual_models are left to that
// declaration.
func FailoverConfigModels(cfg config.FailoverConfig, declared []VirtualModel) []VirtualModel {
	if len(cfg.Manual) == 0 {
		return nil
	}
	taken := make(map[string]struct{}, len(declared))
	for _, model := range declared {
		taken[strings.TrimSpace(model.Source)] = struct{}{}
	}
	sources := make([]string, 0, len(cfg.Manual))
	for source := range cfg.Manual {
		sources = append(sources, source)
	}
	sort.Strings(sources)

	models := make([]VirtualModel, 0, len(sources))
	for _, source := range sources {
		source = strings.TrimSpace(source)
		fallbacks := cfg.Manual[source]
		if source == "" || len(fallbacks) == 0 || cfg.Disabled[source] {
			continue
		}
		if _, ok := taken[source]; ok {
			continue
		}
		models = append(models, failoverModel(source, fallbacks, true))
	}
	if len(models) > 0 {
		slog.Warn("the failover rules configuration is deprecated; declare a virtual model with strategy \"failover\" under virtual_models instead",
			"migrated", len(models))
	}
	return models
}

// failoverModel builds the failover-strategy redirect equivalent to a legacy
// failover rule: source shadows the primary model, listed first, followed by
// its ordered fallbacks.
func failoverModel(source string, fallbacks []string, managed bool) VirtualModel {
	targets := make([]Target, 0, len(fallbacks)+1)
	targets = append(targets, Target{Model: source})
	for _, fallback := range fallbacks {
		if fallback = strings.TrimSpace(fallback); fallback != "" && fallback != source {
			targets = append(targets, Target{Model: fallback})
		}
	}
	return VirtualModel{
		Source:      source,
		Strategy:    StrategyFailover,
		Targets:     targets,
		Description: "Migrated from failover rules",
		Enabled:     true,
		Managed:     managed,
	}
}
