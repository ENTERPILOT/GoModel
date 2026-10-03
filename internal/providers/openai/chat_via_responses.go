package openai

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"maps"
	"strings"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
)

// OpenAI Chat Completions rejects or silently degrades some requests that the
// Responses API serves fully. For those, the provider sends the chat request to
// /v1/responses and renders the result back as a chat completion. Everything
// else stays on Chat Completions, and a request whose fields the translation
// cannot carry exactly stays there too, so routing never loses data.

// ChatCompletion serves the request through Chat Completions, or through the
// Responses API when Chat Completions cannot serve it (see needsResponses).
// Stop sequences the model rejects are emulated (see takeStopSequences).
func (p *Provider) ChatCompletion(ctx context.Context, req *core.ChatRequest) (*core.ChatResponse, error) {
	req, stops, err := takeStopSequences(req)
	if err != nil {
		return nil, err
	}
	var resp *core.ChatResponse
	if responsesReq, ok := responsesRoute(req); ok {
		routed, err := p.Responses(ctx, responsesReq)
		if err != nil {
			return nil, err
		}
		resp, err = chatResponseFromResponses(routed, p.providerName)
		if err != nil {
			return nil, err
		}
	} else if resp, err = p.CompatibleProvider.ChatCompletion(ctx, req); err != nil {
		return nil, err
	}
	applyStopSequences(resp, stops)
	return resp, nil
}

// StreamChatCompletion is ChatCompletion for streaming requests; a request
// served through the Responses API is streamed back as chat completion chunks.
func (p *Provider) StreamChatCompletion(ctx context.Context, req *core.ChatRequest) (io.ReadCloser, error) {
	req, stops, err := takeStopSequences(req)
	if err != nil {
		return nil, err
	}
	var stream io.ReadCloser
	if responsesReq, ok := responsesRoute(req); ok {
		routed, err := p.StreamResponses(ctx, responsesReq)
		if err != nil {
			return nil, err
		}
		includeUsage := req.StreamOptions != nil && req.StreamOptions.IncludeUsage
		stream = newResponsesChatStream(routed, p.providerName, req.Model, includeUsage)
	} else if stream, err = p.CompatibleProvider.StreamChatCompletion(ctx, req); err != nil {
		return nil, err
	}
	if len(stops) > 0 {
		stream = newStopSequenceStream(stream, stops)
	}
	return stream, nil
}

// responsesRoute returns the Responses request for a chat request that must
// be served through the Responses API and can be translated exactly.
func responsesRoute(req *core.ChatRequest) (*core.ResponsesRequest, bool) {
	if !needsResponses(req) {
		return nil, false
	}
	responsesReq, err := chatToResponsesRequest(req)
	if err != nil {
		slog.Debug("chat request stays on Chat Completions", "model", req.Model, "reason", err)
		return nil, false
	}
	return responsesReq, true
}

// errNotTranslatable marks a chat request the Responses translation cannot
// represent exactly; such a request is sent to Chat Completions unchanged.
var errNotTranslatable = errors.New("chat request not translatable to the Responses API")

// chatPassthroughFields are chat request members the Responses API accepts
// under the same name and meaning.
var chatPassthroughFields = []string{"prompt_cache_key", "prompt_cache_retention", "safety_identifier", "store", "metadata"}

// chatMappedFields are chat request members the translation maps onto
// Responses fields.
var chatMappedFields = []string{"max_completion_tokens", "reasoning_effort", "verbosity", "response_format"}

// needsResponses reports whether a chat request must be served through the
// Responses API:
//   - function tools with reasoning on GPT-5.6+: Chat Completions accepts tools
//     there only with reasoning_effort "none", which gpt-6-astra and gpt-6.1
//     reject outright;
//   - tool results carrying images or files, which Chat Completions accepts
//     but the model never sees;
//   - file parts given by URL, which Chat Completions does not accept.
//
// The content cases apply to every OpenAI model name; models that predate
// the Responses API cannot take images or files on Chat Completions either.
func needsResponses(req *core.ChatRequest) bool {
	if req == nil {
		return false
	}
	if len(req.Tools) > 0 && restrictsChatTools(req.Model) {
		effort := requestedEffort(req)
		if rejectsNoReasoning(req.Model) || (effort != "" && effort != "none") {
			return true
		}
	}
	return isOpenAIModelName(req.Model) && hasContentChatCannotCarry(req.Messages)
}

// isOpenAIModelName reports whether the model is named like an OpenAI chat
// model, so a custom endpoint configured as this provider keeps its own
// model names on Chat Completions.
func isOpenAIModelName(model string) bool {
	m := strings.ToLower(strings.TrimSpace(model))
	return strings.HasPrefix(m, "gpt-") || strings.HasPrefix(m, "chatgpt-") || isOSeriesModel(m)
}

// carriesChatOnlyFields reports whether a message, part or tool call has
// members beyond the ones the translation maps (for example a speaker name).
// Other providers' replay state (extra_content) is removed by the router
// before the request reaches this provider.
func carriesChatOnlyFields(fields core.UnknownJSONFields) bool {
	return !fields.Without(core.ExtraContentField).IsEmpty()
}

// requestedEffort returns the reasoning effort the caller asked for, from the
// nested reasoning object or the flat reasoning_effort member.
func requestedEffort(req *core.ChatRequest) string {
	if req.Reasoning != nil {
		if effort := strings.TrimSpace(req.Reasoning.Effort); effort != "" {
			return effort
		}
	}
	var effort string
	if raw := req.ExtraFields.Lookup("reasoning_effort"); len(raw) > 0 && json.Unmarshal(raw, &effort) == nil {
		return strings.TrimSpace(effort)
	}
	return ""
}

// hasContentChatCannotCarry reports whether any message holds content Chat
// Completions rejects or hides from the model: non-text tool results, or files
// given by URL.
func hasContentChatCannotCarry(messages []core.Message) bool {
	for _, msg := range messages {
		content, err := core.NormalizeMessageContent(msg.Content)
		if err != nil {
			continue
		}
		parts, _ := content.([]core.ContentPart)
		for _, part := range parts {
			if msg.Role == "tool" && part.Type != "text" {
				return true
			}
			if part.Type == "file" && part.File != nil && strings.TrimSpace(part.File.FileURL) != "" {
				return true
			}
		}
	}
	return false
}

// chatToResponsesRequest translates a chat request into a Responses request.
// It returns errNotTranslatable for anything it cannot carry exactly.
// The request is not stored on OpenAI's side (store: false), matching Chat
// Completions, unless the caller set store.
func chatToResponsesRequest(req *core.ChatRequest) (*core.ResponsesRequest, error) {
	if !req.ExtraFields.Without(append(chatPassthroughFields, chatMappedFields...)...).IsEmpty() {
		return nil, errNotTranslatable
	}
	input, err := responsesInput(req.Messages)
	if err != nil {
		return nil, err
	}
	tools, err := responsesTools(req.Tools)
	if err != nil {
		return nil, err
	}
	toolChoice, err := responsesToolChoice(req.ToolChoice)
	if err != nil {
		return nil, err
	}
	out := &core.ResponsesRequest{
		Model:             req.Model,
		Input:             input,
		Tools:             tools,
		ToolChoice:        toolChoice,
		ParallelToolCalls: req.ParallelToolCalls,
		Temperature:       samplingForResponses(req, req.Temperature),
		TopP:              samplingForResponses(req, req.TopP),
		MaxOutputTokens:   req.MaxTokens,
		Stream:            req.Stream,
		User:              req.User,
		ServiceTier:       req.ServiceTier,
		Store:             new(false),
	}
	if err := applyMappedChatFields(out, req); err != nil {
		return nil, err
	}
	passthrough := map[string]json.RawMessage{}
	for _, name := range chatPassthroughFields {
		if raw := req.ExtraFields.Lookup(name); len(raw) > 0 {
			passthrough[name] = raw
		}
	}
	if store, ok := passthrough["store"]; ok {
		var stored bool
		if err := json.Unmarshal(store, &stored); err != nil {
			return nil, errNotTranslatable
		}
		out.Store = &stored
		delete(passthrough, "store")
	}
	if len(passthrough) > 0 {
		out.ExtraFields = core.UnknownJSONFieldsFromMap(passthrough)
	}
	return out, nil
}

// samplingForResponses keeps a temperature or top_p value only where the
// model accepts it, as the chat path does: always for non-reasoning models,
// and for reasoning models only with reasoning turned off.
func samplingForResponses(req *core.ChatRequest, value *float64) *float64 {
	if isReasoningChatModel(req.Model) && !reasoningOff(supportedEffort(req.Model, requestedEffort(req))) {
		return nil
	}
	return value
}

// applyMappedChatFields sets the Responses fields that chat members map onto:
// the token limit, reasoning effort, and text verbosity and format.
func applyMappedChatFields(out *core.ResponsesRequest, req *core.ChatRequest) error {
	if raw := req.ExtraFields.Lookup("max_completion_tokens"); len(raw) > 0 {
		var limit int
		if err := json.Unmarshal(raw, &limit); err != nil {
			return errNotTranslatable
		}
		out.MaxOutputTokens = &limit
	}
	if effort := requestedEffort(req); effort != "" {
		out.Reasoning = &core.Reasoning{Effort: supportedEffort(req.Model, effort)}
	}
	text := map[string]any{}
	if raw := req.ExtraFields.Lookup("verbosity"); len(raw) > 0 {
		var verbosity string
		if err := json.Unmarshal(raw, &verbosity); err != nil {
			return errNotTranslatable
		}
		text["verbosity"] = verbosity
	}
	if raw := req.ExtraFields.Lookup("response_format"); len(raw) > 0 {
		format, err := responsesTextFormat(raw)
		if err != nil {
			return err
		}
		text["format"] = format
	}
	if len(text) > 0 {
		out.Text = text
	}
	return nil
}

// responsesTextFormat maps a chat response_format onto Responses text.format,
// which carries the json_schema members flat instead of nested.
func responsesTextFormat(raw json.RawMessage) (map[string]any, error) {
	var format struct {
		Type       string         `json:"type"`
		JSONSchema map[string]any `json:"json_schema"`
	}
	if err := json.Unmarshal(raw, &format); err != nil {
		return nil, errNotTranslatable
	}
	switch format.Type {
	case "text", "json_object":
		return map[string]any{"type": format.Type}, nil
	case "json_schema":
		out := map[string]any{"type": "json_schema"}
		maps.Copy(out, format.JSONSchema)
		return out, nil
	default:
		return nil, errNotTranslatable
	}
}

// responsesInput translates chat messages into Responses input items. An
// assistant message becomes its text plus one function_call item per tool
// call; a tool message becomes a function_call_output item.
func responsesInput(messages []core.Message) ([]any, error) {
	input := make([]any, 0, len(messages))
	for _, msg := range messages {
		content, err := core.NormalizeMessageContent(msg.Content)
		if err != nil || carriesChatOnlyFields(msg.ExtraFields) {
			return nil, errNotTranslatable
		}
		switch msg.Role {
		case "system", "developer", "user":
			parts, err := responsesInputContent(content)
			if err != nil {
				return nil, err
			}
			input = append(input, map[string]any{"role": msg.Role, "content": parts})
		case "assistant":
			if !textOnly(content) {
				return nil, errNotTranslatable
			}
			if text := core.ExtractTextContent(content); text != "" {
				input = append(input, map[string]any{
					"role":    "assistant",
					"content": []any{map[string]any{"type": "output_text", "text": text}},
				})
			}
			for _, call := range msg.ToolCalls {
				if (call.Type != "" && call.Type != "function") ||
					carriesChatOnlyFields(call.ExtraFields) || carriesChatOnlyFields(call.Function.ExtraFields) {
					return nil, errNotTranslatable
				}
				input = append(input, map[string]any{
					"type":      "function_call",
					"call_id":   call.ID,
					"name":      call.Function.Name,
					"arguments": call.Function.Arguments,
				})
			}
		case "tool":
			output, err := functionCallOutput(content)
			if err != nil {
				return nil, err
			}
			input = append(input, map[string]any{"type": "function_call_output", "call_id": msg.ToolCallID, "output": output})
		default:
			return nil, errNotTranslatable
		}
	}
	return input, nil
}

// textOnly reports whether content is a string or text parts with no members
// the translation does not carry.
func textOnly(content any) bool {
	parts, ok := content.([]core.ContentPart)
	if !ok {
		return true
	}
	for _, part := range parts {
		if part.Type != "text" || carriesChatOnlyFields(part.ExtraFields) {
			return false
		}
	}
	return true
}

// functionCallOutput renders a tool result: a string stays a string, while a
// list of parts (text, images, files) stays a list, so attachments are visible
// to the model and part boundaries are kept.
func functionCallOutput(content any) (any, error) {
	if text, ok := content.(string); ok {
		return text, nil
	}
	return responsesInputContent(content)
}

// responsesInputContent translates message content into Responses input
// content parts.
func responsesInputContent(content any) ([]any, error) {
	if text, ok := content.(string); ok {
		return []any{map[string]any{"type": "input_text", "text": text}}, nil
	}
	parts, _ := content.([]core.ContentPart)
	out := make([]any, 0, len(parts))
	for _, part := range parts {
		if carriesChatOnlyFields(part.ExtraFields) {
			return nil, errNotTranslatable
		}
		switch part.Type {
		case "text":
			out = append(out, map[string]any{"type": "input_text", "text": part.Text})
		case "image_url":
			if part.ImageURL == nil || carriesChatOnlyFields(part.ImageURL.ExtraFields) {
				return nil, errNotTranslatable
			}
			detail := part.ImageURL.Detail
			if detail == "" {
				detail = "auto"
			}
			out = append(out, map[string]any{"type": "input_image", "image_url": part.ImageURL.URL, "detail": detail})
		case "file":
			if !core.ValidFilePayload(part.File) || carriesChatOnlyFields(part.File.ExtraFields) {
				return nil, errNotTranslatable
			}
			file := map[string]any{"type": "input_file"}
			if part.File.FileData != "" && part.File.Filename == "" {
				// OpenAI rejects inline file data without a filename.
				file["filename"] = core.DefaultFilename(part.File.FileData)
			}
			for key, value := range map[string]string{
				"file_data": part.File.FileData,
				"file_url":  part.File.FileURL,
				"file_id":   part.File.FileID,
				"filename":  part.File.Filename,
			} {
				if value != "" {
					file[key] = value
				}
			}
			out = append(out, file)
		default:
			return nil, errNotTranslatable
		}
	}
	return out, nil
}

// responsesTools translates chat function tools into the flat Responses
// shape. Responses treats a function tool as strict unless told otherwise,
// while Chat Completions does not, so strict is always set explicitly.
func responsesTools(tools []map[string]any) ([]map[string]any, error) {
	if len(tools) == 0 {
		return nil, nil
	}
	out := make([]map[string]any, 0, len(tools))
	for _, tool := range tools {
		function, ok := tool["function"].(map[string]any)
		if tool["type"] != "function" || !ok {
			return nil, errNotTranslatable
		}
		converted := map[string]any{"type": "function", "strict": false}
		maps.Copy(converted, function)
		out = append(out, converted)
	}
	return out, nil
}

// responsesToolChoice translates a chat tool_choice. Mode strings carry over;
// a named function choice loses its nested function object.
func responsesToolChoice(choice any) (any, error) {
	switch c := choice.(type) {
	case nil:
		return nil, nil
	case string:
		return c, nil
	case map[string]any:
		function, ok := c["function"].(map[string]any)
		if c["type"] != "function" || !ok {
			return nil, errNotTranslatable
		}
		return map[string]any{"type": "function", "name": function["name"]}, nil
	default:
		// A typed choice from an internal caller: decode it into the map form.
		raw, err := json.Marshal(choice)
		var decoded map[string]any
		if err != nil || json.Unmarshal(raw, &decoded) != nil || decoded == nil {
			return nil, errNotTranslatable
		}
		return responsesToolChoice(decoded)
	}
}
