package anthropic

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/goccy/go-json"

	"github.com/google/uuid"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/streaming"
)

// convertAnthropicResponseToResponses converts an Anthropic response to ResponsesResponse
func convertAnthropicResponseToResponses(resp *anthropicResponse, model string) *core.ResponsesResponse {
	content := extractTextContent(resp.Content)
	toolCalls := extractToolCalls(resp.Content)

	msg := core.ResponseMessage{
		Role:      "assistant",
		Content:   content,
		ToolCalls: toolCalls,
	}
	if thinking := extractThinkingContent(resp.Content); thinking != "" {
		if raw, err := json.Marshal(thinking); err == nil {
			msg.ExtraFields = core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{
				"reasoning_content": raw,
			})
		}
	}
	// The reasoning item carries the signatures Anthropic needs back; without
	// them a Responses client cannot continue a thinking conversation.
	msg.ExtraFields = withThinkingReplay(msg.ExtraFields, resp.Content)
	output := providers.BuildResponsesOutputItems(msg)

	return &core.ResponsesResponse{
		ID:        resp.ID,
		Object:    "response",
		CreatedAt: time.Now().Unix(),
		Model:     model,
		Status:    "completed",
		Output:    output,
		Usage:     buildAnthropicResponsesUsage(resp.Usage),
	}
}

// buildAnthropicResponsesUsage creates a ResponsesUsage from anthropicUsage, including RawUsage.
func buildAnthropicResponsesUsage(u anthropicUsage) *core.ResponsesUsage {
	usage := &core.ResponsesUsage{
		InputTokens:  u.InputTokens,
		OutputTokens: u.OutputTokens,
		TotalTokens:  u.InputTokens + u.OutputTokens,
	}
	rawUsage := buildAnthropicRawUsage(u)
	if len(rawUsage) > 0 {
		usage.RawUsage = rawUsage
	}
	return usage
}

func anthropicResponsesUsagePayload(usage *anthropicUsage) map[string]any {
	if usage == nil {
		return nil
	}

	payload := map[string]any{
		"input_tokens":  usage.InputTokens,
		"output_tokens": usage.OutputTokens,
		"total_tokens":  usage.InputTokens + usage.OutputTokens,
	}
	addAnthropicUsagePayloadDetails(payload, usage, "output_tokens_details")
	return payload
}

// Responses sends a Responses API request to Anthropic (converted to messages format)
func (p *Provider) Responses(ctx context.Context, req *core.ResponsesRequest) (*core.ResponsesResponse, error) {
	anthropicReq, err := convertResponsesRequestToAnthropic(req)
	if err != nil {
		return nil, err
	}

	var anthropicResp anthropicResponse
	err = p.client.Do(ctx, llmclient.Request{
		Method:    http.MethodPost,
		Endpoint:  "/messages",
		Operation: llmclient.OperationChat,
		Model:     req.Model,
		Body:      anthropicReq,
	}, &anthropicResp)
	if err != nil {
		return nil, err
	}

	return convertAnthropicResponseToResponses(&anthropicResp, req.Model), nil
}

// StreamResponses returns a raw response body for streaming Responses API (caller must close)
func (p *Provider) StreamResponses(ctx context.Context, req *core.ResponsesRequest) (io.ReadCloser, error) {
	anthropicReq, err := convertResponsesRequestToAnthropic(req)
	if err != nil {
		return nil, err
	}
	stream, err := p.openMessagesStream(ctx, anthropicReq, req.Model)
	if err != nil {
		return nil, err
	}

	// Return a reader that converts Anthropic SSE format to Responses API format
	return newResponsesStreamConverter(stream, req.Model, p.responseProviderName()), nil
}

// responsesStreamConverter wraps an Anthropic stream and converts it to Responses API format
type responsesStreamConverter struct {
	reader               *bufio.Reader
	body                 io.ReadCloser
	model                string
	providerName         string
	responseID           string
	createdAt            int64
	output               *providers.ResponsesOutputEventState
	nextOutputIndex      int
	assistantOutputIndex int
	reasoningOutputIndex int
	toolCalls            map[int]*providers.ResponsesOutputToolCallState
	thinking             thinkingReplayState
	buffer               streaming.StreamBuffer
	closed               bool
	sentCreate           bool
	sentDone             bool
	sawStop              bool  // upstream signalled the end of the message
	pendingErr           error // upstream read error deferred until terminal events are drained
	usage                anthropicUsage
	hasUsage             bool
}

func newResponsesStreamConverter(body io.ReadCloser, model, providerName string) *responsesStreamConverter {
	responseID := "resp_" + uuid.New().String()
	return &responsesStreamConverter{
		reader:       bufio.NewReader(body),
		body:         body,
		model:        model,
		providerName: providerName,
		responseID:   responseID,
		createdAt:    time.Now().Unix(),
		output:       providers.NewResponsesOutputEventState(responseID),
		toolCalls:    make(map[int]*providers.ResponsesOutputToolCallState),
		thinking:     newThinkingReplayState(),
		buffer:       streaming.NewStreamBuffer(1024),
	}
}

func (sc *responsesStreamConverter) Read(p []byte) (n int, err error) {
	if sc.closed {
		sc.releaseBuffer()
		return 0, io.EOF
	}

	// If we have buffered data, return it first
	if sc.buffer.Len() > 0 {
		return sc.buffer.Read(p), nil
	}

	// Terminal events for a failed upstream read have been drained; surface
	// the deferred error now.
	if sc.pendingErr != nil {
		pendingErr := sc.pendingErr
		sc.closed = true
		sc.releaseBuffer()
		_ = sc.body.Close() //nolint:errcheck
		if pendingErr == io.EOF {
			return 0, io.EOF
		}
		return 0, pendingErr
	}

	// Read the next SSE event from Anthropic
	for {
		line, err := sc.reader.ReadBytes('\n')
		if err != nil {
			// Any upstream read failure — clean EOF, io.ErrUnexpectedEOF from
			// a chunked body cut mid-transfer, a connection reset — ends the
			// upstream stream, so emit the terminal events before surfacing a
			// non-EOF error.
			sc.appendTerminalEvents()
			if sc.buffer.Len() > 0 {
				sc.pendingErr = err
				return sc.buffer.Read(p), nil
			}
			sc.closed = true
			sc.releaseBuffer()
			_ = sc.body.Close() //nolint:errcheck
			if err == io.EOF {
				return 0, io.EOF
			}
			return 0, err
		}

		n, handled, err := consumeAnthropicSSELine(p, line, sc.body, &sc.buffer, sc.convertEvent)
		if err != nil {
			sc.closed = true
			sc.releaseBuffer()
			return 0, err
		}
		if handled {
			if n == 0 {
				continue
			}
			return n, nil
		}
	}
}

func (sc *responsesStreamConverter) Close() error {
	if sc.closed {
		sc.releaseBuffer()
		return nil
	}
	sc.closed = true
	sc.releaseBuffer()
	return sc.body.Close()
}

func (sc *responsesStreamConverter) releaseBuffer() {
	sc.buffer.Release()
}

// reserveReasoningOutput claims the first output slot for the reasoning item.
// Anthropic emits thinking before any text or tool call, so reserving on the
// first thinking block gives it index 0.
func (sc *responsesStreamConverter) reserveReasoningOutput() {
	if sc.output.ReasoningReserved() {
		return
	}
	sc.output.ReserveReasoning()
	sc.reasoningOutputIndex = sc.nextOutputIndex
	sc.nextOutputIndex++
}

func (sc *responsesStreamConverter) reserveAssistantMessageOutput() {
	if sc.output.AssistantReserved() {
		return
	}
	sc.output.ReserveAssistant()
	sc.assistantOutputIndex = sc.nextOutputIndex
	sc.nextOutputIndex++
}

// appendTerminalEvents finalizes items still open when the upstream stream
// ends and appends the terminal event plus the trailing [DONE] marker exactly
// once. A stream that ends without Anthropic signalling the end of the message
// (message_stop or a stop_reason) was interrupted: it ends with
// response.incomplete instead of fabricating completion, and its open items
// close with status "incomplete".
func (sc *responsesStreamConverter) appendTerminalEvents() {
	if sc.sentDone {
		return
	}
	sc.sentDone = true
	// A body cut before message_start still owes the client the opening
	// events: stream helpers snapshot the created response before anything else.
	sc.buffer.AppendString(sc.startResponse())
	status := "completed"
	eventName := "response.completed"
	if !sc.sawStop {
		status = "incomplete"
		eventName = "response.incomplete"
	}
	// A stream cut after a thinking block's signature but before its stop
	// still holds a block Anthropic accepts back, and this is the client's
	// only chance to receive it.
	if extra := sc.thinking.extraContent(); extra != nil {
		sc.output.SetReasoningExtraContent(extra)
	}
	prefix := sc.output.FinishReasoningOutput(sc.reasoningOutputIndex, status) +
		sc.output.FinishAssistantOutput(sc.assistantOutputIndex, status) +
		sc.completePendingToolCalls(status)
	responseData := map[string]any{
		"id":         sc.responseID,
		"object":     "response",
		"status":     status,
		"model":      sc.model,
		"provider":   sc.providerName,
		"created_at": sc.createdAt,
		"output":     sc.output.FinalOutputItems(sc.reasoningOutputIndex, sc.assistantOutputIndex, sc.toolCalls, true),
	}
	if status == "incomplete" {
		responseData["incomplete_details"] = map[string]any{"reason": "interrupted"}
	}
	// Include merged usage data captured across message_start/message_delta.
	if sc.hasUsage {
		responseData["usage"] = anthropicResponsesUsagePayload(&sc.usage)
	}
	sc.buffer.AppendString(prefix)
	sc.buffer.AppendString(sc.output.FinishResponse(eventName, responseData))
}

// startResponse opens the stream with response.created and
// response.in_progress once.
func (sc *responsesStreamConverter) startResponse() string {
	if sc.sentCreate {
		return ""
	}
	sc.sentCreate = true
	return sc.output.StartResponse(map[string]any{
		"id":         sc.responseID,
		"object":     "response",
		"status":     "in_progress",
		"model":      sc.model,
		"provider":   sc.providerName,
		"created_at": sc.createdAt,
	})
}

// completePendingToolCalls emits the done events for tool calls the upstream
// stream left open, in content-block order, carrying the given terminal status.
func (sc *responsesStreamConverter) completePendingToolCalls(status string) string {
	indices := make([]int, 0, len(sc.toolCalls))
	for index := range sc.toolCalls {
		indices = append(indices, index)
	}
	slices.Sort(indices)

	var out strings.Builder
	for _, index := range indices {
		out.WriteString(sc.output.FinishToolCall(sc.toolCalls[index], status, true))
	}
	return out.String()
}

func (sc *responsesStreamConverter) newResponsesToolCallState(contentBlock *anthropicContent) *providers.ResponsesOutputToolCallState {
	callID := providers.ResponsesFunctionCallCallID(contentBlock.ID)
	state := &providers.ResponsesOutputToolCallState{
		CallID:      callID,
		Name:        contentBlock.Name,
		OutputIndex: sc.nextOutputIndex,
	}
	sc.nextOutputIndex++

	initialArguments := extractInitialToolArguments(contentBlock.Input)
	state.PlaceholderObject = initialArguments == "{}"
	if initialArguments != "" && !state.PlaceholderObject {
		_, _ = state.Arguments.WriteString(initialArguments)
	}

	return state
}

func (sc *responsesStreamConverter) convertEvent(event *anthropicStreamEvent) string {
	switch event.Type {
	case "message_start":
		if event.Message != nil {
			if mergeAnthropicUsage(&sc.usage, &event.Message.Usage) {
				sc.hasUsage = true
			}
		}
		if mergeAnthropicUsage(&sc.usage, event.Usage) {
			sc.hasUsage = true
		}
		return sc.startResponse()

	case "content_block_start":
		if sc.thinking.track(event.Index, event.ContentBlock) {
			// A redacted block has no readable text; its reasoning item
			// exists purely to carry the replay state.
			sc.reserveReasoningOutput()
			return ""
		}
		if event.ContentBlock != nil && event.ContentBlock.Type == "tool_use" {
			// Thinking precedes the tool call it led to, so the reasoning item
			// closes here; a thinking-enabled tool-use turn is the common case.
			prefix := sc.output.CompleteReasoningOutput(sc.reasoningOutputIndex)
			if sc.output.AssistantStarted() && !sc.output.AssistantDone() {
				prefix += sc.output.CompleteAssistantOutput(sc.assistantOutputIndex)
			}
			state := sc.newResponsesToolCallState(event.ContentBlock)
			sc.toolCalls[event.Index] = state
			return prefix + sc.output.StartToolCall(state, true)
		}
		return ""

	case "content_block_delta":
		if event.Delta == nil {
			return ""
		}

		switch event.Delta.Type {
		case "thinking_delta":
			if !sc.thinking.isThinking(event.Index) || event.Delta.Thinking == "" {
				return ""
			}
			sc.thinking.appendThinking(event.Index, event.Delta.Thinking)
			// A stream has one reasoning item, closed once text or a tool call
			// follows, and output_item.done cannot be taken back. Thinking
			// that arrives after that (interleaved thinking) adds no delta;
			// its text and signature still reach the client inside the
			// replay state of the terminal output.
			if sc.output.ReasoningDone() {
				return ""
			}
			return sc.output.AppendReasoningDelta(sc.reasoningOutputIndex, event.Delta.Thinking)
		case "signature_delta":
			// A signature has no field of its own in the Responses dialect; it
			// rides on the reasoning item as replay state instead.
			sc.thinking.appendSignature(event.Index, event.Delta.Signature)
			return ""
		case "text_delta":
			if event.Delta.Text != "" {
				prefix := sc.output.CompleteReasoningOutput(sc.reasoningOutputIndex)
				sc.reserveAssistantMessageOutput()
				return prefix + sc.output.AppendAssistantDelta(sc.assistantOutputIndex, event.Delta.Text)
			}
		case "input_json_delta":
			if event.Delta.PartialJSON == "" {
				return ""
			}
			state := sc.toolCalls[event.Index]
			// A delta for an already-closed call (stray event after its
			// content_block_stop) must not mutate the arguments its
			// output_item.done event declared.
			if state == nil || state.Completed {
				return ""
			}
			if state.PlaceholderObject {
				state.Arguments = strings.Builder{}
				state.PlaceholderObject = false
			}
			_, _ = state.Arguments.WriteString(event.Delta.PartialJSON)
			return sc.output.WriteEvent("response.function_call_arguments.delta", map[string]any{
				"type":         "response.function_call_arguments.delta",
				"item_id":      state.ItemID,
				"output_index": state.OutputIndex,
				"delta":        event.Delta.PartialJSON,
			})
		}
		return ""

	case "content_block_stop":
		if sc.thinking.tracked(event.Index) {
			// The block is complete, signature included, before whatever
			// closes the reasoning item can arrive.
			sc.output.SetReasoningExtraContent(sc.thinking.extraContent())
			return ""
		}
		state := sc.toolCalls[event.Index]
		return sc.output.CompleteToolCall(state, true)

	case "message_delta":
		// Capture usage data for inclusion in response.completed
		if mergeAnthropicUsage(&sc.usage, event.Usage) {
			sc.hasUsage = true
		}
		// A stop_reason means Anthropic finished generating, even if the
		// stream is cut before message_stop arrives.
		if event.Delta != nil && event.Delta.StopReason != "" {
			sc.sawStop = true
		}
		if !sc.output.AssistantReserved() && len(sc.toolCalls) == 0 {
			sc.reserveAssistantMessageOutput()
		}
		return ""

	case "message_stop":
		// Will be handled in Read() when we get EOF
		sc.sawStop = true
		return ""
	}

	return ""
}
