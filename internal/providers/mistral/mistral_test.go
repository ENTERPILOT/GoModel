package mistral

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
)

// Mistral is the shared chat-centric adapter plus the thinking-chunk
// flattening, so the shared contract covers its surface; Responses is
// translated through chat completions.
func TestChatCompatibleContract(t *testing.T) {
	providertest.AssertChatCompatible(t, providertest.ChatCompatible{
		Registration:   Registration,
		Type:           "mistral",
		DefaultBaseURL: defaultBaseURL,
		New: func(apiKey, baseURL string, client *http.Client, hooks llmclient.Hooks) core.Provider {
			opts := providertest.Options(hooks)
			opts.HTTPClient = client
			return New(providers.ProviderConfig{APIKey: apiKey, BaseURL: baseURL}, opts)
		},
		Embeddings:      true,
		NativeResponses: false,
	})
}

// newTestProvider builds the provider through its own constructor on a test transport.
func newTestProvider(server *httptest.Server) core.Provider {
	opts := providertest.Options(llmclient.Hooks{})
	opts.HTTPClient = server.Client()
	return New(providers.ProviderConfig{APIKey: "mistral-key", BaseURL: server.URL}, opts)
}

// A reasoning-model non-streaming reply: content is a chunk list, not a string.
const thinkingCompletionJSON = `{
	"id": "cmpl-mistral-1",
	"object": "chat.completion",
	"created": 1773842038,
	"model": "mistral-large-4-0",
	"choices": [{
		"index": 0,
		"message": {
			"role": "assistant",
			"content": [
				{"type": "thinking", "thinking": [{"type": "text", "text": "17 * 23 = "}, {"type": "text", "text": "391."}]},
				{"type": "text", "text": "17 × 23 = 391."}
			]
		},
		"finish_reason": "stop"
	}],
	"usage": {"prompt_tokens": 12, "completion_tokens": 30, "total_tokens": 42}
}`

func TestChatCompletion_FlattensThinkingChunks(t *testing.T) {
	server, _ := providertest.JSONServer(t, http.StatusOK, thinkingCompletionJSON)
	provider := newTestProvider(server)

	resp, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
		Model:    "mistral-large-4-0",
		Messages: []core.Message{{Role: "user", Content: "What is 17 * 23?"}},
	})
	require.NoError(t, err)
	require.Len(t, resp.Choices, 1)
	assert.Equal(t, "17 × 23 = 391.", resp.Choices[0].Message.Content)
	assert.Equal(t, "17 * 23 = 391.", rawString(resp.Choices[0].Message.ExtraFields.Lookup(reasoningContentKey)))
	assert.Equal(t, 42, resp.Usage.TotalTokens)

	// The flattened message must re-encode with a string content and the
	// reasoning_content extension, i.e. the shape clients expect.
	encoded, err := json.Marshal(resp.Choices[0].Message)
	require.NoError(t, err)
	assert.Contains(t, string(encoded), `"content":"17 × 23 = 391."`)
	assert.Contains(t, string(encoded), `"reasoning_content":"17 * 23 = 391."`)
	assert.NotContains(t, string(encoded), `"thinking"`)
}

// A response whose content array also carries a chunk type GoModel does not
// model (Mistral's "reference") must still decode: the answer text lands in
// content and the unknown chunk rides along as a vendor member instead of
// failing the whole completion with a 502.
func TestChatCompletion_PreservesUnknownChunks(t *testing.T) {
	const body = `{
		"id": "cmpl-mistral-2",
		"object": "chat.completion",
		"created": 1773842038,
		"model": "mistral-large-4-0",
		"choices": [{
			"index": 0,
			"message": {
				"role": "assistant",
				"content": [
					{"type": "text", "text": "Paris. "},
					{"type": "reference", "reference_ids": [3, 7]}
				]
			},
			"finish_reason": "stop"
		}],
		"usage": {"prompt_tokens": 9, "completion_tokens": 4, "total_tokens": 13}
	}`
	server, _ := providertest.JSONServer(t, http.StatusOK, body)
	provider := newTestProvider(server)

	resp, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
		Model:    "mistral-large-4-0",
		Messages: []core.Message{{Role: "user", Content: "Capital of France?"}},
	})
	require.NoError(t, err, "an unknown chunk type must not fail the decode")
	require.Len(t, resp.Choices, 1)
	assert.Equal(t, "Paris. ", resp.Choices[0].Message.Content)

	encoded, err := json.Marshal(resp.Choices[0].Message)
	require.NoError(t, err)
	assert.Contains(t, string(encoded), `"content":"Paris. "`)
	assert.Contains(t, string(encoded), `"mistral_content_chunks":[{"type":"reference","reference_ids":[3,7]}]`)
}

// A plain string content (reasoning_effort "none") must pass through untouched.
func TestChatCompletion_StringContentUnchanged(t *testing.T) {
	server, _ := providertest.JSONServer(t, http.StatusOK, providertest.ChatCompletionJSON)
	provider := newTestProvider(server)

	resp, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
		Model:    providertest.Model,
		Messages: []core.Message{{Role: "user", Content: providertest.Prompt}},
	})
	require.NoError(t, err)
	require.Len(t, resp.Choices, 1)
	assert.Equal(t, providertest.Reply, resp.Choices[0].Message.Content)
	assert.Nil(t, resp.Choices[0].Message.ExtraFields.Lookup(reasoningContentKey))
}

// A reasoning stream: delta.content is a chunk array while thinking, a mixed
// array at the transition, and a plain string afterwards.
const thinkingChunkSSE = "data: {\"id\":\"cmpl-mistral-1\",\"object\":\"chat.completion.chunk\",\"model\":\"mistral-large-4-0\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":[{\"type\":\"thinking\",\"thinking\":[{\"type\":\"text\",\"text\":\"Let me compute. \"}]}]}}]}\n\n" +
	"data: {\"id\":\"cmpl-mistral-1\",\"object\":\"chat.completion.chunk\",\"model\":\"mistral-large-4-0\",\"choices\":[{\"index\":0,\"delta\":{\"content\":[{\"type\":\"thinking\",\"thinking\":[{\"type\":\"text\",\"text\":\"17*23=391.\"}]},{\"type\":\"text\",\"text\":\"The answer is \"}]}}]}\n\n" +
	"data: {\"id\":\"cmpl-mistral-1\",\"object\":\"chat.completion.chunk\",\"model\":\"mistral-large-4-0\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"391.\"}}]}\n\n" +
	"data: {\"id\":\"cmpl-mistral-1\",\"object\":\"chat.completion.chunk\",\"model\":\"mistral-large-4-0\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
	"data: [DONE]\n\n"

func TestStreamChatCompletion_FlattensThinkingDeltas(t *testing.T) {
	server, _ := providertest.SSEServer(t, thinkingChunkSSE)
	provider := newTestProvider(server)

	stream, err := provider.StreamChatCompletion(context.Background(), &core.ChatRequest{
		Model:    "mistral-large-4-0",
		Messages: []core.Message{{Role: "user", Content: "What is 17 * 23?"}},
		Stream:   true,
	})
	require.NoError(t, err)
	body, err := io.ReadAll(stream)
	require.NoError(t, err)
	require.NoError(t, stream.Close())

	out := string(body)
	// Thinking text rides in reasoning_content, answer text in content, and no
	// chunk array survives on the wire.
	assert.Contains(t, out, `"reasoning_content":"Let me compute. "`)
	assert.Contains(t, out, `"reasoning_content":"17*23=391."`)
	assert.Contains(t, out, `"content":"The answer is "`)
	assert.Contains(t, out, `"content":"391."`)
	assert.NotContains(t, out, `"type":"thinking"`)
	assert.Contains(t, out, "data: [DONE]")

	// Every emitted data line must be a well-formed chunk with string deltas.
	for line := range strings.Lines(out) {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data: ") || line == "data: [DONE]" {
			continue
		}
		var chunk map[string]json.RawMessage
		require.NoError(t, json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &chunk), "line: %s", line)
		var choices []map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(chunk["choices"], &choices))
		for _, choice := range choices {
			deltaRaw, ok := choice["delta"]
			if !ok {
				continue
			}
			var delta map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(deltaRaw, &delta))
			if content, ok := delta["content"]; ok {
				var s string
				require.NoError(t, json.Unmarshal(content, &s), "delta.content must be a string: %s", content)
			}
		}
	}
}

// The request dialect: nested reasoning becomes flat reasoning_effort (passed
// through, e.g. "max" for hosted GLM models), a replayed assistant turn's
// reasoning travels as a Mistral thinking chunk inside content (per Mistral's
// guide, stripping it degrades output), and members Mistral cannot parse are
// removed from the wire.
func TestChatCompletion_AdaptsRequest(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, providertest.ChatCompletionJSON)
	provider := newTestProvider(server)

	_, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
		Model: "zai-glm-5-3",
		Messages: []core.Message{
			{Role: "user", Content: "hi"},
			{Role: "assistant", Content: "hello", ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{
				reasoningContentKey: json.RawMessage(`"earlier thoughts"`),
				unknownChunksKey:    json.RawMessage(`[{"type":"reference"}]`),
			})},
			{Role: "user", Content: "again"},
		},
		Reasoning: &core.Reasoning{Effort: "MAX"},
	})
	require.NoError(t, err)

	wire := capture.Last(t).JSON(t)
	assert.Equal(t, "max", wire["reasoning_effort"], "effort passes through lowercased, including max")
	assert.NotContains(t, wire, "reasoning", "nested reasoning object must be replaced")
	messages, ok := wire["messages"].([]any)
	require.True(t, ok)
	assistant, ok := messages[1].(map[string]any)
	require.True(t, ok)
	assert.NotContains(t, assistant, reasoningContentKey, "Mistral 422s on a replayed reasoning_content member")
	assert.NotContains(t, assistant, unknownChunksKey, "vendor members are not valid on a request")
	content, ok := assistant["content"].([]any)
	require.True(t, ok, "replayed content = %#v, want chunk list", assistant["content"])
	assert.JSONEq(t, `{"type":"thinking","thinking":[{"type":"text","text":"earlier thoughts"}]}`, string(mustJSON(content[0])))
	assert.JSONEq(t, `{"type":"text","text":"hello"}`, string(mustJSON(content[1])))
}

// The same replay adaptation must reach the streaming path, which goes
// through the shared adapter's own request pipeline.
func TestStreamChatCompletion_AdaptsRequest(t *testing.T) {
	server, capture := providertest.SSEServer(t, providertest.ChatChunkSSE)
	provider := newTestProvider(server)

	stream, err := provider.StreamChatCompletion(context.Background(), &core.ChatRequest{
		Model: "mistral-large-4-0",
		Messages: []core.Message{
			{Role: "assistant", Content: "hello", ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{
				reasoningContentKey: json.RawMessage(`"earlier thoughts"`),
				"refusal":           json.RawMessage(`null`),
			})},
			{Role: "user", Content: "again", ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{
				"name": json.RawMessage(`"alice"`),
			})},
		},
		Stream: true,
		// The gateway adds stream_options by default to collect usage.
		StreamOptions: &core.StreamOptions{IncludeUsage: true},
		User:          "user-123",
	})
	require.NoError(t, err)
	_, err = io.ReadAll(stream)
	require.NoError(t, err)
	require.NoError(t, stream.Close())

	wire := capture.Last(t).JSON(t)
	assert.NotContains(t, wire, "stream_options", "Mistral's schema has no stream_options")
	assert.NotContains(t, wire, "user", "Mistral's schema has no user")
	messages, ok := wire["messages"].([]any)
	require.True(t, ok)
	require.Len(t, messages, 2)
	assert.NotContains(t, messages[1], "name", "Mistral allows name on tool messages only")
	assistant, ok := messages[0].(map[string]any)
	require.True(t, ok)
	assert.NotContains(t, assistant, "refusal")
	content, ok := assistant["content"].([]any)
	require.True(t, ok, "replayed content = %#v, want chunk list", assistant["content"])
	assert.JSONEq(t, `{"type":"thinking","thinking":[{"type":"text","text":"earlier thoughts"}]}`, string(mustJSON(content[0])))
	assert.JSONEq(t, `{"type":"text","text":"hello"}`, string(mustJSON(content[1])))
}

// The Responses API is served through chat-completions translation, so the
// reasoning flattened out of Mistral's thinking chunks must surface as a
// reasoning item (buffered) and reasoning deltas (streamed).
func TestResponses_PreservesFlattenedThinking(t *testing.T) {
	server, _ := providertest.JSONServer(t, http.StatusOK, thinkingCompletionJSON)
	provider := newTestProvider(server)

	resp, err := provider.Responses(context.Background(), &core.ResponsesRequest{
		Model:  "mistral-large-4-0",
		Input:  "What is 17 * 23?",
		Stream: false,
	})
	require.NoError(t, err)
	require.NotEmpty(t, resp.Output)
	reasoning := resp.Output[0]
	assert.Equal(t, "reasoning", reasoning.Type, "the flattened reasoning must lead the output")
	require.NotEmpty(t, reasoning.Content)
	assert.Equal(t, "reasoning_text", reasoning.Content[0].Type)
	assert.Equal(t, "17 * 23 = 391.", reasoning.Content[0].Text)
}

func TestStreamResponses_PreservesFlattenedThinking(t *testing.T) {
	server, _ := providertest.SSEServer(t, thinkingChunkSSE)
	provider := newTestProvider(server)

	stream, err := provider.StreamResponses(context.Background(), &core.ResponsesRequest{
		Model:  "mistral-large-4-0",
		Input:  "What is 17 * 23?",
		Stream: true,
	})
	require.NoError(t, err)
	body, err := io.ReadAll(stream)
	require.NoError(t, err)
	require.NoError(t, stream.Close())

	// Parse the frames and assert the thinking strings travel on reasoning
	// events, and the answer on its own exact delta sequence, not merely
	// somewhere in the stream.
	var reasoningTexts, answerTexts []string
	for line := range strings.Lines(string(body)) {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data: ") || line == "data: [DONE]" {
			continue
		}
		var frame struct {
			Type  string `json:"type"`
			Delta string `json:"delta"`
		}
		require.NoError(t, json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &frame), "frame: %s", line)
		switch frame.Type {
		case "response.reasoning_text.delta":
			reasoningTexts = append(reasoningTexts, frame.Delta)
		case "response.output_text.delta":
			answerTexts = append(answerTexts, frame.Delta)
		}
	}
	assert.Equal(t, []string{"Let me compute. ", "17*23=391."}, reasoningTexts)
	assert.Equal(t, []string{"The answer is ", "391."}, answerTexts,
		"the answer must stream as its own exact delta sequence")
}

// A cited answer, as Mistral returns it after a tool result that supplied the
// references: reference chunks between the text chunks, each listing the
// caller's reference ids.
const citedCompletionJSON = `{
	"id": "cmpl-mistral-3",
	"object": "chat.completion",
	"created": 1773842038,
	"model": "mistral-large-4-0",
	"choices": [{
		"index": 0,
		"message": {
			"role": "assistant",
			"content": [
				{"type": "text", "text": "Paris is the capital"},
				{"type": "reference", "reference_ids": [0]},
				{"type": "text", "text": " and the largest city."},
				{"type": "reference", "reference_ids": [0, 2]}
			]
		},
		"finish_reason": "stop"
	}],
	"usage": {"prompt_tokens": 9, "completion_tokens": 8, "total_tokens": 17}
}`

// The same answer streamed: a reference chunk arrives in a delta whose
// content is a chunk array.
const citedChunkSSE = "data: {\"id\":\"cmpl-mistral-3\",\"object\":\"chat.completion.chunk\",\"model\":\"mistral-large-4-0\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"Paris is the capital\"}}]}\n\n" +
	"data: {\"id\":\"cmpl-mistral-3\",\"object\":\"chat.completion.chunk\",\"model\":\"mistral-large-4-0\",\"choices\":[{\"index\":0,\"delta\":{\"content\":[{\"type\":\"reference\",\"reference_ids\":[0]}]}}]}\n\n" +
	"data: {\"id\":\"cmpl-mistral-3\",\"object\":\"chat.completion.chunk\",\"model\":\"mistral-large-4-0\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\" and the largest city.\"}}]}\n\n" +
	"data: {\"id\":\"cmpl-mistral-3\",\"object\":\"chat.completion.chunk\",\"model\":\"mistral-large-4-0\",\"choices\":[{\"index\":0,\"delta\":{\"content\":[{\"type\":\"reference\",\"reference_ids\":[0,2]}]},\"finish_reason\":\"stop\"}]}\n\n" +
	"data: [DONE]\n\n"

// citedExtraContentJSON is the extra_content of the Responses message item
// for the cited answer.
const citedExtraContentJSON = `{"mistral":{"content_chunks":[{"type":"reference","reference_ids":[0]},{"type":"reference","reference_ids":[0,2]}]}}`

// Responses has no standard member for a citation that carries only reference
// ids, so the message item keeps the chunks in its extra_content, next to the
// unchanged text part.
func TestResponses_KeepsReferenceChunks(t *testing.T) {
	server, _ := providertest.JSONServer(t, http.StatusOK, citedCompletionJSON)
	provider := newTestProvider(server)

	resp, err := provider.Responses(context.Background(), &core.ResponsesRequest{
		Model: "mistral-large-4-0",
		Input: "Tell me about Paris.",
	})
	require.NoError(t, err)
	require.Len(t, resp.Output, 1)
	message := resp.Output[0]
	require.Equal(t, "message", message.Type)
	require.Len(t, message.Content, 1)
	assert.Equal(t, "Paris is the capital and the largest city.", message.Content[0].Text)
	assert.Empty(t, message.Content[0].Annotations)

	encoded, err := json.Marshal(message)
	require.NoError(t, err)
	var wire map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(encoded, &wire))
	assert.JSONEq(t, citedExtraContentJSON, string(wire[core.ExtraContentField]))
	assert.NotContains(t, wire, unknownChunksKey)
}

func TestStreamResponses_KeepsReferenceChunks(t *testing.T) {
	server, _ := providertest.SSEServer(t, citedChunkSSE)
	provider := newTestProvider(server)

	stream, err := provider.StreamResponses(context.Background(), &core.ResponsesRequest{
		Model:  "mistral-large-4-0",
		Input:  "Tell me about Paris.",
		Stream: true,
	})
	require.NoError(t, err)
	body, err := io.ReadAll(stream)
	require.NoError(t, err)
	require.NoError(t, stream.Close())

	// The chunks ride on the message item where it is stated whole: its
	// output_item.done event and the terminal output.
	var answer strings.Builder
	var doneItem, terminalItem map[string]json.RawMessage
	for _, payload := range dataLines(string(body)) {
		var frame struct {
			Type     string                     `json:"type"`
			Delta    string                     `json:"delta"`
			Item     map[string]json.RawMessage `json:"item"`
			Response struct {
				Output []map[string]json.RawMessage `json:"output"`
			} `json:"response"`
		}
		require.NoError(t, json.Unmarshal([]byte(payload), &frame), "frame: %s", payload)
		switch frame.Type {
		case "response.output_text.delta":
			answer.WriteString(frame.Delta)
		case "response.output_item.done":
			doneItem = frame.Item
		case "response.completed":
			require.Len(t, frame.Response.Output, 1)
			terminalItem = frame.Response.Output[0]
		}
	}
	assert.Equal(t, "Paris is the capital and the largest city.", answer.String())
	require.NotNil(t, doneItem)
	require.NotNil(t, terminalItem)
	assert.JSONEq(t, `"message"`, string(doneItem["type"]))
	assert.JSONEq(t, citedExtraContentJSON, string(doneItem[core.ExtraContentField]))
	assert.JSONEq(t, citedExtraContentJSON, string(terminalItem[core.ExtraContentField]))
}

// A Responses client replays output items as they came. The router strips
// extra_content before the request reaches this provider; a request that
// skips the router must still not send it to Mistral, which rejects members
// its schema does not define.
func TestResponses_DropsReplayedReferenceChunks(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, providertest.ChatCompletionJSON)
	provider := newTestProvider(server)

	_, err := provider.Responses(context.Background(), &core.ResponsesRequest{
		Model: "mistral-large-4-0",
		Input: []any{
			map[string]any{"type": "message", "role": "user", "content": "Tell me about Paris."},
			map[string]any{
				"type":   "message",
				"id":     "msg_1",
				"role":   "assistant",
				"status": "completed",
				"content": []any{
					map[string]any{"type": "output_text", "text": "Paris is the capital.", "annotations": []any{}},
				},
				core.ExtraContentField: map[string]any{"mistral": map[string]any{
					"content_chunks": []any{map[string]any{"type": "reference", "reference_ids": []any{0}}},
				}},
			},
			map[string]any{"type": "message", "role": "user", "content": "And Lyon?"},
		},
	})
	require.NoError(t, err)

	messages, ok := capture.Last(t).JSON(t)["messages"].([]any)
	require.True(t, ok)
	require.Len(t, messages, 3)
	assert.Equal(t, map[string]any{"role": "assistant", "content": "Paris is the capital."}, messages[1])
}

// A request without reasoning and without replayed thinking must reach the
// upstream byte-identical in shape: no injected members.
func TestChatCompletion_AdaptsRequest_Noop(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, providertest.ChatCompletionJSON)
	provider := newTestProvider(server)

	_, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
		Model:    providertest.Model,
		Messages: []core.Message{{Role: "user", Content: providertest.Prompt}},
	})
	require.NoError(t, err)

	wire := capture.Last(t).JSON(t)
	assert.NotContains(t, wire, "reasoning_effort")
	assert.NotContains(t, wire, "reasoning")
	assert.NotContains(t, wire, "stream_options")
}

// Mistral reports a completion cut off by the model's context window as
// finish_reason "model_length". Clients only know the OpenAI "length" value,
// so it must be mapped on both the buffered and the streamed path.
func TestChatCompletion_MapsModelLengthFinishReason(t *testing.T) {
	server, _ := providertest.JSONServer(t, http.StatusOK, `{"id":"cmpl-1","object":"chat.completion","created":1,"model":"mistral-large-4-0","choices":[{"index":0,"message":{"role":"assistant","content":"cut"},"finish_reason":"model_length"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	provider := newTestProvider(server)

	resp, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
		Model:    "mistral-large-4-0",
		Messages: []core.Message{{Role: "user", Content: "hi"}},
	})
	require.NoError(t, err)
	require.Len(t, resp.Choices, 1)
	assert.Equal(t, "length", resp.Choices[0].FinishReason)
}

func TestStreamChatCompletion_MapsModelLengthFinishReason(t *testing.T) {
	server, _ := providertest.SSEServer(t, "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"cut\"},\"finish_reason\":null}]}\n\n"+
		"data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"model_length\"}]}\n\n"+
		"data: [DONE]\n\n")
	provider := newTestProvider(server)

	stream, err := provider.StreamChatCompletion(context.Background(), &core.ChatRequest{
		Model:    "mistral-large-4-0",
		Messages: []core.Message{{Role: "user", Content: "hi"}},
		Stream:   true,
	})
	require.NoError(t, err)
	body, err := io.ReadAll(stream)
	require.NoError(t, err)
	require.NoError(t, stream.Close())

	out := string(body)
	assert.Contains(t, out, `"finish_reason":"length"`)
	assert.NotContains(t, out, "model_length")
	assert.Contains(t, out, `"content":"cut"`)
	assert.Contains(t, out, "data: [DONE]")
}
