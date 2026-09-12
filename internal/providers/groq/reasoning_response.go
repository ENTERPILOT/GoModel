package groq

import (
	"bufio"
	"bytes"
	"io"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
)

// Groq returns the chain of thought under "reasoning", while GoModel's own
// canonical spelling on /v1/chat/completions is "reasoning_content": that is
// what the Anthropic, DeepSeek, Cohere and vLLM adapters emit, what the
// Responses and Messages translation layers read, and what the dashboard
// renders. Relaying Groq's spelling makes the same client code work on
// DeepSeek and silently lose the reasoning on Groq, so the member is renamed
// rather than duplicated: two copies of the same chain of thought would double
// the payload of every reasoning response, and clients that want Groq's own
// wire shape can still reach it through the passthrough route.
const (
	groqReasoningKey = "reasoning"
	canonicalKey     = "reasoning_content"
)

// normalizeChatResponse renames the reasoning member on every choice of a
// buffered chat completion. A response that already carries
// "reasoning_content" is left alone.
func normalizeChatResponse(resp *core.ChatResponse) {
	if resp == nil {
		return
	}
	for i := range resp.Choices {
		fields := resp.Choices[i].Message.ExtraFields
		raw := fields.Lookup(groqReasoningKey)
		if raw == nil || fields.Lookup(canonicalKey) != nil {
			continue
		}
		merged, err := core.MergeUnknownJSONFields(fields.Without(groqReasoningKey), map[string]json.RawMessage{
			canonicalKey: raw,
		})
		if err != nil {
			// Leave the upstream member in place rather than lose the text.
			continue
		}
		resp.Choices[i].Message.ExtraFields = merged
	}
}

// sseDataPrefix introduces the JSON payload of an SSE event.
var sseDataPrefix = []byte("data: ")

// reasoningMarker gates the per-chunk decode. Chunks without it — every
// content delta, the role chunk and the terminal [DONE] — are relayed with
// their original bytes, so only the reasoning deltas of a reasoning model pay
// for a re-encode.
var reasoningMarker = []byte(`"reasoning"`)

// normalizeChatStream renames the reasoning delta member on a chat
// completions SSE stream. Lines that are not data events, and data events
// that do not mention "reasoning", pass through byte for byte.
func normalizeChatStream(stream io.ReadCloser) io.ReadCloser {
	if stream == nil {
		return nil
	}
	return &reasoningStream{src: bufio.NewReader(stream), closer: stream}
}

type reasoningStream struct {
	src     *bufio.Reader
	closer  io.Closer
	pending bytes.Buffer
	err     error
}

func (s *reasoningStream) Read(p []byte) (int, error) {
	for s.pending.Len() == 0 {
		if s.err != nil {
			return 0, s.err
		}
		line, err := s.src.ReadBytes('\n')
		s.err = err
		if len(line) > 0 {
			s.pending.Write(renameReasoningLine(line))
		}
	}
	return s.pending.Read(p)
}

func (s *reasoningStream) Close() error { return s.closer.Close() }

// renameReasoningLine rewrites one SSE line, returning the input unchanged
// whenever the rename does not apply or the payload does not parse.
func renameReasoningLine(line []byte) []byte {
	if !bytes.HasPrefix(line, sseDataPrefix) || !bytes.Contains(line, reasoningMarker) {
		return line
	}
	payload := bytes.TrimRight(line[len(sseDataPrefix):], "\r\n")
	var chunk map[string]any
	if err := json.Unmarshal(payload, &chunk); err != nil {
		return line
	}
	if !renameReasoningDeltas(chunk) {
		return line
	}
	encoded, err := json.Marshal(chunk)
	if err != nil {
		return line
	}
	out := make([]byte, 0, len(sseDataPrefix)+len(encoded)+1)
	out = append(out, sseDataPrefix...)
	out = append(out, encoded...)
	return append(out, '\n')
}

// renameReasoningDeltas reports whether it changed the decoded chunk.
func renameReasoningDeltas(chunk map[string]any) bool {
	choices, ok := chunk["choices"].([]any)
	if !ok {
		return false
	}
	changed := false
	for _, entry := range choices {
		choice, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		delta, ok := choice["delta"].(map[string]any)
		if !ok {
			continue
		}
		value, ok := delta[groqReasoningKey]
		if !ok {
			continue
		}
		if _, exists := delta[canonicalKey]; !exists {
			delta[canonicalKey] = value
		}
		delete(delta, groqReasoningKey)
		changed = true
	}
	return changed
}
