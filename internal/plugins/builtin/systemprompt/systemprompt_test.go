package systemprompt

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/enterpilot/gomodel/pluginapi"
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
	if err := p.Init(context.Background(), json.RawMessage(cfg), nil); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	x := &pluginapi.Exchange{Prompt: prompt, Values: pluginapi.Values{}}
	decision, err := p.(pluginapi.PromptHook).OnPrompt(context.Background(), x)
	if err != nil {
		t.Fatalf("OnPrompt() error = %v", err)
	}
	if decision.Action != pluginapi.ActionAllow {
		t.Fatalf("decision = %+v, want allow", decision)
	}
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
			if roles(got) != tt.want {
				t.Fatalf("messages = %q, want %q", roles(got), tt.want)
			}
			if got.Changes().Dirty != tt.dirty {
				t.Fatalf("dirty = %v, want %v", got.Changes().Dirty, tt.dirty)
			}
		})
	}
}

func TestParseConfigAndSummarize(t *testing.T) {
	if _, err := ParseConfig(json.RawMessage(`{"mode":"weird","content":"x"}`)); err == nil {
		t.Fatal("invalid mode accepted")
	}
	if _, err := ParseConfig(json.RawMessage(`{"mode":"inject","content":"  "}`)); err == nil {
		t.Fatal("empty content accepted")
	}
	cfg, err := ParseConfig(json.RawMessage(`{"content":" x "}`))
	if err != nil || cfg.Mode != "inject" || cfg.Content != "x" {
		t.Fatalf("cfg = %+v, %v", cfg, err)
	}
	p := New().(*Plugin)
	if got := p.Summarize(json.RawMessage(`{"mode":"decorator","content":"be   very\nsafe"}`)); got != "decorator • be very safe" {
		t.Fatalf("Summarize() = %q", got)
	}
	long := strings.Repeat("a", 100)
	if got := p.Summarize(json.RawMessage(`{"content":"` + long + `"}`)); !strings.HasSuffix(got, "...") || len(got) != len("inject • ")+72 {
		t.Fatalf("Summarize(long) = %q", got)
	}
	if p.Summarize(json.RawMessage(`{}`)) != "" {
		t.Fatal("Summarize(invalid) should be empty")
	}
	m := p.Manifest()
	if m.Name != Name || !m.Mutates || len(m.Kinds) != 1 || len(m.ConfigSchema) != 2 {
		t.Fatalf("manifest = %+v", m)
	}
}
