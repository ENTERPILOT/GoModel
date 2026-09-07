package llmaltering

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/enterpilot/gomodel/pluginapi"
)

type fakeHost struct {
	mu       sync.Mutex
	requests []pluginapi.InferenceRequest
	reply    func(req pluginapi.InferenceRequest) (*pluginapi.Completion, error)
}

func (h *fakeHost) Logger() *slog.Logger           { return slog.Default() }
func (h *fakeHost) Inference() pluginapi.Inference { return h }
func (h *fakeHost) History(context.Context, pluginapi.Meta) ([]pluginapi.Message, error) {
	return nil, nil
}
func (h *fakeHost) Metrics() pluginapi.Metrics { return noopMetrics{} }
func (h *fakeHost) Complete(_ context.Context, req pluginapi.InferenceRequest) (*pluginapi.Completion, error) {
	h.mu.Lock()
	h.requests = append(h.requests, req)
	h.mu.Unlock()
	return h.reply(req)
}

type noopMetrics struct{}

func (noopMetrics) Inc(string, map[string]string)              {}
func (noopMetrics) Observe(string, float64, map[string]string) {}

func replyWith(text string) *pluginapi.Completion {
	return &pluginapi.Completion{Choices: []pluginapi.Choice{{Message: pluginapi.TextMessage(pluginapi.RoleAssistant, text), FinishReason: "stop"}}}
}

func upper(req pluginapi.InferenceRequest) (*pluginapi.Completion, error) {
	text := req.Messages[1].Text()
	inner := strings.TrimSuffix(strings.TrimPrefix(text, "<TEXT_TO_ALTER>\n"), "\n</TEXT_TO_ALTER>")
	return replyWith("<TEXT_TO_ALTER>\n" + strings.ToUpper(inner) + "\n</TEXT_TO_ALTER>"), nil
}

func newPlugin(t *testing.T, cfg string, host *fakeHost) *Plugin {
	t.Helper()
	p := New().(*Plugin)
	if err := p.Init(context.Background(), json.RawMessage(cfg), host); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
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
	host := &fakeHost{reply: upper}
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
	if _, err := p.OnPrompt(context.Background(), x); err != nil {
		t.Fatalf("OnPrompt() error = %v", err)
	}
	if got := pr.Messages[0].Text(); got != "sys" {
		t.Fatalf("system rewritten: %q", got)
	}
	if got := pr.Messages[1].Text(); got != "HELLO" {
		t.Fatalf("user = %q", got)
	}
	if got := pr.Messages[2].Text(); got != "### safe keep" {
		t.Fatalf("skip prefix ignored: %q", got)
	}
	if parts := pr.Messages[3].Parts; parts[0].Text != "A" || parts[1].Kind != pluginapi.PartImage || parts[2].Text != "B" {
		t.Fatalf("multipart = %+v", parts)
	}
	if got := pr.Messages[4].Text(); got != "RESULT" {
		t.Fatalf("tool result = %q", got)
	}
	changes := pr.Changes()
	if changes.Messages["m1"] != pluginapi.ChangeEdited || changes.Messages["m4"] != pluginapi.ChangeEdited || changes.Messages["m0"] != "" {
		t.Fatalf("changes = %+v", changes)
	}
	if len(host.requests) != 4 {
		t.Fatalf("requests = %d, want 4", len(host.requests))
	}
	req := host.requests[0]
	if req.Model != "openai/gpt" || req.MaxTokens != 7 || req.Temperature == nil || *req.Temperature != 0 || req.Messages[0].Text() != DefaultPrompt {
		t.Fatalf("request = %+v", req)
	}
}

func TestOnPromptKeepsTextOnRewriteFailures(t *testing.T) {
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
			p := newPlugin(t, `{"model":"m"}`, &fakeHost{reply: tt.reply})
			pr := prompt(pluginapi.TextMessage(pluginapi.RoleUser, "hello"))
			if _, err := p.OnPrompt(context.Background(), &pluginapi.Exchange{Prompt: pr, Values: pluginapi.Values{}}); err != nil {
				t.Fatalf("OnPrompt() error = %v", err)
			}
			if pr.Messages[0].Text() != "hello" || pr.Changes().Dirty {
				t.Fatalf("prompt changed: %+v", pr.Messages[0])
			}
		})
	}
}

func TestOnPromptPropagatesCancellation(t *testing.T) {
	p := newPlugin(t, `{"model":"m"}`, &fakeHost{reply: func(pluginapi.InferenceRequest) (*pluginapi.Completion, error) { return nil, context.Canceled }})
	pr := prompt(pluginapi.TextMessage(pluginapi.RoleUser, "hello"))
	if _, err := p.OnPrompt(context.Background(), &pluginapi.Exchange{Prompt: pr, Values: pluginapi.Values{}}); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func TestOnResponseRewritesAssistant(t *testing.T) {
	host := &fakeHost{reply: upper}
	completion := &pluginapi.Completion{Choices: []pluginapi.Choice{
		{Index: 0, Message: pluginapi.TextMessage(pluginapi.RoleAssistant, "one")},
		{Index: 1, Message: pluginapi.Message{Role: pluginapi.RoleAssistant, Parts: []pluginapi.Part{{Kind: pluginapi.PartReasoning, Text: "think"}, {Kind: pluginapi.PartText, Text: "two"}}}},
	}}
	completion.Reset()
	x := &pluginapi.Exchange{Response: completion, Values: pluginapi.Values{}}
	if _, err := newPlugin(t, `{"model":"m","roles":["user"]}`, host).OnResponse(context.Background(), x); err != nil {
		t.Fatal(err)
	}
	if completion.Text(0) != "one" || len(host.requests) != 0 {
		t.Fatal("assistant not selected but response rewritten")
	}
	if _, err := newPlugin(t, `{"model":"m","roles":["assistant"]}`, host).OnResponse(context.Background(), x); err != nil {
		t.Fatal(err)
	}
	if completion.Text(0) != "ONE" || completion.Text(1) != "TWO" || completion.Choices[1].Message.Parts[0].Text != "think" {
		t.Fatalf("completion = %+v", completion.Choices)
	}
	if completion.Changes().Messages["choice:0"] != pluginapi.ChangeEdited {
		t.Fatalf("changes = %+v", completion.Changes())
	}
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
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.Model != tt.want.Model || got.Prompt != tt.want.Prompt || got.MaxTokens != tt.want.MaxTokens || got.SkipContentPrefix != tt.want.SkipContentPrefix || strings.Join(got.Roles, ",") != strings.Join(tt.want.Roles, ",") {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestSummarize(t *testing.T) {
	p := New().(*Plugin)
	if got := p.Summarize(json.RawMessage(`{"model":"gpt","provider":"openai","roles":["user","tool"]}`)); got != "openai/gpt • user,tool • default prompt" {
		t.Fatalf("Summarize() = %q", got)
	}
	if got := p.Summarize(json.RawMessage(`{"model":"gpt","prompt":"Rewrite   this\ncarefully"}`)); got != "gpt • user • Rewrite this carefully" {
		t.Fatalf("Summarize(custom) = %q", got)
	}
	if got := p.Summarize(json.RawMessage(`{"model":"gpt","prompt":"` + strings.Repeat("a", 60) + `"}`)); !strings.HasSuffix(got, "...") {
		t.Fatalf("Summarize(long) = %q", got)
	}
	if p.Summarize(json.RawMessage(`{}`)) != "" {
		t.Fatal("invalid config should summarize empty")
	}
	if m := p.Manifest(); m.Name != Name || len(m.Kinds) != 2 || !m.Mutates {
		t.Fatalf("manifest = %+v", m)
	}
}
