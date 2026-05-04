package gemini

import (
	"bufio"
	"encoding/json"
	"io"
	"strconv"
	"strings"
	"time"

	"gomodel/internal/core"
)

type geminiNativeStream struct {
	reader *io.PipeReader
	body   io.ReadCloser
}

func newGeminiNativeStream(body io.ReadCloser, model string, includeUsage bool) io.ReadCloser {
	pr, pw := io.Pipe()
	stream := &geminiNativeStream{reader: pr, body: body}
	go convertGeminiNativeStream(body, pw, model, includeUsage)
	return stream
}

func (s *geminiNativeStream) Read(p []byte) (int, error) {
	return s.reader.Read(p)
}

func (s *geminiNativeStream) Close() error {
	_ = s.reader.Close()
	return s.body.Close()
}

func convertGeminiNativeStream(body io.ReadCloser, out *io.PipeWriter, model string, includeUsage bool) {
	defer func() { _ = body.Close() }()

	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	state := geminiStreamState{
		model:        model,
		includeUsage: includeUsage,
		created:      time.Now().Unix(),
	}
	var data strings.Builder
	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r")
		if line == "" {
			if err := state.consumeEvent(out, data.String()); err != nil {
				_ = out.CloseWithError(err)
				return
			}
			data.Reset()
			continue
		}
		if strings.HasPrefix(line, "data:") {
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	if data.Len() > 0 {
		if err := state.consumeEvent(out, data.String()); err != nil {
			_ = out.CloseWithError(err)
			return
		}
	}
	if err := scanner.Err(); err != nil {
		_ = out.CloseWithError(err)
		return
	}
	if err := state.writeFinalUsage(out); err != nil {
		_ = out.CloseWithError(err)
		return
	}
	_, _ = io.WriteString(out, "data: [DONE]\n\n")
	_ = out.Close()
}

type geminiStreamState struct {
	model        string
	includeUsage bool
	created      int64
	responseID   string
	choices      map[int]*geminiChoiceStreamState
	stopped      bool
	latestUsage  map[string]any
}

type geminiChoiceStreamState struct {
	roleSent     bool
	sawToolCalls bool
}

func (s *geminiStreamState) consumeEvent(out io.Writer, raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "[DONE]" || s.stopped {
		return nil
	}

	var event geminiGenerateContentResponse
	if err := json.Unmarshal([]byte(raw), &event); err != nil {
		return nativeProviderError("failed to parse native Gemini stream event", err)
	}
	if s.responseID == "" {
		s.responseID = event.ResponseID
		if s.responseID == "" {
			s.responseID = "chatcmpl-gemini-" + strconv.FormatInt(s.created, 10)
		}
	}

	if err := geminiBlockedPromptError(&event); err != nil {
		s.stopped = true
		return writeOpenAIStreamError(out, err)
	}

	for i, candidate := range event.Candidates {
		choice, ok := s.chatChunkChoice(candidate, i)
		if !ok {
			continue
		}
		chunk := map[string]any{
			"id":       s.responseID,
			"object":   "chat.completion.chunk",
			"created":  s.created,
			"model":    s.model,
			"provider": "gemini",
			"choices":  []map[string]any{choice},
		}
		if err := writeOpenAIStreamChunk(out, chunk); err != nil {
			return err
		}
	}

	if s.includeUsage {
		s.latestUsage = geminiUsageMap(event.UsageMetadata)
	}
	return nil
}

func (s *geminiStreamState) writeFinalUsage(out io.Writer) error {
	if !s.includeUsage || s.latestUsage == nil || s.stopped {
		return nil
	}
	chunk := map[string]any{
		"id":       s.responseID,
		"object":   "chat.completion.chunk",
		"created":  s.created,
		"model":    s.model,
		"provider": "gemini",
		"choices":  []map[string]any{},
		"usage":    s.latestUsage,
	}
	return writeOpenAIStreamChunk(out, chunk)
}

func (s *geminiStreamState) chatChunkChoice(candidate geminiCandidate, fallbackIndex int) (map[string]any, bool) {
	index := streamChoiceIndex(candidate, fallbackIndex)
	state := s.choiceState(index)

	delta := make(map[string]any)
	if !state.roleSent {
		delta["role"] = "assistant"
		state.roleSent = true
	}

	content, toolCalls := openAIMessageFromGeminiParts(candidate.Content.Parts)
	if content != "" {
		delta["content"] = content
	}
	if len(toolCalls) > 0 {
		state.sawToolCalls = true
		delta["tool_calls"] = streamToolCalls(toolCalls)
	}

	finish := finishReasonFromGemini(candidate.FinishReason, state.sawToolCalls)
	if len(delta) == 0 && finish == "" {
		return nil, false
	}
	choice := map[string]any{
		"index":         index,
		"delta":         delta,
		"finish_reason": nil,
	}
	if finish != "" {
		choice["finish_reason"] = finish
	}
	return choice, true
}

func streamChoiceIndex(candidate geminiCandidate, fallbackIndex int) int {
	index := candidate.Index
	if index == 0 && fallbackIndex > 0 {
		index = fallbackIndex
	}
	return index
}

func (s *geminiStreamState) choiceState(index int) *geminiChoiceStreamState {
	if s.choices == nil {
		s.choices = make(map[int]*geminiChoiceStreamState)
	}
	state := s.choices[index]
	if state == nil {
		state = &geminiChoiceStreamState{}
		s.choices[index] = state
	}
	return state
}

func streamToolCalls(toolCalls []core.ToolCall) []map[string]any {
	out := make([]map[string]any, 0, len(toolCalls))
	for i, call := range toolCalls {
		out = append(out, map[string]any{
			"index": i,
			"id":    call.ID,
			"type":  "function",
			"function": map[string]any{
				"name":      call.Function.Name,
				"arguments": call.Function.Arguments,
			},
		})
	}
	return out
}

func geminiUsageMap(usage geminiUsageMetadata) map[string]any {
	coreUsage := usageFromGemini(usage)
	if coreUsage.PromptTokens == 0 && coreUsage.CompletionTokens == 0 && coreUsage.TotalTokens == 0 && len(coreUsage.RawUsage) == 0 {
		return nil
	}
	out := map[string]any{
		"prompt_tokens":     coreUsage.PromptTokens,
		"completion_tokens": coreUsage.CompletionTokens,
		"total_tokens":      coreUsage.TotalTokens,
	}
	if coreUsage.PromptTokensDetails != nil {
		out["prompt_tokens_details"] = coreUsage.PromptTokensDetails
	}
	if coreUsage.CompletionTokensDetails != nil {
		out["completion_tokens_details"] = coreUsage.CompletionTokensDetails
	}
	if len(coreUsage.RawUsage) > 0 {
		out["raw_usage"] = coreUsage.RawUsage
	}
	return out
}

func writeOpenAIStreamChunk(out io.Writer, chunk map[string]any) error {
	body, err := json.Marshal(chunk)
	if err != nil {
		return err
	}
	_, err = out.Write([]byte("data: " + string(body) + "\n\n"))
	return err
}

func writeOpenAIStreamError(out io.Writer, err *core.GatewayError) error {
	if err == nil {
		return nil
	}
	return writeOpenAIStreamChunk(out, err.ToJSON())
}
