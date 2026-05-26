package providers

import (
	"context"
	"fmt"
	"io"
	"maps"
	"strings"

	"gomodel/internal/core"
)

// ChatProvider is the minimal interface needed by the shared Responses-to-Chat adapter.
// Any provider that supports ChatCompletion and StreamChatCompletion can use the
// ResponsesViaChat and StreamResponsesViaChat helpers to implement the Responses API.
type ChatProvider interface {
	ChatCompletion(ctx context.Context, req *core.ChatRequest) (*core.ChatResponse, error)
	StreamChatCompletion(ctx context.Context, req *core.ChatRequest) (io.ReadCloser, error)
}

// ConvertResponsesRequestToChat converts a ResponsesRequest to a ChatRequest.
// It also validates the supported Responses input shapes and returns an error
// when the request cannot be converted safely.
func ConvertResponsesRequestToChat(req *core.ResponsesRequest) (*core.ChatRequest, error) {
	if req == nil {
		return nil, core.NewInvalidRequestError("responses request is required", nil)
	}
	if err := validateResponsesRequestForChatTranslation(req); err != nil {
		return nil, err
	}

	chatReq := &core.ChatRequest{
		Model:             req.Model,
		Provider:          req.Provider,
		Messages:          make([]core.Message, 0),
		Tools:             normalizeResponsesToolsForChat(req.Tools),
		ToolChoice:        normalizeResponsesToolChoiceForChat(req.ToolChoice),
		ParallelToolCalls: req.ParallelToolCalls,
		Temperature:       req.Temperature,
		TopP:              req.TopP,
		Stream:            req.Stream,
		StreamOptions:     cloneStreamOptions(req.StreamOptions),
		Reasoning:         req.Reasoning,
		User:              req.User,
		ServiceTier:       req.ServiceTier,
		ExtraFields:       core.CloneUnknownJSONFields(req.ExtraFields),
	}

	if req.MaxOutputTokens != nil {
		chatReq.MaxTokens = req.MaxOutputTokens
	}

	if req.Instructions != "" {
		chatReq.Messages = append(chatReq.Messages, core.Message{
			Role:    "system",
			Content: req.Instructions,
		})
	}

	messages, err := ConvertResponsesInputToMessages(req.Input)
	if err != nil {
		return nil, err
	}
	chatReq.Messages = append(chatReq.Messages, messages...)

	return chatReq, nil
}

func validateResponsesRequestForChatTranslation(req *core.ResponsesRequest) error {
	if strings.TrimSpace(req.PreviousResponseID) != "" {
		return unsupportedResponsesChatTranslationField("previous_response_id")
	}
	if req.Conversation != nil {
		return unsupportedResponsesChatTranslationField("conversation")
	}
	if len(req.Include) > 0 {
		return unsupportedResponsesChatTranslationField("include")
	}
	if req.Prompt != nil {
		return unsupportedResponsesChatTranslationField("prompt")
	}
	if strings.TrimSpace(req.Truncation) != "" {
		return unsupportedResponsesChatTranslationField("truncation")
	}
	if req.Text != nil {
		return unsupportedResponsesChatTranslationField("text")
	}
	if strings.TrimSpace(req.PromptCacheRetention) != "" {
		return unsupportedResponsesChatTranslationField("prompt_cache_retention")
	}
	if req.ContextManagement != nil {
		return unsupportedResponsesChatTranslationField("context_management")
	}
	if req.TopLogprobs != nil {
		return unsupportedResponsesChatTranslationField("top_logprobs")
	}
	if strings.TrimSpace(req.SafetyIdentifier) != "" {
		return unsupportedResponsesChatTranslationField("safety_identifier")
	}
	return nil
}

func unsupportedResponsesChatTranslationField(field string) error {
	return core.NewInvalidRequestError(
		fmt.Sprintf("responses field %q is only supported by native Responses providers; use an OpenAI-compatible provider or passthrough for this request", field),
		nil,
	)
}

func cloneStreamOptions(src *core.StreamOptions) *core.StreamOptions {
	if src == nil {
		return nil
	}
	cloned := *src
	return &cloned
}

func normalizeResponsesToolsForChat(tools []map[string]any) []map[string]any {
	if len(tools) == 0 {
		return nil
	}

	normalized := make([]map[string]any, 0, len(tools))
	for _, tool := range tools {
		normalized = append(normalized, normalizeResponsesToolForChat(tool))
	}
	return normalized
}

func normalizeResponsesToolForChat(tool map[string]any) map[string]any {
	if len(tool) == 0 {
		return tool
	}

	toolType, _ := tool["type"].(string)
	if strings.TrimSpace(toolType) != "function" {
		return cloneStringAnyMap(tool)
	}
	if _, ok := tool["function"].(map[string]any); ok {
		return cloneStringAnyMap(tool)
	}

	normalized := cloneStringAnyMap(tool)
	function := map[string]any{}
	for _, key := range []string{"name", "description", "parameters", "strict"} {
		if value, ok := normalized[key]; ok {
			function[key] = value
			delete(normalized, key)
		}
	}
	if len(function) == 0 {
		return normalized
	}

	normalized["function"] = function
	return normalized
}

func normalizeResponsesToolChoiceForChat(choice any) any {
	choiceMap, ok := choice.(map[string]any)
	if !ok {
		return choice
	}

	choiceType, _ := choiceMap["type"].(string)
	if strings.TrimSpace(choiceType) != "function" {
		return choice
	}
	if _, ok := choiceMap["function"].(map[string]any); ok {
		return cloneStringAnyMap(choiceMap)
	}

	name, hasName := choiceMap["name"]
	if !hasName {
		return cloneStringAnyMap(choiceMap)
	}

	normalized := cloneStringAnyMap(choiceMap)
	delete(normalized, "name")
	normalized["function"] = map[string]any{"name": name}
	return normalized
}

func cloneStringAnyMap(src map[string]any) map[string]any {
	if src == nil {
		return nil
	}
	dst := make(map[string]any, len(src))
	maps.Copy(dst, src)
	return dst
}

// ResponsesViaChat implements the Responses API by converting to/from Chat format.
func ResponsesViaChat(ctx context.Context, p ChatProvider, req *core.ResponsesRequest) (*core.ResponsesResponse, error) {
	chatReq, err := ConvertResponsesRequestToChat(req)
	if err != nil {
		return nil, err
	}

	chatResp, err := p.ChatCompletion(ctx, chatReq)
	if err != nil {
		return nil, err
	}

	return ConvertChatResponseToResponses(chatResp), nil
}

// StreamResponsesViaChat implements streaming Responses API by converting to/from Chat format.
func StreamResponsesViaChat(ctx context.Context, p ChatProvider, req *core.ResponsesRequest, providerName string) (io.ReadCloser, error) {
	chatReq, err := ConvertResponsesRequestToChat(req)
	if err != nil {
		return nil, err
	}
	if core.GetEnforceReturningUsageData(ctx) {
		if chatReq.StreamOptions == nil {
			chatReq.StreamOptions = &core.StreamOptions{}
		}
		chatReq.StreamOptions.IncludeUsage = true
	}

	stream, err := p.StreamChatCompletion(ctx, chatReq)
	if err != nil {
		return nil, err
	}

	return NewOpenAIResponsesStreamConverter(stream, req.Model, providerName), nil
}
