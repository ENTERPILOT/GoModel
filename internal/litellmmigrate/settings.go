package litellmmigrate

import (
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// section tracks which keys of a LiteLLM settings map were consumed, so the
// rest can be reported as not migrated.
type section struct {
	name   string
	values map[string]any
	used   map[string]bool
}

func newSection(name string, values map[string]any) *section {
	return &section{name: name, values: values, used: map[string]bool{}}
}

func (s *section) get(key string) (any, bool) {
	value, ok := s.values[key]
	if ok {
		s.used[key] = true
	}
	return value, ok
}

func (s *section) leftovers() []string {
	var keys []string
	for key := range s.values {
		if !s.used[key] {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys
}

// routingStrategy maps router_settings.routing_strategy to a GoModel load
// balancing strategy. Empty means GoModel's default, round_robin.
func (c *converter) routingStrategy() string {
	raw, ok := c.router.get("routing_strategy")
	if !ok {
		return ""
	}
	strategy, _ := raw.(string)
	subject := "router_settings.routing_strategy"
	switch strategy {
	case "", "simple-shuffle":
		return ""
	case "cost-based-routing":
		return "cost"
	case "usage-based-routing", "usage-based-routing-v2":
		c.usageBasedRouting = true
		c.report.info(subject, strategy+" became weighted round_robin; deployment rpm/tpm became per-model rate limits")
	case "latency-based-routing", "least-busy":
		c.report.info(subject, strategy+" became round_robin with failover; GoModel Pro's intelligent routing picks targets by latency and health")
	default:
		c.report.warn(subject, fmt.Sprintf("unknown routing strategy %q; GoModel uses round_robin", strategy))
	}
	return ""
}

// fallbacks merges fallbacks from router_settings and litellm_settings and
// applies default_fallbacks to every group without its own list.
func (c *converter) fallbacks() map[string][]string {
	out := map[string][]string{}
	for _, s := range []*section{c.router, c.litellm} {
		if raw, ok := s.get("fallbacks"); ok {
			for name, chain := range parseFallbacks(raw) {
				if _, exists := out[name]; !exists {
					out[name] = chain
				}
			}
		}
		for _, key := range []string{"context_window_fallbacks", "content_policy_fallbacks"} {
			if _, ok := s.get(key); ok {
				c.report.skip(s.name+"."+key, "GoModel fails over on 429, 5xx, and model-not-found errors; add the error phrases you need to failover.retry_on_errors")
			}
		}
	}
	var defaults []string
	if raw, _, ok := c.routerOrLiteLLM("default_fallbacks", "default_fallbacks"); ok {
		defaults = stringList(raw)
	}
	if len(defaults) > 0 {
		for _, g := range c.groups {
			if _, exists := out[g.name]; !exists {
				out[g.name] = defaults
			}
		}
	}
	return out
}

// parseFallbacks reads LiteLLM's [{"model": ["fallback", ...]}, ...] shape.
func parseFallbacks(raw any) map[string][]string {
	out := map[string][]string{}
	entries, _ := raw.([]any)
	for _, entry := range entries {
		mapping, _ := entry.(map[string]any)
		for name, chain := range mapping {
			out[name] = append(out[name], stringList(chain)...)
		}
	}
	return out
}

func stringList(raw any) []string {
	switch v := raw.(type) {
	case string:
		return []string{v}
	case []any:
		var out []string
		for _, item := range v {
			if s, ok := item.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func (c *converter) convertSettings() {
	c.convertResilience()
	c.convertCallbacks()
	c.convertGeneralSettings()
	c.reportKnownUnsupported()
	for _, s := range []*section{c.router, c.litellm, c.general} {
		for _, key := range s.leftovers() {
			c.report.skip(s.name+"."+key, "no GoModel equivalent; not migrated")
		}
	}
	keys := make([]string, 0, len(c.src.Extra))
	for key := range c.src.Extra {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if key == "guardrails" {
			c.reportGuardrails()
			continue
		}
		c.report.skip(key, "no GoModel equivalent; not migrated")
	}
}

// reportGuardrails flags LiteLLM guardrails as work to finish: GoModel
// guardrails are off by default, so traffic would lose the protection.
func (c *converter) reportGuardrails() {
	entries, _ := c.src.Extra["guardrails"].([]any)
	var names []string
	for _, entry := range entries {
		if fields, ok := entry.(map[string]any); ok {
			if name, _ := fields["guardrail_name"].(string); name != "" {
				names = append(names, name)
			}
		}
	}
	subject := "guardrails"
	if len(names) > 0 {
		subject += " (" + strings.Join(names, ", ") + ")"
	}
	c.report.warn(subject, "not migrated, and GoModel guardrails are off by default: rebuild them before switching traffic; see /advanced/guardrails")
}

func (c *converter) convertResilience() {
	if raw, subject, ok := c.routerOrLiteLLM("num_retries", "num_retries"); ok {
		c.setMaxRetries(raw, subject)
	}
	if raw, _, ok := c.routerOrLiteLLM("timeout", "request_timeout"); ok {
		if seconds, valid := number(raw); valid && seconds > 0 {
			c.out.HTTP = &httpOut{Timeout: int(math.Ceil(seconds))}
		}
	}

	breaker := &circuitBreakerOut{}
	if raw, ok := c.router.get("allowed_fails"); ok {
		if n, valid := number(raw); valid && n >= 0 {
			breaker.FailureThreshold = int(n) + 1
		}
	}
	if raw, ok := c.router.get("cooldown_time"); ok {
		if seconds, valid := number(raw); valid && seconds > 0 {
			breaker.Timeout = time.Duration(seconds * float64(time.Second)).String()
		}
	}
	if breaker.FailureThreshold > 0 || breaker.Timeout != "" {
		c.resilience().CircuitBreaker = breaker
		c.report.info("router_settings.allowed_fails/cooldown_time", "became the circuit breaker's failure_threshold/timeout; GoModel counts consecutive failures per provider rather than failures per minute per deployment")
	}
}

// routerOrLiteLLM reads a setting LiteLLM accepts in both router_settings and
// litellm_settings. The router value wins, as it does in LiteLLM.
func (c *converter) routerOrLiteLLM(routerKey, litellmKey string) (any, string, bool) {
	routerValue, inRouter := c.router.get(routerKey)
	litellmValue, inLiteLLM := c.litellm.get(litellmKey)
	switch {
	case inRouter && inLiteLLM:
		c.report.info("litellm_settings."+litellmKey, "ignored: router_settings."+routerKey+" takes precedence, as in LiteLLM")
		return routerValue, "router_settings." + routerKey, true
	case inRouter:
		return routerValue, "router_settings." + routerKey, true
	case inLiteLLM:
		return litellmValue, "litellm_settings." + litellmKey, true
	}
	return nil, "", false
}

func (c *converter) setMaxRetries(raw any, subject string) {
	n, valid := number(raw)
	if !valid || n < 0 {
		c.report.warn(subject, "not a number; not migrated")
		return
	}
	retries := int(n)
	c.resilience().Retry = &retryOut{MaxRetries: &retries}
}

func (c *converter) resilience() *resilienceOut {
	if c.out.Resilience == nil {
		c.out.Resilience = &resilienceOut{}
	}
	return c.out.Resilience
}

func (c *converter) convertCallbacks() {
	var callbacks []string
	for _, key := range []string{"callbacks", "success_callback", "failure_callback", "service_callback"} {
		if raw, ok := c.litellm.get(key); ok {
			for _, name := range stringList(raw) {
				callbacks = appendUnique(callbacks, name)
			}
		}
	}
	for _, name := range callbacks {
		subject := "litellm_settings.callbacks: " + name
		switch strings.ToLower(name) {
		case "prometheus":
			c.out.Metrics = &enabledOut{Enabled: true}
			c.report.info(subject, "enabled GoModel's Prometheus endpoint at /metrics; metric names differ, so update dashboards and alerts")
		case "otel", "opentelemetry", "arize", "arize_phoenix":
			c.out.OpenTelemetry = &enabledOut{Enabled: true}
			c.report.info(subject, "enabled OpenTelemetry export; point it at your collector with OTEL_EXPORTER_OTLP_ENDPOINT")
		case "langfuse", "langfuse_otel":
			c.report.warn(subject, "send traces to Langfuse through GoModel's OpenTelemetry export; see /guides/langfuse")
		default:
			c.report.skip(subject, "no GoModel integration; GoModel keeps request and usage logs in its own storage and exports OpenTelemetry")
		}
	}
}

func (c *converter) convertGeneralSettings() {
	if raw, ok := c.general.get("master_key"); ok {
		if key, _ := raw.(string); key != "" {
			c.convertMasterKey(key)
		}
	}
	if _, ok := c.general.get("database_url"); ok {
		c.report.info("general_settings.database_url", "not GoModel's storage: give GoModel its own database (storage.type: postgresql). The migration reads keys, teams, and budgets from it")
	}
	if _, ok := c.general.get("store_model_in_db"); ok {
		c.report.warn("general_settings.store_model_in_db", "models added through the LiteLLM UI live in its database, not in config.yaml; recreate them in GoModel")
	}
}

// setting returns a general_settings string with an os.environ/ reference
// resolved from the environment, then from the config's
// environment_variables, which LiteLLM loads into its environment.
func (c *converter) setting(key string) string {
	value, _ := c.general.values[key].(string)
	name, isRef := envRef(value)
	if !isRef {
		return strings.TrimSpace(value)
	}
	if resolved := os.Getenv(name); resolved != "" {
		return resolved
	}
	return c.src.EnvironmentVariables[name]
}

// masterKeyEnv is the variable GoModel reads its master key from. It
// overrides server.master_key, so the migrated master key must own it.
const masterKeyEnv = "GOMODEL_MASTER_KEY"

// freeMasterKeyVariable moves a GOMODEL_MASTER_KEY set in
// environment_variables to another name when LiteLLM's master key is a
// different value, so it cannot replace the master key clients use.
// Settings that read os.environ/GOMODEL_MASTER_KEY follow it.
func (c *converter) freeMasterKeyVariable() {
	key, _ := c.general.values["master_key"].(string)
	value, written := c.env.values[masterKeyEnv]
	if key == "" || !written || key == value || key == "os.environ/"+masterKeyEnv {
		return
	}
	to := c.env.move(masterKeyEnv, "LITELLM_"+masterKeyEnv)
	c.report.warn("environment_variables."+masterKeyEnv, "moved to "+to+" in .env: GoModel uses "+masterKeyEnv+" as its master key, so it would replace general_settings.master_key")
}

// convertMasterKey keeps LiteLLM's master key under GOMODEL_MASTER_KEY.
func (c *converter) convertMasterKey(key string) {
	name, isRef := envRef(key)
	if isRef {
		c.env.require(name)
	}
	if _, written := c.env.values[masterKeyEnv]; name != masterKeyEnv && !written && c.env.reserved[masterKeyEnv] {
		c.report.warn("general_settings.master_key", "the config also reads "+masterKeyEnv+", which GoModel uses as its master key in place of this one; rename that variable before starting GoModel")
	}
	if isRef {
		c.out.Server = &serverOut{MasterKey: "${" + name + "}"}
	} else if written := c.env.set(masterKeyEnv, key); written != masterKeyEnv {
		// Only when the environment provides GOMODEL_MASTER_KEY (warned
		// above): keep the key in .env under the name it was written as.
		c.out.Server = &serverOut{MasterKey: "${" + written + "}"}
	}
	c.report.info("general_settings.master_key", "kept: admin scripts using the LiteLLM master key keep working against GoModel")
}

// reportKnownUnsupported explains LiteLLM settings that are deliberately
// not carried over, so they do not show up as unexplained leftovers.
func (c *converter) reportKnownUnsupported() {
	notes := []struct {
		section *section
		keys    []string
		message string
	}{
		{c.litellm, []string{"drop_params", "modify_params"}, "not needed: GoModel adapts parameters to each provider by default"},
		{c.litellm, []string{"cache", "cache_params"}, "GoModel's response cache is configured separately; see /features/cache"},
		{c.litellm, []string{"set_verbose", "json_logs"}, "GoModel logging is set with LOG_LEVEL and LOG_FORMAT"},
		{c.router, []string{"redis_host", "redis_port", "redis_password", "redis_url"}, "not needed for routing: GoModel keeps routing state in the gateway"},
		{c.router, []string{"enable_pre_call_checks"}, "not needed: GoModel checks model availability before routing"},
		{c.general, []string{"alerting", "alerting_threshold", "alert_types"}, "GoModel has no built-in alerting; alert on its Prometheus metrics"},
	}
	for _, note := range notes {
		for _, key := range note.keys {
			if _, ok := note.section.get(key); ok {
				c.report.info(note.section.name+"."+key, note.message)
			}
		}
	}
}

// convertEnvironmentVariables copies LiteLLM's environment_variables section
// into the generated .env file.
func (c *converter) convertEnvironmentVariables() {
	names := make([]string, 0, len(c.src.EnvironmentVariables))
	for name := range c.src.EnvironmentVariables {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		c.env.set(name, c.src.EnvironmentVariables[name])
	}
}

func number(raw any) (float64, bool) {
	switch v := raw.(type) {
	case int:
		return float64(v), true
	case float64:
		return v, true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		return f, err == nil
	}
	return 0, false
}
