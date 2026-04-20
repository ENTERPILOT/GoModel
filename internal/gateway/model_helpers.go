package gateway

import (
	"strings"

	"gomodel/internal/core"
)

// CloneChatRequestForStreamUsage clones chat stream options before usage mutation.
func CloneChatRequestForStreamUsage(req *core.ChatRequest) *core.ChatRequest {
	if req == nil {
		return nil
	}
	cloned := *req
	if req.StreamOptions != nil {
		streamOptions := *req.StreamOptions
		cloned.StreamOptions = &streamOptions
	}
	return &cloned
}

// CloneChatRequestForSelector clones a chat request for a concrete selector.
func CloneChatRequestForSelector(req *core.ChatRequest, selector core.ModelSelector) *core.ChatRequest {
	if req == nil {
		return nil
	}
	cloned := *req
	cloned.Model = selector.Model
	cloned.Provider = selector.Provider
	if req.Messages != nil {
		cloned.Messages = make([]core.Message, len(req.Messages))
		copy(cloned.Messages, req.Messages)
		for i := range cloned.Messages {
			if req.Messages[i].ToolCalls != nil {
				cloned.Messages[i].ToolCalls = make([]core.ToolCall, len(req.Messages[i].ToolCalls))
				copy(cloned.Messages[i].ToolCalls, req.Messages[i].ToolCalls)
			}
			cloned.Messages[i].ExtraFields = core.CloneUnknownJSONFields(req.Messages[i].ExtraFields)
		}
	}
	cloned.Tools = cloneToolMaps(req.Tools)
	if req.StreamOptions != nil {
		streamOptions := *req.StreamOptions
		cloned.StreamOptions = &streamOptions
	}
	if req.Reasoning != nil {
		reasoning := *req.Reasoning
		cloned.Reasoning = &reasoning
	}
	cloned.ExtraFields = core.CloneUnknownJSONFields(req.ExtraFields)
	return &cloned
}

// CloneResponsesRequestForSelector clones a Responses request for a concrete selector.
func CloneResponsesRequestForSelector(req *core.ResponsesRequest, selector core.ModelSelector) *core.ResponsesRequest {
	if req == nil {
		return nil
	}
	cloned := *req
	cloned.Model = selector.Model
	cloned.Provider = selector.Provider
	cloned.Tools = cloneToolMaps(req.Tools)
	if req.Metadata != nil {
		cloned.Metadata = make(map[string]string, len(req.Metadata))
		for key, value := range req.Metadata {
			cloned.Metadata[key] = value
		}
	}
	if req.StreamOptions != nil {
		streamOptions := *req.StreamOptions
		cloned.StreamOptions = &streamOptions
	}
	if req.Reasoning != nil {
		reasoning := *req.Reasoning
		cloned.Reasoning = &reasoning
	}
	cloned.ExtraFields = core.CloneUnknownJSONFields(req.ExtraFields)
	return &cloned
}

func cloneToolMaps(tools []map[string]any) []map[string]any {
	if tools == nil {
		return nil
	}

	cloned := make([]map[string]any, len(tools))
	for i, tool := range tools {
		if tool == nil {
			continue
		}
		cloned[i] = make(map[string]any, len(tool))
		for key, value := range tool {
			cloned[i][key] = value
		}
	}
	return cloned
}

// ProviderNameFromWorkflow returns the resolved configured provider name.
func ProviderNameFromWorkflow(workflow *core.Workflow) string {
	if workflow == nil || workflow.Resolution == nil {
		return ""
	}
	return strings.TrimSpace(workflow.Resolution.ProviderName)
}

func resolvedModelPrefix(workflow *core.Workflow, providerName string) string {
	name := strings.TrimSpace(providerName)
	if name != "" {
		return name
	}
	if workflow == nil || workflow.Resolution == nil {
		return ""
	}
	resolvedProviderName := strings.TrimSpace(workflow.Resolution.ProviderName)
	if resolvedProviderName != "" {
		return resolvedProviderName
	}
	return strings.TrimSpace(workflow.Resolution.ResolvedSelector.Provider)
}

// QualifyModelWithProvider prefixes a model with providerName when needed.
func QualifyModelWithProvider(model, providerName string) string {
	model = strings.TrimSpace(model)
	providerName = strings.TrimSpace(providerName)
	if model == "" {
		return ""
	}
	if providerName == "" || strings.HasPrefix(model, providerName+"/") {
		return model
	}
	return providerName + "/" + model
}

// QualifyExecutedModel returns the public executed model selector.
func QualifyExecutedModel(workflow *core.Workflow, model, providerName string) string {
	return QualifyModelWithProvider(model, resolvedModelPrefix(workflow, providerName))
}

// ResolvedModelFromWorkflow returns the resolved model or fallback.
func ResolvedModelFromWorkflow(workflow *core.Workflow, fallback string) string {
	fallback = strings.TrimSpace(fallback)
	if workflow == nil || workflow.Resolution == nil {
		return fallback
	}
	if resolvedModel := strings.TrimSpace(workflow.Resolution.ResolvedSelector.Model); resolvedModel != "" {
		return resolvedModel
	}
	return fallback
}

// ProviderTypeFromWorkflow returns the workflow provider type.
func ProviderTypeFromWorkflow(workflow *core.Workflow) string {
	if workflow == nil {
		return ""
	}
	return strings.TrimSpace(workflow.ProviderType)
}

func currentSelectorForWorkflow(workflow *core.Workflow, model, provider string) string {
	if workflow != nil && workflow.Resolution != nil {
		if resolved := strings.TrimSpace(workflow.Resolution.ResolvedQualifiedModel()); resolved != "" {
			return resolved
		}
	}
	selector, err := core.ParseModelSelector(model, provider)
	if err != nil {
		return strings.TrimSpace(model)
	}
	return selector.QualifiedModel()
}

// ResponseProviderType returns responseProvider when set, otherwise fallback.
func ResponseProviderType(fallback, responseProvider string) string {
	responseProvider = strings.TrimSpace(responseProvider)
	if responseProvider != "" {
		return responseProvider
	}
	return strings.TrimSpace(fallback)
}
