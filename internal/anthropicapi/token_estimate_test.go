package anthropicapi

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/png"
	"strings"
	"testing"
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
	if err := png.Encode(&buf, image.NewGray(image.Rect(0, 0, w, h))); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

// Text that is not English prose tokenizes far denser than four characters
// per token, and an estimate that ignores that sends requests upstream that
// no longer fit. The weights are calibrated against Anthropic's own counts.
func TestEstimateInputTokens_CharacterClasses(t *testing.T) {
	prose := strings.Repeat("the quick brown fox jumps over the lazy dog ", 20) // 880 chars
	proseTokens := userMessage(t, prose)
	if proseTokens < 200 || proseTokens > 260 {
		t.Errorf("prose = %d tokens for %d chars, want roughly chars/4", proseTokens, len(prose))
	}

	cjk := strings.Repeat("漢字仮名交じり文", 40) // 320 runes
	if got := userMessage(t, cjk); got < 260 {
		t.Errorf("CJK = %d tokens for 320 runes, want close to one per rune", got)
	}

	emoji := strings.Repeat("😀🎉🚀", 50) // 150 runes
	if got := userMessage(t, emoji); got < 300 {
		t.Errorf("emoji = %d tokens for 150 runes, want at least two per rune", got)
	}

	dense := strings.Repeat(`{"id":42,"ok":true,"tags":["a","b"]},`, 30) // 1110 chars
	if got := userMessage(t, dense); got < 500 {
		t.Errorf("dense JSON = %d tokens for %d chars, want punctuation weighted near one token each", got, len(dense))
	}

	// Identifiers and hashes: long runs mixing letters and digits.
	sha := "3f2a9c8e1b7d6f4a0c5e2b9d8a7f6e5c4b3a2d1e"
	if got := userMessage(t, sha); got < 20 {
		t.Errorf("hex sha = %d tokens, want at least 20", got)
	}
}

// Every message and every tool carries framing tokens of its own, and a
// request with tools carries Anthropic's tool-use system prompt.
func TestEstimateInputTokens_Overheads(t *testing.T) {
	one := estimateFor(t, `{"model":"m","max_tokens":1,"messages":[{"role":"user","content":"hello there"}]}`)
	two := estimateFor(t, `{"model":"m","max_tokens":1,"messages":[{"role":"user","content":"hello"},{"role":"assistant","content":"there"}]}`)
	if two <= one {
		t.Errorf("two messages = %d, one message = %d; want per-message overhead", two, one)
	}

	plain := estimateFor(t, `{"model":"m","max_tokens":1,"messages":[{"role":"user","content":"hi"}]}`)
	withTool := estimateFor(t, `{"model":"m","max_tokens":1,"messages":[{"role":"user","content":"hi"}],"tools":[{"name":"t","description":"d","input_schema":{"type":"object"}}]}`)
	if withTool-plain < 300 {
		t.Errorf("one tool adds %d tokens, want the tool-use system prompt (~300) on top of the schema", withTool-plain)
	}
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
	if small < 5 || small > 8 {
		t.Errorf("64x64 image adds %d tokens, want about 64*64/750 = 6", small)
	}

	large := estimateFor(t, imageBody(`{"type":"base64","media_type":"image/png","data":"`+pngBase64(t, 2000, 2000)+`"}`)) - textOnly
	if large < 1500 || large > 1700 {
		t.Errorf("2000x2000 image adds %d tokens, want the ~1600 cap after Anthropic downscales it", large)
	}

	remote := estimateFor(t, imageBody(`{"type":"url","url":"https://example.com/photo.jpg"}`)) - textOnly
	if remote < 1500 || remote > 1700 {
		t.Errorf("URL image adds %d tokens, want the ~1600 upper bound when it cannot be measured", remote)
	}
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
		if err != nil {
			t.Fatalf("ToChatRequest: %v", err)
		}
		wire, seed := EstimateInputTokens(req), EstimateChatInputTokens(chat)
		if seed < wire-2 || seed > wire+2 {
			t.Errorf("stream seed = %d, wire estimate = %d; want the document priced the same way on both", seed, wire)
		}
		if wire < 10 {
			t.Errorf("wire estimate = %d, want the document text or title counted", wire)
		}
	}
}

// A search_result block reaches the model as its title, source, and body, so
// a rich result must cost far more than an empty one; counting it by the
// text field alone priced both the same.
func TestEstimateInputTokens_SearchResult(t *testing.T) {
	body := strings.Repeat("The gateway routes each request to the provider that owns the model. ", 30)
	rich := estimateFor(t, `{"model":"m","max_tokens":1,"messages":[{"role":"user","content":[{"type":"search_result","title":"Routing","source":"https://example.com/docs/routing","content":[{"type":"text","text":"`+body+`"}]},{"type":"text","text":"summarize"}]}]}`)
	empty := estimateFor(t, `{"model":"m","max_tokens":1,"messages":[{"role":"user","content":[{"type":"search_result","title":"","source":"","content":[]},{"type":"text","text":"summarize"}]}]}`)
	if rich-empty < 400 {
		t.Errorf("rich search_result adds %d tokens over an empty one, want its title, source, and body counted (>= 400)", rich-empty)
	}
}
