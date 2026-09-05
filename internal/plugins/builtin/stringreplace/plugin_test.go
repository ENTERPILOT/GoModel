package stringreplace

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"github.com/enterpilot/gomodel/pluginapi"
)

type fakeHost struct{}

func (fakeHost) Logger() *slog.Logger           { return slog.New(slog.NewTextHandler(io.Discard, nil)) }
func (fakeHost) Inference() pluginapi.Inference { return nil }
func (fakeHost) History(context.Context, pluginapi.Meta) ([]pluginapi.Message, error) {
	return nil, nil
}
func (fakeHost) Metrics() pluginapi.Metrics { return noopMetrics{} }

type noopMetrics struct{}

func (noopMetrics) Inc(string, map[string]string)              {}
func (noopMetrics) Observe(string, float64, map[string]string) {}

func newPlugin(t *testing.T, cfg string) *Plugin {
	t.Helper()
	p := New()
	if err := p.Init(context.Background(), json.RawMessage(cfg), fakeHost{}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	return p.(*Plugin)
}

func exchange(prompt *pluginapi.Prompt, resp *pluginapi.Completion) *pluginapi.Exchange {
	return &pluginapi.Exchange{Prompt: prompt, Response: resp, Values: pluginapi.Values{}}
}

func text(role pluginapi.Role, id, s string) pluginapi.Message {
	m := pluginapi.TextMessage(role, s)
	m.ID = id
	return m
}

func TestManifest(t *testing.T) {
	m := New().Manifest()
	if m.Name != "string_replace" || !m.Mutates {
		t.Fatalf("manifest = %+v", m)
	}
	if !reflect.DeepEqual(m.Kinds, []pluginapi.Kind{pluginapi.KindPrompt, pluginapi.KindResponse, pluginapi.KindStream}) {
		t.Errorf("kinds = %v", m.Kinds)
	}
	want := []string{"rules", "mode", "case_insensitive", "roles", "on_match", "message", "block_status", "stream_lookbehind"}
	var keys []string
	for _, f := range m.ConfigSchema {
		keys = append(keys, f.Key)
		if f.Label == "" || f.Help == "" {
			t.Errorf("field %s lacks label or help", f.Key)
		}
		if (f.Input == pluginapi.InputSelect || f.Input == pluginapi.InputCheckboxes) && len(f.Options) == 0 {
			t.Errorf("field %s lacks options", f.Key)
		}
	}
	if !reflect.DeepEqual(keys, want) {
		t.Errorf("keys = %v, want %v", keys, want)
	}
	for _, iface := range []struct {
		name string
		ok   bool
	}{
		{"PromptHook", func() bool { _, ok := New().(pluginapi.PromptHook); return ok }()},
		{"ResponseHook", func() bool { _, ok := New().(pluginapi.ResponseHook); return ok }()},
		{"StreamHook", func() bool { _, ok := New().(pluginapi.StreamHook); return ok }()},
	} {
		if !iface.ok {
			t.Errorf("plugin must implement %s", iface.name)
		}
	}
}

func TestInitErrors(t *testing.T) {
	tests := []struct {
		name string
		cfg  string
		want string
	}{
		{"empty", ``, "rules is required"},
		{"no rules", `{"rules": "# only a comment\n"}`, "rules is required"},
		{"unknown key", `{"rules": "a => b", "bogus": 1}`, `unknown field "bogus"`},
		{"bad separator", `{"rules": "a=>b"}`, `rules line 1: expected "find => replace"`},
		{"empty find", `{"rules": " => b"}`, "find side is empty"},
		{"bad mode", `{"rules": "a => b", "mode": "glob"}`, "mode must be one of literal, regex"},
		{"bad regex", `{"rules": "a( => b", "mode": "regex"}`, "rules line 1: invalid regex"},
		{"empty match regex", `{"rules": "a* => b", "mode": "regex"}`, "matches the empty string"},
		{"bad on_match", `{"rules": "a => b", "on_match": "drop"}`, "on_match must be one of"},
		{"bad role", `{"rules": "a => b", "roles": ["admin"]}`, `unknown role "admin"`},
		{"bad bool", `{"rules": "a => b", "case_insensitive": "maybe"}`, "case_insensitive must be true or false"},
		{"status too low", `{"rules": "a => b", "block_status": 200}`, "block_status must be an HTTP status between 400 and 599"},
		{"status text", `{"rules": "a => b", "block_status": "abc"}`, "block_status must be a number"},
		{"negative lookbehind", `{"rules": "a => b", "stream_lookbehind": -1}`, "stream_lookbehind must be a whole number between 0"},
		{"line number", `{"rules": "a => b\n\nbroken"}`, "rules line 3"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := New().Init(context.Background(), json.RawMessage(tt.cfg), fakeHost{})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestDefaults(t *testing.T) {
	p := newPlugin(t, `{"rules": "a => b"}`)
	if p.mode != ModeLiteral || p.onMatch != OnMatchReplace || p.caseInsensitive || p.message != DefaultMessage ||
		p.blockStatus != 0 || p.lookbehind != DefaultStreamLookbehind {
		t.Errorf("defaults = %+v", p.settings)
	}
	if !reflect.DeepEqual(p.roles, map[pluginapi.Role]bool{pluginapi.RoleUser: true}) {
		t.Errorf("roles = %v", p.roles)
	}
	// Numbers and booleans as strings (dashboard forms), roles as CSV.
	p = newPlugin(t, `{"rules": ["a => b"], "block_status": "451", "stream_lookbehind": "8", "case_insensitive": "yes", "roles": "system, tool"}`)
	if p.blockStatus != 451 || p.lookbehind != 8 || !p.caseInsensitive {
		t.Errorf("settings = %+v", p.settings)
	}
	if !p.roles[pluginapi.RoleSystem] || !p.roles[pluginapi.RoleDeveloper] || !p.roles[pluginapi.RoleTool] || p.roles[pluginapi.RoleUser] {
		t.Errorf("roles = %v", p.roles)
	}
}

func TestApplyRules(t *testing.T) {
	tests := []struct {
		name  string
		cfg   string
		in    string
		want  string
		count int
	}{
		{"literal", `{"rules": "ACME Corp => [company]"}`, "Hi ACME Corp, ACME Corp!", "Hi [company], [company]!", 2},
		{"literal is not regex", `{"rules": "a.c => x"}`, "abc a.c", "abc x", 1},
		{"empty replacement", `{"rules": "secret => "}`, "a secret b", "a  b", 1},
		{"empty replacement trimmed separator", `{"rules": "secret =>"}`, "a secret b", "a  b", 1},
		{"case sensitive by default", `{"rules": "acme => x"}`, "ACME acme", "ACME x", 1},
		{"case insensitive literal", `{"rules": "acme => x", "case_insensitive": true}`, "ACME Acme", "x x", 2},
		{"case insensitive regex", `{"rules": "a+ => x", "mode": "regex", "case_insensitive": "true"}`, "AAA aa", "x x", 2},
		{"regex groups", `{"rules": "(\\d{3})-\\d{4} => $1-XXXX", "mode": "regex"}`, "call 555-1234", "call 555-XXXX", 1},
		{"regex literal dollar", `{"rules": "\\d+ => $$", "mode": "regex"}`, "cost 42", "cost $", 1},
		{"escapes both sides literal", `{"rules": "a\\nb => c\\td"}`, "xa\nby", "xc\tdy", 1},
		{"escaped backslash literal", `{"rules": "C:\\\\tmp => [path]"}`, `C:\tmp`, "[path]", 1},
		{"regex find keeps escapes", `{"rules": "\\\\d => [digit]", "mode": "regex"}`, `\d 5`, "[digit] 5", 1},
		{"regex newline escape", `{"rules": "a\\nb => c\\nd", "mode": "regex"}`, "a\nb", "c\nd", 1},
		{"rule order", `{"rules": "a => b\nb => c"}`, "ab", "cc", 3},
		{"comments and blanks", `{"rules": "# header\n\na => b\n  \n"}`, "aa", "bb", 2},
		{"no match", `{"rules": "zzz => y"}`, "abc", "abc", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := newPlugin(t, tt.cfg)
			got, n := apply(p.rules, tt.in, 0)
			if got != tt.want || n != tt.count {
				t.Errorf("apply = %q (%d), want %q (%d)", got, n, tt.want, tt.count)
			}
		})
	}
}

func prompt() *pluginapi.Prompt {
	p := &pluginapi.Prompt{Messages: []pluginapi.Message{
		text(pluginapi.RoleSystem, "m0", "You work for ACME."),
		text(pluginapi.RoleUser, "m1", "Tell me about ACME and ACME."),
		{ID: "m2", Role: pluginapi.RoleAssistant, Parts: []pluginapi.Part{
			{Kind: pluginapi.PartText, Text: "ACME is great."},
			{Kind: pluginapi.PartToolCall, ToolCall: &pluginapi.ToolCall{ID: "c1", Name: "lookup", Arguments: json.RawMessage(`{"q":"ACME"}`)}},
		}},
		{ID: "m3", Role: pluginapi.RoleTool, ToolCallID: "c1", Parts: []pluginapi.Part{
			{Kind: pluginapi.PartToolResult, ToolResult: &pluginapi.ToolResult{CallID: "c1", Parts: []pluginapi.Part{
				{Kind: pluginapi.PartText, Text: "ACME founded 1999"},
				{Kind: pluginapi.PartImage, URL: "https://x/y.png"},
			}}},
		}},
		{ID: "m4", Role: pluginapi.RoleUser, Parts: []pluginapi.Part{
			{Kind: pluginapi.PartImage, URL: "https://x/z.png"},
			{Kind: pluginapi.PartText, Text: "and ACME?"},
		}},
	}}
	p.Reset()
	return p
}

func TestOnPromptReplace(t *testing.T) {
	tests := []struct {
		name     string
		cfg      string
		wantText []string // per message m0..m4 (Message.Text())
		detail   map[string]any
		edited   []string
	}{
		{
			name:     "default user role",
			cfg:      `{"rules": "ACME => [co]"}`,
			wantText: []string{"You work for ACME.", "Tell me about [co] and [co].", "ACME is great.", "ACME founded 1999", "and [co]?"},
			detail:   map[string]any{"replacements": 3, "messages": 2},
			edited:   []string{"m1", "m4"},
		},
		{
			name:     "all roles including tool results",
			cfg:      `{"rules": "ACME => [co]", "roles": ["system", "user", "assistant", "tool"]}`,
			wantText: []string{"You work for [co].", "Tell me about [co] and [co].", "[co] is great.", "[co] founded 1999", "and [co]?"},
			detail:   map[string]any{"replacements": 6, "messages": 5},
			edited:   []string{"m0", "m1", "m2", "m3", "m4"},
		},
		{
			name:     "no match",
			cfg:      `{"rules": "nothing => x", "roles": ["user", "tool"]}`,
			wantText: []string{"You work for ACME.", "Tell me about ACME and ACME.", "ACME is great.", "ACME founded 1999", "and ACME?"},
			detail:   map[string]any{"replacements": 0, "messages": 0},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := newPlugin(t, tt.cfg)
			x := exchange(prompt(), nil)
			d, err := p.OnPrompt(context.Background(), x)
			if err != nil || d.Action != pluginapi.ActionAllow {
				t.Fatalf("OnPrompt = %+v, %v", d, err)
			}
			for i, m := range x.Prompt.Messages {
				if got := m.Text(); got != tt.wantText[i] {
					t.Errorf("message %s = %q, want %q", m.ID, got, tt.wantText[i])
				}
			}
			if !reflect.DeepEqual(d.Detail, tt.detail) {
				t.Errorf("detail = %v, want %v", d.Detail, tt.detail)
			}
			changes := x.Prompt.Changes()
			var edited []string
			for id, kind := range changes.Messages {
				if kind == pluginapi.ChangeEdited {
					edited = append(edited, id)
				}
			}
			sortStrings(edited)
			if !reflect.DeepEqual(edited, tt.edited) {
				t.Errorf("edited = %v, want %v", edited, tt.edited)
			}
			if changes.Dirty != (len(tt.edited) > 0) {
				t.Errorf("dirty = %v", changes.Dirty)
			}
			// Tool call arguments and non-text parts are never touched.
			if args := string(x.Prompt.Messages[2].Parts[1].ToolCall.Arguments); args != `{"q":"ACME"}` {
				t.Errorf("tool call arguments changed: %s", args)
			}
			if x.Prompt.Messages[3].Parts[0].ToolResult.Parts[1].URL != "https://x/y.png" {
				t.Error("image part changed")
			}
		})
	}
}

func TestOnPromptDecisions(t *testing.T) {
	tests := []struct {
		name    string
		cfg     string
		action  pluginapi.Action
		status  int
		message string
		detail  map[string]any
	}{
		{"block default status", `{"rules": "ACME => x", "on_match": "block"}`, pluginapi.ActionBlock, 0, DefaultMessage, map[string]any{"matches": 3, "messages": 2}},
		{"block custom", `{"rules": "ACME => x", "on_match": "block", "block_status": 446, "message": "nope"}`, pluginapi.ActionBlock, 446, "nope", map[string]any{"matches": 3, "messages": 2}},
		{"respond", `{"rules": "ACME => x", "on_match": "respond", "message": "I cannot discuss that."}`, pluginapi.ActionRespond, 0, "", map[string]any{"matches": 3, "messages": 2}},
		{"warn", `{"rules": "ACME => x", "on_match": "warn", "roles": ["system"]}`, pluginapi.ActionWarn, 0, DefaultMessage, map[string]any{"matches": 1, "messages": 1}},
		{"no match allows", `{"rules": "zzz => x", "on_match": "block"}`, pluginapi.ActionAllow, 0, "", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := newPlugin(t, tt.cfg)
			x := exchange(prompt(), nil)
			d, err := p.OnPrompt(context.Background(), x)
			if err != nil {
				t.Fatal(err)
			}
			if d.Action != tt.action || d.Status != tt.status || d.Message != tt.message {
				t.Errorf("decision = %+v", d)
			}
			if tt.action != pluginapi.ActionAllow && d.Code != Code {
				t.Errorf("code = %q", d.Code)
			}
			if tt.action == pluginapi.ActionRespond {
				if d.Response == nil || d.Response.Text(0) != "I cannot discuss that." {
					t.Errorf("respond completion = %+v", d.Response)
				}
			}
			if tt.detail == nil {
				if d.Detail != nil {
					t.Errorf("detail = %v, want none", d.Detail)
				}
			} else if !reflect.DeepEqual(d.Detail, tt.detail) {
				t.Errorf("detail = %v, want %v", d.Detail, tt.detail)
			}
			if x.Prompt.Changes().Dirty {
				t.Error("non-replace modes must not edit the prompt")
			}
			if got := x.Prompt.Messages[1].Text(); got != "Tell me about ACME and ACME." {
				t.Errorf("prompt edited: %q", got)
			}
		})
	}
}

func completion() *pluginapi.Completion {
	c := &pluginapi.Completion{Choices: []pluginapi.Choice{
		{Index: 0, Message: pluginapi.Message{Role: pluginapi.RoleAssistant, Parts: []pluginapi.Part{
			{Kind: pluginapi.PartReasoning, Text: "ACME thinking"},
			{Kind: pluginapi.PartText, Text: "ACME rocks."},
			{Kind: pluginapi.PartText, Text: " Go ACME."},
		}}, FinishReason: "stop"},
		{Index: 1, Message: pluginapi.TextMessage(pluginapi.RoleAssistant, "nothing here"), FinishReason: "stop"},
		{Index: 2, Message: pluginapi.TextMessage(pluginapi.RoleAssistant, "ACME again"), FinishReason: "stop"},
	}}
	c.Reset()
	return c
}

func TestOnResponse(t *testing.T) {
	t.Run("replace", func(t *testing.T) {
		p := newPlugin(t, `{"rules": "ACME => [co]", "roles": ["system"]}`) // roles are ignored on responses
		x := exchange(nil, completion())
		d, err := p.OnResponse(context.Background(), x)
		if err != nil || d.Action != pluginapi.ActionAllow {
			t.Fatalf("OnResponse = %+v, %v", d, err)
		}
		if got := x.Response.Text(0); got != "[co] rocks. Go [co]." {
			t.Errorf("choice 0 = %q", got)
		}
		if got := x.Response.Text(2); got != "[co] again" {
			t.Errorf("choice 2 = %q", got)
		}
		if got := x.Response.Choices[0].Message.Parts[0].Text; got != "ACME thinking" {
			t.Errorf("reasoning part edited: %q", got)
		}
		if !reflect.DeepEqual(d.Detail, map[string]any{"replacements": 3, "messages": 2}) {
			t.Errorf("detail = %v", d.Detail)
		}
		changes := x.Response.Changes()
		if changes.Messages["choice:0"] != pluginapi.ChangeEdited || changes.Messages["choice:2"] != pluginapi.ChangeEdited || changes.Messages["choice:1"] != "" {
			t.Errorf("changes = %v", changes.Messages)
		}
	})
	t.Run("block uses phase default status", func(t *testing.T) {
		p := newPlugin(t, `{"rules": "ACME => x", "on_match": "block"}`)
		x := exchange(nil, completion())
		d, err := p.OnResponse(context.Background(), x)
		if err != nil || d.Action != pluginapi.ActionBlock || d.Status != 0 || d.Message != DefaultMessage {
			t.Fatalf("OnResponse = %+v, %v", d, err)
		}
		if x.Response.Changes().Dirty || x.Response.Text(0) != "ACME rocks. Go ACME." {
			t.Error("block must not edit the response")
		}
		if !reflect.DeepEqual(d.Detail, map[string]any{"matches": 3, "messages": 2}) {
			t.Errorf("detail = %v", d.Detail)
		}
	})
	t.Run("nil exchange parts", func(t *testing.T) {
		p := newPlugin(t, `{"rules": "ACME => x", "on_match": "block"}`)
		if d, err := p.OnResponse(context.Background(), exchange(nil, nil)); err != nil || d.Action != pluginapi.ActionAllow {
			t.Errorf("OnResponse(nil) = %+v, %v", d, err)
		}
		if d, err := p.OnPrompt(context.Background(), exchange(nil, nil)); err != nil || d.Action != pluginapi.ActionAllow {
			t.Errorf("OnPrompt(nil) = %+v, %v", d, err)
		}
	})
}

func TestStreamPolicy(t *testing.T) {
	tests := []struct {
		cfg  string
		want pluginapi.StreamPolicy
	}{
		{`{"rules": "a => b"}`, pluginapi.StreamPolicy{Mode: pluginapi.StreamTransform, LookbehindChars: 64}},
		{`{"rules": "a => b", "stream_lookbehind": 128}`, pluginapi.StreamPolicy{Mode: pluginapi.StreamTransform, LookbehindChars: 128}},
		{`{"rules": "a => b", "on_match": "warn", "stream_lookbehind": 0}`, pluginapi.StreamPolicy{Mode: pluginapi.StreamTransform}},
		{`{"rules": "a => b", "on_match": "block"}`, pluginapi.StreamPolicy{Mode: pluginapi.StreamBuffer}},
		{`{"rules": "a => b", "on_match": "respond"}`, pluginapi.StreamPolicy{Mode: pluginapi.StreamBuffer}},
	}
	for _, tt := range tests {
		t.Run(tt.cfg, func(t *testing.T) {
			if got := newPlugin(t, tt.cfg).StreamPolicy(); got != tt.want {
				t.Errorf("StreamPolicy = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestStreamEvents(t *testing.T) {
	events := []*pluginapi.StreamEvent{
		{Seq: 1, Kind: pluginapi.EventTextDelta, Text: "Hello ACME"},
		{Seq: 2, Kind: pluginapi.EventReasoningDelta, Text: "ACME"},
		{Seq: 3, Kind: pluginapi.EventToolCallDelta, Text: `{"q":"ACME"}`},
		{Seq: 4, Kind: pluginapi.EventTextDelta, Text: " and ACME"},
		{Seq: 5, Kind: pluginapi.EventTextDelta, Text: " bye"},
		{Seq: 6, Kind: pluginapi.EventFinish},
	}
	tests := []struct {
		name    string
		cfg     string
		actions []pluginapi.StreamAction
		texts   []string
		end     pluginapi.Action
		detail  map[string]any
	}{
		{
			name:    "replace",
			cfg:     `{"rules": "ACME => [co]"}`,
			actions: []pluginapi.StreamAction{pluginapi.StreamReplace, pluginapi.StreamPass, pluginapi.StreamPass, pluginapi.StreamReplace, pluginapi.StreamPass, pluginapi.StreamPass},
			texts:   []string{"Hello [co]", "", "", " and [co]", "", ""},
			end:     pluginapi.ActionAllow,
			detail:  map[string]any{"replacements": 2},
		},
		{
			name:    "warn",
			cfg:     `{"rules": "ACME => [co]", "on_match": "warn"}`,
			actions: []pluginapi.StreamAction{pluginapi.StreamPass, pluginapi.StreamPass, pluginapi.StreamPass, pluginapi.StreamPass, pluginapi.StreamPass, pluginapi.StreamPass},
			texts:   []string{"", "", "", "", "", ""},
			end:     pluginapi.ActionWarn,
			detail:  map[string]any{"matches": 2},
		},
		{
			name:    "block passes and leaves the decision to OnResponse",
			cfg:     `{"rules": "ACME => [co]", "on_match": "block"}`,
			actions: []pluginapi.StreamAction{pluginapi.StreamPass, pluginapi.StreamPass, pluginapi.StreamPass, pluginapi.StreamPass, pluginapi.StreamPass, pluginapi.StreamPass},
			texts:   []string{"", "", "", "", "", ""},
			end:     pluginapi.ActionAllow,
		},
		{
			name:    "no match",
			cfg:     `{"rules": "zzz => y", "on_match": "warn"}`,
			actions: []pluginapi.StreamAction{pluginapi.StreamPass, pluginapi.StreamPass, pluginapi.StreamPass, pluginapi.StreamPass, pluginapi.StreamPass, pluginapi.StreamPass},
			texts:   []string{"", "", "", "", "", ""},
			end:     pluginapi.ActionAllow,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := newPlugin(t, tt.cfg)
			x := exchange(nil, nil)
			for i, ev := range events {
				d, err := p.OnStreamEvent(context.Background(), x, ev)
				if err != nil {
					t.Fatal(err)
				}
				if d.Action != tt.actions[i] || d.Text != tt.texts[i] {
					t.Errorf("event %d: decision = %+v, want %s %q", ev.Seq, d, tt.actions[i], tt.texts[i])
				}
			}
			d, err := p.OnStreamEnd(context.Background(), x)
			if err != nil {
				t.Fatal(err)
			}
			if d.Action != tt.end {
				t.Errorf("end action = %s, want %s", d.Action, tt.end)
			}
			if tt.detail != nil && !reflect.DeepEqual(d.Detail, tt.detail) {
				t.Errorf("end detail = %v, want %v", d.Detail, tt.detail)
			}
			if tt.end == pluginapi.ActionWarn && (d.Code != Code || d.Message != DefaultMessage) {
				t.Errorf("warn decision = %+v", d)
			}
		})
	}
}

func TestStreamNilValues(t *testing.T) {
	p := newPlugin(t, `{"rules": "ACME => [co]", "on_match": "warn"}`)
	x := &pluginapi.Exchange{}
	if _, err := p.OnStreamEvent(context.Background(), x, &pluginapi.StreamEvent{Kind: pluginapi.EventTextDelta, Text: "ACME"}); err != nil {
		t.Fatal(err)
	}
	if d, err := p.OnStreamEnd(context.Background(), x); err != nil || d.Action != pluginapi.ActionAllow {
		t.Errorf("OnStreamEnd without Values = %+v, %v", d, err)
	}
}

func TestSummarize(t *testing.T) {
	tests := []struct {
		cfg  string
		want string
	}{
		{`{"rules": "a => b"}`, "1 literal rule, replace"},
		{`{"rules": "a => b\nc => d", "mode": "regex", "case_insensitive": true, "on_match": "block"}`, "2 regex rules (case-insensitive), block"},
		{`{"rules": ""}`, ""},
	}
	for _, tt := range tests {
		if got := New().(*Plugin).Summarize(json.RawMessage(tt.cfg)); got != tt.want {
			t.Errorf("Summarize(%s) = %q, want %q", tt.cfg, got, tt.want)
		}
	}
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// TestStreamOverlapIsNotReprocessed replays the lookbehind windows GoModel
// presents: the withheld tail comes back in front of the next delta with
// Overlap set, already carrying the earlier replacement.
func TestStreamOverlapIsNotReprocessed(t *testing.T) {
	tests := []struct {
		name   string
		cfg    string
		events []*pluginapi.StreamEvent
		want   []pluginapi.StreamDecision
		total  int
	}{
		{
			name: "expanding rule is applied once",
			cfg:  `{"rules": "a => aa"}`,
			events: []*pluginapi.StreamEvent{
				{Seq: 1, Kind: pluginapi.EventTextDelta, Text: "a"},
				{Seq: 2, Kind: pluginapi.EventTextDelta, Text: "aa", Overlap: 2}, // the flushed tail
			},
			want:  []pluginapi.StreamDecision{pluginapi.Replace("aa"), pluginapi.Pass()},
			total: 1,
		},
		{
			name: "match spanning the boundary is applied",
			cfg:  `{"rules": "ab => X"}`,
			events: []*pluginapi.StreamEvent{
				{Seq: 1, Kind: pluginapi.EventTextDelta, Text: "a"},
				{Seq: 2, Kind: pluginapi.EventTextDelta, Text: "ab", Overlap: 1},
			},
			want:  []pluginapi.StreamDecision{pluginapi.Pass(), pluginapi.Replace("X")},
			total: 1,
		},
		{
			name: "match inside the overlap is skipped but a later one in the same window is not",
			cfg:  `{"rules": "é => e"}`,
			events: []*pluginapi.StreamEvent{
				{Seq: 1, Kind: pluginapi.EventTextDelta, Text: "é"},
				{Seq: 2, Kind: pluginapi.EventTextDelta, Text: "e é", Overlap: 1},
			},
			want:  []pluginapi.StreamDecision{pluginapi.Replace("e"), pluginapi.Replace("e e")},
			total: 2,
		},
		{
			name: "regex group expansion after the overlap",
			cfg:  `{"rules": "(\\d+)-(\\d+) => $2-$1", "mode": "regex"}`,
			events: []*pluginapi.StreamEvent{
				{Seq: 1, Kind: pluginapi.EventTextDelta, Text: "12-34 "},
				{Seq: 2, Kind: pluginapi.EventTextDelta, Text: "34-12 56-78", Overlap: 6},
			},
			want:  []pluginapi.StreamDecision{pluginapi.Replace("34-12 "), pluginapi.Replace("34-12 78-56")},
			total: 2,
		},
		{
			name: "warn counts only new matches",
			cfg:  `{"rules": "ACME => x", "on_match": "warn"}`,
			events: []*pluginapi.StreamEvent{
				{Seq: 1, Kind: pluginapi.EventTextDelta, Text: "ACME"},
				{Seq: 2, Kind: pluginapi.EventTextDelta, Text: "ACME ACME", Overlap: 4},
			},
			want:  []pluginapi.StreamDecision{pluginapi.Pass(), pluginapi.Pass()},
			total: 2,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := newPlugin(t, tt.cfg)
			x := exchange(nil, nil)
			for i, ev := range tt.events {
				d, err := p.OnStreamEvent(context.Background(), x, ev)
				if err != nil {
					t.Fatal(err)
				}
				if d.Action != tt.want[i].Action || d.Text != tt.want[i].Text {
					t.Errorf("event %d: decision = %+v, want %+v", ev.Seq, d, tt.want[i])
				}
			}
			if got := p.streamCount(x); got != tt.total {
				t.Errorf("stream matches = %d, want %d", got, tt.total)
			}
		})
	}
}
