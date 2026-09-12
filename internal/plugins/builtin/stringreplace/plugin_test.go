package stringreplace

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/enterpilot/gomodel/pluginapi"
	"github.com/enterpilot/gomodel/pluginapi/plugintest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newPlugin(t *testing.T, cfg string) *Plugin {
	t.Helper()
	p := New()
	err := p.Init(context.Background(), json.RawMessage(cfg), plugintest.NewHost())
	require.NoError(t, err)

	return p.(*Plugin)
}

// A chained Responses request replays its stored history through the prompt
// phase only when an instance edits content, so an instance that only blocks,
// responds, or warns must say so.
func TestEditsContent(t *testing.T) {
	tests := []struct {
		name string
		cfg  string
		want bool
	}{
		{name: "default replaces", cfg: `{"rules":"a => b"}`, want: true},
		{name: "replace edits", cfg: `{"rules":"a => b","on_match":"replace"}`, want: true},
		{name: "block only rejects", cfg: `{"rules":"a => b","on_match":"block"}`},
		{name: "respond only answers", cfg: `{"rules":"a => b","on_match":"respond"}`},
		{name: "warn only flags", cfg: `{"rules":"a => b","on_match":"warn"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := newPlugin(t, tt.cfg).EditsContent()
			require.Equal(t, tt.want, got)
		})
	}
}

func TestManifest(t *testing.T) {
	m := New().Manifest()
	require.Equal(t, "string_replace", m.Name)
	require.True(t, m.Mutates)
	require.True(t, m.Guardrail)
	assert.Equal(t, []pluginapi.Kind{pluginapi.KindPrompt, pluginapi.KindResponse, pluginapi.KindStream}, m.Kinds)

	want := []string{"rules", "mode", "case_insensitive", "roles", "on_match", "message", "block_status", "stream_lookbehind"}
	var keys []string
	for _, f := range m.ConfigSchema {
		keys = append(keys, f.Key)
		assert.NotEmpty(t, f.Label)
		assert.NotEmpty(t, f.Help, "field %s lacks label or help", f.Key)

		if f.Input == pluginapi.InputSelect || f.Input == pluginapi.InputCheckboxes {
			assert.NotEmpty(t, f.Options, "field %s lacks options", f.Key)
		}
	}
	assert.Equal(t, want, keys)

	for _, iface := range []struct {
		name string
		ok   bool
	}{
		{"PromptHook", func() bool { _, ok := New().(pluginapi.PromptHook); return ok }()},
		{"ResponseHook", func() bool { _, ok := New().(pluginapi.ResponseHook); return ok }()},
		{"StreamHook", func() bool { _, ok := New().(pluginapi.StreamHook); return ok }()},
	} {
		assert.True(t, iface.ok, "plugin must implement %s", iface.name)
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
		{"negative lookbehind", `{"rules": "a => b", "stream_lookbehind": -1}`, "stream_lookbehind must be between 0"},
		{"line number", `{"rules": "a => b\n\nbroken"}`, "rules line 3"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := New().Init(context.Background(), json.RawMessage(tt.cfg), plugintest.NewHost())
			require.ErrorContains(t, err, tt.want)
		})
	}
}

func TestDefaults(t *testing.T) {
	p := newPlugin(t, `{"rules": "a => b"}`)
	assert.Equal(t, ModeLiteral, p.mode)
	assert.Equal(t, OnMatchReplace, p.onMatch)
	assert.False(t, p.caseInsensitive)
	assert.Equal(t, DefaultMessage, p.enforcement.Message)
	assert.Equal(t, 0, p.enforcement.BlockStatus)
	assert.Equal(t, DefaultStreamLookbehind, p.lookbehind)
	assert.Equal(t, map[pluginapi.Role]bool{pluginapi.RoleUser: true}, p.roles)

	// Numbers and booleans as strings (dashboard forms), roles as CSV.
	p = newPlugin(t, `{"rules": ["a => b"], "block_status": "451", "stream_lookbehind": "8", "case_insensitive": "yes", "roles": "system, tool"}`)
	assert.Equal(t, 451, p.enforcement.BlockStatus)
	assert.Equal(t, 8, p.lookbehind)
	assert.True(t, p.caseInsensitive)

	assert.True(t, p.roles[pluginapi.RoleSystem])
	assert.True(t, p.roles[pluginapi.RoleDeveloper])
	assert.True(t, p.roles[pluginapi.RoleTool])
	assert.False(t, p.roles[pluginapi.RoleUser])
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
			got, n := apply(p.rules, tt.in, whole)
			assert.Equal(t, tt.want, got)
			assert.Equal(t, tt.count, n)
		})
	}
}

func prompt() *pluginapi.Prompt {
	p := &pluginapi.Prompt{Messages: []pluginapi.Message{
		plugintest.Text(pluginapi.RoleSystem, "m0", "You work for ACME."),
		plugintest.Text(pluginapi.RoleUser, "m1", "Tell me about ACME and ACME."),
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
			x := plugintest.Exchange(prompt(), nil)
			d, err := p.OnPrompt(context.Background(), x)
			require.NoError(t, err)
			require.Equal(t, pluginapi.ActionAllow, d.Action)

			for i, m := range x.Prompt.Messages {
				got := m.Text()
				assert.Equal(t, tt.wantText[i], got)
			}
			assert.Equal(t, tt.detail, d.Detail)

			changes := x.Prompt.Changes()
			var edited []string
			for id, kind := range changes.Messages {
				if kind == pluginapi.ChangeEdited {
					edited = append(edited, id)
				}
			}
			sortStrings(edited)
			assert.Equal(t, tt.edited, edited)
			assert.Equal(t, len(tt.edited) > 0, changes.Dirty)
			// Tool call arguments and non-text parts are never touched.
			args := string(x.Prompt.Messages[2].Parts[1].ToolCall.Arguments)
			assert.Equal(t, `{"q":"ACME"}`, args)
			assert.Equal(t, "https://x/y.png", x.Prompt.Messages[3].Parts[0].ToolResult.Parts[1].URL)
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
			x := plugintest.Exchange(prompt(), nil)
			d, err := p.OnPrompt(context.Background(), x)
			require.NoError(t, err)
			assert.Equal(t, tt.action, d.Action)
			assert.Equal(t, tt.status, d.Status)
			assert.Equal(t, tt.message, d.Message)

			if tt.action != pluginapi.ActionAllow {
				assert.Equal(t, Code, d.Code)
			}
			if tt.action == pluginapi.ActionRespond {
				assert.NotNil(t, d.Response)
				assert.Equal(t, "I cannot discuss that.", d.Response.Text(0))
			}
			if tt.detail == nil {
				assert.Nil(t, d.Detail)
			} else {
				assert.Equal(t, tt.detail, d.Detail)
			}
			assert.False(t, x.Prompt.Changes().Dirty)
			got := x.Prompt.Messages[1].Text()
			assert.Equal(t, "Tell me about ACME and ACME.", got)
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
		x := plugintest.Exchange(nil, completion())
		d, err := p.OnResponse(context.Background(), x)
		require.NoError(t, err)
		require.Equal(t, pluginapi.ActionAllow, d.Action)
		got := x.Response.Text(0)
		assert.Equal(t, "[co] rocks. Go [co].", got)
		got = x.Response.Text(2)
		assert.Equal(t, "[co] again", got)
		got = x.Response.Choices[0].Message.Parts[0].Text
		assert.Equal(t, "ACME thinking", got)
		assert.Equal(t, map[string]any{"replacements": 3, "messages": 2}, d.Detail)

		changes := x.Response.Changes()
		assert.Equal(t, pluginapi.ChangeEdited, changes.Messages["choice:0"])
		assert.Equal(t, pluginapi.ChangeEdited, changes.Messages["choice:2"])
		assert.Empty(t, changes.Messages["choice:1"], "changes = %v", changes.Messages)
	})
	t.Run("block uses phase default status", func(t *testing.T) {
		p := newPlugin(t, `{"rules": "ACME => x", "on_match": "block"}`)
		x := plugintest.Exchange(nil, completion())
		d, err := p.OnResponse(context.Background(), x)
		require.NoError(t, err)
		require.Equal(t, pluginapi.ActionBlock, d.Action)
		require.Equal(t, 0, d.Status)
		require.Equal(t, DefaultMessage, d.Message)
		assert.False(t, x.Response.Changes().Dirty)
		assert.Equal(t, "ACME rocks. Go ACME.", x.Response.Text(0))
		assert.Equal(t, map[string]any{"matches": 3, "messages": 2}, d.Detail)
	})
	t.Run("match split across parts is not a match", func(t *testing.T) {
		p := newPlugin(t, `{"rules": "secret => x", "on_match": "block"}`)
		c := &pluginapi.Completion{Choices: []pluginapi.Choice{{Message: pluginapi.Message{Role: pluginapi.RoleAssistant, Parts: []pluginapi.Part{
			{Kind: pluginapi.PartText, Text: "my sec"},
			{Kind: pluginapi.PartText, Text: "ret"},
		}}}}}
		c.Reset()
		d, err := p.OnResponse(context.Background(), plugintest.Exchange(nil, c))
		require.NoError(t, err)
		require.Equal(t, pluginapi.ActionAllow, d.Action, "OnResponse = %+v, %v; want allow like replace, which cannot edit across parts", d, err)
	})
	t.Run("nil exchange parts", func(t *testing.T) {
		p := newPlugin(t, `{"rules": "ACME => x", "on_match": "block"}`)
		d, err := p.OnResponse(context.Background(), plugintest.Exchange(nil, nil))
		assert.NoError(t, err)
		assert.Equal(t, pluginapi.ActionAllow, d.Action)
		d, err = p.OnPrompt(context.Background(), plugintest.Exchange(nil, nil))
		assert.NoError(t, err)
		assert.Equal(t, pluginapi.ActionAllow, d.Action)
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
			got := newPlugin(t, tt.cfg).StreamPolicy()
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestStreamEvents(t *testing.T) {
	// Each text delta closes its window (Final), so a match at its end is due.
	events := []*pluginapi.StreamEvent{
		{Seq: 1, Kind: pluginapi.EventTextDelta, Text: "Hello ACME", Final: true},
		{Seq: 2, Kind: pluginapi.EventReasoningDelta, Text: "ACME"},
		{Seq: 3, Kind: pluginapi.EventToolCallDelta, Text: `{"q":"ACME"}`},
		{Seq: 4, Kind: pluginapi.EventTextDelta, Text: " and ACME", Final: true},
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
			x := plugintest.Exchange(nil, nil)
			for i, ev := range events {
				d, err := p.OnStreamEvent(context.Background(), x, ev)
				require.NoError(t, err)
				assert.Equal(t, tt.actions[i], d.Action)
				assert.Equal(t, tt.texts[i], d.Text, "event %d: decision = %+v, want %s %q", ev.Seq, d, tt.actions[i], tt.texts[i])
			}
			d, err := p.OnStreamEnd(context.Background(), x)
			require.NoError(t, err)
			assert.Equal(t, tt.end, d.Action)

			if tt.detail != nil {
				assert.Equal(t, tt.detail, d.Detail)
			}
			if tt.end == pluginapi.ActionWarn {
				assert.Equal(t, Code, d.Code)
				assert.Equal(t, DefaultMessage, d.Message)
			}
		})
	}
}

func TestStreamNilValues(t *testing.T) {
	p := newPlugin(t, `{"rules": "ACME => [co]", "on_match": "warn"}`)
	x := &pluginapi.Exchange{}
	_, err := p.OnStreamEvent(context.Background(), x, &pluginapi.StreamEvent{Kind: pluginapi.EventTextDelta, Text: "ACME"})
	require.NoError(t, err)
	d, err := p.OnStreamEnd(context.Background(), x)
	assert.NoError(t, err)
	assert.Equal(t, pluginapi.ActionAllow, d.Action)
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
		got := New().(*Plugin).Summarize(json.RawMessage(tt.cfg))
		assert.Equal(t, tt.want, got)
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
				{Seq: 1, Kind: pluginapi.EventTextDelta, Text: "ab"},
				{Seq: 2, Kind: pluginapi.EventTextDelta, Text: "aab c", Overlap: 3},
			},
			want:  []pluginapi.StreamDecision{pluginapi.Replace("aab"), pluginapi.Pass()},
			total: 1,
		},
		{
			name: "match at the window end waits until it stops growing",
			cfg:  `{"rules": "sk-[A-Z]{3,} => [k]", "mode": "regex"}`,
			events: []*pluginapi.StreamEvent{
				{Seq: 1, Kind: pluginapi.EventTextDelta, Text: "key sk-ABC"},
				{Seq: 2, Kind: pluginapi.EventTextDelta, Text: "key sk-ABCDEF", Overlap: 10},
				{Seq: 3, Kind: pluginapi.EventTextDelta, Text: "key sk-ABCDEF end", Overlap: 13},
			},
			want:  []pluginapi.StreamDecision{pluginapi.Pass(), pluginapi.Pass(), pluginapi.Replace("key [k] end")},
			total: 1,
		},
		{
			name: "held match is applied when the window closes",
			cfg:  `{"rules": "sk-[A-Z]{3,} => [k]", "mode": "regex"}`,
			events: []*pluginapi.StreamEvent{
				{Seq: 1, Kind: pluginapi.EventTextDelta, Text: "key sk-ABC"},
				{Seq: 2, Kind: pluginapi.EventTextDelta, Text: "key sk-ABC", Overlap: 10, Final: true},
			},
			want:  []pluginapi.StreamDecision{pluginapi.Pass(), pluginapi.Replace("key [k]")},
			total: 1,
		},
		{
			name: "match longer than the lookbehind is not held",
			cfg:  `{"rules": "secret => [x]", "stream_lookbehind": 4}`,
			events: []*pluginapi.StreamEvent{
				{Seq: 1, Kind: pluginapi.EventTextDelta, Text: "my secret"},
				{Seq: 2, Kind: pluginapi.EventTextDelta, Text: "[x] ok", Overlap: 3},
			},
			want:  []pluginapi.StreamDecision{pluginapi.Replace("my [x]"), pluginapi.Pass()},
			total: 1,
		},
		{
			name: "match spanning the boundary is applied",
			cfg:  `{"rules": "ab => X"}`,
			events: []*pluginapi.StreamEvent{
				{Seq: 1, Kind: pluginapi.EventTextDelta, Text: "a"},
				{Seq: 2, Kind: pluginapi.EventTextDelta, Text: "ab c", Overlap: 1},
			},
			want:  []pluginapi.StreamDecision{pluginapi.Pass(), pluginapi.Replace("X c")},
			total: 1,
		},
		{
			name: "match inside the overlap is skipped but a later one in the same window is not",
			cfg:  `{"rules": "é => e"}`,
			events: []*pluginapi.StreamEvent{
				{Seq: 1, Kind: pluginapi.EventTextDelta, Text: "é x"},
				{Seq: 2, Kind: pluginapi.EventTextDelta, Text: "e x é y", Overlap: 3},
			},
			want:  []pluginapi.StreamDecision{pluginapi.Replace("e x"), pluginapi.Replace("e x e y")},
			total: 2,
		},
		{
			name: "regex group expansion after the overlap",
			cfg:  `{"rules": "(\\d+)-(\\d+) => $2-$1", "mode": "regex"}`,
			events: []*pluginapi.StreamEvent{
				{Seq: 1, Kind: pluginapi.EventTextDelta, Text: "12-34 "},
				{Seq: 2, Kind: pluginapi.EventTextDelta, Text: "34-12 56-78 ", Overlap: 6},
			},
			want:  []pluginapi.StreamDecision{pluginapi.Replace("34-12 "), pluginapi.Replace("34-12 78-56 ")},
			total: 2,
		},
		{
			name: "warn counts only new matches",
			cfg:  `{"rules": "ACME => x", "on_match": "warn"}`,
			events: []*pluginapi.StreamEvent{
				{Seq: 1, Kind: pluginapi.EventTextDelta, Text: "ACME "},
				{Seq: 2, Kind: pluginapi.EventTextDelta, Text: "ACME ACME ", Overlap: 5},
			},
			want:  []pluginapi.StreamDecision{pluginapi.Pass(), pluginapi.Pass()},
			total: 2,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := newPlugin(t, tt.cfg)
			x := plugintest.Exchange(nil, nil)
			for i, ev := range tt.events {
				d, err := p.OnStreamEvent(context.Background(), x, ev)
				require.NoError(t, err)
				assert.Equal(t, tt.want[i].Action, d.Action)
				assert.Equal(t, tt.want[i].Text, d.Text)
			}
			got := p.streamCount(x)
			assert.Equal(t, tt.total, got)
		})
	}
}

// Block, warn and respond count matches over the same units replace edits:
// each text part and each tool-result text part on its own.
func TestOnPromptCountsPerPartLikeReplace(t *testing.T) {
	split := pluginapi.Message{ID: "m0", Role: pluginapi.RoleUser, Parts: []pluginapi.Part{
		{Kind: pluginapi.PartText, Text: "my sec"},
		{Kind: pluginapi.PartText, Text: "ret is here"},
	}}
	toolMsg := pluginapi.Message{ID: "m1", Role: pluginapi.RoleTool, ToolCallID: "c1", Parts: []pluginapi.Part{
		{Kind: pluginapi.PartToolResult, ToolResult: &pluginapi.ToolResult{CallID: "c1", Parts: []pluginapi.Part{{Kind: pluginapi.PartText, Text: "the secret result"}}}},
	}}
	newPrompt := func() *pluginapi.Prompt {
		p := &pluginapi.Prompt{Messages: []pluginapi.Message{split, toolMsg}}
		p.Reset()
		return p
	}

	t.Run("split across parts matches in no mode", func(t *testing.T) {
		block := newPlugin(t, `{"rules": "secret => x", "on_match": "block", "roles": ["user"]}`)
		d, err := block.OnPrompt(context.Background(), plugintest.Exchange(newPrompt(), nil))
		require.NoError(t, err)
		require.Equal(t, pluginapi.ActionAllow, d.Action, "block decision = %+v, %v; want allow like replace, which cannot edit across parts", d, err)

		replace := newPlugin(t, `{"rules": "secret => x", "roles": ["user"]}`)
		x := plugintest.Exchange(newPrompt(), nil)
		_, err = replace.OnPrompt(context.Background(), x)
		require.NoError(t, err)
		require.False(t, x.Prompt.Changes().Dirty)
	})
	t.Run("tool results count like they are edited", func(t *testing.T) {
		block := newPlugin(t, `{"rules": "secret => x", "on_match": "block", "roles": ["tool"]}`)
		d, err := block.OnPrompt(context.Background(), plugintest.Exchange(newPrompt(), nil))
		require.NoError(t, err)
		require.Equal(t, pluginapi.ActionBlock, d.Action)
		require.Equal(t, map[string]any{"matches": 1, "messages": 1}, d.Detail)
	})
}

// TestStreamDriver runs the plugin through plugintest's stream driver, which
// reproduces the host's lookbehind and overlap handling: a match split
// across two deltas is rewritten once and the withheld tail is not
// rewritten again when it is shown a second time.
func TestStreamDriver(t *testing.T) {
	p := newPlugin(t, `{"rules": "secret => [x]", "stream_lookbehind": 4}`)
	res, err := plugintest.RunStream(context.Background(), p, plugintest.Exchange(nil, nil), []*pluginapi.StreamEvent{
		plugintest.TextDelta("my se"), plugintest.TextDelta("cret is a secret"), plugintest.TextDelta(" here"), plugintest.Event(pluginapi.EventFinish),
	})
	require.NoError(t, err)
	assert.Equal(t, "my [x] is a [x] here", res.Text[0])
	assert.Nil(t, res.Terminated)
	detail, ok := res.End.Detail.(map[string]any)
	assert.True(t, ok)
	assert.Equal(t, 2, detail["replacements"])

	p = newPlugin(t, `{"rules": "secret => [x]", "on_match": "block", "message": "leak"}`)
	res, err = plugintest.RunStream(context.Background(), p, plugintest.Exchange(nil, nil), []*pluginapi.StreamEvent{plugintest.TextDelta("a se"), plugintest.TextDelta("cret")})
	assert.NoError(t, err)
	assert.Equal(t, pluginapi.ActionBlock, res.End.Action)
	assert.Equal(t, "leak", res.End.Message)
	assert.Empty(t, res.Text)
}

// TestStreamOpenEndedPatternMasksWholeKey streams a key in three-character
// deltas: an open-ended pattern must cover the whole key, not stop at
// whatever had arrived when the minimum length first matched.
func TestStreamOpenEndedPatternMasksWholeKey(t *testing.T) {
	p := newPlugin(t, `{"rules": "sk-[A-Za-z0-9]{20,} => [redacted]", "mode": "regex"}`)
	for _, text := range []string{"my key is sk-ABCDEFGHIJKLMNOPQRSTUV end", "sk-ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789abcdefghij and more", "last: sk-ABCDEFGHIJKLMNOPQRSTUVWXYZ"} {
		var events []*pluginapi.StreamEvent
		for i := 0; i < len(text); i += 3 {
			events = append(events, plugintest.TextDelta(text[i:min(i+3, len(text))]))
		}
		res, err := plugintest.RunStream(context.Background(), p, plugintest.Exchange(nil, nil), append(events, plugintest.Event(pluginapi.EventFinish)))
		require.NoError(t, err)
		got := res.Text[0]
		assert.NotContains(t, got, "ABC")
		assert.NotContains(t, got, "UV")
		assert.Contains(t, got, "[redacted]", "%q streamed as %q", text, got)
	}
}
