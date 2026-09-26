package providers

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
)

// ResponsesProvider is the minimal interface needed by the shared Chat-to-Responses adapter.
// Any provider that supports Responses and StreamResponses can use the
// ChatViaResponses and StreamChatViaResponses helpers to implement the Chat Completions API.
type ResponsesProvider interface {
	Responses(ctx context.Context, req *core.ResponsesRequest) (*core.ResponsesResponse, error)
	StreamResponses(ctx context.Context, req *core.ResponsesRequest) (io.ReadCloser, error)
}

// ConvertChatRequestToResponses converts a ChatRequest to a ResponsesRequest.
// It validates the request first and returns an error naming the field when
// the request carries a parameter that has no Responses API equivalent.
func ConvertChatRequestToResponses(req *core.ChatRequest) (*core.ResponsesRequest, error) {
	if req == nil {
		return nil, core.NewInvalidRequestError("chat request is required", nil)
	}
	if err := validateChatRequestForResponsesTranslation(req); err != nil {
		return nil, err
	}

	tools, err := flattenChatToolsForResponses(req.Tools)
	if err != nil {
		return nil, err
	}
	toolChoice, err := flattenChatToolChoiceForResponses(req.ToolChoice)
	if err != nil {
		return nil, err
	}

	responsesReq := &core.ResponsesRequest{
		Model:             req.Model,
		Provider:          req.Provider,
		Tools:             tools,
		ToolChoice:        toolChoice,
		ParallelToolCalls: req.ParallelToolCalls,
		Temperature:       req.Temperature,
		TopP:              req.TopP,
		Stream:            req.Stream,
		Reasoning:         req.Reasoning,
		User:              req.User,
		ServiceTier:       req.ServiceTier,
		ExtraFields:       core.CloneUnknownJSONFields(req.ExtraFields),
		// Chat stream_options are not forwarded: Responses stream options
		// differ, and include_usage only decides whether the stream converter
		// emits a client-facing usage chunk.
	}

	// prediction and n have no Responses equivalent: prediction is a pure
	// speed hint and a validated n=1 is a no-op, so neither is rejected —
	// but both are stripped from the translated request rather than leaking
	// upstream as unknown fields.
	responsesReq.ExtraFields = responsesReq.ExtraFields.Without("prediction", "n")

	// An explicit max_completion_tokens extra wins over the mapped max_tokens,
	// mirroring the documented Bailian quirk. Both map to max_output_tokens.
	if raw := responsesReq.ExtraFields.Lookup("max_completion_tokens"); raw != nil {
		var maxCompletionTokens int
		if err := json.Unmarshal(raw, &maxCompletionTokens); err == nil {
			responsesReq.MaxOutputTokens = &maxCompletionTokens
			responsesReq.ExtraFields = responsesReq.ExtraFields.Without("max_completion_tokens")
		}
	}
	if responsesReq.MaxOutputTokens == nil && req.MaxTokens != nil {
		responsesReq.MaxOutputTokens = req.MaxTokens
	}

	// metadata is typed on ResponsesRequest but not on ChatRequest, so it
	// arrives as an extra. Lift it onto the typed field; a malformed value
	// stays in the extras as a passthrough instead of being dropped.
	if raw := responsesReq.ExtraFields.Lookup("metadata"); raw != nil {
		var metadata map[string]string
		if err := json.Unmarshal(raw, &metadata); err == nil {
			responsesReq.Metadata = metadata
			responsesReq.ExtraFields = responsesReq.ExtraFields.Without("metadata")
		}
	}

	// response_format becomes text.format, the exact inverse of
	// responsesTextFormatToChatResponseFormat.
	if raw := responsesReq.ExtraFields.Lookup("response_format"); raw != nil {
		text, err := chatResponseFormatToResponsesText(raw)
		if err != nil {
			return nil, err
		}
		responsesReq.Text = text
		responsesReq.ExtraFields = responsesReq.ExtraFields.Without("response_format")
	}

	input, instructions, err := ConvertMessagesToResponsesInput(req.Messages)
	if err != nil {
		return nil, err
	}
	responsesReq.Input = input
	responsesReq.Instructions = instructions

	return responsesReq, nil
}

// unsupportedChatResponsesTranslationExtraFields lists Chat Completions
// parameters with no Responses API equivalent. ChatRequest has no typed
// fields for them, so the request decoder always places them in ExtraFields;
// sweeping the extras covers both the typed and the untyped spelling.
var unsupportedChatResponsesTranslationExtraFields = []string{
	"logit_bias",
	"stop",
	"seed",
	"frequency_penalty",
	"presence_penalty",
	// Chat logprobs booleans do not map: Responses logprobs need "include"
	// values, and Responses-only upstreams such as Codex drop them.
	"logprobs",
	"top_logprobs",
	"modalities",
	"audio",
	// prediction and n are absent from this list: Responses has no
	// equivalent for either, so ConvertChatRequestToResponses strips them
	// from the translated request (prediction is a pure speed hint, a
	// validated n=1 a no-op) instead of rejecting them.
	"web_search_options",
	// Deprecated function_call/functions are superseded by tool_choice/tools.
	"function_call",
	"functions",
}

func validateChatRequestForResponsesTranslation(req *core.ChatRequest) error {
	if raw := req.ExtraFields.Lookup("n"); !core.IsJSONNull(raw) {
		var n float64
		if err := json.Unmarshal(raw, &n); err != nil || n != 1 {
			// Responses always returns a single output; n=1 is a harmless
			// no-op, anything else cannot be honored.
			return unsupportedChatResponsesTranslationField("n")
		}
	}
	for _, field := range unsupportedChatResponsesTranslationExtraFields {
		// An explicit JSON null spells "not set" on the wire, so it is
		// tolerated like an absent field rather than rejected.
		if raw := req.ExtraFields.Lookup(field); !core.IsJSONNull(raw) {
			return unsupportedChatResponsesTranslationField(field)
		}
	}
	return nil
}

func unsupportedChatResponsesTranslationField(field string) error {
	return core.NewInvalidRequestError(
		fmt.Sprintf("chat field %q is only supported by native Chat Completions providers; use an OpenAI-compatible provider or passthrough for this request", field),
		nil,
	)
}

// chatResponseFormatToResponsesText converts a Chat Completions response_format
// into the Responses "text" settings. Plain text yields nil (Responses
// default). Chat nests json_schema fields under a json_schema member, while
// the Responses API places them directly on the format object.
func chatResponseFormatToResponsesText(raw json.RawMessage) (any, error) {
	var format map[string]any
	if err := json.Unmarshal(raw, &format); err != nil {
		return nil, unsupportedChatResponsesTranslationField("response_format")
	}

	formatType, _ := format["type"].(string)
	switch strings.TrimSpace(formatType) {
	case "", "text":
		return nil, nil
	case "json_object":
		return map[string]any{"format": map[string]any{"type": "json_object"}}, nil
	case "json_schema":
		jsonSchema, ok := format["json_schema"].(map[string]any)
		if !ok {
			return nil, unsupportedChatResponsesTranslationField("response_format")
		}
		flattened := make(map[string]any, len(jsonSchema)+1)
		flattened["type"] = "json_schema"
		for key, value := range jsonSchema {
			flattened[key] = value
		}
		return map[string]any{"format": flattened}, nil
	default:
		return nil, unsupportedChatResponsesTranslationField("response_format")
	}
}

// flattenChatToolsForResponses flattens chat function tools
// ({type:"function", function:{...}}) into the Responses shape
// ({type:"function", name, ...}), the inverse of normalizeResponsesToolForChat.
// Non-function tools have no meaning on a chat request and are rejected.
func flattenChatToolsForResponses(tools []map[string]any) ([]map[string]any, error) {
	if len(tools) == 0 {
		return nil, nil
	}

	flattened := make([]map[string]any, 0, len(tools))
	for _, tool := range tools {
		toolType, _ := tool["type"].(string)
		if strings.TrimSpace(toolType) != "function" {
			return nil, unsupportedChatResponsesTranslationField("tools")
		}
		flattened = append(flattened, flattenChatToolForResponses(tool))
	}
	return flattened, nil
}

func flattenChatToolForResponses(tool map[string]any) map[string]any {
	if len(tool) == 0 {
		return tool
	}

	function, ok := tool["function"].(map[string]any)
	if !ok {
		// Already flat (Responses-shaped); pass through unchanged.
		return cloneStringAnyMap(tool)
	}

	flattened := cloneStringAnyMap(tool)
	delete(flattened, "function")
	for _, key := range []string{"name", "description", "parameters", "strict"} {
		delete(flattened, key)
		if value, ok := function[key]; ok {
			flattened[key] = value
		}
	}
	return flattened
}

// flattenChatToolChoiceForResponses maps a chat tool_choice onto the Responses
// shape: strings pass through, and {type:"function", function:{name}} flattens
// to {type:"function", name}, the inverse of normalizeResponsesToolChoiceForChat.
func flattenChatToolChoiceForResponses(choice any) (any, error) {
	if choice == nil {
		return nil, nil
	}
	if choiceString, ok := choice.(string); ok {
		return choiceString, nil
	}

	choiceMap, ok := choice.(map[string]any)
	if !ok {
		return nil, unsupportedChatResponsesTranslationField("tool_choice")
	}
	choiceType, _ := choiceMap["type"].(string)
	if strings.TrimSpace(choiceType) != "function" {
		// Chat tool_choice objects only ever name a function; anything else
		// cannot be honored on the translated path.
		return nil, unsupportedChatResponsesTranslationField("tool_choice")
	}

	function, ok := choiceMap["function"].(map[string]any)
	if !ok {
		// Already flat (Responses-shaped {type:"function", name}); pass through.
		return cloneStringAnyMap(choiceMap), nil
	}

	flattened := cloneStringAnyMap(choiceMap)
	delete(flattened, "function")
	if name, ok := function["name"]; ok {
		flattened["name"] = name
	}
	return flattened, nil
}

// ChatViaResponses implements the Chat Completions API by converting to/from Responses format.
// providerName attributes errors raised here, before the router stamps the
// response with its provider.
func ChatViaResponses(ctx context.Context, p ResponsesProvider, req *core.ChatRequest, providerName string) (*core.ChatResponse, error) {
	responsesReq, err := ConvertChatRequestToResponses(req)
	if err != nil {
		return nil, err
	}

	resp, err := p.Responses(ctx, responsesReq)
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return nil, core.NewEmptyProviderResponseError(providerName)
	}
	if resp.Status == "failed" || resp.Error != nil {
		// A failed Responses generation has no honest chat completion
		// shape, mirroring the stream converter's failed handling.
		message := "provider reported a failed response"
		var code string
		if resp.Error != nil {
			code = resp.Error.Code
			if strings.TrimSpace(resp.Error.Message) != "" {
				message = resp.Error.Message
			}
		}
		providerErr := core.NewProviderError(providerName, http.StatusBadGateway, message, nil)
		if code != "" {
			providerErr = providerErr.WithCode(code)
		}
		return nil, providerErr
	}

	chatResp := ConvertResponsesResponseToChat(resp)
	if chatResp == nil || len(chatResp.Choices) == 0 {
		return nil, core.NewNoChoicesProviderError(providerName)
	}
	return chatResp, nil
}

// StreamChatViaResponses implements streaming Chat Completions API by converting to/from Responses format.
func StreamChatViaResponses(ctx context.Context, p ResponsesProvider, req *core.ChatRequest, providerName string) (io.ReadCloser, error) {
	responsesReq, err := ConvertChatRequestToResponses(req)
	if err != nil {
		return nil, err
	}
	// The upstream call must stream: Responses-only providers emit SSE, and
	// the converter below turns it into chat completion chunks.
	responsesReq.Stream = true

	// stream_options is not forwarded; include_usage only decides whether the
	// converter emits a client-facing usage chunk after the terminal event.
	// The usage-enforcement flag forces the chunk, matching the mirror
	// direction (StreamResponsesViaChat forces upstream include_usage).
	includeUsage := (req.StreamOptions != nil && req.StreamOptions.IncludeUsage) || core.GetEnforceReturningUsageData(ctx)

	stream, err := p.StreamResponses(ctx, responsesReq)
	if err != nil {
		return nil, err
	}

	return NewOpenAIChatStreamConverter(stream, req.Model, providerName, includeUsage), nil
}
