package geminiapi

import (
	"bufio"
	"bytes"
	"cmp"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/streaming"
)

// chatChunk is the subset of an OpenAI chat.completion.chunk the stream
// converter consumes.
type chatChunk struct {
	ID    string `json:"id"`
	Model string `json:"model"`
	// Error is an upstream failure reported inside the stream
	// (data: {"error": {...}}).
	Error   *upstreamStreamError `json:"error"`
	Choices []struct {
		Delta struct {
			Content          string              `json:"content"`
			ReasoningContent string              `json:"reasoning_content"`
			Reasoning        string              `json:"reasoning"`
			ToolCalls        []chatToolCallDelta `json:"tool_calls"`
			// ExtraContent is provider replay state for the turn so far; a
			// Gemini text turn's thought signature arrives here.
			ExtraContent json.RawMessage `json:"extra_content"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *chatUsage `json:"usage"`
}

// upstreamStreamError is the error object an OpenAI-compatible upstream sends
// in place of a chunk when it fails mid-stream.
type upstreamStreamError struct {
	Message string `json:"message"`
}

type chatToolCallDelta struct {
	Index    int    `json:"index"`
	ID       string `json:"id"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
	// ExtraContent is provider replay state on the first delta of a tool call.
	ExtraContent json.RawMessage `json:"extra_content"`
}

type chatUsage struct {
	PromptTokens             int                           `json:"prompt_tokens"`
	CompletionTokens         int                           `json:"completion_tokens"`
	CacheCreationInputTokens int                           `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int                           `json:"cache_read_input_tokens"`
	PromptTokensDetails      *core.PromptTokensDetails     `json:"prompt_tokens_details"`
	CompletionTokensDetails  *core.CompletionTokensDetails `json:"completion_tokens_details"`
}

// streamedCall accumulates one tool call. Chat streams its arguments as
// string fragments, while Gemini sends a function call whole, so the call is
// held until the turn ends.
type streamedCall struct {
	id        string
	name      string
	arguments strings.Builder
	signature string
}

// NewStreamConverter wraps an OpenAI-style chat completion SSE stream and
// emits the equivalent streamGenerateContent SSE stream (alt=sse): one
// "data:" event per GenerateContentResponse. The returned reader owns body
// and closes it on Close.
func NewStreamConverter(body io.ReadCloser, model string, opts ResponseOptions) io.ReadCloser {
	return &streamConverter{
		reader: bufio.NewReader(body),
		body:   body,
		model:  model,
		opts:   opts,
		buffer: streaming.NewStreamBuffer(1024),
		calls:  make(map[int]*streamedCall),
	}
}

// streamConverter is an io.ReadCloser that converts a chat SSE stream into a
// Gemini SSE stream. It is single-reader and must not be shared.
type streamConverter struct {
	reader *bufio.Reader
	body   io.ReadCloser
	buffer streaming.StreamBuffer
	opts   ResponseOptions

	model      string
	responseID string
	calls      map[int]*streamedCall
	// signature is the latest thought signature of the text turn.
	signature string
	finish    string
	finished  bool
	usage     *chatUsage
	finalized bool
	closed    bool
	// endErr is what Read returns once a stream that stopped early has
	// emitted its error event.
	endErr error
	// upstreamErr is the error the upstream reported mid-stream, if any; it
	// becomes the error event instead of the generic incomplete-stream one.
	upstreamErr *upstreamStreamError
}

func (sc *streamConverter) Read(p []byte) (int, error) {
	if sc.buffer.Len() > 0 {
		return sc.buffer.Read(p), nil
	}
	// End-of-stream is signalled by io.EOF only. Ownership of the underlying
	// stream stays with Close so observers (request log, usage) still fire on
	// OnStreamClose; Read must never mark the converter closed.
	if sc.finalized || sc.closed {
		return 0, sc.endOfStream()
	}

	for {
		line, err := sc.reader.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 && sc.consumeLine(line) {
			sc.finish = cmp.Or(sc.finish, "stop")
			sc.finished = true
			sc.end(nil)
		}
		if err != nil {
			sc.end(err)
		}
		if sc.buffer.Len() > 0 {
			return sc.buffer.Read(p), nil
		}
		if sc.finalized {
			return 0, sc.endOfStream()
		}
	}
}

func (sc *streamConverter) endOfStream() error {
	if sc.endErr != nil {
		return sc.endErr
	}
	return io.EOF
}

func (sc *streamConverter) Close() error {
	if sc.closed {
		return nil
	}
	sc.closed = true
	sc.buffer.Release()
	return sc.body.Close()
}

// consumeLine parses a single SSE line. It returns true when the stream's
// terminal [DONE] sentinel is seen.
func (sc *streamConverter) consumeLine(line []byte) (done bool) {
	payload, found := bytes.CutPrefix(bytes.TrimSpace(line), []byte("data:"))
	if !found {
		return false
	}
	payload = bytes.TrimSpace(payload)
	if bytes.Equal(payload, []byte("[DONE]")) {
		return true
	}
	var chunk chatChunk
	if err := json.Unmarshal(payload, &chunk); err != nil {
		return false
	}
	sc.handleChunk(&chunk)
	return false
}

func (sc *streamConverter) handleChunk(chunk *chatChunk) {
	if chunk.Error != nil {
		sc.upstreamErr = chunk.Error
		return
	}
	sc.responseID = cmp.Or(sc.responseID, chunk.ID)
	sc.model = cmp.Or(chunk.Model, sc.model)
	if chunk.Usage != nil {
		sc.usage = chunk.Usage
	}
	for _, choice := range chunk.Choices {
		// "reasoning_content" wins over "reasoning" (Groq, OpenRouter), the
		// same precedence the streaming codec applies.
		if thinking := cmp.Or(choice.Delta.ReasoningContent, choice.Delta.Reasoning); thinking != "" && sc.opts.IncludeThoughts {
			sc.emitParts(ResponsePart{Text: &thinking, Thought: true})
		}
		if signature := signatureFromExtraContent(choice.Delta.ExtraContent); signature != "" {
			sc.signature = signature
		}
		if text := choice.Delta.Content; text != "" {
			sc.emitParts(ResponsePart{Text: &text})
		}
		for _, delta := range choice.Delta.ToolCalls {
			sc.handleToolCall(delta)
		}
		if choice.FinishReason != "" {
			sc.finish = choice.FinishReason
			sc.finished = true
		}
	}
}

func (sc *streamConverter) handleToolCall(delta chatToolCallDelta) {
	call, seen := sc.calls[delta.Index]
	if !seen {
		call = &streamedCall{}
		sc.calls[delta.Index] = call
	}
	call.id = cmp.Or(call.id, delta.ID)
	call.name = cmp.Or(call.name, delta.Function.Name)
	call.signature = cmp.Or(call.signature, signatureFromExtraContent(delta.ExtraContent))
	call.arguments.WriteString(delta.Function.Arguments)
}

// signatureFromExtraContent reads extra_content.google.thought_signature.
func signatureFromExtraContent(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var extra struct {
		Google googleExtraContent `json:"google"`
	}
	if err := json.Unmarshal(raw, &extra); err != nil {
		return ""
	}
	return extra.Google.ThoughtSignature
}

// end closes the converted stream once the upstream stops. An upstream that
// signalled completion ([DONE] or a finish_reason) ends with the final event:
// the function calls, the finish reason and the usage. One that stopped early
// or reported an error ends with an error event instead, so a truncated
// answer is not presented as a finished turn.
func (sc *streamConverter) end(err error) {
	if sc.finalized {
		return
	}
	sc.finalized = true
	if sc.finished && sc.upstreamErr == nil {
		sc.emitFinal()
		return
	}
	message := streaming.ErrStreamIncomplete.Error()
	if upstream := sc.upstreamErr; upstream != nil && strings.TrimSpace(upstream.Message) != "" {
		message = upstream.Message
		err = fmt.Errorf("upstream error: %s", upstream.Message)
	}
	sc.endErr = streaming.IncompleteStreamError(err)
	sc.emit(NewErrorResponse(http.StatusInternalServerError, message))
}

func (sc *streamConverter) emitFinal() {
	indexes := make([]int, 0, len(sc.calls))
	for index := range sc.calls {
		indexes = append(indexes, index)
	}
	slices.Sort(indexes)
	parts := make([]ResponsePart, 0, len(indexes))
	for _, index := range indexes {
		call := sc.calls[index]
		parts = append(parts, functionCallPart(call.id, call.name, call.arguments.String(), call.signature))
	}
	if len(parts) == 0 {
		empty := ""
		parts = append(parts, ResponsePart{Text: &empty, ThoughtSignature: sc.signature})
	}
	response := sc.response(parts)
	response.Candidates[0].FinishReason = finishReasonFromChat(sc.finish)
	if usage := sc.usage; usage != nil {
		reasoning := 0
		if usage.CompletionTokensDetails != nil {
			reasoning = usage.CompletionTokensDetails.ReasoningTokens
		}
		response.UsageMetadata = geminiUsage(usage.PromptTokens, usage.CompletionTokens, reasoning,
			usage.CacheReadInputTokens, usage.CacheCreationInputTokens, usage.PromptTokensDetails)
	}
	sc.emit(response)
}

func (sc *streamConverter) emitParts(parts ...ResponsePart) {
	sc.emit(sc.response(parts))
}

func (sc *streamConverter) response(parts []ResponsePart) *GenerateContentResponse {
	return &GenerateContentResponse{
		Candidates:   []Candidate{{Content: ResponseContent{Role: "model", Parts: parts}}},
		ModelVersion: sc.model,
		ResponseID:   sc.responseID,
	}
}

// emit appends one SSE event to the output buffer.
func (sc *streamConverter) emit(payload any) {
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}
	sc.buffer.AppendString("data: ")
	sc.buffer.AppendBytes(data)
	sc.buffer.AppendString("\n\n")
}
