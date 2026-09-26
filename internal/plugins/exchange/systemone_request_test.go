package exchange

import (
	"testing"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/pluginapi"
)

func TestFromSystemOneRequestExposesStateAsUserMessage(t *testing.T) {
	tests := []struct {
		name  string
		state string
		want  string
	}{
		{name: "string state", state: `"charged twice"`, want: "charged twice"},
		{name: "record state", state: `{"ticket":"charged twice"}`, want: `{"ticket":"charged twice"}`},
		{name: "null state", state: `null`, want: "null"},
		{name: "no state", state: ``, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := FromSystemOneRequest(&core.SystemOneRequest{Model: "kev-latest", State: json.RawMessage(tt.state)})
			require.NoError(t, err)
			require.Len(t, p.Messages, 1)

			assert.Equal(t, SystemOneStateMessageID, p.Messages[0].ID)
			assert.Equal(t, pluginapi.RoleUser, p.Messages[0].Role)
			assert.Equal(t, tt.want, p.Messages[0].Text())
			assert.Equal(t, "kev-latest", p.Params.Model)
			assert.False(t, p.Changes().Dirty)
		})
	}
}

func TestApplyToSystemOneRequest(t *testing.T) {
	tests := []struct {
		name      string
		state     string
		edit      func(p *pluginapi.Prompt) error
		wantState string
		wantErr   string
	}{
		{
			name:      "untouched",
			state:     `"John"`,
			edit:      func(*pluginapi.Prompt) error { return nil },
			wantState: `"John"`,
		},
		{
			name:      "string state stays a string",
			state:     `"John \"J\" Doe"`,
			edit:      func(p *pluginapi.Prompt) error { return p.SetText(SystemOneStateMessageID, 0, `[PERSON] "J"`) },
			wantState: `"[PERSON] \"J\""`,
		},
		{
			name:      "record state stays a record",
			state:     `{"name":"John"}`,
			edit:      func(p *pluginapi.Prompt) error { return p.SetText(SystemOneStateMessageID, 0, `{"name":"[PERSON]"}`) },
			wantState: `{"name":"[PERSON]"}`,
		},
		{
			name:      "null state keeps its JSON type",
			state:     `null`,
			edit:      func(p *pluginapi.Prompt) error { return p.SetText(SystemOneStateMessageID, 0, `{"redacted":true}`) },
			wantState: `{"redacted":true}`,
		},
		{
			name:    "record state must stay valid JSON",
			state:   `{"name":"John"}`,
			edit:    func(p *pluginapi.Prompt) error { return p.SetText(SystemOneStateMessageID, 0, `name: [PERSON]`) },
			wantErr: "no longer valid JSON",
		},
		{
			name:    "state cannot be removed",
			state:   `"John"`,
			edit:    func(p *pluginapi.Prompt) error { return p.Remove(SystemOneStateMessageID) },
			wantErr: "state was removed",
		},
		{
			name:  "uncarried edits are not applied",
			state: `"John"`,
			edit: func(p *pluginapi.Prompt) error {
				p.Insert(0, pluginapi.TextMessage(pluginapi.RoleSystem, "be safe"))
				p.SetParam("temperature", 0.1)
				return nil
			},
			wantState: `"John"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			original := &core.SystemOneRequest{Model: "kev-latest", State: json.RawMessage(tt.state)}
			p, err := FromSystemOneRequest(original)
			require.NoError(t, err)
			require.NoError(t, tt.edit(p))

			got, err := ApplyToSystemOneRequest(original, p)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.JSONEq(t, tt.wantState, string(got.State))
			assert.Equal(t, "kev-latest", got.Model)
			assert.JSONEq(t, tt.state, string(original.State), "the original request must not change")
		})
	}
}

func TestSystemOneUncarriedEdits(t *testing.T) {
	p, err := FromSystemOneRequest(&core.SystemOneRequest{Model: "kev-latest", State: json.RawMessage(`"John"`)})
	require.NoError(t, err)
	assert.Nil(t, SystemOneUncarriedEdits(p))

	require.NoError(t, p.SetText(SystemOneStateMessageID, 0, "[PERSON]"))
	assert.Nil(t, SystemOneUncarriedEdits(p), "a state edit is carried")

	id := p.Insert(0, pluginapi.TextMessage(pluginapi.RoleSystem, "be safe"))
	p.SetParam("temperature", 0.1)
	assert.Equal(t, []string{`inserted message "` + id + `"`, `parameter "temperature"`}, SystemOneUncarriedEdits(p))
}
