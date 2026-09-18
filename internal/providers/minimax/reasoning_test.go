package minimax

import (
	"context"
	"net/http"
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
		{"minimax-m3", true},
		{"MiniMax-M2.7", true},
		{"minimax-m2.7", true},
		{"MiniMax-M2", true},
		{"MiniMax-M2.5", true},
		{"MINIMAX-M2.5", true},
		{"MiniMax-M3.1", false},
		{"MiniMax-M3-0301", false},
		{"minimax-m4", false},
		{"minimax-m2.1", false},
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
			name:      "multiple think blocks split sequentially",
			in:        "<think>one</think>mid<think>two</think>end",
			wantText:  "midend",
			wantThink: "onetwo",
		},
		{
			name:      "M2 prod shape: closed think followed by max_tokens cutoff",
			in:        "<think>The user asks a simple question: \"How many primes below 30?\" This is a straightforward math question.\n\nWe need to comply. They ask: \"How many primes below 30?\" We must answer concisely, but not too terse. Let's think: primes less than 30: 2,3,5,7,11,13,17,19,23,29. That's 10 primes. So answer: 10.\n\nWe need to see any instructions: The developer message is empty, but system is default.\n\nWe also see the developer message is empty, but then we have a \"caveman ultra output style\" in the \"Context compression notice\"? That is not a system instruction, it's an instruction in the conversation. Wait let's read carefully:\n\nThe conversation:\n\n```\nSystem: ...\nUser: How many primes below 30?\n```\n\nWait we see the system messages: There's a system message (first). Then we\n</think>\n",
			wantText:  "\n",
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
			name:         "multiple think blocks split sequentially",
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
			name:         "orphan close in content is escaped without reasoning",
			content:      "hello</think>world",
			wantContent:  `hello\<\/think\>world`,
			wantThinkKey: false,
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

func TestNormalizeChoice_WhitespaceContentPreserved(t *testing.T) {
	// Whitespace-only padding with no think tags must survive verbatim:
	// splitThink never trims content, so normalizeChoice takes the
	// early `stripped == raw` return and leaves the message untouched.
	resp := &core.ChatResponse{
		Choices: []core.Choice{{
			Message: core.ResponseMessage{Role: "assistant", Content: "  hello  "},
		}},
	}
	normalizeChatResponse(resp)
	assert.Equal(t, "  hello  ", resp.Choices[0].Message.Content)
}

func TestSplitThink_NestedThinkBlock(t *testing.T) {
	// Shape captured from a live MiniMax session (also reported upstream):
	// the model accidentally emits a second, inner <think>…</think> inside
	// its outer think. Confirmed-close matching treats the LAST closing
	// marker as the real close, so the inner open and the inner close stay
	// as reasoning text and only the trailing outer close ends the block.
	raw := "Outer<think>'inner reasoning now can contain orphaned single </think>' reference. Let me fix that.</think>outer-after"
	content, reasoning := splitThink(raw)
	assert.Equal(t, "Outerouter-after",
		content,
		"text before the first <think> and after the last </think> is content")
	assert.NotContains(t, content, "<think>",
		"the inner open is reasoning text and never reaches content")
	assert.Equal(t,
		"'inner reasoning now can contain orphaned single </think>' reference. Let me fix that.",
		reasoning,
		"confirmed-close matching keeps every literal marker the model wrote inside its reasoning")
}

func TestSplitThink_OrphanOpenAndCloseInReasoning(t *testing.T) {
	// The model reasons about the tags themselves: the reasoning mentions
	// a literal <think>, then a literal </think>, then another literal
	// <think>. The literal close is followed by another open before the
	// next close, so confirmed-close matching reads it as a chained block
	// boundary — the same call the streaming parser makes when a new
	// <think> resolves a held close. The shape is genuinely ambiguous, and
	// both paths resolve it the same way.
	raw := "Outer<think>'inner reasoning now can contain orphaned single <think> or </think> wich would result in new reasoning wich it shouldnt or closing it early cause no second prerunning <think> so it can be aware of it'</think>outer-after"
	content, reasoning := splitThink(raw)
	assert.Equal(t,
		"Outer wich would result in new reasoning wich it shouldnt or closing it early cause no second prerunning outer-after",
		content,
		"the text between the literal close and the next literal open reads as content between chained blocks")
	assert.Equal(t,
		"'inner reasoning now can contain orphaned single <think> or  so it can be aware of it'",
		reasoning,
		"the two reasoning bodies concatenate; the markers that formed the chain boundary are consumed")
}

func TestSplitThink_OrphanCloseInContent(t *testing.T) {
	// A stray </think> in content with no open before it is kept visible
	// as Markdown-escaped text so the model's reasoning trace survives in
	// history instead of vanishing from the transcript.
	content, reasoning := splitThink("hello</think>world")
	assert.Equal(t, `hello\<\/think\>world`, content, "orphan close is escaped, not stripped")
	assert.Empty(t, reasoning)
}

func TestSplitThink_MultipleSequentialThinkBlocks(t *testing.T) {
	// A model can think, answer, and think again. Confirmed-close matching
	// ends each block at its own close marker, so the text between blocks
	// is content and the reasoning bodies concatenate.
	raw := "<think>one</think>mid<think>two</think>end"
	content, reasoning := splitThink(raw)
	assert.Equal(t, "midend", content)
	assert.Equal(t, "onetwo", reasoning)
}

func TestSplitThink_NestedThenTrailing(t *testing.T) {
	// Confirmed-close matching: the LAST </think> is the real close. The
	// inner close is reasoning text; the trailing text after the real
	// close is content.
	raw := "<think>a<think>b</think>c</think>d"
	content, reasoning := splitThink(raw)
	assert.Equal(t, "d", content)
	assert.Equal(t, "a<think>b</think>c", reasoning)
}

func TestSplitThink_NamespacedClose(t *testing.T) {
	// MiniMax sometimes closes the block with the namespaced </mm:think>
	// variant instead of </think>. Both spellings must be accepted.
	raw := "<think>reasoning here</mm:think>answer"
	content, reasoning := splitThink(raw)
	assert.Equal(t, "answer", content)
	assert.Equal(t, "reasoning here", reasoning)
}

func TestSplitThink_NamespacedCloseInsideContent(t *testing.T) {
	// The namespaced close is an orphan in content just like the plain
	// one: escaped as visible text, never leaked as a tag.
	content, reasoning := splitThink("hello</mm:think>world")
	assert.Equal(t, `hello\<\/mm:think\>world`, content)
	assert.Empty(t, reasoning)
}

func TestSplitThink_EarliestCloseWins(t *testing.T) {
	// Both spellings present: the LATEST one in the source terminates
	// the block (confirmed-close) and the earlier one stays as reasoning
	// text.
	raw := "<think>plan</think>a</mm:think>b"
	content, reasoning := splitThink(raw)
	assert.Equal(t, "b", content)
	assert.Equal(t, "plan</think>a", reasoning)
}

func TestSplitThink_NamespacedOpenAndClose(t *testing.T) {
	// The namespaced <mm:think> open spelling is accepted just like the
	// plain <think> one, paired with its matching close.
	raw := "<mm:think>reasoning here</mm:think>answer"
	content, reasoning := splitThink(raw)
	assert.Equal(t, "answer", content)
	assert.Equal(t, "reasoning here", reasoning)
}

func TestSplitThink_MinimaxNamespacedClose(t *testing.T) {
	// The </minimax:think> close spelling ends the block like any other
	// accepted close marker.
	raw := "<think>plan</minimax:think>answer"
	content, reasoning := splitThink(raw)
	assert.Equal(t, "answer", content)
	assert.Equal(t, "plan", reasoning)
}

func TestSplitThink_MinimaxNamespacedOpenAndClose(t *testing.T) {
	raw := "<minimax:think>plan</minimax:think>answer"
	content, reasoning := splitThink(raw)
	assert.Equal(t, "answer", content)
	assert.Equal(t, "plan", reasoning)
}

func TestSplitThink_DegradedCloseSequence(t *testing.T) {
	// Degraded long-context shape: the model emits a broken </mm> fragment
	// immediately before the real </minimax:think> close. The close is
	// recognized at the </minimax:think> tail; the </mm> prefix is not a
	// marker and stays as reasoning text verbatim.
	raw := "<think>reasoning</mm></minimax:think>answer"
	content, reasoning := splitThink(raw)
	assert.Equal(t, "answer", content)
	assert.Equal(t, "reasoning</mm>", reasoning)
}

func TestSplitThink_MinimaxNamespacedCloseInsideContent(t *testing.T) {
	// The </minimax:think> close is an orphan in content just like the
	// other spellings: escaped as visible text, never leaked as a tag.
	content, reasoning := splitThink("hello</minimax:think>world")
	assert.Equal(t, `hello\<\/minimax:think\>world`, content)
	assert.Empty(t, reasoning)
}

func TestEscapeOrphanClosesFastPath(t *testing.T) {
	// The fast-path that returns the input verbatim when there is nothing to
	// escape is small enough that the compiler may inline it; assert it
	// directly so coverage sees it.
	assert.Equal(t, "hello world", escapeOrphanCloses("hello world"))
	assert.Equal(t, "<tag>", escapeOrphanCloses("<tag>"))
	assert.Empty(t, escapeOrphanCloses(""))
	assert.Equal(t, "no tags here", escapeOrphanCloses("no tags here"))
}

func TestEscapeOrphanClosesSlowPath(t *testing.T) {
	// The slow path fires when a stray close marker survives the streaming
	// parse (rare: the parser escapes most orphans earlier, but a marker can
	// land inside a content segment that is emitted wholesale).
	assert.Equal(t, `abc\<\/think\> def`, escapeOrphanCloses("abc</think> def"))
	assert.Equal(t, `\<\/think\>ab\<\/think\>`, escapeOrphanCloses("</think>ab</think>"))
	assert.Equal(t, `a\<\/mm:think\>b`, escapeOrphanCloses("a</mm:think>b"))
}

func TestSplitThink_NoOrphanCloseLeavesContentUntouched(t *testing.T) {
	// No think blocks at all: the round-trip must be byte-for-byte.
	got, reasoning := splitThink("hello world")
	assert.Equal(t, "hello world", got)
	assert.Empty(t, reasoning)
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
				Role:        "assistant",
				Content:     "<think>hidden</think>after",
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
