package providers

import (
	"sort"
	"strings"
	"time"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/core"
)

type configuredProviderModelsApplyReason string

const (
	configuredProviderModelsNotApplied    configuredProviderModelsApplyReason = ""
	configuredProviderModelsAllowlist     configuredProviderModelsApplyReason = "allowlist"
	configuredProviderModelsMerge         configuredProviderModelsApplyReason = "merge"
	configuredProviderModelsUpstreamError configuredProviderModelsApplyReason = "upstream_error"
	configuredProviderModelsUpstreamNil   configuredProviderModelsApplyReason = "upstream_nil"
	configuredProviderModelsUpstreamEmpty configuredProviderModelsApplyReason = "upstream_empty"
)

func normalizeConfiguredProviderModels(models []string) []string {
	if len(models) == 0 {
		return nil
	}

	seen := make(map[string]struct{}, len(models))
	normalized := make([]string, 0, len(models))
	for _, model := range models {
		model = strings.TrimSpace(model)
		if model == "" {
			continue
		}
		if _, exists := seen[model]; exists {
			continue
		}
		seen[model] = struct{}{}
		normalized = append(normalized, model)
	}
	if len(normalized) == 0 {
		return nil
	}
	return normalized
}

func applyConfiguredProviderModels(
	providerName string,
	providerType string,
	mode config.ConfiguredProviderModelsMode,
	configuredModels []string,
	upstream *core.ModelsResponse,
	upstreamErr error,
	fallbackCreated int64,
) (*core.ModelsResponse, configuredProviderModelsApplyReason) {
	if len(configuredModels) == 0 {
		return upstream, configuredProviderModelsNotApplied
	}

	mode = config.ResolveConfiguredProviderModelsMode(mode)
	if mode == config.ConfiguredProviderModelsModeAllowlist {
		return configuredProviderModelsResponse(providerName, providerType, configuredModels, upstream, fallbackCreated), configuredProviderModelsAllowlist
	}

	if upstreamErr != nil {
		return configuredProviderModelsResponse(providerName, providerType, configuredModels, upstream, fallbackCreated), configuredProviderModelsUpstreamError
	}
	if upstream == nil {
		return configuredProviderModelsResponse(providerName, providerType, configuredModels, upstream, fallbackCreated), configuredProviderModelsUpstreamNil
	}
	if len(upstream.Data) == 0 {
		return configuredProviderModelsResponse(providerName, providerType, configuredModels, upstream, fallbackCreated), configuredProviderModelsUpstreamEmpty
	}
	if mode == config.ConfiguredProviderModelsModeMerge {
		return mergeConfiguredProviderModelsResponse(providerName, providerType, configuredModels, upstream, fallbackCreated), configuredProviderModelsMerge
	}
	return upstream, configuredProviderModelsNotApplied
}

// configuredModelOwner picks the owned_by value for synthesized entries.
func configuredModelOwner(providerName, providerType string) string {
	owner := strings.TrimSpace(providerType)
	if owner == "" {
		owner = strings.TrimSpace(providerName)
	}
	return owner
}

func normalizeFallbackCreated(fallbackCreated int64) int64 {
	if fallbackCreated <= 0 {
		return time.Now().Unix()
	}
	return fallbackCreated
}

func synthesizedConfiguredModel(modelID, owner string, created int64) core.Model {
	return core.Model{
		ID:      modelID,
		Object:  "model",
		OwnedBy: owner,
		Created: created,
	}
}

// mergeConfiguredProviderModelsResponse unions a healthy upstream inventory
// with the configured list: upstream entries stay authoritative and configured
// models the upstream does not list are appended as synthesized entries, so
// models a provider serves without listing them remain routable.
func mergeConfiguredProviderModelsResponse(providerName, providerType string, configuredModels []string, upstream *core.ModelsResponse, fallbackCreated int64) *core.ModelsResponse {
	seen := make(map[string]struct{}, len(upstream.Data))
	data := make([]core.Model, 0, len(upstream.Data)+len(configuredModels))
	for _, model := range upstream.Data {
		modelID := strings.TrimSpace(model.ID)
		if modelID == "" {
			continue
		}
		seen[modelID] = struct{}{}
		data = append(data, model)
	}

	owner := configuredModelOwner(providerName, providerType)
	created := normalizeFallbackCreated(fallbackCreated)
	for _, modelID := range configuredModels {
		if _, ok := seen[modelID]; ok {
			continue
		}
		data = append(data, synthesizedConfiguredModel(modelID, owner, created))
	}

	return &core.ModelsResponse{
		Object: "list",
		Data:   data,
	}
}

func configuredProviderModelsResponse(providerName, providerType string, configuredModels []string, upstream *core.ModelsResponse, fallbackCreated int64) *core.ModelsResponse {
	byID := make(map[string]core.Model)
	if upstream != nil {
		for _, model := range upstream.Data {
			modelID := strings.TrimSpace(model.ID)
			if modelID == "" {
				continue
			}
			byID[modelID] = model
		}
	}

	owner := configuredModelOwner(providerName, providerType)
	fallbackCreated = normalizeFallbackCreated(fallbackCreated)

	data := make([]core.Model, 0, len(configuredModels))
	for _, modelID := range configuredModels {
		model, ok := byID[modelID]
		if !ok {
			model = synthesizedConfiguredModel(modelID, owner, fallbackCreated)
		} else {
			model.ID = strings.TrimSpace(model.ID)
			if model.ID == "" {
				model.ID = modelID
			}
			if strings.TrimSpace(model.Object) == "" {
				model.Object = "model"
			}
			if strings.TrimSpace(model.OwnedBy) == "" {
				model.OwnedBy = owner
			}
			if model.Created == 0 {
				model.Created = fallbackCreated
			}
		}
		data = append(data, model)
	}

	return &core.ModelsResponse{
		Object: "list",
		Data:   data,
	}
}

func modelsResponseFromProviderMap(providerModels map[string]*ModelInfo) *core.ModelsResponse {
	if len(providerModels) == 0 {
		return &core.ModelsResponse{Object: "list"}
	}
	modelIDs := make([]string, 0, len(providerModels))
	for modelID := range providerModels {
		modelIDs = append(modelIDs, modelID)
	}
	sort.Strings(modelIDs)

	data := make([]core.Model, 0, len(modelIDs))
	for _, modelID := range modelIDs {
		if info := providerModels[modelID]; info != nil {
			data = append(data, info.Model)
		}
	}
	return &core.ModelsResponse{
		Object: "list",
		Data:   data,
	}
}

func modelInfoMapFromResponse(resp *core.ModelsResponse, provider core.Provider, providerName, providerType string) map[string]*ModelInfo {
	out := make(map[string]*ModelInfo)
	if resp == nil {
		return out
	}
	for _, model := range resp.Data {
		modelID := strings.TrimSpace(model.ID)
		if modelID == "" {
			continue
		}
		model.ID = modelID
		out[modelID] = newModelInfo(model, provider, providerName, providerType)
	}
	return out
}

func rebuildGlobalModelMap(modelsByProvider map[string]map[string]*ModelInfo, providerOrderNames []string) map[string]*ModelInfo {
	global := make(map[string]*ModelInfo)
	seenProvider := make(map[string]struct{}, len(providerOrderNames))
	for _, providerName := range providerOrderNames {
		seenProvider[providerName] = struct{}{}
		addProviderModels(global, modelsByProvider[providerName])
	}

	remaining := make([]string, 0, len(modelsByProvider))
	for providerName := range modelsByProvider {
		if _, seen := seenProvider[providerName]; seen {
			continue
		}
		remaining = append(remaining, providerName)
	}
	sort.Strings(remaining)
	for _, providerName := range remaining {
		addProviderModels(global, modelsByProvider[providerName])
	}
	return global
}

func addProviderModels(global map[string]*ModelInfo, providerModels map[string]*ModelInfo) {
	if len(providerModels) == 0 {
		return
	}
	modelIDs := make([]string, 0, len(providerModels))
	for modelID := range providerModels {
		modelIDs = append(modelIDs, modelID)
	}
	sort.Strings(modelIDs)
	for _, modelID := range modelIDs {
		if _, exists := global[modelID]; exists {
			continue
		}
		if info := providerModels[modelID]; info != nil {
			global[modelID] = info
		}
	}
}
