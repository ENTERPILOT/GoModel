package providers

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/goccy/go-json"

	"github.com/google/uuid"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/streaming"
)

// maxResponsesStreamEventBytes caps one upstream Responses SSE event.
// Reasoning summaries and encrypted reasoning blobs are large, so the
// scanner default is too small; this mirrors the chatgpt provider's line cap.
const maxResponsesStreamEventBytes = 8 << 20

// OpenAIChatStreamConverter wraps a Responses API SSE stream and converts it
// to OpenAI chat.completion.chunk SSE. It serves providers whose upstream
// speaks only the Responses API (the ChatGPT Codex backend).
//
// The converter is a state machine over the upstream event types, the
// inverse of OpenAIResponsesStreamConverter:
//   - response.created emits the first chunk with delta {role: "assistant"}.
//   - response.output_item.added announces items; function_call items get a
//     dense 0-based chat tool_calls[].index in arrival order (the upstream
//     output_index counts reasoning and message items too, so it is not the
//     chat index) and emit a start chunk with id/name.
//   - output_text / refusal / reasoning deltas map to delta.content,
//     delta.refusal, and the reasoning_content extension.
//   - function_call_arguments.delta maps to a tool_calls delta carrying only
//     the arguments fragment.
//   - response.completed / response.incomplete emit the finish chunk
//     (finish_reason from the terminal response's status and output, never
//     re-emitting that output as content), an optional usage chunk when the
//     chat request asked for it, and [DONE].
//   - response.failed, an error event, or a stream that ends without a
//     terminal event emit the repo's in-band error convention and Read
//     returns a streaming.ErrStreamIncomplete error; finish_reason "stop"
//     is never reported for an interrupted or failed stream.
//
// Events are classified on the JSON "type" field of the data payload only;
// SSE "event:" lines are never relied on (some proxies strip them).
type OpenAIChatStreamConverter struct {
	reader       io.ReadCloser
	model        string
	provider     string
	includeUsage bool
	// chatID is the client-facing ID minted once at construction and carried
	// by every chunk. The upstream resp_ ID never reaches the chat client.
	chatID  string
	created int64

	scanner streaming.EventScanner
	buffer  streaming.StreamBuffer
	readBuf []byte

	sentRole        bool
	items           map[string]*chatStreamItemState
	itemsByIndex    map[int]*chatStreamItemState
	nextToolCallIdx int
	finished        bool // terminal success events (finish chunk, usage, [DONE]) emitted
	failed          bool // in-band error emitted
	closed          bool
	endErr          error // returned by Read once the error bytes are drained
}

// NewOpenAIChatStreamConverter creates a converter that transforms a
// Responses API SSE stream into OpenAI chat.completion.chunk SSE. The
// returned reader owns reader and closes it on Close.
func NewOpenAIChatStreamConverter(reader io.ReadCloser, model, provider string, includeUsage bool) io.ReadCloser {
	return &OpenAIChatStreamConverter{
		reader:       reader,
		model:        model,
		provider:     provider,
		includeUsage: includeUsage,
		chatID:       "chatcmpl-" + uuid.New().String(),
		created:      time.Now().Unix(),
		scanner:      streaming.EventScanner{MaxEventBytes: maxResponsesStreamEventBytes},
		buffer:       streaming.NewStreamBuffer(4096),
		readBuf:      make([]byte, 4096),
		items:        make(map[string]*chatStreamItemState),
		itemsByIndex: make(map[int]*chatStreamItemState),
	}
}

// chatStreamItemState tracks one upstream output item: for function_call
// items the dense chat tool_calls[].index plus the id and name the start
// chunk announced.
type chatStreamItemState struct {
	toolIndex int
	callID    string
	name      string
	started   bool
}

// responsesStreamEventView decodes the members of a Responses API stream
// event the converter classifies on.
type responsesStreamEventView struct {
	Type        string          `json:"type"`
	Delta       string          `json:"delta"`
	ItemID      string          `json:"item_id"`
	OutputIndex int             `json:"output_index"`
	Item        json.RawMessage `json:"item"`
	Response    json.RawMessage `json:"response"`
	// Code and Message belong to the top-level "error" event.
	Code    string `json:"code"`
	Message string `json:"message"`
}

// responsesStreamItemView decodes the item of a response.output_item.added
// event.
type responsesStreamItemView struct {
	ID     string `json:"id"`
	Type   string `json:"type"`
	CallID string `json:"call_id"`
	Name   string `json:"name"`
}

// responsesStreamErrorView decodes the error member of a failed terminal
// response.
type responsesStreamErrorView struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// responsesTerminalResponseView decodes the response object carried by the
// terminal response.completed / response.incomplete / response.failed
// events. Only the members deciding the chat finish_reason and usage chunk
// are read; the full output is never re-emitted after its deltas.
type responsesTerminalResponseView struct {
	Status string `json:"status"`
	Output []struct {
		Type string `json:"type"`
	} `json:"output"`
	IncompleteDetails *struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details"`
	Error *responsesStreamErrorView   `json:"error"`
	Usage *responsesTerminalUsageView `json:"usage"`
}

// responsesTerminalUsageView decodes the usage object of a terminal
// Responses event, with the Responses API field names.
type responsesTerminalUsageView struct {
	InputTokens         int                           `json:"input_tokens"`
	OutputTokens        int                           `json:"output_tokens"`
	TotalTokens         int                           `json:"total_tokens"`
	InputTokensDetails  *core.PromptTokensDetails     `json:"input_tokens_details"`
	OutputTokensDetails *core.CompletionTokensDetails `json:"output_tokens_details"`
}

// chatCompletionStreamUsage is the conservative Chat Completions
// representation of a Responses API usage object, renamed from
// input/output_tokens to prompt/completion_tokens.
type chatCompletionStreamUsage struct {
	PromptTokens            int                           `json:"prompt_tokens"`
	CompletionTokens        int                           `json:"completion_tokens"`
	TotalTokens             int                           `json:"total_tokens"`
	PromptTokensDetails     *core.PromptTokensDetails     `json:"prompt_tokens_details,omitempty"`
	CompletionTokensDetails *core.CompletionTokensDetails `json:"completion_tokens_details,omitempty"`
}

type chatCompletionStreamChunk struct {
	ID       string                       `json:"id"`
	Object   string                       `json:"object"`
	Created  int64                        `json:"created"`
	Model    string                       `json:"model"`
	Provider string                       `json:"provider,omitempty"`
	Choices  []chatCompletionStreamChoice `json:"choices"`
	Usage    *chatCompletionStreamUsage   `json:"usage,omitempty"`
}

type chatCompletionStreamChoice struct {
	Index int `json:"index"`
	// Delta is a map so the chunk can carry the extension members the repo
	// relays (reasoning_content) alongside the spec members.
	Delta        map[string]any `json:"delta"`
	FinishReason *string        `json:"finish_reason"`
}

// processEvent translates one upstream SSE event into chat chunks appended
// to the output buffer.
func (sc *OpenAIChatStreamConverter) processEvent(raw streaming.RawEvent) {
	if sc.finished || sc.failed || raw.Comment || raw.Oversized {
		return
	}
	data := bytes.TrimSpace(raw.Data)
	if len(data) == 0 || data[0] != '{' {
		return
	}
	var event responsesStreamEventView
	if err := json.Unmarshal(data, &event); err != nil {
		return
	}
	switch event.Type {
	case "response.created":
		sc.handleCreated(event.Response)
	case "response.in_progress", "response.queued":
		// response.in_progress duplicates the response.created payload.
	case "response.output_item.added":
		sc.handleItemAdded(event.OutputIndex, event.Item)
	case "response.output_text.delta":
		sc.emitDelta(map[string]any{"content": event.Delta})
	case "response.refusal.delta":
		sc.emitDelta(map[string]any{"refusal": event.Delta})
	case "response.reasoning_text.delta", "response.reasoning_summary_text.delta":
		sc.emitDelta(map[string]any{"reasoning_content": event.Delta})
	case "response.function_call_arguments.delta":
		sc.handleArgumentsDelta(event.ItemID, event.OutputIndex, event.Delta)
	case "response.completed", "response.incomplete":
		sc.handleTerminal(event.Type, event.Response)
	case "response.failed":
		sc.handleFailed(event.Response)
	case "error":
		sc.failUpstream(event.Code, event.Message)
	}
	// Everything else (content_part.*, *.done, output_item.done, annotation
	// events, hosted-tool items) carries nothing the deltas did not already
	// deliver.
}

// handleCreated takes model and created from the response.created payload
// when present and emits the first chunk announcing the assistant role.
func (sc *OpenAIChatStreamConverter) handleCreated(raw json.RawMessage) {
	var response struct {
		Model     string `json:"model"`
		CreatedAt int64  `json:"created_at"`
	}
	_ = json.Unmarshal(raw, &response)
	if response.Model != "" {
		sc.model = response.Model
	}
	if response.CreatedAt != 0 {
		sc.created = response.CreatedAt
	}
	sc.ensureRoleChunk()
}

// ensureRoleChunk emits the first chunk carrying delta {role: "assistant"}
// exactly once, ahead of the first delta the client sees.
func (sc *OpenAIChatStreamConverter) ensureRoleChunk() {
	if sc.sentRole || sc.finished || sc.failed {
		return
	}
	sc.sentRole = true
	sc.emitChunk(map[string]any{"role": "assistant"}, nil)
}

func (sc *OpenAIChatStreamConverter) emitDelta(delta map[string]any) {
	sc.ensureRoleChunk()
	sc.emitChunk(delta, nil)
}

// handleItemAdded registers an announced output item by item_id and
// output_index. A function_call item claims the next dense chat
// tool_calls[].index and emits its start chunk (id, type, name, empty
// arguments); the chat id is the item's call_id, which clients echo back as
// tool_call_id.
func (sc *OpenAIChatStreamConverter) handleItemAdded(outputIndex int, raw json.RawMessage) {
	var item responsesStreamItemView
	if err := json.Unmarshal(raw, &item); err != nil {
		return
	}
	state := sc.registerItem(item.ID, outputIndex, item.Type)
	if item.Type != "function_call" {
		return
	}
	state.callID = item.CallID
	state.name = item.Name
	sc.emitToolCallStart(state)
}

// registerItem records an output item, claiming a dense tool-call index for
// function_call items.
func (sc *OpenAIChatStreamConverter) registerItem(id string, outputIndex int, itemType string) *chatStreamItemState {
	state := &chatStreamItemState{toolIndex: -1}
	if itemType == "function_call" {
		state.toolIndex = sc.nextToolCallIdx
		sc.nextToolCallIdx++
	}
	if id != "" {
		sc.items[id] = state
	}
	sc.itemsByIndex[outputIndex] = state
	return state
}

// handleArgumentsDelta emits one tool-call delta carrying the arguments
// fragment under the item's dense chat index. Deltas of parallel calls may
// interleave; each carries its item_id, so they never share an index.
func (sc *OpenAIChatStreamConverter) handleArgumentsDelta(itemID string, outputIndex int, delta string) {
	state := sc.items[itemID]
	if state == nil {
		state = sc.itemsByIndex[outputIndex]
	}
	if state == nil {
		// A delta for an item the stream never announced: register it so the
		// arguments still land under a stable dense index (Postel's law).
		state = sc.registerItem(itemID, outputIndex, "function_call")
	}
	if state.toolIndex < 0 {
		return
	}
	sc.emitToolCallStart(state)
	if delta == "" {
		return
	}
	sc.emitDelta(map[string]any{"tool_calls": []any{map[string]any{
		"index":    state.toolIndex,
		"function": map[string]any{"arguments": delta},
	}}})
}

// emitToolCallStart emits the first chunk of a function_call item exactly
// once, carrying the dense index, the call id, and the function name with
// empty arguments.
func (sc *OpenAIChatStreamConverter) emitToolCallStart(state *chatStreamItemState) {
	if state.started {
		return
	}
	state.started = true
	function := map[string]any{"arguments": ""}
	if state.name != "" {
		function["name"] = state.name
	}
	call := map[string]any{
		"index":    state.toolIndex,
		"type":     "function",
		"function": function,
	}
	if state.callID != "" {
		call["id"] = state.callID
	}
	sc.emitDelta(map[string]any{"tool_calls": []any{call}})
}

// handleTerminal inspects the terminal event's response object, emits the
// finish chunk, then the usage chunk when the chat request asked for usage,
// then [DONE]. The role chunk goes out first when the stream saw neither
// response.created nor any delta, so clients always see a role ahead of the
// finish. The finish reason mirrors responsesChatFinishReason on the
// non-streaming path: incomplete reasons without a chat equivalent yield a
// null finish_reason rather than an invented one. The terminal event's full
// output is never re-emitted as content; the deltas already carried it.
func (sc *OpenAIChatStreamConverter) handleTerminal(eventType string, raw json.RawMessage) {
	var response responsesTerminalResponseView
	if err := json.Unmarshal(raw, &response); err != nil {
		// An unreadable terminal payload tells nothing; the stream ending
		// without a usable terminal event is reported as truncated.
		return
	}
	if response.Status == "" {
		// The event type carries the status when the payload omits it.
		response.Status = strings.TrimPrefix(eventType, "response.")
	}
	if response.Status == "failed" {
		sc.failTerminalError(response.Error)
		return
	}
	sc.ensureRoleChunk()
	sc.finished = true
	sc.emitChunk(map[string]any{}, terminalFinishReason(&response))
	if sc.includeUsage && response.Usage != nil {
		sc.emitUsage(response.Usage)
	}
	sc.buffer.AppendString("data: [DONE]\n\n")
}

// terminalFinishReason maps the terminal response's status onto a chat
// finish reason, returning nil when no honest mapping exists.
func terminalFinishReason(response *responsesTerminalResponseView) *string {
	switch response.Status {
	case "completed":
		for _, item := range response.Output {
			if item.Type == "function_call" {
				reason := "tool_calls"
				return &reason
			}
		}
		reason := "stop"
		return &reason
	case "incomplete":
		if response.IncompleteDetails == nil {
			return nil
		}
		switch response.IncompleteDetails.Reason {
		case "max_output_tokens":
			reason := "length"
			return &reason
		case "content_filter":
			reason := "content_filter"
			return &reason
		}
	}
	return nil
}

// emitUsage emits one chunk with an empty choices array carrying the usage
// renamed to the Chat Completions field names.
func (sc *OpenAIChatStreamConverter) emitUsage(usage *responsesTerminalUsageView) {
	sc.emitPayload(chatCompletionStreamChunk{
		ID:       sc.chatID,
		Object:   "chat.completion.chunk",
		Created:  sc.created,
		Model:    sc.model,
		Provider: sc.provider,
		Choices:  []chatCompletionStreamChoice{},
		Usage: &chatCompletionStreamUsage{
			PromptTokens:            usage.InputTokens,
			CompletionTokens:        usage.OutputTokens,
			TotalTokens:             usage.TotalTokens,
			PromptTokensDetails:     usage.InputTokensDetails,
			CompletionTokensDetails: usage.OutputTokensDetails,
		},
	})
}

// handleFailed ends the stream on a response.failed event: the terminal
// response's error member describes the failure.
func (sc *OpenAIChatStreamConverter) handleFailed(raw json.RawMessage) {
	var response struct {
		Error *responsesStreamErrorView `json:"error"`
	}
	_ = json.Unmarshal(raw, &response)
	sc.failTerminalError(response.Error)
}

func (sc *OpenAIChatStreamConverter) failTerminalError(upstreamError *responsesStreamErrorView) {
	code, message := "", ""
	if upstreamError != nil {
		code, message = upstreamError.Code, upstreamError.Message
	}
	sc.failUpstream(code, message)
}

// failUpstream emits the in-band error event for a stream the provider
// failed. Like appendFailedEvents in the inverse converter, an upstream that
// names no code gets "provider_error".
func (sc *OpenAIChatStreamConverter) failUpstream(code, message string) {
	if sc.finished || sc.failed {
		return
	}
	sc.failed = true
	if code == "" {
		code = "provider_error"
	}
	if strings.TrimSpace(message) == "" {
		message = "provider stream failed"
	}
	sc.emitError(code, message)
	sc.endErr = streaming.IncompleteStreamError(errors.New(message))
}

// failTruncated ends a stream that stopped before its terminal event with
// the repo's stream-error convention (#1017): an in-band error event, then
// Read returns the read failure wrapped in streaming.ErrStreamIncomplete.
func (sc *OpenAIChatStreamConverter) failTruncated(err error) {
	if sc.finished || sc.failed {
		return
	}
	sc.failed = true
	sc.endErr = streaming.IncompleteStreamError(err)
	sc.emitError("stream_incomplete", streaming.ErrStreamIncomplete.Error())
}

// emitError renders the chat dialect's in-band error event, the same shape
// the server's completion guard appends to a truncated chat stream.
func (sc *OpenAIChatStreamConverter) emitError(code, message string) {
	sc.emitPayload(map[string]any{"error": map[string]any{
		"type":    string(core.ErrorTypeProvider),
		"message": message,
		"param":   nil,
		"code":    code,
	}})
}

func (sc *OpenAIChatStreamConverter) emitChunk(delta map[string]any, finishReason *string) {
	if delta == nil {
		delta = map[string]any{}
	}
	sc.emitPayload(chatCompletionStreamChunk{
		ID:       sc.chatID,
		Object:   "chat.completion.chunk",
		Created:  sc.created,
		Model:    sc.model,
		Provider: sc.provider,
		Choices: []chatCompletionStreamChoice{
			{Index: 0, Delta: delta, FinishReason: finishReason},
		},
	})
}

// emitPayload marshals payload as one SSE event and appends it to the output
// buffer. Chat chunks carry no SSE "event:" line.
func (sc *OpenAIChatStreamConverter) emitPayload(payload any) {
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}
	event := streaming.Event{Data: data}
	sc.buffer.AppendBytes(event.Encode())
}

func (sc *OpenAIChatStreamConverter) Read(p []byte) (int, error) {
	if sc.closed {
		return 0, io.EOF
	}
	if sc.buffer.Len() > 0 {
		return sc.buffer.Read(p), nil
	}
	if sc.endErr != nil {
		return sc.closeRead(sc.endErr)
	}
	if sc.finished {
		return sc.closeRead(io.EOF)
	}

	nr, readErr := sc.reader.Read(sc.readBuf)
	if nr > 0 {
		for _, raw := range sc.scanner.Feed(sc.readBuf[:nr]) {
			sc.processEvent(raw)
		}
	}
	if readErr != nil {
		// The upstream stream ended; flush a trailing unterminated event,
		// then finish. A stream that saw no terminal event is truncated,
		// whatever the read error (a clean EOF included).
		for _, raw := range sc.scanner.Flush() {
			sc.processEvent(raw)
		}
		if !sc.finished && !sc.failed {
			sc.failTruncated(readErr)
		}
		if sc.buffer.Len() > 0 {
			return sc.buffer.Read(p), nil
		}
		if sc.endErr != nil {
			return sc.closeRead(sc.endErr)
		}
		return sc.closeRead(io.EOF)
	}
	if sc.buffer.Len() > 0 {
		return sc.buffer.Read(p), nil
	}

	// No data yet, try again
	return 0, nil
}

// closeRead ends the Read side: buffers are released and the upstream reader
// closed before err (io.EOF or the stream error) is returned.
func (sc *OpenAIChatStreamConverter) closeRead(err error) (int, error) {
	sc.closed = true
	sc.buffer.Release()
	_ = sc.reader.Close()
	return 0, err
}

func (sc *OpenAIChatStreamConverter) Close() error {
	if sc.closed {
		return nil
	}
	sc.closed = true
	sc.buffer.Release()
	return sc.reader.Close()
}
