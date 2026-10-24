package llmaltering

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/enterpilot/gomodel/pluginapi"
	"github.com/enterpilot/gomodel/pluginapi/plugintest"
	"github.com/stretchr/testify/require"
)

func replyWith(text string) *pluginapi.Completion {
	return &pluginapi.Completion{Choices: []pluginapi.Choice{{Message: pluginapi.TextMessage(pluginapi.RoleAssistant, text), FinishReason: "stop"}}}
}

func upper(req pluginapi.InferenceRequest) (*pluginapi.Completion, error) {
	text := req.Messages[1].Text()
	inner := strings.TrimSuffix(strings.TrimPrefix(text, "<TEXT_TO_ALTER>\n"), "\n</TEXT_TO_ALTER>")
	return replyWith("<TEXT_TO_ALTER>\n" + strings.ToUpper(inner) + "\n</TEXT_TO_ALTER>"), nil
}

func newPlugin(t *testing.T, cfg string, host *plugintest.Host) *Plugin {
	t.Helper()
	p := New().(*Plugin)
	err := p.Init(context.Background(), json.RawMessage(cfg), host)
	require.NoError(t, err)

	return p
}

func prompt(msgs ...pluginapi.Message) *pluginapi.Prompt {
	for i := range msgs {
		msgs[i].ID = "m" + string(rune('0'+i))
	}
	p := &pluginapi.Prompt{Messages: msgs}
	p.Reset()
	return p
}

func TestOnPromptRewritesSelectedRoles(t *testing.T) {
	host := &plugintest.Host{Reply: upper}
	p := newPlugin(t, `{"model":"gpt","provider":"openai","roles":["user","tool"],"skip_content_prefix":"### safe","max_tokens":7}`, host)
	toolMsg := pluginapi.Message{Role: pluginapi.RoleTool, ToolCallID: "c1", Parts: []pluginapi.Part{{Kind: pluginapi.PartToolResult, ToolResult: &pluginapi.ToolResult{CallID: "c1", Parts: []pluginapi.Part{{Kind: pluginapi.PartText, Text: "result"}}}}}}
	pr := prompt(
		pluginapi.TextMessage(pluginapi.RoleSystem, "sys"),
		pluginapi.TextMessage(pluginapi.RoleUser, "hello"),
		pluginapi.TextMessage(pluginapi.RoleUser, "### safe keep"),
		pluginapi.Message{Role: pluginapi.RoleUser, Parts: []pluginapi.Part{{Kind: pluginapi.PartText, Text: "a"}, {Kind: pluginapi.PartImage, URL: "http://x"}, {Kind: pluginapi.PartText, Text: "b"}}},
		toolMsg,
	)
	x := &pluginapi.Exchange{Prompt: pr, Values: pluginapi.Values{}}
	_, err := p.OnPrompt(context.Background(), x)
	require.NoError(t, err)
	got := pr.Messages[0].Text()
	require.Equal(t, "sys", got)
	got = pr.Messages[1].Text()
	require.Equal(t, "HELLO", got)
	got = pr.Messages[2].Text()
	require.Equal(t, "### safe keep", got)
	parts := pr.Messages[3].Parts
	require.Equal(t, "A", parts[0].Text)
	require.Equal(t, pluginapi.PartImage, parts[1].Kind)
	require.Equal(t, "B", parts[2].Text)
	got = pr.Messages[4].Text()
	require.Equal(t, "RESULT", got)

	changes := pr.Changes()
	require.Equal(t, pluginapi.ChangeEdited, changes.Messages["m1"])
	require.Equal(t, pluginapi.ChangeEdited, changes.Messages["m4"])
	require.Empty(t, changes.Messages["m0"])
	require.Len(t, host.Requests(), 4)

	req := host.Requests()[0]
	require.Equal(t, "openai/gpt", req.Model)
	require.Equal(t, 7, req.MaxTokens)
	require.NotNil(t, req.Temperature)
	require.Equal(t, float64(0), *req.Temperature)
	require.Equal(t, DefaultPrompt, req.Messages[0].Text())
}

// A rewrite that fails is reported to the runtime, so the instance's
// fail_mode decides; the prompt is left untouched either way.
func TestOnPromptReturnsRewriteFailures(t *testing.T) {
	tests := []struct {
		name  string
		reply func(pluginapi.InferenceRequest) (*pluginapi.Completion, error)
	}{
		{"error", func(pluginapi.InferenceRequest) (*pluginapi.Completion, error) { return nil, errors.New("boom") }},
		{"no choices", func(pluginapi.InferenceRequest) (*pluginapi.Completion, error) { return &pluginapi.Completion{}, nil }},
		{"empty", func(pluginapi.InferenceRequest) (*pluginapi.Completion, error) { return replyWith(""), nil }},
		{"length finish", func(pluginapi.InferenceRequest) (*pluginapi.Completion, error) {
			c := replyWith("x")
			c.Choices[0].FinishReason = "length"
			return c, nil
		}},
		{"tool call", func(pluginapi.InferenceRequest) (*pluginapi.Completion, error) {
			c := replyWith("x")
			c.Choices[0].Message.Parts = append(c.Choices[0].Message.Parts, pluginapi.Part{Kind: pluginapi.PartToolCall, ToolCall: &pluginapi.ToolCall{ID: "t"}})
			return c, nil
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := newPlugin(t, `{"model":"m"}`, &plugintest.Host{Reply: tt.reply})
			pr := prompt(pluginapi.TextMessage(pluginapi.RoleUser, "hello"))
			_, err := p.OnPrompt(context.Background(), &pluginapi.Exchange{Prompt: pr, Values: pluginapi.Values{}})
			require.Error(t, err)
			require.Equal(t, "hello", pr.Messages[0].Text())
			require.False(t, pr.Changes().Dirty, "prompt changed: %+v", pr.Messages[0])
		})
	}
}

func TestOnPromptPropagatesCancellation(t *testing.T) {
	p := newPlugin(t, `{"model":"m"}`, &plugintest.Host{Reply: func(pluginapi.InferenceRequest) (*pluginapi.Completion, error) { return nil, context.Canceled }})
	pr := prompt(pluginapi.TextMessage(pluginapi.RoleUser, "hello"))
	_, err := p.OnPrompt(context.Background(), &pluginapi.Exchange{Prompt: pr, Values: pluginapi.Values{}})
	require.ErrorIs(t, err, context.Canceled)
}

func TestOnResponseRewritesAssistant(t *testing.T) {
	host := &plugintest.Host{Reply: upper}
	completion := &pluginapi.Completion{Choices: []pluginapi.Choice{
		{Index: 0, Message: pluginapi.TextMessage(pluginapi.RoleAssistant, "one")},
		{Index: 1, Message: pluginapi.Message{Role: pluginapi.RoleAssistant, Parts: []pluginapi.Part{{Kind: pluginapi.PartReasoning, Text: "think"}, {Kind: pluginapi.PartText, Text: "two"}}}},
	}}
	completion.Reset()
	x := &pluginapi.Exchange{Response: completion, Values: pluginapi.Values{}}
	_, err := newPlugin(t, `{"model":"m","roles":["user"]}`, host).OnResponse(context.Background(), x)
	require.NoError(t, err)
	require.Equal(t, "one", completion.Text(0))
	require.Empty(t, host.Requests())
	_, err = newPlugin(t, `{"model":"m","roles":["assistant"]}`, host).OnResponse(context.Background(), x)
	require.NoError(t, err)
	require.Equal(t, "ONE", completion.Text(0))
	require.Equal(t, "TWO", completion.Text(1))
	require.Equal(t, "think", completion.Choices[1].Message.Parts[0].Text)
	require.Equal(t, pluginapi.ChangeEdited, completion.Changes().Messages["choice:0"])
}

func TestParseConfig(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    Config
		wantErr string
	}{
		{name: "defaults", raw: `{"model":" gpt "}`, want: Config{Model: "gpt", Prompt: DefaultPrompt, Roles: []string{"user"}, MaxTokens: DefaultMaxTokens}},
		{name: "provider folded", raw: `{"model":"gpt","provider":"openai","roles":["User","user","TOOL"],"max_tokens":3,"prompt":" custom ","skip_content_prefix":" x "}`, want: Config{Model: "openai/gpt", Prompt: "custom", Roles: []string{"user", "tool"}, MaxTokens: 3, SkipContentPrefix: "x"}},
		{name: "qualified model keeps provider", raw: `{"model":"openai/gpt","provider":"openai"}`, want: Config{Model: "openai/gpt", Prompt: DefaultPrompt, Roles: []string{"user"}, MaxTokens: DefaultMaxTokens}},
		{name: "provider conflict", raw: `{"model":"openai/gpt","provider":"azure"}`, wantErr: "conflicts"},
		{name: "model required", raw: `{}`, wantErr: "model is required"},
		{name: "invalid role", raw: `{"model":"m","roles":["admin"]}`, wantErr: "invalid llm_based_altering role"},
		{name: "empty roles default", raw: `{"model":"m","roles":["", " "]}`, want: Config{Model: "m", Prompt: DefaultPrompt, Roles: []string{"user"}, MaxTokens: DefaultMaxTokens}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseConfig(json.RawMessage(tt.raw))
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want.Model, got.Model)
			require.Equal(t, tt.want.Prompt, got.Prompt)
			require.Equal(t, tt.want.MaxTokens, got.MaxTokens)
			require.Equal(t, tt.want.SkipContentPrefix, got.SkipContentPrefix)
			require.Equal(t, strings.Join(tt.want.Roles, ","), strings.Join(got.Roles, ","), "got %+v, want %+v", got, tt.want)
		})
	}
}

func TestSummarize(t *testing.T) {
	p := New().(*Plugin)
	got := p.Summarize(json.RawMessage(`{"model":"gpt","provider":"openai","roles":["user","tool"]}`))
	require.Equal(t, "openai/gpt • user,tool • default prompt", got)
	got = p.Summarize(json.RawMessage(`{"model":"gpt","prompt":"Rewrite   this\ncarefully"}`))
	require.Equal(t, "gpt • user • Rewrite this carefully", got)
	got = p.Summarize(json.RawMessage(`{"model":"gpt","prompt":"` + strings.Repeat("a", 60) + `"}`))
	require.True(t, strings.HasSuffix(got, "..."), "Summarize(long) = %q", got)
	require.Empty(t, p.Summarize(json.RawMessage(`{}`)))
	m := p.Manifest()
	require.Equal(t, Name, m.Name)
	require.Len(t, m.Kinds, 2)
	require.True(t, m.Mutates)
	require.True(t, m.Guardrail)
}

// "system" covers developer messages, the Responses spelling of system.
func TestOnPromptSystemRoleIncludesDeveloper(t *testing.T) {
	host := &plugintest.Host{Reply: upper}
	p := newPlugin(t, `{"model":"gpt","provider":"openai","roles":["system"]}`, host)
	pr := prompt(
		pluginapi.TextMessage(pluginapi.RoleDeveloper, "dev rules"),
		pluginapi.TextMessage(pluginapi.RoleUser, "hello"),
	)
	_, err := p.OnPrompt(context.Background(), &pluginapi.Exchange{Prompt: pr, Values: pluginapi.Values{}})
	require.NoError(t, err)
	got := pr.Messages[0].Text()
	require.Equal(t, "DEV RULES", got)
	got = pr.Messages[1].Text()
	require.Equal(t, "hello", got)
}
