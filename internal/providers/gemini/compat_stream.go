package gemini

import (
	"bufio"
	"io"
	"strings"

	"github.com/goccy/go-json"
)

// newCompatToolCallStream repairs the tool-call chunks of a stream from
// Google's OpenAI-compatible endpoint. That endpoint omits the "index" member
// of streamed tool_calls deltas, which OpenAI SDK helpers key their
// accumulation on, and finishes a tool-call turn with "stop" rather than
// "tool_calls". Each tool call arrives whole in one chunk, so indexes are
// handed out per choice in arrival order. Chunks without tool calls pass
// through byte for byte; only lines that need a repair are re-encoded.
func newCompatToolCallStream(body io.ReadCloser) io.ReadCloser {
	pr, pw := io.Pipe()
	stream := &geminiNativeStream{reader: pr, body: body}
	go repairCompatToolCallStream(body, pw)
	return stream
}

type compatChoiceState struct {
	nextToolIndex int
	sawToolCalls  bool
}

func repairCompatToolCallStream(body io.ReadCloser, out *io.PipeWriter) {
	defer func() { _ = body.Close() }()

	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	choices := make(map[int]*compatChoiceState)
	for scanner.Scan() {
		line := scanner.Text()
		if repaired, ok := repairCompatChunkLine(line, choices); ok {
			line = repaired
		}
		if _, err := io.WriteString(out, line+"\n"); err != nil {
			return
		}
	}
	if err := scanner.Err(); err != nil {
		_ = out.CloseWithError(err)
		return
	}
	_ = out.Close()
}

// repairCompatChunkLine returns the repaired SSE line and whether anything
// changed. Lines that cannot carry a repair are skipped before any decoding.
func repairCompatChunkLine(line string, choices map[int]*compatChoiceState) (string, bool) {
	const prefix = "data:"
	if !strings.HasPrefix(line, prefix) {
		return line, false
	}
	payload := strings.TrimSpace(strings.TrimPrefix(line, prefix))
	hasToolCalls := strings.Contains(payload, `"tool_calls"`)
	if !hasToolCalls && (len(choices) == 0 || !strings.Contains(payload, `"finish_reason"`)) {
		return line, false
	}
	var chunk map[string]any
	if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
		return line, false
	}
	rawChoices, _ := chunk["choices"].([]any)
	changed := false
	for position, raw := range rawChoices {
		choice, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		index := position
		if n, ok := choice["index"].(float64); ok {
			index = int(n)
		}
		state := choices[index]
		if state == nil {
			state = &compatChoiceState{}
			choices[index] = state
		}
		if delta, ok := choice["delta"].(map[string]any); ok {
			if calls, ok := delta["tool_calls"].([]any); ok && len(calls) > 0 {
				state.sawToolCalls = true
				for _, rawCall := range calls {
					call, ok := rawCall.(map[string]any)
					if !ok {
						continue
					}
					if _, has := call["index"]; !has {
						call["index"] = state.nextToolIndex
						changed = true
					}
					state.nextToolIndex++
				}
			}
		}
		if finish, _ := choice["finish_reason"].(string); finish == "stop" && state.sawToolCalls {
			choice["finish_reason"] = "tool_calls"
			changed = true
		}
	}
	if !changed {
		return line, false
	}
	encoded, err := json.Marshal(chunk)
	if err != nil {
		return line, false
	}
	return prefix + " " + string(encoded), true
}
