package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/echotest"
	"github.com/enterpilot/gomodel/internal/guardrails"
	"github.com/enterpilot/gomodel/internal/plugins"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
	"github.com/enterpilot/gomodel/pluginapi"
)

// personAnalyzer fakes the Presidio analyzer: it reports every occurrence of
// name as a PERSON.
func personAnalyzer(t *testing.T, name string) string {
	t.Helper()
	srv, _ := providertest.Server(t, func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Text string `json:"text"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		results := []map[string]any{}
		for i := 0; ; {
			idx := strings.Index(req.Text[i:], name)
			if idx < 0 {
				break
			}
			start := utf8.RuneCountInString(req.Text[:i+idx])
			results = append(results, map[string]any{"entity_type": "PERSON", "start": start, "end": start + utf8.RuneCountInString(name), "score": 0.85})
			i += idx + len(name)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(results)
	})
	return srv.URL
}

// restoreChains anonymizes the prompt and restores values in the given
// phase, both with Presidio restore on.
func restoreChains(t *testing.T, analyzerURL string, phase pluginapi.Kind) *plugins.Chains {
	t.Helper()
	cfg, _ := json.Marshal(map[string]any{"analyzer_url": analyzerURL, "restore": true, "stream_lookbehind": 16, "stream_chunk": 0})
	return newGuardrailChains(t, nil, []guardrails.StepReference{
		{Ref: "pii-in", Phase: pluginapi.KindPrompt, Step: 1},
		{Ref: "pii-out", Phase: phase, Step: 1},
	}, nil,
		guardrails.Definition{Name: "pii-in", Type: "presidio", Config: cfg},
		guardrails.Definition{Name: "pii-out", Type: "presidio", Config: cfg})
}

// thinkingStream is a chat stream as a provider adapter emits it: reasoning
// deltas, then (when signature is set) the signed block as replay state, as
// the Anthropic adapter hands it over, then the answer.
func thinkingStream(signature string) string {
	chunk := func(delta string, finish string) string {
		reason := "null"
		if finish != "" {
			reason = `"` + finish + `"`
		}
		return `data: {"id":"c1","object":"chat.completion.chunk","model":"claude-test","choices":[{"index":0,"delta":` + delta + `,"finish_reason":` + reason + `}]}` + "\n\n"
	}
	out := chunk(`{"role":"assistant"}`, "") +
		chunk(`{"reasoning_content":"User <PERS"}`, "") +
		chunk(`{"reasoning_content":"ON_1>"}`, "")
	if signature != "" {
		out += chunk(`{"extra_content":{"anthropic":{"thinking_blocks":[{"type":"thinking","thinking":"User <PERSON_1>","signature":"`+signature+`"}]}}}`, "")
	}
	return out + chunk(`{"content":"Hi <PERSON_1>"}`, "") +
		chunk(`{}`, "stop") +
		"data: [DONE]\n\n"
}

// messagesBlocks folds an Anthropic Messages event stream into its content
// blocks, the way a client accumulates them to send the turn back.
func messagesBlocks(t *testing.T, body string) []map[string]any {
	t.Helper()
	var blocks []map[string]any
	for line := range strings.SplitSeq(body, "\n") {
		data, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			continue
		}
		var ev struct {
			Type         string            `json:"type"`
			Index        int               `json:"index"`
			ContentBlock map[string]any    `json:"content_block"`
			Delta        map[string]string `json:"delta"`
		}
		require.NoError(t, json.Unmarshal([]byte(data), &ev), data)
		switch ev.Type {
		case "content_block_start":
			require.Len(t, blocks, ev.Index)
			blocks = append(blocks, ev.ContentBlock)
		case "content_block_delta":
			require.Less(t, ev.Index, len(blocks))
			block := blocks[ev.Index]
			switch ev.Delta["type"] {
			case "thinking_delta":
				block["thinking"] = block["thinking"].(string) + ev.Delta["thinking"]
			case "signature_delta":
				block["signature"] = block["signature"].(string) + ev.Delta["signature"]
			case "text_delta":
				block["text"] = block["text"].(string) + ev.Delta["text"]
			}
		}
	}
	return blocks
}

// A Messages client replays a thinking block verbatim with its signature, so
// signed thinking must reach it as the provider signed it: restoring values
// in it would fail the signature check on the next turn and send the values
// upstream in clear. Unsigned reasoning is still restored.
func TestMessagesStreamRestoreKeepsSignedThinking(t *testing.T) {
	analyzerURL := personAnalyzer(t, "Ann Lee")
	tests := []struct {
		name          string
		providerType  string
		signature     string
		wantThinking  string
		wantSignature string
	}{
		{"anthropic signed thinking", "anthropic", "SIG", "User <PERSON_1>", "SIG"},
		{"unsigned reasoning", "openai", "", "User Ann Lee", ""},
	}
	for _, phase := range []pluginapi.Kind{pluginapi.KindStream, pluginapi.KindResponse} {
		for _, tt := range tests {
			t.Run(string(phase)+"/"+tt.name, func(t *testing.T) {
				inner := &capturingProvider{
					supportedModels: []string{"claude-test"},
					providerTypes:   map[string]string{"claude-test": tt.providerType},
					streamData:      thinkingStream(tt.signature),
				}
				handler := phaseHandler(t, inner, restoreChains(t, analyzerURL, phase))
				body := `{"model":"claude-test","max_tokens":64,"stream":true,"thinking":{"type":"enabled","budget_tokens":32},"messages":[{"role":"user","content":"I am Ann Lee."}]}`
				c, rec := echotest.Post(t, "/v1/messages", body)
				err := handler.Messages(c)
				require.NoError(t, err)
				require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

				require.NotNil(t, inner.capturedChatReq)
				assert.Equal(t, "I am <PERSON_1>.", core.ExtractTextContent(inner.capturedChatReq.Messages[0].Content), "the provider sees the placeholder")

				blocks := messagesBlocks(t, rec.Body.String())
				require.Len(t, blocks, 2, rec.Body.String())
				assert.Equal(t, "thinking", blocks[0]["type"])
				assert.Equal(t, tt.wantThinking, blocks[0]["thinking"])
				assert.Equal(t, tt.wantSignature, blocks[0]["signature"])
				assert.Equal(t, "Hi Ann Lee", blocks[1]["text"], "the answer is restored")
			})
		}
	}
}

// A response edit may move a reasoning part (ReplaceText puts a new text part
// first when the choice had none); its original text still comes back.
func TestKeepReasoningFollowsMovedParts(t *testing.T) {
	completion := &pluginapi.Completion{Choices: []pluginapi.Choice{{Message: pluginapi.Message{Role: pluginapi.RoleAssistant, Parts: []pluginapi.Part{
		{Kind: pluginapi.PartReasoning, Text: "User <PERSON_1>"},
	}}}}}
	original := completion.ReasoningTargets()
	require.NoError(t, completion.ReplaceText(0, "answer"))
	require.NoError(t, completion.SetReasoning(0, 1, "User Ann Lee"))

	keepReasoning(completion, original)
	assert.Equal(t, []pluginapi.Part{
		{Kind: pluginapi.PartText, Text: "answer"},
		{Kind: pluginapi.PartReasoning, Text: "User <PERSON_1>"},
	}, completion.Choices[0].Message.Parts)
}

// The host ignores replace and drop on signed reasoning whatever plugin asks
// for them; unsigned reasoning stays editable.
func TestMessagesStreamIgnoresEditsOfSignedThinking(t *testing.T) {
	tests := []struct {
		name         string
		mode         string
		providerType string
		signature    string
		wantKept     bool
	}{
		{"replace signed", "replace_reasoning", "anthropic", "SIG", true},
		{"replace unsigned", "replace_reasoning", "openai", "", false},
		{"drop signed", "drop_reasoning", "anthropic", "SIG", true},
		{"drop unsigned", "drop_reasoning", "openai", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inner := &capturingProvider{
				supportedModels: []string{"claude-test"},
				providerTypes:   map[string]string{"claude-test": tt.providerType},
				streamData:      thinkingStream(tt.signature),
			}
			chains := phaseChains(t, map[string]string{"stream": tt.mode, "text": "edited"}, guardrails.StepReference{Ref: "phase", Phase: pluginapi.KindStream, Step: 1})
			body := `{"model":"claude-test","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"hi"}]}`
			c, rec := echotest.Post(t, "/v1/messages", body)
			err := phaseHandler(t, inner, chains).Messages(c)
			require.NoError(t, err)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

			var thinking string
			for _, block := range messagesBlocks(t, rec.Body.String()) {
				if block["type"] == "thinking" {
					thinking += block["thinking"].(string)
				}
			}
			if tt.wantKept {
				assert.Equal(t, "User <PERSON_1>", thinking)
			} else {
				assert.NotContains(t, thinking, "User")
			}
		})
	}
}
