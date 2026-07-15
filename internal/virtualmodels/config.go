package virtualmodels

import (
	"strings"

	"github.com/enterpilot/gomodel/config"
)

// ConfigModels converts declarative config.yaml / VIRTUAL_MODELS entries into
// virtual model rows marked as managed. The rows are fully validated when the
// service builds its snapshot, so an invalid declaration fails startup loudly.
func ConfigModels(entries []config.VirtualModelConfig) []VirtualModel {
	if len(entries) == 0 {
		return nil
	}
	models := make([]VirtualModel, 0, len(entries))
	for _, entry := range entries {
		models = append(models, configModel(entry))
	}
	return models
}

func configModel(entry config.VirtualModelConfig) VirtualModel {
	enabled := true
	if entry.Enabled != nil {
		enabled = *entry.Enabled
	}
	return VirtualModel{
		Source:      entry.Source,
		Strategy:    entry.Strategy,
		Targets:     configTargets(entry),
		UserPaths:   entry.UserPaths,
		Description: entry.Description,
		Enabled:     enabled,
		Managed:     true,
	}
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
