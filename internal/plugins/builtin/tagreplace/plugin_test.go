package tagreplace

import (
	"context"
	"testing"
	"time"

	"github.com/enterpilot/gomodel/pluginapi"
	"github.com/enterpilot/gomodel/pluginapi/plugintest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var fixedNow = time.Date(2026, 9, 15, 22, 30, 5, 0, time.UTC)

func newPlugin(t *testing.T, cfg string) *Plugin {
	t.Helper()
	p := plugintest.Init(t, New, cfg, nil).(*Plugin)
	p.now = func() time.Time { return fixedNow }
	return p
}

func testMeta() pluginapi.Meta {
	return pluginapi.Meta{
		RequestID:          "req-1",
		UserPath:           "/team/a",
		SessionID:          "sess-1",
		Labels:             map[string]string{"team": "search"},
		RequestedModel:     "smart",
		Provider:           "openai",
		ProviderName:       "openai-eu",
		Model:              "gpt-4o",
		VirtualModelSource: "smart",
	}
}

func runPrompt(t *testing.T, p *Plugin, msgs ...pluginapi.Message) (*pluginapi.Exchange, pluginapi.Decision) {
	t.Helper()
	x := plugintest.Exchange(plugintest.Prompt(msgs...), nil)
	x.Meta = testMeta()
	d, err := p.OnPrompt(context.Background(), x)
	require.NoError(t, err)
	require.Equal(t, pluginapi.ActionAllow, d.Action)
	return x, d
}

func TestExpandTags(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"requested model", "{{gomodel.requested_model}}", "smart"},
		{"resolved model", "{{gomodel.resolved_model}}", "gpt-4o"},
		{"virtual model", "{{gomodel.virtual_model}}", "smart"},
		{"provider", "{{gomodel.provider_type}}/{{gomodel.provider_name}}", "openai/openai-eu"},
		{"user path", "{{gomodel.user_path}}", "/team/a"},
		{"request and session", "{{gomodel.request_id}} {{gomodel.session_id}}", "req-1 sess-1"},
		{"date", "Today is {{gomodel.date}}.", "Today is 2026-09-15."},
		{"time", "{{gomodel.time}}", "22:30:05"},
		{"datetime", "{{gomodel.datetime}}", "2026-09-15T22:30:05Z"},
		{"weekday", "{{gomodel.weekday}}", "Tuesday"},
		{"label", "{{gomodel.label.team}}", "search"},
		{"missing label is empty", "[{{gomodel.label.nope}}]", "[]"},
		{"spaces inside braces", "{{ gomodel.resolved_model }}", "gpt-4o"},
		{"unknown tag kept", "{{gomodel.secret}} {{other.date}}", "{{gomodel.secret}} {{other.date}}"},
		{"no tags", "plain text", "plain text"},
	}
	v := newValues(testMeta(), fixedNow)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _ := expand(tt.in, v)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestOnPromptRolesAndDetail(t *testing.T) {
	p := newPlugin(t, `{"roles": ["system", "user"]}`)
	x, d := runPrompt(t, p,
		plugintest.Text(pluginapi.RoleSystem, "s", "You are {{gomodel.resolved_model}}. Date: {{gomodel.date}}"),
		plugintest.Text(pluginapi.RoleUser, "u", "which model? {{gomodel.requested_model}}"),
		plugintest.Text(pluginapi.RoleAssistant, "a", "{{gomodel.resolved_model}}"),
	)
	assert.Equal(t, "You are gpt-4o. Date: 2026-09-15", x.Prompt.Messages[0].Text())
	assert.Equal(t, "which model? smart", x.Prompt.Messages[1].Text())
	assert.Equal(t, "{{gomodel.resolved_model}}", x.Prompt.Messages[2].Text(), "assistant is not a configured role")
	assert.Equal(t, map[string]any{"replacements": 3}, d.Detail)
	assert.True(t, x.Prompt.Changes().Dirty)
}

func TestDefaultRolesSkipUserMessages(t *testing.T) {
	p := newPlugin(t, "")
	x, d := runPrompt(t, p,
		plugintest.Text(pluginapi.RoleDeveloper, "d", "{{gomodel.resolved_model}}"),
		plugintest.Text(pluginapi.RoleUser, "u", "{{gomodel.label.team}}"),
	)
	assert.Equal(t, "gpt-4o", x.Prompt.Messages[0].Text())
	assert.Equal(t, "{{gomodel.label.team}}", x.Prompt.Messages[1].Text())
	assert.Equal(t, map[string]any{"replacements": 1}, d.Detail)
}

func TestOnPromptWithoutTagsLeavesPromptClean(t *testing.T) {
	p := newPlugin(t, `{"roles": ["user"]}`)
	x, d := runPrompt(t, p,
		plugintest.Text(pluginapi.RoleSystem, "s", "{{gomodel.date}}"),
		plugintest.Text(pluginapi.RoleUser, "u", "hello {{gomodel.unknown}}"),
	)
	assert.Equal(t, "{{gomodel.date}}", x.Prompt.Messages[0].Text())
	assert.Nil(t, d.Detail)
	assert.False(t, x.Prompt.Changes().Dirty)
}

func TestTimezone(t *testing.T) {
	p := newPlugin(t, `{"timezone": "Asia/Tokyo"}`)
	x, _ := runPrompt(t, p, plugintest.Text(pluginapi.RoleSystem, "s", "{{gomodel.date}} {{gomodel.weekday}}"))
	assert.Equal(t, "2026-09-16 Wednesday", x.Prompt.Messages[0].Text())
}

func TestInitErrors(t *testing.T) {
	tests := []struct {
		name string
		cfg  string
		want string
	}{
		{"unknown key", `{"bogus": 1}`, `unknown field "bogus"`},
		{"bad role", `{"roles": ["admin"]}`, `unknown role "admin"`},
		{"bad timezone", `{"timezone": "Mars/Base"}`, "not a known IANA timezone"},
		{"host timezone", `{"timezone": "Local"}`, "must be UTC or an IANA timezone"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := New().Init(context.Background(), []byte(tt.cfg), plugintest.NewHost())
			require.ErrorContains(t, err, tt.want)
		})
	}
}

func TestManifestAndSummarize(t *testing.T) {
	p := New()
	m := p.Manifest()
	assert.Equal(t, Name, m.Name)
	assert.True(t, m.Mutates)
	assert.True(t, m.Guardrail)
	assert.Equal(t, []pluginapi.Kind{pluginapi.KindPrompt}, m.Kinds)
	_, ok := p.(pluginapi.PromptHook)
	assert.True(t, ok)

	s := p.(*Plugin)
	assert.Equal(t, "system messages, UTC", s.Summarize(nil))
	assert.Equal(t, "tool messages, Europe/Warsaw", s.Summarize([]byte(`{"roles":["tool"],"timezone":"Europe/Warsaw"}`)))
	assert.Empty(t, s.Summarize([]byte(`{"timezone":"nope"}`)))
}
