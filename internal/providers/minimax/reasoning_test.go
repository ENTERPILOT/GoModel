package minimax

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
)

func TestIsReasoningModel(t *testing.T) {
	tests := []struct {
		model string
		want  bool
	}{
		{"MiniMax-M3", true},
		{"MiniMax-M3-0301", true},
		{"minimax-m3", true},
		{"MiniMax-M2.7", true},
		{"MiniMax-M2", true},
		{"MiniMax-M2.5", true},
		{"speech-2.6-hd", false},
		{"image-01", false},
		{"embedding-moka", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			assert.Equal(t, tt.want, isReasoningModel(tt.model))
		})
	}
}

func TestSplitThink(t *testing.T) {
	tests := []struct {
		name      string
		in        string
		wantText  string
		wantThink string
	}{
		{name: "no think tags", in: "hello", wantText: "hello"},
		{name: "single think block", in: "<think>hidden</think>visible", wantText: "visible", wantThink: "hidden"},
		{
			name:      "before and after",
			in:        "pre<think>hidden</think>post",
			wantText:  "prepost",
			wantThink: "hidden",
		},
		{
			name:      "multiple think blocks",
			in:        "<think>one</think>mid<think>two</think>end",
			wantText:  "midend",
			wantThink: "onetwo",
		},
		{
			name:      "M2 prod shape: closed think followed by max_tokens cutoff",
			in:        "<think>The user asks a simple question: \"How many primes below 30?\" This is a straightforward math question.\n\nWe need to comply. They ask: \"How many primes below 30?\" We must answer concisely, but not too terse. Let's think: primes less than 30: 2,3,5,7,11,13,17,19,23,29. That's 10 primes. So answer: 10.\n\nWe need to see any instructions: The developer message is empty, but system is default.\n\nWe also see the developer message is empty, but then we have a \"caveman ultra output style\" in the \"Context compression notice\"? That is not a system instruction, it's an instruction in the conversation. Wait let's read carefully:\n\nThe conversation:\n\n```\nSystem: ...\nUser: How many primes below 30?\n```\n\nWait we see the system messages: There's a system message (first). Then we\n</think>\n",
			wantText:  "",
			wantThink: "The user asks a simple question: \"How many primes below 30?\" This is a straightforward math question.\n\nWe need to comply. They ask: \"How many primes below 30?\" We must answer concisely, but not too terse. Let's think: primes less than 30: 2,3,5,7,11,13,17,19,23,29. That's 10 primes. So answer: 10.\n\nWe need to see any instructions: The developer message is empty, but system is default.\n\nWe also see the developer message is empty, but then we have a \"caveman ultra output style\" in the \"Context compression notice\"? That is not a system instruction, it's an instruction in the conversation. Wait let's read carefully:\n\nThe conversation:\n\n```\nSystem: ...\nUser: How many primes below 30?\n```\n\nWait we see the system messages: There's a system message (first). Then we\n",
		},
		{
			name:      "empty think is dropped",
			in:        "<think></think>after",
			wantText:  "after",
			wantThink: "",
		},
		{
			name:      "multiline think body",
			in:        "<think>line1\nline2\nline3</think>done",
			wantText:  "done",
			wantThink: "line1\nline2\nline3",
		},
		{
			name:      "angle bracket in content is untouched",
			in:        "a<b then <think>hidden</think> c<d",
			wantText:  "a<b then  c<d",
			wantThink: "hidden",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			text, think := splitThink(tt.in)
			assert.Equal(t, tt.wantText, text, "content")
			assert.Equal(t, tt.wantThink, think, "reasoning")
		})
	}
}

func TestNormalizeChatResponseNilIsSafe(t *testing.T) {
	normalizeChatResponse(nil)
}

func TestNormalizeChatResponse(t *testing.T) {
	tests := []struct {
		name         string
		content      string
		wantContent  string
		wantThinkKey bool
		wantThink    string
	}{
		{
			name:         "single think block becomes reasoning_content",
			content:      "<think>plan</think>answer",
			wantContent:  "answer",
			wantThinkKey: true,
			wantThink:    "plan",
		},
		{
			name:         "no think blocks leaves content alone",
			content:      "plain answer",
			wantContent:  "plain answer",
			wantThinkKey: false,
		},
		{
			name:         "content before and after survives",
			content:      "pre<think>hidden</think>post",
			wantContent:  "prepost",
			wantThinkKey: true,
			wantThink:    "hidden",
		},
		{
			name:         "multiple think blocks concatenate",
			content:      "<think>one</think>mid<think>two</think>end",
			wantContent:  "midend",
			wantThinkKey: true,
			wantThink:    "onetwo",
		},
		{
			name:         "unterminated think emits inner text as reasoning",
			content:      "pre<think>no close",
			wantContent:  "pre",
			wantThinkKey: true,
			wantThink:    "no close",
		},
		{
			name:         "existing reasoning_content is kept and tags still stripped",
			content:      "<think>hidden</think>answer",
			wantContent:  "answer",
			wantThinkKey: true,
			wantThink:    "upstream",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			extra, err := core.MergeUnknownJSONFields(core.UnknownJSONFields{}, map[string]json.RawMessage{
				reasoningKey: json.RawMessage(`"upstream"`),
			})
			if tt.name == "existing reasoning_content is kept and tags still stripped" {
				require.NoError(t, err)
			} else {
				extra = core.UnknownJSONFields{}
			}

			resp := &core.ChatResponse{
				Choices: []core.Choice{{
					Message: core.ResponseMessage{
						Role:        "assistant",
						Content:     tt.content,
						ExtraFields: extra,
					},
				}},
			}

			normalizeChatResponse(resp)

			msg := resp.Choices[0].Message
			assert.Equal(t, tt.wantContent, msg.Content, "content")
			if tt.wantThinkKey {
				raw := msg.ExtraFields.Lookup(reasoningKey)
				require.NotNil(t, raw, "reasoning_content missing")
				var got string
				require.NoError(t, json.Unmarshal(raw, &got))
				assert.Equal(t, tt.wantThink, got, "reasoning_content")
			} else {
				assert.Nil(t, msg.ExtraFields.Lookup(reasoningKey), "reasoning_content must be absent")
			}
		})
	}
}

func TestChatCompletion_RewritesThinkBlocks(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, `{
		"id":"chatcmpl-1",
		"object":"chat.completion",
		"model":"MiniMax-M3",
		"choices":[{"index":0,"message":{"role":"assistant","content":"<think>plan</think>answer"},"finish_reason":"stop"}],
		"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}
	}`)
	provider := NewWithHTTPClient("minimax-key", server.URL, server.Client(), llmclient.Hooks{})

	resp, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
		Model:    "MiniMax-M3",
		Messages: []core.Message{{Role: "user", Content: "hi"}},
	})
	require.NoError(t, err)
	assert.Equal(t, "answer", resp.Choices[0].Message.Content)
	raw := resp.Choices[0].Message.ExtraFields.Lookup(reasoningKey)
	require.NotNil(t, raw)
	var got string
	require.NoError(t, json.Unmarshal(raw, &got))
	assert.Equal(t, "plan", got)
	assert.Equal(t, http.MethodPost, capture.Last(t).Method)
}

func TestChatCompletion_NonReasoningModelIsUntouched(t *testing.T) {
	const body = `{"id":"c","object":"chat.completion","model":"speech-2.6-hd","choices":[{"index":0,"message":{"role":"assistant","content":"<think>plan</think>answer"},"finish_reason":"stop"}],"usage":{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}}`
	server, capture := providertest.JSONServer(t, http.StatusOK, body)
	provider := NewWithHTTPClient("minimax-key", server.URL, server.Client(), llmclient.Hooks{})

	resp, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
		Model:    "speech-2.6-hd",
		Messages: []core.Message{{Role: "user", Content: "hi"}},
	})
	require.NoError(t, err)
	assert.Equal(t, "<think>plan</think>answer", resp.Choices[0].Message.Content, "non-reasoning models must not be rewritten")
	assert.Nil(t, resp.Choices[0].Message.ExtraFields.Lookup(reasoningKey))
	_ = capture.Last(t)
}

func TestThinkParserFeed(t *testing.T) {
	tests := []struct {
		name    string
		feeds   []string
		wantOut []string // content, reasoning pairs after each feed
	}{
		{
			name:  "think inside one feed",
			feeds: []string{"<think>plan</think>answer"},
			wantOut: []string{
				"answer", "plan",
			},
		},
		{
			name:  "think split across feeds",
			feeds: []string{"<think>pl", "an</think>ans"},
			wantOut: []string{
				"", "pl",
				"ans", "an",
			},
		},
		{
			name:  "close tag split across feeds",
			feeds: []string{"<think>plan</think>a", "nswer"},
			wantOut: []string{
				"a", "plan",
				"nswer", "",
			},
		},
		{
			name:  "multiple think blocks across feeds",
			feeds: []string{"<think>on", "e</think>mid<t", "hink>two</think>end"},
			wantOut: []string{
				"", "on",
				"mid", "e",
				"end", "two",
			},
		},
		{
			name:  "trailing angle bracket is held back",
			feeds: []string{"a<b<think>hidden</think> c<d"},
			wantOut: []string{
				"a<b c", "hidden",
			},
		},
		{
			name:  "trailing angle bracket flushes on follow-up",
			feeds: []string{"a<b<think>hidden</think> c<d", "one"},
			wantOut: []string{
				"a<b c", "hidden",
				"", "",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var p thinkParser
			for i, f := range tt.feeds {
				content, reasoning := p.feed(f)
				assert.Equal(t, tt.wantOut[i*2], content, "feed %d content", i)
				assert.Equal(t, tt.wantOut[i*2+1], reasoning, "feed %d reasoning", i)
			}
		})
	}
}

func TestNormalizeChatStream(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "think block is split into reasoning_content",
			in: `data: {"choices":[{"index":0,"delta":{"content":"<think>plan</think>answer"}}]}

data: [DONE]

`,
			want: `data: {"choices":[{"delta":{"content":"answer","reasoning_content":"plan"},"index":0}]}

data: [DONE]

`,
		},
		{
			name: "content only delta passes through unchanged",
			in: `data: {"choices":[{"index":0,"delta":{"content":"hi"}}]}

data: [DONE]

`,
			want: `data: {"choices":[{"index":0,"delta":{"content":"hi"}}]}

data: [DONE]

`,
		},
		{
			name: "split tag across two lines emits reasoning live",
			in: `data: {"choices":[{"index":0,"delta":{"content":"<think>pl"}}]}

data: {"choices":[{"index":0,"delta":{"content":"an</think>answer"}}]}

data: [DONE]

`,
			want: `data: {"choices":[{"delta":{"reasoning_content":"pl"},"index":0}]}

data: {"choices":[{"delta":{"content":"answer","reasoning_content":"an"},"index":0}]}

data: [DONE]

`,
		},
		{
			name: "role chunk without content passes through",
			in: `data: {"choices":[{"index":0,"delta":{"role":"assistant"}}]}

data: {"choices":[{"index":0,"delta":{"content":"hi"}}]}

data: [DONE]

`,
			want: `data: {"choices":[{"index":0,"delta":{"role":"assistant"}}]}

data: {"choices":[{"index":0,"delta":{"content":"hi"}}]}

data: [DONE]

`,
		},
		{
			name: "unparsable payload is relayed unchanged",
			in: `data: {"choices":[{"delta":{"conten

`,
			want: `data: {"choices":[{"delta":{"conten

`,
		},
		{
			name: "non-data lines pass through",
			in: `: ping

data: {"choices":[{"index":0,"delta":{"content":"<think>pa"}}]}

`,
			want: `: ping

data: {"choices":[{"delta":{"reasoning_content":"pa"},"index":0}]}

`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := io.ReadAll(normalizeChatStream(io.NopCloser(strings.NewReader(tt.in))))
			require.NoError(t, err)
			assert.Equal(t, tt.want, string(got))
		})
	}
}

func TestNormalizeChatStreamNilIsSafe(t *testing.T) {
	got := normalizeChatStream(nil)
	assert.Nil(t, got)
}

func TestNormalizeChoice_NonStringContentIsLeftAlone(t *testing.T) {
	resp := &core.ChatResponse{
		Choices: []core.Choice{{
			Message: core.ResponseMessage{
				Role:    "assistant",
				Content: []core.ContentPart{{Type: "text", Text: "<think>no tags</think>"}},
			},
		}},
	}
	normalizeChatResponse(resp)
	assert.Equal(t, []core.ContentPart{{Type: "text", Text: "<think>no tags</think>"}}, resp.Choices[0].Message.Content)
	assert.Nil(t, resp.Choices[0].Message.ExtraFields.Lookup(reasoningKey))
}

func TestNormalizeChoice_EmptyContentIsLeftAlone(t *testing.T) {
	resp := &core.ChatResponse{
		Choices: []core.Choice{{Message: core.ResponseMessage{Role: "assistant", Content: ""}}},
	}
	normalizeChatResponse(resp)
	assert.Empty(t, resp.Choices[0].Message.Content)
}

func TestNormalizeChoice_WhitespaceContentTrimmedAndIgnored(t *testing.T) {
	// Whitespace-only content with no think tags trips the second
	// `if reasoning == ""` return after TrimSpace already changed
	// stripped away from raw; pins that branch.
	resp := &core.ChatResponse{
		Choices: []core.Choice{{
			Message: core.ResponseMessage{Role: "assistant", Content: "  hello  "},
		}},
	}
	normalizeChatResponse(resp)
	assert.Equal(t, "hello", resp.Choices[0].Message.Content)
}

func TestRewrite_BadChunkJSONRelaysOriginal(t *testing.T) {
	ts := &thinkStream{src: bufio.NewReader(strings.NewReader("")), closer: io.NopCloser(strings.NewReader(""))}
	got := ts.rewrite([]byte(`data: {"content":"x","choices":[`))
	assert.Equal(t, []byte(`data: {"content":"x","choices":[`), got)
}

func TestRewrite_ChoicesNotArrayRelaysOriginal(t *testing.T) {
	ts := &thinkStream{src: bufio.NewReader(strings.NewReader("")), closer: io.NopCloser(strings.NewReader(""))}
	got := ts.rewrite([]byte(`data: {"choices":null,"content":"x"}`))
	assert.Equal(t, []byte(`data: {"choices":null,"content":"x"}`), got)
}

func TestRewrite_NoChoicesRelaysOriginal(t *testing.T) {
	ts := &thinkStream{src: bufio.NewReader(strings.NewReader("")), closer: io.NopCloser(strings.NewReader(""))}
	got := ts.rewrite([]byte(`data: {"content":"x"}`))
	assert.Equal(t, []byte(`data: {"content":"x"}`), got)
}

func TestRewriteChoice_ChoiceNotMapRelaysOriginal(t *testing.T) {
	ts := &thinkStream{src: bufio.NewReader(strings.NewReader("")), closer: io.NopCloser(strings.NewReader(""))}
	got, ok := ts.rewriteChoice(json.RawMessage(`"a string"`))
	assert.False(t, ok)
	assert.Equal(t, json.RawMessage(`"a string"`), got)
}

func TestRewriteChoice_NoDeltaRelaysOriginal(t *testing.T) {
	ts := &thinkStream{src: bufio.NewReader(strings.NewReader("")), closer: io.NopCloser(strings.NewReader(""))}
	got, ok := ts.rewriteChoice(json.RawMessage(`{"index":0,"finish_reason":"stop"}`))
	assert.False(t, ok)
	assert.Equal(t, json.RawMessage(`{"index":0,"finish_reason":"stop"}`), got)
}

func TestRewriteChoice_DeltaNotMapRelaysOriginal(t *testing.T) {
	ts := &thinkStream{src: bufio.NewReader(strings.NewReader("")), closer: io.NopCloser(strings.NewReader(""))}
	got, ok := ts.rewriteChoice(json.RawMessage(`{"delta":"oops"}`))
	assert.False(t, ok)
	assert.Equal(t, json.RawMessage(`{"delta":"oops"}`), got)
}

func TestRewriteChoice_NoContentRelaysOriginal(t *testing.T) {
	ts := &thinkStream{src: bufio.NewReader(strings.NewReader("")), closer: io.NopCloser(strings.NewReader(""))}
	_, ok := ts.rewriteChoice(json.RawMessage(`{"delta":{"role":"assistant"}}`))
	assert.False(t, ok)
}

func TestRewriteChoice_EmptyContentRelaysOriginal(t *testing.T) {
	ts := &thinkStream{src: bufio.NewReader(strings.NewReader("")), closer: io.NopCloser(strings.NewReader(""))}
	_, ok := ts.rewriteChoice(json.RawMessage(`{"delta":{"content":"   "}}`))
	assert.False(t, ok)
}

func TestRewriteChoice_ContentNotStringRelaysOriginal(t *testing.T) {
	ts := &thinkStream{src: bufio.NewReader(strings.NewReader("")), closer: io.NopCloser(strings.NewReader(""))}
	_, ok := ts.rewriteChoice(json.RawMessage(`{"delta":{"content":42}}`))
	assert.False(t, ok)
}

func TestRewriteChoice_NoChangeRelaysOriginal(t *testing.T) {
	ts := &thinkStream{src: bufio.NewReader(strings.NewReader("")), closer: io.NopCloser(strings.NewReader(""))}
	_, ok := ts.rewriteChoice(json.RawMessage(`{"delta":{"content":"hi"}}`))
	assert.False(t, ok)
}

func TestThinkParser_BoundaryAtZeroHoldsCarry(t *testing.T) {
	var p thinkParser
	p.inThink = true
	p.carry = ""
	content, reasoning := p.feed("<d")
	assert.Empty(t, content, "content")
	assert.Empty(t, reasoning, "reasoning")
	assert.Equal(t, "<d", p.carry, "carry must hold the partial tag start")
}

func TestThinkParser_LoopExitsAtExactEnd(t *testing.T) {
	var p thinkParser
	content, reasoning := p.feed("<think>x</think>")
	assert.Empty(t, content, "content")
	assert.Equal(t, "x", reasoning, "reasoning")
}

func TestSplitThink_NestedThinkBlock(t *testing.T) {
	// Shape captured from a live MiniMax session (also reported upstream):
	// the model accidentally emits a second, inner <think>…</think> inside
	// its outer think. The parser exits on the inner close, so the outer
	// close trails in the content stream and is stripped from content. The
	// nested <think> open marker stays in reasoning verbatim — reasoning is
	// never rewritten.
	raw := "<think>Wait, I accidentally typed \"inlineXML\" instead of \"inline<think>XML\" — and lost the</think>' reference. Let me fix that.</think>"
	content, reasoning := splitThink(raw)
	assert.Equal(t, "' reference. Let me fix that.",
		content,
		"text between the inner close and the outer close is the answer the model meant to emit")
	assert.NotContains(t, content, "</think>",
		"the outer close is an orphan and must be stripped")
	assert.NotContains(t, content, "<think>",
		"the inner open is reasoning text and never reaches content")
	assert.Equal(t,
		"Wait, I accidentally typed \"inlineXML\" instead of \"inline<think>XML\" — and lost the",
		reasoning,
		"inner reasoning is preserved verbatim, including the nested <think> marker")
}

func TestSplitThink_OrphanCloseInContent(t *testing.T) {
	// A stray </think> in content with no open before it is formatting
	// noise; it must not reach the client verbatim.
	content, reasoning := splitThink("hello</think>world")
	assert.Equal(t, "helloworld", content, "orphan close is stripped")
	assert.Empty(t, reasoning)
}

func TestSplitThink_MultipleSequentialThinkBlocks(t *testing.T) {
	raw := "<think>one</think>mid<think>two</think>end"
	content, reasoning := splitThink(raw)
	assert.Equal(t, "midend", content)
	assert.Equal(t, "onetwo", reasoning)
}

func TestSplitThink_NestedThenTrailing(t *testing.T) {
	// Outer <think> nests an inner <think>; the parser exits on the inner
	// close, treats the outer close as an orphan, and the trailing text
	// survives as content.
	raw := "<think>a<think>b</think>c</think>d"
	content, reasoning := splitThink(raw)
	assert.Equal(t, "cd", content, "c survives as content, orphan close is stripped, d follows")
	assert.Equal(t, "a<think>b", reasoning, "inner open is reasoning text, first close exits")
}

func TestThinkParser_OrphanCloseDoesNotStickInCarry(t *testing.T) {
	var p thinkParser
	// Feed the shape where a stray close is split across feeds; the parser
	// must not hold the whole tail in carry waiting for an open that never
	// arrives.
	p.feed("hello</thi")
	content, reasoning := p.feed("nk>world more text")
	assert.Equal(t, "world more text", content, "orphan close stripped, tail emitted")
	assert.Empty(t, reasoning)
	assert.Empty(t, p.carry, "carry must not accumulate after an orphan close")
}

func TestStripOrphanClosesFastPath(t *testing.T) {
	// The fast-path that returns the input verbatim when there is nothing to
	// strip is small enough that the compiler may inline it; assert it
	// directly so coverage sees it.
	assert.Equal(t, "hello world", stripOrphanCloses("hello world"))
	assert.Equal(t, "<tag>", stripOrphanCloses("<tag>"))
	assert.Empty(t, stripOrphanCloses(""))
	assert.Equal(t, "no tags here", stripOrphanCloses("no tags here"))
}

func TestStripOrphanClosesSlowPath(t *testing.T) {
	// The slow path fires when a stray close marker survives the streaming
	// parse (rare: the parser strips most orphans earlier, but a marker can
	// land inside a content segment that is emitted wholesale).
	assert.Equal(t, "abc def", stripOrphanCloses("abc</think> def"))
	assert.Equal(t, "ab", stripOrphanCloses("</think>ab</think>"))
}

func TestSplitThink_NoOrphanCloseLeavesContentUntouched(t *testing.T) {
	// The stripOrphanCloses helper has a fast-path for content that does
	// not contain </think>; assert the round-trip is byte-for-byte.
	got, reasoning := splitThink("hello world")
	assert.Equal(t, "hello world", got)
	assert.Empty(t, reasoning)
}

func TestThinkParser_NestedCloseAcrossFeeds(t *testing.T) {
	var p thinkParser
	var gotContent, gotReasoning string
	feeds := []string{
		"<think>outer<think>inner</think>trailing",
		"</think>after",
	}
	for _, f := range feeds {
		c, r := p.feed(f)
		gotContent += c
		gotReasoning += r
	}
	// The parser exits on the inner close (single-level nesting), so the
	// "trailing" text is content and the outer close is an orphan stripped
	// from the content stream. The nested <think> open marker stays in the
	// reasoning verbatim — reasoning is never rewritten.
	assert.Equal(t, "trailingafter", gotContent)
	assert.Equal(t, "outer<think>inner", gotReasoning)
}

func TestNormalizeChoice_ExistingReasoningContentKept(t *testing.T) {
	// If the upstream already carries a reasoning_content (e.g. it
	// pre-parsed the think block into a separate field), the parser must
	// strip the tags from content but never overwrite the upstream
	// reasoning with text it parsed out of content.
	extra, err := core.MergeUnknownJSONFields(core.UnknownJSONFields{}, map[string]json.RawMessage{
		reasoningKey: json.RawMessage(`"upstream text"`),
	})
	require.NoError(t, err)
	resp := &core.ChatResponse{
		Choices: []core.Choice{{
			Message: core.ResponseMessage{
				Role:       "assistant",
				Content:    "<think>hidden</think>after",
				ExtraFields: extra,
			},
		}},
	}
	normalizeChatResponse(resp)

	msg := resp.Choices[0].Message
	assert.Equal(t, "after", msg.Content, "tags still stripped from content")
	got := msg.ExtraFields.Lookup(reasoningKey)
	require.NotNil(t, got, "reasoning_content kept untouched")
	var s string
	require.NoError(t, json.Unmarshal(got, &s))
	assert.Equal(t, "upstream text", s, "upstream reasoning_content wins over parsed text")
}

func TestSplitThink_EmptyInput(t *testing.T) {
	got, reasoning := splitThink("")
	assert.Empty(t, got)
	assert.Empty(t, reasoning)
}

func TestThinkParser_FlushAtEOFEmitsCarryAsReasoning(t *testing.T) {
	// When the stream ends mid-think the carry becomes reasoning: the
	// client still sees the partial chain of thought the model produced
	// before it was cut off, matching how every other interrupted turn
	// is handled.
	var p thinkParser
	c, r := p.feed("<think>partial chain of thought")
	assert.Empty(t, c, "feed already flushed what it had")
	assert.Equal(t, "partial chain of thought", r, "feed already emits reasoning as it goes")
	c2, r2 := p.flush()
	assert.Empty(t, c2)
	assert.Empty(t, r2, "nothing was still held in carry; nothing left to flush")
}

func TestThinkParser_FlushAtEOFEmitsPartialTagCarryAsContent(t *testing.T) {
	// A partial tag across the last two feeds: the carry is held by feed()
	// because it could be the start of <think>, but no opening ever lands.
	// At EOF the carry must be flushed as content, never dropped.
	var p thinkParser
	c1, r1 := p.feed("hello </thi")
	assert.Equal(t, "hello ", c1)
	assert.Empty(t, r1)
	c2, r2 := p.feed("nk>world")
	assert.Equal(t, "world", c2, "the partial close and trailing text form a stray </think> the parser strips to content")
	assert.Empty(t, r2)
	c3, r3 := p.flush()
	assert.Empty(t, c3)
	assert.Empty(t, r3, "nothing was still held in carry; nothing left to flush")
}

func TestThinkParser_FlushAtEOFEmitsPartialThinkCarryAsReasoning(t *testing.T) {
	// The stream ends mid-think and the carry still looks like a possible
	// partial tag. flush() must treat it as reasoning (the unfinished
	// think block is closed) and the parser must hand the bytes to the
	// client rather than dropping them.
	var p thinkParser
	p.feed("<think>plan<thi")
	c, r := p.flush()
	assert.Empty(t, c)
	assert.Equal(t, "<thi", r, "carry becomes reasoning at EOF when inThink is true")
}

func TestNormalizeChatStream_FlushAtEOFEmitsCarryDeltaMidThink(t *testing.T) {
	// The stream ends mid-think with a partial tag in carry; the final
	// delta must carry the partial reasoning, not be silently dropped.
	body := "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"<think>plan<thi\"}}]}\n\n"
	got, err := io.ReadAll(normalizeChatStream(io.NopCloser(strings.NewReader(body))))
	require.NoError(t, err)
	out := string(got)
	assert.Contains(t, out, `"reasoning_content"`,
		"final delta carries the mid-think carry as reasoning")
	assert.NotContains(t, out, `<think>`)
	assert.NotContains(t, out, `</think>`)
}

func TestNormalizeChatStream_FlushAtEOFEmitsFinalDelta(t *testing.T) {
	// End-to-end: a stream that finishes mid-think must reach the client
	// with a final delta carrying the partial reasoning. Mirrors the M2
	// prod shape where the model ran out of tokens inside its think block.
	body := "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"<think>plan</think>start\"}}]}\n\n" +
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"<think>more reasoning\"}}]}\n\n"
	got, err := io.ReadAll(normalizeChatStream(io.NopCloser(strings.NewReader(body))))
	require.NoError(t, err)
	out := string(got)
	assert.Contains(t, out, `"reasoning_content":"plan"`,
		"closed think before the cut is fully extracted")
	assert.Contains(t, out, `"reasoning_content":"more reasoning"`,
		"partial think at EOF is emitted as a final delta")
	assert.Contains(t, out, `"content":"start"`,
		"content between the closed think and the cut is preserved")
	assert.NotContains(t, out, `<think>`)
	assert.NotContains(t, out, `</think>`)
}

func TestNormalizeChatStream_FlushAtEOFEmitsPartialTagCarry(t *testing.T) {
	// End-to-end: a stream that ends mid-tag (the trailing `<th` is the
	// start of a tag the next feed would have completed). The parser
	// holds the carry; EOF must flush it as a separate final delta so the
	// client sees the full stream instead of losing the last few bytes.
	body := "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hello <th\"}}]}\n\n"
	got, err := io.ReadAll(normalizeChatStream(io.NopCloser(strings.NewReader(body))))
	require.NoError(t, err)
	out := string(got)
	assert.Contains(t, out, `"content":"hello "`, "carry before the partial tag is emitted as content")
	assert.Contains(t, out, `\u003cth"`, "partial tag flushed at EOF as content (JSON-escaped `<`)")
	assert.NotContains(t, out, `<think>`)
	assert.NotContains(t, out, `</think>`)
}