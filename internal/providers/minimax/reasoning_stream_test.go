package minimax

import (
	"bufio"
	"io"
	"strings"
	"testing"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
			name:  "multiple think blocks toggle sequentially in fast mode",
			feeds: []string{"<think>on", "e</think>mid<t", "hink>two</think>end"},
			wantOut: []string{
				"", "on",
				"mid", "e",
				"end", "two",
			},
		},
		{
			name:  "marker prefix at end of feed is held back",
			feeds: []string{"answer<th", "ink>hidden</think>done"},
			wantOut: []string{
				"answer", "",
				"done", "hidden",
			},
		},
		{
			name:  "non-marker angle bracket passes through immediately",
			feeds: []string{"a<b<think>hidden</think> c<d"},
			wantOut: []string{
				"a<b c<d", "hidden",
			},
		},
		{
			name:  "non-marker angle bracket follow-up emits immediately",
			feeds: []string{"a<b<think>hidden</think> c<d", "one"},
			wantOut: []string{
				"a<b c<d", "hidden",
				"one", "",
			},
		},
		{
			name:  "invalid angle suffix passes through in the same feed",
			feeds: []string{"answer<invalid"},
			wantOut: []string{
				"answer<invalid", "",
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

func TestNormalizeChatStream_PerChoiceParsers(t *testing.T) {
	// With n>1 each choice keeps its own think state: a think block in
	// choice 0 must not reclassify choice 1's normal content as
	// reasoning_content.
	body := `data: {"choices":[{"index":0,"delta":{"content":"<think>plan"}},{"index":1,"delta":{"content":"answer one"}}]}

data: {"choices":[{"index":0,"delta":{"content":"</think>answer zero"}},{"index":1,"delta":{"content":" more"}}]}

data: [DONE]

`
	got, err := io.ReadAll(normalizeChatStream(io.NopCloser(strings.NewReader(body))))
	require.NoError(t, err)
	out := string(got)
	assert.Contains(t, out, `{"delta":{"reasoning_content":"plan"},"index":0}`,
		"choice 0 think block becomes reasoning_content")
	assert.Contains(t, out, `{"index":1,"delta":{"content":"answer one"}}`,
		"choice 1 content passes through as content, not reasoning")
	assert.Contains(t, out, `{"delta":{"content":"answer zero"},"index":0}`,
		"choice 0 close marker returns its parser to content")
	assert.Contains(t, out, `{"index":1,"delta":{"content":" more"}}`,
		"choice 1 stays in content mode throughout")
}

func TestNormalizeChatStream_CarryFlushesBeforeDone(t *testing.T) {
	// A stream that ends mid-tag right before [DONE]: clients stop reading
	// at [DONE], so the held carry must flush as a complete synthetic delta
	// frame BEFORE the terminal event, not at the following EOF.
	body := "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hello <th\"}}]}\n\n" +
		"data: [DONE]\n\n"
	got, err := io.ReadAll(normalizeChatStream(io.NopCloser(strings.NewReader(body))))
	require.NoError(t, err)

	frames := strings.Split(strings.TrimRight(string(got), "\n"), "\n\n")
	require.Len(t, frames, 3, "rewritten delta, synthetic carry frame, [DONE]")
	assert.Contains(t, frames[0], `"content":"hello "`, "carry before the partial tag emits as content")
	assert.Contains(t, frames[1], `\u003cth`, "partial tag flushes as a synthetic delta")
	assert.Contains(t, frames[1], `"index":0`, "synthetic delta carries its choice index")
	assert.Equal(t, "data: [DONE]", frames[2], "carry frame precedes the terminal [DONE] event")
}

func TestNormalizeChatStream_CarryFlushesPerChoiceBeforeDone(t *testing.T) {
	// With n>1 the EOF/[DONE] flush must emit one synthetic delta per
	// choice that has pending carry, each with its own index, in index
	// order — never everything attached to choice 0.
	body := "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"zero <th\"}},{\"index\":1,\"delta\":{\"content\":\"one </th\"}}]}\n\n" +
		"data: [DONE]\n\n"
	got, err := io.ReadAll(normalizeChatStream(io.NopCloser(strings.NewReader(body))))
	require.NoError(t, err)

	frames := strings.Split(strings.TrimRight(string(got), "\n"), "\n\n")
	require.Len(t, frames, 4, "rewritten delta, two synthetic carry frames, [DONE]")
	assert.Contains(t, frames[1], `\u003cth`, "choice 0 carry flushes")
	assert.Contains(t, frames[1], `"index":0`, "choice 0 carry frame keeps index 0")
	assert.Contains(t, frames[2], `\u003c/th`, "choice 1 carry flushes")
	assert.Contains(t, frames[2], `"index":1`, "choice 1 carry frame keeps index 1")
	assert.Equal(t, "data: [DONE]", frames[3], "both carry frames precede [DONE]")
}

func TestNormalizeChatStream_ExistingReasoningContentKept(t *testing.T) {
	// Streaming mirror of the buffered rule: a delta that already carries
	// reasoning_content keeps it verbatim; the parser strips the think
	// tags from content but never overwrites the existing member.
	body := `data: {"choices":[{"index":0,"delta":{"content":"<think>hidden</think>after","reasoning_content":"upstream"}}]}

data: [DONE]

`
	got, err := io.ReadAll(normalizeChatStream(io.NopCloser(strings.NewReader(body))))
	require.NoError(t, err)
	out := string(got)
	assert.Contains(t, out, `"content":"after"`, "tags still stripped from content")
	assert.Contains(t, out, `"reasoning_content":"upstream"`, "upstream reasoning_content wins over parsed text")
	assert.NotContains(t, out, "hidden", "parsed reasoning is discarded, not merged")
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

func TestRewriteChoice_NonNumericIndexFallsBackToZero(t *testing.T) {
	ts := &thinkStream{src: bufio.NewReader(strings.NewReader("")), closer: io.NopCloser(strings.NewReader(""))}
	got, ok := ts.rewriteChoice(json.RawMessage(`{"index":"x","delta":{"content":"<think>plan</think>answer"}}`))
	require.True(t, ok)
	assert.Contains(t, string(got), `"reasoning_content":"plan"`)
	assert.NotNil(t, ts.parsers[0], "non-numeric index uses the choice 0 parser")
}

func TestThinkParser_BoundaryAtZeroHoldsCarry(t *testing.T) {
	var p thinkParser
	p.inThink = true
	p.carry = ""
	content, reasoning := p.feed("</th")
	assert.Empty(t, content, "content")
	assert.Empty(t, reasoning, "reasoning")
	assert.Equal(t, "</th", p.carry, "carry must hold the partial close marker")
}

func TestThinkParser_LoopExitsAtExactEnd(t *testing.T) {
	var p thinkParser
	content, reasoning := p.feed("<think>x</think>")
	assert.Empty(t, content, "content")
	assert.Equal(t, "x", reasoning, "reasoning")
}

func TestThinkParser_NamespacedCloseAcrossFeeds(t *testing.T) {
	// The namespaced close marker is split across two feeds: the parser
	// must recognise it once the pieces join.
	var p thinkParser
	var gotContent, gotReasoning string
	for _, f := range []string{"<think>plan</mm:", "think>answer"} {
		c, r := p.feed(f)
		gotContent += c
		gotReasoning += r
	}
	assert.Equal(t, "answer", gotContent)
	assert.Equal(t, "plan", gotReasoning)
}

func TestNormalizeChatStream_NamespacedClose(t *testing.T) {
	// End-to-end: the namespaced close must be consumed in the stream and
	// never reach the client.
	body := "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"<think>plan</mm:think>answer\"}}]}\n\n"
	got, err := io.ReadAll(normalizeChatStream(io.NopCloser(strings.NewReader(body))))
	require.NoError(t, err)
	out := string(got)
	assert.Contains(t, out, `"reasoning_content":"plan"`)
	assert.Contains(t, out, `"content":"answer"`)
	assert.NotContains(t, out, `</mm:think>`)
	assert.NotContains(t, out, `</think>`)
}

func TestThinkParser_OrphanCloseDoesNotStickInCarry(t *testing.T) {
	var p thinkParser
	// Feed the shape where a stray close is split across feeds; the parser
	// must not hold the whole tail in carry waiting for an open that never
	// arrives. The orphan marker is escaped, not stripped, so the text
	// stays visible in the transcript.
	p.feed("hello</thi")
	content, reasoning := p.feed("nk>world more text")
	assert.Equal(t, `\<\/think\>world more text`, content, "orphan close escaped, tail emitted")
	assert.Empty(t, reasoning)
	assert.Empty(t, p.carry, "carry must not accumulate after an orphan close")
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
	// The parser exits on the inner close at once, so the "trailing" text
	// is content and the outer close is an orphan escaped into the content
	// stream. The nested <think> open marker stays in the reasoning
	// verbatim — reasoning is never rewritten.
	assert.Equal(t, `trailing\<\/think\>after`, gotContent)
	assert.Equal(t, "outer<think>inner", gotReasoning)
}

func TestThinkParser_DiscordShapeAcrossFeeds(t *testing.T) {
	// The shape reported upstream: the model reasons about the tags
	// themselves and writes a literal <think> and a literal </think>
	// mid-sentence. Streaming cannot wait for confirmation, so the literal
	// close ends the reasoning early and the rest of the trace lands in
	// content. The tag bytes themselves never reach the client as tags —
	// the orphan close is escaped into visible text. This is the pinned
	// worst case, and it still beats full XML passthrough. The buffered
	// splitThink handles the same shape exactly.
	var p thinkParser
	var gotContent, gotReasoning string
	feeds := []string{
		`<think>Wait, I accidentally typed "inlineXML" instead of "inline<th`,
		`ink>XML" — and lost the</think>' reference. `,
		"Let me fix that.</think>Done.",
	}
	for _, f := range feeds {
		c, r := p.feed(f)
		gotContent += c
		gotReasoning += r
	}
	c, _ := p.flush()
	gotContent += c
	assert.Equal(t, `' reference. Let me fix that.\<\/think\>Done.`, gotContent)
	assert.Equal(t,
		`Wait, I accidentally typed "inlineXML" instead of "inline<think>XML" — and lost the`,
		gotReasoning,
		"reasoning ends at the literal close; the nested open stays reasoning text verbatim")
}

func TestThinkParser_OrphanCloseEscapedInContent(t *testing.T) {
	// Pinned accepted trade-off: without a nested open there is no hold,
	// so a lone literal </think> inside reasoning closes the block early.
	// The tail lands in content with the orphan close escaped as visible
	// text; nothing sticks in carry.
	var p thinkParser
	var gotContent, gotReasoning string
	for _, f := range []string{"<think>a</think>b</think>c"} {
		c, r := p.feed(f)
		gotContent += c
		gotReasoning += r
	}
	c, _ := p.flush()
	gotContent += c
	assert.Equal(t, `b\<\/think\>c`, gotContent)
	assert.Equal(t, "a", gotReasoning)
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
	// The joined bytes form a stray close that is escaped into content.
	var p thinkParser
	c1, r1 := p.feed("hello </thi")
	assert.Equal(t, "hello ", c1)
	assert.Empty(t, r1)
	c2, r2 := p.feed("nk>world")
	assert.Equal(t, `\<\/think\>world`, c2, "the joined stray close is escaped into content")
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

func TestThinkParser_NamespacedOpenVariants(t *testing.T) {
	tests := []struct {
		name          string
		feeds         []string
		wantContent   string
		wantReasoning string
	}{
		{
			name:          "mm namespaced open and close",
			feeds:         []string{"<mm:think>plan</mm:think>answer"},
			wantContent:   "answer",
			wantReasoning: "plan",
		},
		{
			name:          "minimax namespaced open and close",
			feeds:         []string{"<minimax:think>plan</minimax:think>answer"},
			wantContent:   "answer",
			wantReasoning: "plan",
		},
		{
			name:          "plain open with minimax namespaced close",
			feeds:         []string{"<think>plan</minimax:think>answer"},
			wantContent:   "answer",
			wantReasoning: "plan",
		},
		{
			name: "degraded close sequence",
			// The broken </mm> fragment is not a marker and stays reasoning
			// text; the close is recognized at the </minimax:think> tail.
			feeds:         []string{"<think>reasoning</mm></minimax:think>answer"},
			wantContent:   "answer",
			wantReasoning: "reasoning</mm>",
		},
		{
			name:          "namespaced open split across feeds",
			feeds:         []string{"answer<mm:th", "ink>hidden</mm:think>done"},
			wantContent:   "answerdone",
			wantReasoning: "hidden",
		},
		{
			name:          "minimax namespaced close split across feeds",
			feeds:         []string{"<think>plan</minimax:th", "ink>answer"},
			wantContent:   "answer",
			wantReasoning: "plan",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var p thinkParser
			var gotContent, gotReasoning string
			for _, f := range tt.feeds {
				c, r := p.feed(f)
				gotContent += c
				gotReasoning += r
			}
			assert.Equal(t, tt.wantContent, gotContent, "content")
			assert.Equal(t, tt.wantReasoning, gotReasoning, "reasoning")
		})
	}
}

func TestNormalizeChatStream_MinimaxNamespacedClose(t *testing.T) {
	// End-to-end: the </minimax:think> close must be consumed in the
	// stream and never reach the client.
	body := "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"<think>plan</minimax:think>answer\"}}]}\n\n"
	got, err := io.ReadAll(normalizeChatStream(io.NopCloser(strings.NewReader(body))))
	require.NoError(t, err)
	out := string(got)
	assert.Contains(t, out, `"reasoning_content":"plan"`)
	assert.Contains(t, out, `"content":"answer"`)
	assert.NotContains(t, out, `</minimax:think>`)
}

func TestNormalizeChatStream_FinishFlushesCarryBeforeFinish(t *testing.T) {
	// Regression: a content delta ending with a partial marker parks bytes
	// in the choice's carry; a finish-only delta carries no content and
	// would bypass the rewrite, forwarding finish_reason BEFORE the carry
	// flushed at [DONE]. Clients treating finish as terminal would lose the
	// carried text, so the carry must flush as a synthetic delta frame
	// before the finish chunk.
	body := "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"answer <th\"}}]}\n\n" +
		"data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"length\"}]}\n\n" +
		"data: [DONE]\n\n"
	got, err := io.ReadAll(normalizeChatStream(io.NopCloser(strings.NewReader(body))))
	require.NoError(t, err)

	frames := strings.Split(strings.TrimRight(string(got), "\n"), "\n\n")
	require.Len(t, frames, 4, "rewritten delta, synthetic carry frame, finish chunk, [DONE]")
	assert.Contains(t, frames[0], `"content":"answer "`, "carry before the partial tag emits as content")
	assert.Contains(t, frames[1], `\u003cth`, "carry flushes as a synthetic delta")
	assert.Contains(t, frames[1], `"index":0`, "synthetic delta carries its choice index")
	assert.Contains(t, frames[2], `"finish_reason":"length"`, "finish chunk follows the carry frame")
	assert.Equal(t, "data: [DONE]", frames[3], "nothing left to flush at [DONE]")
}

func TestNormalizeChatStream_FinishFlushesOnlyFinishingChoice(t *testing.T) {
	// Multi-choice: a finish_reason on choice 1 must flush only choice 1's
	// carry. Choice 0's carry stays held until its own finish or [DONE].
	body := "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"zero <th\"}},{\"index\":1,\"delta\":{\"content\":\"one </th\"}}]}\n\n" +
		"data: {\"choices\":[{\"index\":1,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: [DONE]\n\n"
	got, err := io.ReadAll(normalizeChatStream(io.NopCloser(strings.NewReader(body))))
	require.NoError(t, err)

	frames := strings.Split(strings.TrimRight(string(got), "\n"), "\n\n")
	require.Len(t, frames, 5, "rewritten delta, choice 1 carry frame, finish chunk, choice 0 carry frame, [DONE]")
	assert.Contains(t, frames[1], `\u003c/th`, "choice 1 carry flushes at its finish")
	assert.Contains(t, frames[1], `"index":1`)
	assert.Contains(t, frames[2], `"finish_reason":"stop"`)
	assert.Contains(t, frames[3], `\u003cth`, "choice 0 carry is untouched by choice 1's finish and flushes at [DONE]")
	assert.Contains(t, frames[3], `"index":0`)
	assert.Equal(t, "data: [DONE]", frames[4])
}

func TestNormalizeChatStream_NullFinishReasonDoesNotFlush(t *testing.T) {
	// A non-terminal finish_reason: null must not trigger the carry flush;
	// the carry stays held for the next content delta.
	body := "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hello <th\"}}]}\n\n" +
		"data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":null}]}\n\n" +
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ink>hidden</think>done\"}}]}\n\n" +
		"data: [DONE]\n\n"
	got, err := io.ReadAll(normalizeChatStream(io.NopCloser(strings.NewReader(body))))
	require.NoError(t, err)

	frames := strings.Split(strings.TrimRight(string(got), "\n"), "\n\n")
	require.Len(t, frames, 4, "rewritten delta, null-finish chunk, final delta, [DONE]")
	assert.Contains(t, frames[1], `"finish_reason":null`, "null finish chunk passes through")
	assert.Contains(t, frames[2], `"reasoning_content":"hidden"`, "carry completed the tag across the null-finish chunk")
	assert.Contains(t, frames[2], `"content":"done"`)
}

func TestNormalizeChatStream_FinishWithoutCarryEmitsNoSyntheticFrame(t *testing.T) {
	// A finishing choice whose parser holds nothing must forward the
	// finish chunk with no synthetic frame.
	body := "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"}}]}\n\n" +
		"data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: [DONE]\n\n"
	got, err := io.ReadAll(normalizeChatStream(io.NopCloser(strings.NewReader(body))))
	require.NoError(t, err)

	frames := strings.Split(strings.TrimRight(string(got), "\n"), "\n\n")
	require.Len(t, frames, 3, "content delta, finish chunk, [DONE]")
	assert.Contains(t, frames[1], `"finish_reason":"stop"`)
}

func TestNormalizeChatStream_FinishFlushesMidThinkCarryAsReasoning(t *testing.T) {
	// finish_reason: length mid-think: the held partial-tag carry flushes
	// as reasoning before the finish chunk — the unfinished think block is
	// treated as closed, matching the [DONE] posture.
	body := "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"<think>plan<thi\"}}]}\n\n" +
		"data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"length\"}]}\n\n" +
		"data: [DONE]\n\n"
	got, err := io.ReadAll(normalizeChatStream(io.NopCloser(strings.NewReader(body))))
	require.NoError(t, err)

	frames := strings.Split(strings.TrimRight(string(got), "\n"), "\n\n")
	require.Len(t, frames, 4, "rewritten delta, synthetic carry frame, finish chunk, [DONE]")
	assert.Contains(t, frames[1], `"reasoning_content":"\u003cthi"`, "mid-think carry flushes as reasoning")
	assert.Contains(t, frames[2], `"finish_reason":"length"`)
}

func TestNormalizeChatStream_FinishForUnknownChoiceEmitsNoFrame(t *testing.T) {
	// A finish chunk for a choice that never carried content has no parser
	// entry; the finish chunk forwards with no synthetic frame.
	body := "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"}}]}\n\n" +
		"data: {\"choices\":[{\"index\":1,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: [DONE]\n\n"
	got, err := io.ReadAll(normalizeChatStream(io.NopCloser(strings.NewReader(body))))
	require.NoError(t, err)

	frames := strings.Split(strings.TrimRight(string(got), "\n"), "\n\n")
	require.Len(t, frames, 3, "content delta, finish chunk, [DONE]")
	assert.Contains(t, frames[1], `"finish_reason":"stop"`)
	assert.Contains(t, frames[1], `"index":1`)
}

func TestFinishCarryFrame_ChoiceNotMapReturnsNil(t *testing.T) {
	ts := &thinkStream{src: bufio.NewReader(strings.NewReader("")), closer: io.NopCloser(strings.NewReader(""))}
	assert.Nil(t, ts.finishCarryFrame(json.RawMessage(`"a string"`)))
}
