package litellmmigrate

import (
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
)

// group is every deployment sharing one LiteLLM model_name.
type group struct {
	name    string
	members []groupMember
}

type groupMember struct {
	target           targetOut
	weight, rpm, tpm *float64
}

func (c *converter) groupFor(name string) *group {
	if g, ok := c.groupByName[name]; ok {
		return g
	}
	g := &group{name: name}
	c.groupByName[name] = g
	c.groups = append(c.groups, g)
	return g
}

func (g *group) add(target targetOut, p litellmParams) {
	g.members = append(g.members, groupMember{target: target, weight: p.Weight, rpm: p.RPM, tpm: p.TPM})
}

// targets returns the group's deduplicated load-balancing targets. Like
// LiteLLM's simple-shuffle, it weights by weight when any deployment sets one,
// otherwise by rpm or tpm when every deployment sets it. Equal weights are
// omitted.
func (g *group) targets() []targetOut {
	weightOf := func(m groupMember) *float64 { return m.weight }
	switch {
	case slices.ContainsFunc(g.members, func(m groupMember) bool { return m.weight != nil }):
	case !slices.ContainsFunc(g.members, func(m groupMember) bool { return m.rpm == nil }):
		weightOf = func(m groupMember) *float64 { return m.rpm }
	case !slices.ContainsFunc(g.members, func(m groupMember) bool { return m.tpm == nil }):
		weightOf = func(m groupMember) *float64 { return m.tpm }
	}
	var out []targetOut
	index := map[string]int{}
	for _, m := range g.members {
		weight := 1.0
		if w := weightOf(m); w != nil && *w > 0 {
			weight = *w
		}
		key := m.target.qualified()
		if i, seen := index[key]; seen {
			out[i].Weight += weight
			continue
		}
		index[key] = len(out)
		t := m.target
		t.Weight = weight
		out = append(out, t)
	}
	if !slices.ContainsFunc(out, func(t targetOut) bool { return t.Weight != out[0].Weight }) {
		for i := range out {
			out[i].Weight = 0
		}
	}
	return out
}

// buildVirtualModels turns model groups, fallbacks, and model_group_alias into
// GoModel virtual models. A group behind one deployment is a plain alias,
// several deployments are a load balancer, and fallbacks wrap the group in a
// failover virtual model. Fallbacks point at the fallback group's own
// deployments, never at its fallbacks, matching LiteLLM's one-level fallback.
func (c *converter) buildVirtualModels() {
	strategy := c.routingStrategy()
	fallbacks := c.fallbacks()
	pools := map[string]string{}
	for _, g := range c.groups {
		if len(g.targets()) > 1 && len(fallbacks[g.name]) > 0 {
			pools[g.name] = c.uniqueSource(g.name + "-pool")
		}
	}

	aliasesComplete := true
	for _, g := range c.groups {
		targets := g.targets()
		chain := c.fallbackTargets(g.name, fallbacks[g.name], pools)
		description := "Migrated from LiteLLM model_name " + g.name
		if len(chain) == 0 {
			if len(targets) == 1 && targets[0].qualified() == g.name {
				aliasesComplete = false // served directly; a self-alias is invalid
				continue
			}
			c.addVirtualModel(virtualModelOut{Source: g.name, Strategy: balanced(strategy, targets), Targets: targets, Description: description})
			continue
		}
		primary := targets
		if pool, ok := pools[g.name]; ok {
			c.addVirtualModel(virtualModelOut{Source: pool, Strategy: balanced(strategy, targets), Targets: targets, Description: "Load balancer behind " + g.name})
			c.report.info("virtual_models."+pool, fmt.Sprintf("added so %s can load balance its deployments and still fail over", g.name))
			primary = []targetOut{{Model: pool}}
		}
		c.addVirtualModel(virtualModelOut{Source: g.name, Strategy: "failover", Targets: append(primary, chain...), Description: description})
	}
	c.convertModelGroupAliases(pools)
	c.convertDeploymentLimits()

	switch {
	case len(c.out.VirtualModels) == 0:
	case c.wildcard:
		c.report.info("models", "GET /v1/models lists provider models as well as aliases, because wildcard deployments expose whole provider catalogs")
	case !aliasesComplete:
		c.report.info("models", "GET /v1/models lists provider-qualified models as well as aliases, because some model_name values already are provider/model IDs")
	default:
		c.models().KeepOnlyAliasesAtModelsEndpoint = true
	}
}

func balanced(strategy string, targets []targetOut) string {
	if len(targets) < 2 {
		return ""
	}
	return strategy
}

func (c *converter) addVirtualModel(vm virtualModelOut) {
	if len(vm.Targets) == 1 {
		vm.Targets[0].Weight = 0
	}
	c.out.VirtualModels = append(c.out.VirtualModels, vm)
	c.report.VirtualModels = append(c.report.VirtualModels, vm)
}

// uniqueSource returns name, suffixed until it clashes with no model group,
// model_group_alias entry, or earlier generated name.
func (c *converter) uniqueSource(name string) string {
	aliases, _ := c.router.values["model_group_alias"].(map[string]any)
	taken := func(candidate string) bool {
		_, alias := aliases[candidate]
		return alias || c.groupByName[candidate] != nil || c.hasVirtualModel(candidate) || slices.Contains(c.poolNames, candidate)
	}
	candidate := name
	for i := 2; taken(candidate); i++ {
		candidate = fmt.Sprintf("%s-%d", name, i)
	}
	c.poolNames = append(c.poolNames, candidate)
	return candidate
}

// baseTarget is what a fallback or alias pointing at a group resolves to: the
// group's deployments without its own fallbacks.
func (c *converter) baseTarget(name string, pools map[string]string) (targetOut, bool) {
	g, ok := c.groupByName[name]
	if !ok {
		return targetOut{}, false
	}
	targets := g.targets()
	switch {
	case len(targets) == 1:
		t := targets[0]
		t.Weight = 0
		return t, true
	case pools[name] != "":
		return targetOut{Model: pools[name]}, true
	default:
		return targetOut{Model: name}, true
	}
}

func (c *converter) fallbackTargets(name string, chain []string, pools map[string]string) []targetOut {
	var out []targetOut
	for _, fallback := range chain {
		if fallback == name {
			continue
		}
		if t, ok := c.baseTarget(fallback, pools); ok {
			out = append(out, t)
			continue
		}
		if c.wildcard && strings.Contains(fallback, "/") {
			out = append(out, targetOut{Model: fallback})
			continue
		}
		c.report.warn("fallbacks."+name, fmt.Sprintf("fallback %q is not a model_name in model_list; dropped", fallback))
	}
	return out
}

func (c *converter) convertModelGroupAliases(pools map[string]string) {
	raw, ok := c.router.get("model_group_alias")
	if !ok {
		return
	}
	aliases, _ := raw.(map[string]any)
	names := make([]string, 0, len(aliases))
	for alias := range aliases {
		names = append(names, alias)
	}
	sort.Strings(names)
	for _, alias := range names {
		subject := "model_group_alias." + alias
		target, hidden := aliasTarget(aliases[alias])
		if _, clash := c.groupByName[alias]; clash {
			c.report.warn(subject, "the alias is also a model_name; kept the model_name")
			continue
		}
		t, ok := c.baseTarget(target, pools)
		if !ok {
			c.report.warn(subject, fmt.Sprintf("target %q is not a model_name in model_list; dropped", target))
			continue
		}
		if c.hasVirtualModel(target) {
			t = targetOut{Model: target}
		}
		c.addVirtualModel(virtualModelOut{Source: alias, Targets: []targetOut{t}, Description: "Migrated from LiteLLM model_group_alias"})
		if hidden {
			c.report.info(subject, "GoModel has no hidden aliases; this one is listed in GET /v1/models")
		}
	}
}

func aliasTarget(value any) (target string, hidden bool) {
	switch v := value.(type) {
	case string:
		return v, false
	case map[string]any:
		target, _ = v["model"].(string)
		hidden, _ = v["hidden"].(bool)
		return target, hidden
	}
	return "", false
}

func (c *converter) hasVirtualModel(source string) bool {
	return slices.ContainsFunc(c.out.VirtualModels, func(vm virtualModelOut) bool { return vm.Source == source })
}

// convertDeploymentLimits enforces deployment rpm/tpm as model rate limits
// only under LiteLLM's usage-based routing, the one strategy where LiteLLM
// itself enforces them. Elsewhere they only weight load balancing.
func (c *converter) convertDeploymentLimits() {
	if !c.usageBasedRouting {
		return
	}
	limits := map[string]*rateLimitOut{}
	var order []string
	for _, g := range c.groups {
		for _, m := range g.members {
			if m.rpm == nil && m.tpm == nil {
				continue
			}
			key := m.target.qualified()
			limit, seen := limits[key]
			if !seen {
				limit = &rateLimitOut{Period: "minute"}
				limits[key] = limit
				order = append(order, key)
			}
			limit.MaxRequests = minLimit(limit.MaxRequests, m.rpm)
			limit.MaxTokens = minLimit(limit.MaxTokens, m.tpm)
		}
	}
	if len(order) == 0 {
		return
	}
	c.out.RateLimits = &rateLimitsOut{}
	for _, key := range order {
		c.out.RateLimits.Models = append(c.out.RateLimits.Models, modelRateLimitOut{Model: key, Limits: []rateLimitOut{*limits[key]}})
	}
}

func minLimit(current int64, value *float64) int64 {
	if value == nil || *value <= 0 {
		return current
	}
	v := int64(math.Ceil(*value))
	if current == 0 || v < current {
		return v
	}
	return current
}
