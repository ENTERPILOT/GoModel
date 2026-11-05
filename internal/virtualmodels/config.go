package virtualmodels

import (
	"fmt"
	"strings"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/requestparams"
)

// ConfigModels converts declarative config.yaml / VIRTUAL_MODELS entries into
// virtual model rows marked as managed. The rows are fully validated when the
// service builds its snapshot, so an invalid declaration fails startup loudly;
// a parameter value that cannot be encoded (a YAML mapping with a non-string
// key) is reported here, naming the entry and the parameter.
func ConfigModels(entries []config.VirtualModelConfig) ([]VirtualModel, error) {
	if len(entries) == 0 {
		return nil, nil
	}
	models := make([]VirtualModel, 0, len(entries))
	for _, entry := range entries {
		model, err := configModel(entry)
		if err != nil {
			return nil, fmt.Errorf("virtual model %q: %w", entry.Source, err)
		}
		models = append(models, model)
	}
	return models, nil
}

func configModel(entry config.VirtualModelConfig) (VirtualModel, error) {
	enabled := true
	if entry.Enabled != nil {
		enabled = *entry.Enabled
	}
	params, err := requestparams.FromAny(entry.Params)
	if err != nil {
		return VirtualModel{}, fmt.Errorf("params: %w", err)
	}
	defaultParams, err := requestparams.FromAny(entry.DefaultParams)
	if err != nil {
		return VirtualModel{}, fmt.Errorf("default_params: %w", err)
	}
	return VirtualModel{
		Source:          entry.Source,
		Strategy:        entry.Strategy,
		StrategyPlugin:  entry.StrategyPlugin,
		StrategyConfig:  cloneStrategyConfig(entry.StrategyConfig),
		SessionAffinity: entry.SessionAffinity,
		Failover:        entry.Failover,
		Targets:         configTargets(entry),
		UserPaths:       entry.UserPaths,
		Description:     entry.Description,
		Slowdown:        entry.Slowdown,
		ReasoningEffort: entry.ReasoningEffort,
		Params:          params,
		DefaultParams:   defaultParams,
		Enabled:         enabled,
		Managed:         true,
	}, nil
}

// configTargets maps the explicit targets list, falling back to the single
// `target` shorthand. An empty result makes the entry an access policy.
func configTargets(entry config.VirtualModelConfig) []Target {
	if len(entry.Targets) > 0 {
		targets := make([]Target, 0, len(entry.Targets))
		for _, target := range entry.Targets {
			targets = append(targets, Target{
				Provider: target.Provider,
				Model:    target.Model,
				Weight:   target.Weight,
			})
		}
		return targets
	}
	if strings.TrimSpace(entry.Target) != "" {
		return []Target{{Model: entry.Target}}
	}
	return nil
}
