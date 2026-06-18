package bedrock

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/goccy/go-json"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	brdoc "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/document"
	brtypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"

	"gomodel/internal/core"
)

// ChatCompletion runs a Bedrock Converse call and normalizes the result into
// an OpenAI-compatible chat response.
func (p *Provider) ChatCompletion(ctx context.Context, req *core.ChatRequest) (*core.ChatResponse, error) {
	if err := p.ready(); err != nil {
		return nil, err
	}
	if req == nil {
		return nil, core.NewInvalidRequestError("bedrock chat request is required", nil)
	}

	parts, err := buildConverseParts(req)
	if err != nil {
		return nil, err
	}

	out, err := p.runtime.Converse(ctx, &bedrockruntime.ConverseInput{
		ModelId:         parts.modelID,
		Messages:        parts.messages,
		System:          parts.system,
		InferenceConfig: parts.infCfg,
		ToolConfig:      parts.toolCfg,
	})
	if err != nil {
		return nil, mapAWSError(err)
	}

	return convertConverseOutput(req.Model, out), nil
}

// converseParts holds the shared pieces of a Converse / ConverseStream request
// so the two API entry points can stay otherwise independent.
type converseParts struct {
	modelID  *string
	system   []brtypes.SystemContentBlock
	messages []brtypes.Message
	infCfg   *brtypes.InferenceConfiguration
	toolCfg  *brtypes.ToolConfiguration
}

// buildConverseParts translates a core.ChatRequest into the Bedrock Converse
// shape. Tool definitions and tool_choice are forwarded only for models that
// accept them; unsupported families will surface their own validation errors.
func buildConverseParts(req *core.ChatRequest) (converseParts, error) {
	model := strings.TrimSpace(req.Model)
	if model == "" {
		return converseParts{}, core.NewInvalidRequestError("bedrock chat request requires a model", nil)
	}

	system, messages, err := convertMessages(req.Messages)
	if err != nil {
		return converseParts{}, err
	}

	parts := converseParts{
		modelID:  awssdk.String(model),
		messages: messages,
	}
	if len(system) > 0 {
		parts.system = system
	}

	infCfg := &brtypes.InferenceConfiguration{}
	hasInfCfg := false
	if mt := resolveMaxTokens(req); mt > 0 {
		if mt > math.MaxInt32 {
			return converseParts{}, core.NewInvalidRequestError(
				fmt.Sprintf("max_tokens %d exceeds the maximum of %d", mt, math.MaxInt32), nil)
		}
		infCfg.MaxTokens = awssdk.Int32(int32(mt))
		hasInfCfg = true
	}
	if req.Temperature != nil {
		infCfg.Temperature = awssdk.Float32(float32(*req.Temperature))
		hasInfCfg = true
	}
	if topP, ok := resolveTopP(req); ok {
		infCfg.TopP = awssdk.Float32(float32(topP))
		hasInfCfg = true
	}
	if hasInfCfg {
		parts.infCfg = infCfg
	}

	toolCfg, err := convertTools(req.Tools, req.ToolChoice)
	if err != nil {
		return converseParts{}, err
	}
	parts.toolCfg = toolCfg

	return parts, nil
}

func convertMessages(messages []core.Message) ([]brtypes.SystemContentBlock, []brtypes.Message, error) {
	system := make([]brtypes.SystemContentBlock, 0)
	out := make([]brtypes.Message, 0, len(messages))

	// Bedrock's Converse API requires strictly alternating user/assistant
	// turns. Two adjacent inputs with the same role (the common pattern
	// being a tool result followed by an additional user text message, or N
	// parallel tool results) must collapse into a single Bedrock turn whose
	// Content holds the union of their blocks.
	appendOrMerge := func(role brtypes.ConversationRole, content []brtypes.ContentBlock) {
		if len(content) == 0 {
			return
		}
		if n := len(out); n > 0 && out[n-1].Role == role {
			out[n-1].Content = append(out[n-1].Content, content...)
			return
		}
		out = append(out, brtypes.Message{Role: role, Content: content})
	}
	// flushToolResults emits the buffered tool-result blocks (if any) as a
	// user turn and returns nil so the caller's pending slice no longer
	// aliases the emitted message's Content backing array. Returning
	// blocks[:0] would let a subsequent append silently overwrite the first
	// element of the previously emitted message.
	flushToolResults := func(blocks []brtypes.ContentBlock) []brtypes.ContentBlock {
		if len(blocks) == 0 {
			return nil
		}
		appendOrMerge(brtypes.ConversationRoleUser, blocks)
		return nil
	}

	var pendingToolResults []brtypes.ContentBlock
	for _, msg := range messages {
		if msg.Role != "tool" {
			pendingToolResults = flushToolResults(pendingToolResults)
		}
		switch msg.Role {
		case "system", "developer":
			text := core.ExtractTextContent(msg.Content)
			if text == "" {
				continue
			}
			system = append(system, &brtypes.SystemContentBlockMemberText{Value: text})
		case "tool":
			block, err := convertToolResultMessage(msg)
			if err != nil {
				return nil, nil, err
			}
			pendingToolResults = append(pendingToolResults, block)
		case "user":
			text := core.ExtractTextContent(msg.Content)
			if text == "" {
				continue
			}
			appendOrMerge(brtypes.ConversationRoleUser,
				[]brtypes.ContentBlock{&brtypes.ContentBlockMemberText{Value: text}})
		case "assistant":
			blocks, err := convertAssistantMessage(msg)
			if err != nil {
				return nil, nil, err
			}
			appendOrMerge(brtypes.ConversationRoleAssistant, blocks)
		default:
			return nil, nil, core.NewInvalidRequestError("unsupported message role: "+msg.Role, nil)
		}
	}
	flushToolResults(pendingToolResults)

	return system, out, nil
}

func convertAssistantMessage(msg core.Message) ([]brtypes.ContentBlock, error) {
	blocks := make([]brtypes.ContentBlock, 0, 1+len(msg.ToolCalls))
	if text := core.ExtractTextContent(msg.Content); text != "" {
		blocks = append(blocks, &brtypes.ContentBlockMemberText{Value: text})
	}
	for _, call := range msg.ToolCalls {
		name := strings.TrimSpace(call.Function.Name)
		if name == "" {
			return nil, core.NewInvalidRequestError("assistant tool_call.function.name is required", nil)
		}
		toolID := strings.TrimSpace(call.ID)
		if toolID == "" {
			return nil, core.NewInvalidRequestError("assistant tool_call.id is required", nil)
		}
		input, err := decodeToolArgs(call.Function.Arguments)
		if err != nil {
			return nil, err
		}
		blocks = append(blocks, &brtypes.ContentBlockMemberToolUse{
			Value: brtypes.ToolUseBlock{
				Name:      awssdk.String(name),
				ToolUseId: awssdk.String(toolID),
				Input:     toDocument(input),
			},
		})
	}
	return blocks, nil
}

func convertToolResultMessage(msg core.Message) (brtypes.ContentBlock, error) {
	toolID := strings.TrimSpace(msg.ToolCallID)
	if toolID == "" {
		return nil, core.NewInvalidRequestError("tool message is missing tool_call_id", nil)
	}
	text := core.ExtractTextContent(msg.Content)
	content := []brtypes.ToolResultContentBlock{
		&brtypes.ToolResultContentBlockMemberText{Value: text},
	}
	return &brtypes.ContentBlockMemberToolResult{
		Value: brtypes.ToolResultBlock{
			ToolUseId: awssdk.String(toolID),
			Content:   content,
		},
	}, nil
}

func convertTools(tools []map[string]any, toolChoice any) (*brtypes.ToolConfiguration, error) {
	if len(tools) == 0 {
		return nil, nil
	}
	// Bedrock has no ToolChoiceMemberNone; passing tools with no choice would
	// let the model auto-call. Honor an explicit "none" by suppressing tools
	// entirely, matching OpenAI semantics.
	if isToolChoiceNone(toolChoice) {
		return nil, nil
	}
	out := &brtypes.ToolConfiguration{Tools: make([]brtypes.Tool, 0, len(tools))}
	for _, tool := range tools {
		toolType, _ := tool["type"].(string)
		if toolType != "function" {
			return nil, core.NewInvalidRequestError("unsupported tool type: "+toolType, nil)
		}
		fn, ok := tool["function"].(map[string]any)
		if !ok {
			return nil, core.NewInvalidRequestError("tool.function must be an object", nil)
		}
		name, _ := fn["name"].(string)
		if strings.TrimSpace(name) == "" {
			return nil, core.NewInvalidRequestError("tool.function.name is required", nil)
		}
		spec := brtypes.ToolSpecification{
			Name: awssdk.String(name),
		}
		if desc, _ := fn["description"].(string); desc != "" {
			spec.Description = awssdk.String(desc)
		}
		params, hasParams := fn["parameters"]
		if !hasParams || params == nil {
			params = map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			}
		}
		paramsObj, ok := params.(map[string]any)
		if !ok {
			return nil, core.NewInvalidRequestError("tool.function.parameters must be an object", nil)
		}
		spec.InputSchema = &brtypes.ToolInputSchemaMemberJson{Value: toDocument(paramsObj)}
		out.Tools = append(out.Tools, &brtypes.ToolMemberToolSpec{Value: spec})
	}

	choice, err := convertToolChoice(toolChoice)
	if err != nil {
		return nil, err
	}
	out.ToolChoice = choice
	return out, nil
}

func isToolChoiceNone(choice any) bool {
	switch c := choice.(type) {
	case string:
		return strings.TrimSpace(c) == "none"
	case map[string]any:
		t, _ := c["type"].(string)
		return t == "none"
	}
	return false
}

func convertToolChoice(choice any) (brtypes.ToolChoice, error) {
	switch c := choice.(type) {
	case nil:
		return nil, nil
	case string:
		switch strings.TrimSpace(c) {
		case "", "auto":
			return &brtypes.ToolChoiceMemberAuto{}, nil
		case "required":
			return &brtypes.ToolChoiceMemberAny{}, nil
		case "none":
			return nil, nil
		default:
			return nil, core.NewInvalidRequestError("unsupported tool_choice value: "+c, nil)
		}
	case map[string]any:
		t, _ := c["type"].(string)
		switch t {
		case "auto":
			return &brtypes.ToolChoiceMemberAuto{}, nil
		case "required", "any":
			return &brtypes.ToolChoiceMemberAny{}, nil
		case "function":
			fn, _ := c["function"].(map[string]any)
			name, _ := fn["name"].(string)
			if strings.TrimSpace(name) == "" {
				return nil, core.NewInvalidRequestError("tool_choice.function.name is required", nil)
			}
			return &brtypes.ToolChoiceMemberTool{
				Value: brtypes.SpecificToolChoice{Name: awssdk.String(name)},
			}, nil
		case "none":
			return nil, nil
		}
	}
	return nil, core.NewInvalidRequestError("unsupported tool_choice", nil)
}

// toDocument wraps an arbitrary Go value into the Bedrock document.Interface
// used by Converse for free-form JSON fields (tool input, tool schemas).
func toDocument(v any) brdoc.Interface {
	return brdoc.NewLazyDocument(v)
}

// resolveMaxTokens returns the caller-requested output token cap, falling back
// to OpenAI's max_completion_tokens (e.g. reasoning-model API style) when
// max_tokens is omitted. Returns 0 when neither is present so the caller can
// leave InferenceConfiguration.MaxTokens unset.
func resolveMaxTokens(req *core.ChatRequest) int {
	if req.MaxTokens != nil && *req.MaxTokens > 0 {
		return *req.MaxTokens
	}
	if raw := req.ExtraFields.Lookup("max_completion_tokens"); len(raw) > 0 {
		var n int
		if err := json.Unmarshal(raw, &n); err == nil && n > 0 {
			return n
		}
	}
	return 0
}

// resolveTopP extracts top_p from the typed request field, falling back to the
// catch-all map for older internal callers that still carry it as an extra.
func resolveTopP(req *core.ChatRequest) (float64, bool) {
	if req.TopP != nil {
		return *req.TopP, true
	}
	raw := req.ExtraFields.Lookup("top_p")
	if len(raw) == 0 {
		return 0, false
	}
	var v float64
	if err := json.Unmarshal(raw, &v); err != nil {
		return 0, false
	}
	return v, true
}

func decodeToolArgs(raw string) (any, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return map[string]any{}, nil
	}
	var out any
	if err := json.Unmarshal([]byte(trimmed), &out); err != nil {
		return nil, core.NewInvalidRequestError("tool_call.function.arguments must be valid JSON: "+err.Error(), err)
	}
	return out, nil
}

func convertConverseOutput(model string, out *bedrockruntime.ConverseOutput) *core.ChatResponse {
	resp := &core.ChatResponse{
		ID:       fmt.Sprintf("bedrock-%d", time.Now().UnixNano()),
		Object:   "chat.completion",
		Model:    model,
		Provider: providerName,
		Created:  time.Now().Unix(),
	}

	msg := core.ResponseMessage{Role: "assistant"}
	var textParts []string

	if out.Output != nil {
		if mem, ok := out.Output.(*brtypes.ConverseOutputMemberMessage); ok {
			for _, block := range mem.Value.Content {
				switch b := block.(type) {
				case *brtypes.ContentBlockMemberText:
					if b.Value != "" {
						textParts = append(textParts, b.Value)
					}
				case *brtypes.ContentBlockMemberToolUse:
					call, err := convertToolUseToCall(b.Value)
					if err == nil {
						msg.ToolCalls = append(msg.ToolCalls, call)
					}
				}
			}
		}
	}
	msg.Content = strings.Join(textParts, "\n\n")

	resp.Choices = []core.Choice{{
		Index:        0,
		Message:      msg,
		FinishReason: mapStopReason(out.StopReason, len(msg.ToolCalls) > 0),
	}}

	if out.Usage != nil {
		resp.Usage = core.Usage{
			PromptTokens:     int(awssdk.ToInt32(out.Usage.InputTokens)),
			CompletionTokens: int(awssdk.ToInt32(out.Usage.OutputTokens)),
			TotalTokens:      int(awssdk.ToInt32(out.Usage.TotalTokens)),
		}
		if raw := bedrockUsageExtras(out.Usage); len(raw) > 0 {
			resp.Usage.RawUsage = raw
		}
	}

	return resp
}

func convertToolUseToCall(use brtypes.ToolUseBlock) (core.ToolCall, error) {
	args := "{}"
	if use.Input != nil {
		// MarshalSmithyDocument returns canonical JSON for the input value,
		// which is what the OpenAI tool_calls.function.arguments field expects.
		if buf, err := use.Input.MarshalSmithyDocument(); err == nil && len(buf) > 0 {
			args = string(buf)
		}
	}
	return core.ToolCall{
		ID:   awssdk.ToString(use.ToolUseId),
		Type: "function",
		Function: core.FunctionCall{
			Name:      awssdk.ToString(use.Name),
			Arguments: args,
		},
	}, nil
}

// mapStopReason normalizes Bedrock StopReason to OpenAI finish_reason values.
// hasToolCalls allows distinguishing a tool_use stop that produced calls
// (mapped to tool_calls) from a malformed stream that did not.
func mapStopReason(sr brtypes.StopReason, hasToolCalls bool) string {
	switch sr {
	case brtypes.StopReasonEndTurn, brtypes.StopReasonStopSequence:
		return "stop"
	case brtypes.StopReasonMaxTokens, brtypes.StopReasonModelContextWindowExceeded:
		return "length"
	case brtypes.StopReasonToolUse:
		if hasToolCalls {
			return "tool_calls"
		}
		return string(sr)
	case "":
		return "stop"
	default:
		return string(sr)
	}
}

func bedrockUsageExtras(u *brtypes.TokenUsage) map[string]any {
	if u == nil {
		return nil
	}
	out := make(map[string]any)
	if u.CacheReadInputTokens != nil {
		out["cache_read_input_tokens"] = int(*u.CacheReadInputTokens)
	}
	if u.CacheWriteInputTokens != nil {
		out["cache_write_input_tokens"] = int(*u.CacheWriteInputTokens)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
