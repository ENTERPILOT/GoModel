package minimax

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
)

// TestProdBufferedM2_ThinkThenTruncatedAnswer exercises the real MiniMax
// M2 buffered response shape captured from production. The model emitted a
// complete <think>…</think> block and then started the answer, which got
// cut at the max_tokens ceiling. The normalizer must move the inner
// reasoning into reasoning_content and leave only the partial answer in
// the content member.
func TestProdBufferedM2_ThinkThenTruncatedAnswer(t *testing.T) {
	raw := loadFixture(t, "testdata/prod_m2_buffered.json")
	var resp core.ChatResponse
	require.NoError(t, json.Unmarshal([]byte(raw), &resp))

	require.NotEmpty(t, resp.Choices)
	require.Equal(t, "length", resp.Choices[0].FinishReason, "fixture has finish_reason=length")
	rawContent, ok := resp.Choices[0].Message.Content.(string)
	require.True(t, ok)
	require.Contains(t, rawContent, "<think>")
	require.Contains(t, rawContent, "</think>", "fixture think block is properly closed before the cutoff")
	require.Nil(t, resp.Choices[0].Message.ExtraFields.Lookup(reasoningKey),
		"raw prod response has no reasoning_content yet")

	normalizeChatResponse(&resp)

	msg := resp.Choices[0].Message
	content, ok := msg.Content.(string)
	require.True(t, ok)
	assert.NotContains(t, content, "<think>",
		"normalizer must strip the opening tag")
	assert.NotContains(t, content, "</think>",
		"normalizer must strip the closing tag")
	assert.Empty(t, strings.TrimSpace(content),
		"only whitespace follows the closed think in this fixture; the answer was truncated by max_tokens before it could be emitted")

	thinking := msg.ExtraFields.Lookup(reasoningKey)
	require.NotNil(t, thinking, "reasoning_content must be populated when the think block closed")
	var got string
	require.NoError(t, json.Unmarshal(thinking, &got))
	assert.Contains(t, got, "How many primes below 30",
		"the inner think text must reach reasoning_content")
	assert.NotContains(t, got, "<think>", "raw tags must never leak into reasoning_content")
}

// TestProdStreamM27_TerminatedThink exercises the real MiniMax M2.7 SSE
// stream captured from production. The think block is closed and the answer
// follows, so both reasoning_content and content must be populated and the
// raw tag bytes must never reach the client. The captured fixture does not
// end with [DONE] (the MiniMax upstream finishes with a usage chunk), so
// the test only checks the cleaned-up data events.
func TestProdStreamM27_TerminatedThink(t *testing.T) {
	raw := loadFixture(t, "testdata/prod_m27_stream.txt")
	stream, err := io.ReadAll(normalizeChatStream(io.NopCloser(strings.NewReader(raw))))
	require.NoError(t, err)

	out := string(stream)
	assert.NotContains(t, out, "<think>", "opening tag must not leak to client")
	assert.NotContains(t, out, "</think>", "closing tag must not leak to client")
	assert.Contains(t, out, `"reasoning_content":`, "reasoning_content deltas must be emitted")
	assert.Contains(t, out, `"content":`, "content deltas must be emitted")
	assert.Contains(t, out, "Proof", "the post-think answer survives in the content member")
}

// TestProdStreamM27_EndToEnd drives a real MiniMax M2.7 stream shape through
// the live Provider.Prov stream method (not just the helper) to confirm the
// wiring in minimax.go calls the normalizer when the model name matches.
func TestProdStreamM27_EndToEnd(t *testing.T) {
	raw := loadFixture(t, "testdata/prod_m27_stream.txt")
	server, _ := providertest.SSEServer(t, raw)
	provider := NewWithHTTPClient("minimax-key", server.URL, server.Client(), llmclient.Hooks{})

	stream, err := provider.StreamChatCompletion(context.Background(), &core.ChatRequest{
		Model:    "MiniMax-M2.7",
		Messages: []core.Message{{Role: "user", Content: "Prove sum of two odd numbers is even."}},
	})
	require.NoError(t, err)
	body, err := io.ReadAll(stream)
	require.NoError(t, err)
	out := string(body)
	assert.NotContains(t, out, "<think>")
	assert.NotContains(t, out, "</think>")
	assert.Contains(t, out, "Proof")
}

// TestProdStreamSpeechModel_PassesThrough verifies that a non-reasoning
// MiniMax model (speech family) sees the stream unchanged. The normalizer
// must stay inert on anything that does not start with MiniMax-M3 / MiniMax-M2.
func TestProdStreamSpeechModel_PassesThrough(t *testing.T) {
	raw := loadFixture(t, "testdata/prod_m27_stream.txt")
	server, _ := providertest.SSEServer(t, raw)
	provider := NewWithHTTPClient("minimax-key", server.URL, server.Client(), llmclient.Hooks{})

	stream, err := provider.StreamChatCompletion(context.Background(), &core.ChatRequest{
		Model:    "speech-2.6-hd",
		Messages: []core.Message{{Role: "user", Content: "hello"}},
	})
	require.NoError(t, err)
	body, err := io.ReadAll(stream)
	require.NoError(t, err)
	assert.Equal(t, raw, string(body),
		"non-reasoning model must be relayed byte for byte")
}

func loadFixture(t *testing.T, name string) string {
	t.Helper()
	body, err := os.ReadFile(name)
	require.NoError(t, err, "fixture %s missing — capture it from prod first", name)
	return string(body)
}

var _ = http.MethodPost // keep the import stable across edits