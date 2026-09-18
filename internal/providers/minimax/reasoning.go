package minimax

import (
	"strings"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
)

// MiniMax reasoning models (M3, M2.7, M2.5, M2) ship their chain of
// thought as inline <think>...</think> XML inside the answer content. Other
// MiniMax models do not emit that tag. GoModel's canonical shape on
// /v1/chat/completions is reasoning_content — the same field Anthropic,
// DeepSeek, Cohere, and vLLM emit and the Responses and Messages
// translation layers read — so the blocks are stripped from content and
// moved there. Relaying the inline XML makes a reasoning model look broken
// to any OpenAI-compatible client that reads reasoning_content (Kimi Code,
// Open WebUI, anything Anthropic-shaped).
//
// MiniMax occasionally uses namespaced spellings of the markers, especially
// on degraded long-context responses: </mm:think> and </minimax:think> as
// closing markers, and the matching <mm:think> and <minimax:think> open
// forms. The parser accepts every spelling and treats them identically.
const reasoningKey = "reasoning_content"

// thinkOpenTags returns every opening marker the parser accepts. Order
// does not matter: the parsers take the earliest match in the scanned
// text.
func thinkOpenTags() [3]string {
	return [3]string{"<think>", "<mm:think>", "<minimax:think>"}
}

// thinkCloseTags returns every closing marker the parser accepts. Order
// does not matter: both the buffered and the streaming parser take the
// earliest match in the text they are scanning.
func thinkCloseTags() [3]string {
	return [3]string{"</think>", "</mm:think>", "</minimax:think>"}
}

// isReasoningModel reports whether model is a MiniMax model known to emit
// inline <think> blocks — exactly M2, M2.5, M2.7, and M3. Anything else
// stays untouched, so the parse step stays inert for non-reasoning and
// future models alike. The case list is exact on purpose: MiniMax ships
// mN.1 successors, and a future model may be a plain text model or fix the
// inline tags upstream — a broad prefix would rewrite an unknown model's
// hot path without evidence.
func isReasoningModel(model string) bool {
	switch strings.ToLower(model) {
	case "minimax-m2", "minimax-m2.5", "minimax-m2.7", "minimax-m3":
		return true
	}
	return false
}

// normalizeChatResponse strips <think>...</think> blocks from every choice's
// content and moves the inner text into the canonical reasoning_content
// member. A response with no <think> blocks is left alone, including any
// existing reasoning_content the upstream carried.
func normalizeChatResponse(resp *core.ChatResponse) {
	if resp == nil {
		return
	}
	for i := range resp.Choices {
		normalizeChoice(&resp.Choices[i])
	}
}

func normalizeChoice(choice *core.Choice) {
	msg := &choice.Message
	raw, ok := msg.Content.(string)
	if !ok || raw == "" {
		return
	}
	stripped, reasoning := splitThink(raw)
	if reasoning == "" && stripped == raw {
		return
	}
	msg.Content = stripped
	if reasoning == "" {
		return
	}
	// Upstream may already carry a reasoning_content (e.g. another layer
	// pre-parsed the think block). When it does, keep the upstream text
	// verbatim — the parser only strips the tags from content, never
	// overwrites an already-canonical reasoning member with what it
	// extracted from content.
	if existing := msg.ExtraFields.Lookup(reasoningKey); len(existing) > 0 {
		return
	}
	// json.Marshal on a string and MergeUnknownJSONFields on a pre-validated
	// base cannot fail; both results are therefore used directly.
	encoded, _ := json.Marshal(reasoning)
	merged, _ := core.MergeUnknownJSONFields(msg.ExtraFields, map[string]json.RawMessage{
		reasoningKey: encoded,
	})
	msg.ExtraFields = merged
}

// splitThink extracts the reasoning body of s.
//
// Confirmed-close matching: a closing marker inside a think block is a real
// close in two cases only — no closing marker comes after it (final close),
// or an opening marker comes after it before the next closing marker (a
// chained block follows). Every other closing marker is literal text the model wrote
// while reasoning about the tags themselves and stays in the reasoning
// output verbatim. This keeps chained think → answer → think → answer
// sequences intact without letting a stray marker close the block early.
//
// Text outside think blocks is content and is returned verbatim — leading
// and trailing whitespace included. Orphan closing markers in content are
// Markdown-escaped (\<\/think\>) so they render as visible text and the
// model keeps its reasoning trace in history. An unterminated block (no
// closing marker at all, typically finish_reason=length) emits the partial
// inner text as reasoning so the client still sees the chain of thought the
// model produced before it was cut off.
func splitThink(s string) (content, reasoning string) {
	var cb, rb strings.Builder
	rest := s
	inThink := false
	for len(rest) > 0 {
		if inThink {
			ci, tag := earliestClose(rest)
			if ci < 0 {
				rb.WriteString(rest)
				break
			}
			if isRealClose(rest[ci+len(tag):]) {
				rb.WriteString(rest[:ci])
				rest = rest[ci+len(tag):]
				inThink = false
				continue
			}
			// Literal close inside reasoning: keep it verbatim.
			rb.WriteString(rest[:ci+len(tag)])
			rest = rest[ci+len(tag):]
			continue
		}
		oi, otag := earliestOpen(rest)
		ci, tag := earliestClose(rest)
		switch {
		case oi < 0 && ci < 0:
			cb.WriteString(rest)
			rest = ""
		case ci >= 0 && (oi < 0 || ci < oi):
			// Orphan close in content: escape the marker so it renders as
			// visible text instead of vanishing from the transcript.
			cb.WriteString(rest[:ci])
			cb.WriteString(escapedClose(tag))
			rest = rest[ci+len(tag):]
		default:
			cb.WriteString(rest[:oi])
			rest = rest[oi+len(otag):]
			inThink = true
		}
	}
	return cb.String(), rb.String()
}

// isRealClose reports whether a closing marker ends the current think
// block. s is the text right after the marker. The marker is real when no
// closing marker follows it, or when an opening marker follows it before
// the next closing marker.
func isRealClose(s string) bool {
	ci, _ := earliestClose(s)
	if ci < 0 {
		return true
	}
	oi, _ := earliestOpen(s)
	return oi >= 0 && oi < ci
}

// earliestOpen returns the index and text of the first accepted opening
// marker in s. index is -1 when s contains none.
func earliestOpen(s string) (index int, tag string) {
	index, tag = -1, ""
	for _, t := range thinkOpenTags() {
		if i := strings.Index(s, t); i >= 0 && (index < 0 || i < index) {
			index, tag = i, t
		}
	}
	return index, tag
}

// earliestClose returns the index and text of the first accepted closing
// marker in s. index is -1 when s contains none.
func earliestClose(s string) (index int, tag string) {
	index, tag = -1, ""
	for _, t := range thinkCloseTags() {
		if i := strings.Index(s, t); i >= 0 && (index < 0 || i < index) {
			index, tag = i, t
		}
	}
	return index, tag
}

// escapedClose renders a closing marker as Markdown-escaped literal text
// (\<\/think\>) so an orphan marker renders as visible text instead of
// disappearing or being parsed as a tag downstream.
func escapedClose(tag string) string {
	return "\\" + tag[:1] + "\\" + tag[1:len(tag)-1] + "\\" + tag[len(tag)-1:]
}

// escapeAllCloses replaces every accepted closing marker in s with its
// Markdown-escaped literal form.
func escapeAllCloses(s string) string {
	for _, tag := range thinkCloseTags() {
		s = strings.ReplaceAll(s, tag, escapedClose(tag))
	}
	return s
}

// escapeLeadingCloses replaces every accepted closing marker at the head of
// s with its Markdown-escaped literal form.
func escapeLeadingCloses(s string) string {
	escaped := true
	for escaped {
		escaped = false
		for _, tag := range thinkCloseTags() {
			if strings.HasPrefix(s, tag) {
				s = escapedClose(tag) + s[len(tag):]
				escaped = true
			}
		}
	}
	return s
}

// escapeOrphanCloses escapes close markers that leaked into the content
// stream because the parser exited on an inner think's close while the
// outer one was still open. They are kept as visible text so the model's
// reasoning trace survives in history.
func escapeOrphanCloses(s string) string {
	if !containsAnyClose(s) {
		return s
	}
	return escapeAllCloses(s)
}

// containsAnyClose reports whether s contains any accepted closing marker.
func containsAnyClose(s string) bool {
	for _, tag := range thinkCloseTags() {
		if strings.Contains(s, tag) {
			return true
		}
	}
	return false
}
