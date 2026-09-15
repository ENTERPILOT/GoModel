package systemprompt

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/enterpilot/gomodel/pluginapi"
	"github.com/stretchr/testify/require"
)

func newPrompt(msgs ...pluginapi.Message) *pluginapi.Prompt {
	for i := range msgs {
		msgs[i].ID = "m" + string(rune('0'+i))
	}
	p := &pluginapi.Prompt{Messages: msgs}
	p.Reset()
	return p
}

func run(t *testing.T, cfg string, prompt *pluginapi.Prompt) *pluginapi.Prompt {
	t.Helper()
	p := New()
	err := p.Init(context.Background(), json.RawMessage(cfg), nil)
	require.NoError(t, err)

	x := &pluginapi.Exchange{Prompt: prompt, Values: pluginapi.Values{}}
	decision, err := p.(pluginapi.PromptHook).OnPrompt(context.Background(), x)
	require.NoError(t, err)
	require.Equal(t, pluginapi.ActionAllow, decision.Action, "decision = %+v, want allow", decision)

	return x.Prompt
}

func roles(p *pluginapi.Prompt) string {
	var out []string
	for _, m := range p.Messages {
		out = append(out, string(m.Role)+":"+m.Text())
	}
	return strings.Join(out, "|")
}

func TestModes(t *testing.T) {
	user := pluginapi.TextMessage(pluginapi.RoleUser, "hi")
	tests := []struct {
		name   string
		cfg    string
		prompt *pluginapi.Prompt
		want   string
		dirty  bool
	}{
		{name: "inject when absent", cfg: `{"mode":"inject","content":"be safe"}`, prompt: newPrompt(user), want: "system:be safe|user:hi", dirty: true},
		{name: "inject leaves existing", cfg: `{"mode":"inject","content":"be safe"}`, prompt: newPrompt(pluginapi.TextMessage(pluginapi.RoleSystem, "old"), user), want: "system:old|user:hi"},
		{name: "inject treats developer as system", cfg: `{"content":"be safe"}`, prompt: newPrompt(pluginapi.TextMessage(pluginapi.RoleDeveloper, "dev"), user), want: "developer:dev|user:hi"},
		{name: "override replaces all", cfg: `{"mode":"override","content":"new"}`, prompt: newPrompt(pluginapi.TextMessage(pluginapi.RoleSystem, "a"), user, pluginapi.TextMessage(pluginapi.RoleSystem, "b")), want: "system:new|user:hi", dirty: true},
		{name: "decorator prepends", cfg: `{"mode":"decorator","content":"prefix"}`, prompt: newPrompt(user, pluginapi.TextMessage(pluginapi.RoleSystem, "old")), want: "user:hi|system:prefix\nold", dirty: true},
		{name: "decorator injects when absent", cfg: `{"mode":"decorator","content":"prefix"}`, prompt: newPrompt(user), want: "system:prefix|user:hi", dirty: true},
		{name: "decorator with no text part", cfg: `{"mode":"decorator","content":"prefix"}`, prompt: newPrompt(pluginapi.Message{Role: pluginapi.RoleSystem}, user), want: "system:prefix|user:hi", dirty: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := run(t, tt.cfg, tt.prompt)
			require.Equal(t, tt.want, roles(got))
			require.Equal(t, tt.dirty, got.Changes().Dirty)
		})
	}
}

func TestParseConfigAndSummarize(t *testing.T) {
	_, err := ParseConfig(json.RawMessage(`{"mode":"weird","content":"x"}`))
	require.Error(t, err)
	_, err = ParseConfig(json.RawMessage(`{"mode":"inject","content":"  "}`))
	require.Error(t, err)

	cfg, err := ParseConfig(json.RawMessage(`{"content":" x "}`))
	require.NoError(t, err)
	require.Equal(t, "inject", cfg.Mode)
	require.Equal(t, "x", cfg.Content)

	p := New().(*Plugin)
	got := p.Summarize(json.RawMessage(`{"mode":"decorator","content":"be   very\nsafe"}`))
	require.Equal(t, "decorator • be very safe", got)

	long := strings.Repeat("a", 100)
	got = p.Summarize(json.RawMessage(`{"content":"` + long + `"}`))
	require.True(t, strings.HasSuffix(got, "..."))
	require.Equal(t, len("inject • ")+72, len(got), "Summarize(long) = %q", got)
	require.Empty(t, p.Summarize(json.RawMessage(`{}`)))

	m := p.Manifest()
	require.Equal(t, Name, m.Name)
	require.True(t, m.Mutates)
	require.True(t, m.Guardrail)
	require.Len(t, m.Kinds, 1)
	require.Len(t, m.ConfigSchema, 2)
}
