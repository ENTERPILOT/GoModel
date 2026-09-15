package anthropicapi

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/png"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func estimateFor(t *testing.T, body string) int {
	t.Helper()
	return EstimateInputTokens(mustDecode(t, body))
}

func userMessage(t *testing.T, text string) int {
	t.Helper()
	return estimateFor(t, fmt.Sprintf(`{"model":"m","max_tokens":1,"messages":[{"role":"user","content":%q}]}`, text))
}

// pngBase64 encodes a blank PNG of the given size.
func pngBase64(t *testing.T, w, h int) string {
	t.Helper()
	var buf bytes.Buffer
	err := png.Encode(&buf, image.NewGray(image.Rect(0, 0, w, h)))
	require.NoError(t, err)

	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

// Text that is not English prose tokenizes far denser than four characters
// per token, and an estimate that ignores that sends requests upstream that
// no longer fit. The weights are calibrated against Anthropic's own counts.
func TestEstimateInputTokens_CharacterClasses(t *testing.T) {
	prose := strings.Repeat("the quick brown fox jumps over the lazy dog ", 20) // 880 chars
	proseTokens := userMessage(t, prose)
	assert.GreaterOrEqual(t, proseTokens, 200)
	assert.LessOrEqual(t, proseTokens, 260, "prose = %d tokens for %d chars, want roughly chars/4", proseTokens, len(prose))

	cjk := strings.Repeat("漢字仮名交じり文", 40)
	// 320 runes
	got := userMessage(t, cjk)
	assert.GreaterOrEqual(t, got, 260)

	emoji := strings.Repeat("😀🎉🚀", 50)
	// 150 runes
	got = userMessage(t, emoji)
	assert.GreaterOrEqual(t, got, 300)

	dense := strings.Repeat(`{"id":42,"ok":true,"tags":["a","b"]},`, 30)
	// 1110 chars
	got = userMessage(t, dense)
	assert.GreaterOrEqual(t, got, 500, "dense JSON = %d tokens for %d chars, want punctuation weighted near one token each", got, len(dense))

	// Identifiers and hashes: long runs mixing letters and digits.
	sha := "3f2a9c8e1b7d6f4a0c5e2b9d8a7f6e5c4b3a2d1e"
	got = userMessage(t, sha)
	assert.GreaterOrEqual(t, got, 20)
}

// Every message and every tool carries framing tokens of its own, and a
// request with tools carries Anthropic's tool-use system prompt.
func TestEstimateInputTokens_Overheads(t *testing.T) {
	one := estimateFor(t, `{"model":"m","max_tokens":1,"messages":[{"role":"user","content":"hello there"}]}`)
	two := estimateFor(t, `{"model":"m","max_tokens":1,"messages":[{"role":"user","content":"hello"},{"role":"assistant","content":"there"}]}`)
	assert.Greater(t, two, one)

	plain := estimateFor(t, `{"model":"m","max_tokens":1,"messages":[{"role":"user","content":"hi"}]}`)
	withTool := estimateFor(t, `{"model":"m","max_tokens":1,"messages":[{"role":"user","content":"hi"}],"tools":[{"name":"t","description":"d","input_schema":{"type":"object"}}]}`)
	assert.GreaterOrEqual(t, withTool-plain, 300)
}

// An image costs (width × height) / 750 tokens. Dimensions come from the
// encoded header; an image that cannot be measured is charged the largest
// size Anthropic keeps, so the estimate errs toward not fitting rather than
// toward an upstream rejection.
func TestEstimateInputTokens_Images(t *testing.T) {
	imageBody := func(source string) string {
		return `{"model":"m","max_tokens":1,"messages":[{"role":"user","content":[{"type":"image","source":` + source + `},{"type":"text","text":"hi"}]}]}`
	}
	textOnly := estimateFor(t, `{"model":"m","max_tokens":1,"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)

	small := estimateFor(t, imageBody(`{"type":"base64","media_type":"image/png","data":"`+pngBase64(t, 64, 64)+`"}`)) - textOnly
	assert.GreaterOrEqual(t, small, 5)
	assert.LessOrEqual(t, small, 8)

	large := estimateFor(t, imageBody(`{"type":"base64","media_type":"image/png","data":"`+pngBase64(t, 2000, 2000)+`"}`)) - textOnly
	assert.GreaterOrEqual(t, large, 1500)
	assert.LessOrEqual(t, large, 1700)

	remote := estimateFor(t, imageBody(`{"type":"url","url":"https://example.com/photo.jpg"}`)) - textOnly
	assert.GreaterOrEqual(t, remote, 1500)
	assert.LessOrEqual(t, remote, 1700)
}

// The streaming message_start seed is computed from the translated chat
// request, so a document must cost the same there as in the wire estimate:
// its text when it is text, its title otherwise.
func TestEstimateChatInputTokens_MatchesWireEstimateForDocuments(t *testing.T) {
	for _, body := range []string{
		`{"model":"m","max_tokens":1,"messages":[{"role":"user","content":[{"type":"document","title":"notes.txt","source":{"type":"text","media_type":"text/plain","data":"` + strings.Repeat("plain document text ", 40) + `"}},{"type":"text","text":"summarize"}]}]}`,
		`{"model":"m","max_tokens":1,"messages":[{"role":"user","content":[{"type":"document","title":"report.pdf","source":{"type":"base64","media_type":"application/pdf","data":"JVBERi0="}},{"type":"text","text":"summarize"}]}]}`,
	} {
		req := mustDecode(t, body)
		chat, err := ToChatRequest(req)
		require.NoError(t, err)

		wire, seed := EstimateInputTokens(req), EstimateChatInputTokens(chat)
		assert.GreaterOrEqual(t, seed, wire-2)
		assert.LessOrEqual(t, seed, wire+2)
		assert.GreaterOrEqual(t, wire, 10)
	}
}

// A search_result block reaches the model as its title, source, and body, so
// a rich result must cost far more than an empty one; counting it by the
// text field alone priced both the same.
func TestEstimateInputTokens_SearchResult(t *testing.T) {
	body := strings.Repeat("The gateway routes each request to the provider that owns the model. ", 30)
	rich := estimateFor(t, `{"model":"m","max_tokens":1,"messages":[{"role":"user","content":[{"type":"search_result","title":"Routing","source":"https://example.com/docs/routing","content":[{"type":"text","text":"`+body+`"}]},{"type":"text","text":"summarize"}]}]}`)
	empty := estimateFor(t, `{"model":"m","max_tokens":1,"messages":[{"role":"user","content":[{"type":"search_result","title":"","source":"","content":[]},{"type":"text","text":"summarize"}]}]}`)
	assert.GreaterOrEqual(t, rich-empty, 400)
}
