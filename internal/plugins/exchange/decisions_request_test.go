package exchange

import (
	"testing"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/pluginapi"
)

const decisionMessagesInput = `[{"type":"message","role":"user","content":[` +
	`{"type":"input_text","text":"Call John at 555-0100.","prompt_cache_breakpoint":{"mode":"explicit"}},` +
	`{"type":"input_image","image_url":"https://example.com/receipt.png"}]},` +
	`{"role":"user","content":"Second message."}]`

func TestFromDecisionRequestExposesInputAsUserMessages(t *testing.T) {
	p, err := FromDecisionRequest(&core.DecisionRequest{Model: "gpt-6-luna", Input: json.RawMessage(decisionMessagesInput)})
	require.NoError(t, err)
	require.Len(t, p.Messages, 2)

	assert.Equal(t, pluginapi.RoleUser, p.Messages[0].Role)
	assert.Equal(t, "Call John at 555-0100.", p.Messages[0].Text())
	assert.Equal(t, "Second message.", p.Messages[1].Text())
	assert.Equal(t, "gpt-6-luna", p.Params.Model)

	str, err := FromDecisionRequest(&core.DecisionRequest{Model: "gpt-6-luna", Input: json.RawMessage(`"plain text"`)})
	require.NoError(t, err)
	require.Len(t, str.Messages, 1)
	assert.Equal(t, "plain text", str.Messages[0].Text())
}

func TestApplyToDecisionRequest(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		edit      func(p *pluginapi.Prompt) error
		wantInput string
		wantErr   string
	}{
		{
			name:      "untouched input keeps its exact JSON",
			input:     decisionMessagesInput,
			edit:      func(*pluginapi.Prompt) error { return nil },
			wantInput: decisionMessagesInput,
		},
		{
			name:      "string input stays a string",
			input:     `"Call John"`,
			edit:      func(p *pluginapi.Prompt) error { return p.SetText("m0", 0, "Call [PERSON]") },
			wantInput: `"Call [PERSON]"`,
		},
		{
			name:  "edited text part keeps its siblings and members",
			input: decisionMessagesInput,
			edit:  func(p *pluginapi.Prompt) error { return p.SetText("m0", 0, "Call [PERSON] at [PHONE].") },
			wantInput: `[{"type":"message","role":"user","content":[` +
				`{"type":"input_text","text":"Call [PERSON] at [PHONE].","prompt_cache_breakpoint":{"mode":"explicit"}},` +
				`{"type":"input_image","image_url":"https://example.com/receipt.png"}]},` +
				`{"role":"user","content":"Second message."}]`,
		},
		{
			name:  "inserted system messages and parameters are dropped",
			input: `"Call John"`,
			edit: func(p *pluginapi.Prompt) error {
				p.Insert(0, pluginapi.TextMessage(pluginapi.RoleSystem, "be safe"))
				p.SetParam("temperature", 0.1)
				return nil
			},
			wantInput: `"Call John"`,
		},
		{
			name:  "appended user message is carried",
			input: `"Call John"`,
			edit: func(p *pluginapi.Prompt) error {
				p.Append(pluginapi.TextMessage(pluginapi.RoleUser, "Context: VIP"))
				return nil
			},
			wantInput: `[{"type":"message","role":"user","content":"Call John"},{"type":"message","role":"user","content":"Context: VIP"}]`,
		},
		{
			name:    "input cannot be removed",
			input:   `"Call John"`,
			edit:    func(p *pluginapi.Prompt) error { return p.Remove("m0") },
			wantErr: "input was removed",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			original := &core.DecisionRequest{Model: "gpt-6-luna", Input: json.RawMessage(tt.input)}
			p, err := FromDecisionRequest(original)
			require.NoError(t, err)
			require.NoError(t, tt.edit(p))

			got, err := ApplyToDecisionRequest(original, p)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.JSONEq(t, tt.wantInput, string(got.Input))
			assert.Equal(t, "gpt-6-luna", got.Model)
			assert.JSONEq(t, tt.input, string(original.Input), "the original request must not change")
		})
	}
}

func TestDecisionUncarriedEdits(t *testing.T) {
	p, err := FromDecisionRequest(&core.DecisionRequest{Model: "gpt-6-luna", Input: json.RawMessage(`"John"`)})
	require.NoError(t, err)
	assert.Nil(t, DecisionUncarriedEdits(p))

	require.NoError(t, p.SetText("m0", 0, "[PERSON]"))
	p.Append(pluginapi.TextMessage(pluginapi.RoleUser, "extra context"))
	assert.Nil(t, DecisionUncarriedEdits(p), "edits and inserted user messages are carried")

	p.Insert(0, pluginapi.TextMessage(pluginapi.RoleSystem, "be safe"))
	p.SetParam("temperature", 0.1)
	assert.Equal(t, []string{"inserted system message", `parameter "temperature"`}, DecisionUncarriedEdits(p))
}
