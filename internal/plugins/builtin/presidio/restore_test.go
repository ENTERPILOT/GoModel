package presidio

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/enterpilot/gomodel/pluginapi"
	"github.com/enterpilot/gomodel/pluginapi/plugintest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPlaceholderFormatErrors(t *testing.T) {
	tests := []struct {
		name   string
		format string
		want   string
	}{
		{"no entity", "<{n}>", "must contain {entity} and {n} once each"},
		{"twice", "<{entity}_{n}_{n}>", "must contain {entity} and {n} once each"},
		{"letter edge", "P{entity}_{n}>", "must start and end with a punctuation character"},
		{"token edge", "{entity}_{n}", "must start and end with a punctuation character"},
		{"backslash", `<{entity}\{n}>`, "printable ASCII"},
		{"non-ascii", "«{entity}_{n}»", "printable ASCII"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parsePlaceholderFormat(tt.format)
			require.ErrorContains(t, err, tt.want)
		})
	}
	err := New().Init(context.Background(), json.RawMessage(`{"placeholder_format": "{entity}"}`), plugintest.NewHost())
	require.ErrorContains(t, err, "placeholder_format must contain")
}

// Models and renderers change how a placeholder is spelled; restore still
// finds it, and never a placeholder of another number.
func TestRestoreFindsPlaceholderVariants(t *testing.T) {
	m := defaultMapping(t)
	m.placeholder("PERSON", "Ann", true)
	m.placeholder("EMAIL_ADDRESS", `a"b@x.io`, true)

	tests := []struct {
		name string
		in   string
		want string
	}{
		{"exact", "Hi <PERSON_1>.", "Hi Ann."},
		{"case", "Hi <Person_1> and <person_1>.", "Hi Ann and Ann."},
		{"markdown", `Hi \<PERSON\_1\> at \<EMAIL\_ADDRESS\_1\>.`, `Hi Ann at a"b@x.io.`},
		{"html", "Hi &lt;PERSON_1&gt;.", "Hi Ann."},
		{"other number", "Hi <PERSON_12> and <PERSON_2>.", "Hi <PERSON_12> and <PERSON_2>."},
		{"not a placeholder", "a < b and c > d", "a < b and c > d"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _ := m.restore(tt.in)
			assert.Equal(t, tt.want, got)
		})
	}

	// In raw JSON: \u-escaped and Markdown-escaped punctuation, values
	// written JSON-escaped.
	got, n := m.restoreJSON(`{"a":"<PERSON_1>","b":"\\<EMAIL_ADDRESS_1\\>","c":"&lt;person_1&gt;"}`)
	assert.Equal(t, `{"a":"Ann","b":"a\"b@x.io","c":"Ann"}`, got)
	assert.Equal(t, 3, n)
}

func TestCustomPlaceholderFormat(t *testing.T) {
	a := newAnalyzer(t, "Ann Lee")
	in := newPlugin(t, a, `{"restore": true, "placeholder_format": "[{entity}_{n}]"}`)
	out := newPlugin(t, a, `{"restore": true}`)
	x := plugintest.Exchange(plugintest.Prompt(plugintest.Text(pluginapi.RoleUser, "m1", "I am Ann Lee, not [PERSON_1].")), nil)
	_, err := in.OnPrompt(context.Background(), x)
	require.NoError(t, err)
	got := x.Prompt.Message("m1").Text()
	require.Equal(t, "I am [PERSON_2], not [PERSON_1].", got)

	// The response instance uses the request's table, in its format.
	x.Response = plugintest.Completion(`Hello \[PERSON\_2\] and [PERSON_1], not <PERSON_2>.`)
	_, err = out.OnResponse(context.Background(), x)
	require.NoError(t, err)
	assert.Equal(t, "Hello Ann Lee and [PERSON_1], not <PERSON_2>.", x.Response.Text(0))
}

// assistantTurn is a replayed assistant message carrying reasoning.
func assistantTurn(id, reasoning, text string) pluginapi.Message {
	return pluginapi.Message{ID: id, Role: pluginapi.RoleAssistant, Parts: []pluginapi.Part{
		{Kind: pluginapi.PartReasoning, Text: reasoning},
		{Kind: pluginapi.PartText, Text: text},
	}}
}

func TestRestoreReasoning(t *testing.T) {
	a := newAnalyzer(t, "Ann Lee", "Bob Ray")
	in := newPlugin(t, a, `{"restore": true}`)
	out := newPlugin(t, a, `{"restore": true}`)
	x := plugintest.Exchange(plugintest.Prompt(
		plugintest.Text(pluginapi.RoleUser, "m1", "I am Ann Lee."),
		// The client sends the restored reasoning of the last turn back.
		assistantTurn("m2", "The user is Ann Lee; Bob Ray is new.", "Hello Ann Lee."),
	), nil)
	_, err := in.OnPrompt(context.Background(), x)
	require.NoError(t, err)
	got := x.Prompt.Message("m2").Parts[0].Text
	assert.Equal(t, "The user is <PERSON_1>; <PERSON_2> is new.", got, "replayed reasoning is anonymized")
	assert.Equal(t, "Hello <PERSON_1>.", x.Prompt.Message("m2").Text())

	x.Response = plugintest.Completion("Hi <PERSON_1>.")
	x.Response.Choices[0].Message.Parts = append([]pluginapi.Part{{Kind: pluginapi.PartReasoning, Text: "Greet <Person_1>, not bob@y.io."}}, x.Response.Choices[0].Message.Parts...)
	before := len(a.texts())
	d, err := out.OnResponse(context.Background(), x)
	require.NoError(t, err)
	assert.Equal(t, "Greet Ann Lee, not bob@y.io.", x.Response.Choices[0].Message.Parts[0].Text)
	assert.Equal(t, "Hi Ann Lee.", x.Response.Text(0))
	assert.Equal(t, []string{"Hi <PERSON_1>."}, a.texts()[before:], "reasoning is restored, not analyzed")
	assert.Equal(t, 2, d.Detail.(map[string]any)["restored"])

	// Without restore, response reasoning is left alone.
	y := plugintest.Exchange(nil, &pluginapi.Completion{Choices: []pluginapi.Choice{{Message: assistantTurn("c", "<PERSON_1>", "")}}})
	_, err = newPlugin(t, a, `{}`).OnResponse(context.Background(), y)
	require.NoError(t, err)
	assert.Equal(t, "<PERSON_1>", y.Response.Choices[0].Message.Parts[0].Text)
}

func TestStreamRestoresReasoning(t *testing.T) {
	a := newAnalyzer(t, "Ann Lee")
	in := newPlugin(t, a, `{"restore": true}`)
	out := newPlugin(t, a, `{"restore": true, "stream_lookbehind": 12, "stream_chunk": 0}`)
	x := plugintest.Exchange(plugintest.Prompt(plugintest.Text(pluginapi.RoleUser, "m1", "I am Ann Lee.")), nil)
	_, err := in.OnPrompt(context.Background(), x)
	require.NoError(t, err)

	before := len(a.texts())
	res, err := plugintest.RunStream(context.Background(), out, x, []*pluginapi.StreamEvent{
		// A placeholder split across reasoning deltas, then the answer.
		{Kind: pluginapi.EventReasoningDelta, Text: "The user is <PER"},
		{Kind: pluginapi.EventReasoningDelta, Text: "SON_1>, greet <PERSON"},
		{Kind: pluginapi.EventReasoningDelta, Text: "_1>"},
		plugintest.TextDelta("Hi <PERSON_1>"),
		plugintest.TextDelta("!"),
	})
	require.NoError(t, err)
	assert.Equal(t, "The user is Ann Lee, greet Ann Lee", res.Reasoning[0])
	assert.Equal(t, "Hi Ann Lee!", res.Text[0])
	for _, text := range a.texts()[before:] {
		assert.NotContains(t, text, "user is", "reasoning was sent to the analyzer")
	}
}

func TestRestoreTools(t *testing.T) {
	tests := []struct {
		name string
		cfg  string
		want map[string]string // tool name -> arguments after restore
	}{
		{"all by default", `{"restore": true}`, map[string]string{"search": `{"q":"Ann Lee"}`, "ask": `{"q":"Ann Lee"}`}},
		{"allow list", `{"restore": true, "restore_tools": ["ask"]}`, map[string]string{"search": `{"q":"<PERSON_1>"}`, "ask": `{"q":"Ann Lee"}`}},
		{"exclude", `{"restore": true, "restore_tools_exclude": "search"}`, map[string]string{"search": `{"q":"<PERSON_1>"}`, "ask": `{"q":"Ann Lee"}`}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := newAnalyzer(t, "Ann Lee")
			p := newPlugin(t, a, tt.cfg)
			x := plugintest.Exchange(plugintest.Prompt(plugintest.Text(pluginapi.RoleUser, "m1", "Find Ann Lee.")), nil)
			_, err := p.OnPrompt(context.Background(), x)
			require.NoError(t, err)

			x.Response = plugintest.Completion("")
			for i, name := range []string{"search", "ask"} {
				x.Response.Choices[0].Message.Parts = append(x.Response.Choices[0].Message.Parts, pluginapi.Part{Kind: pluginapi.PartToolCall, ToolCall: &pluginapi.ToolCall{ID: string(rune('a' + i)), Name: name, Arguments: json.RawMessage(`{"q":"<PERSON_1>"}`)}})
			}
			_, err = p.OnResponse(context.Background(), x)
			require.NoError(t, err)
			for _, part := range x.Response.Choices[0].Message.Parts[1:] {
				assert.Equal(t, tt.want[part.ToolCall.Name], string(part.ToolCall.Arguments), "tool %s", part.ToolCall.Name)
			}

			// The stream names the tool of each argument window; a call it
			// has not named counts as unknown.
			res, err := plugintest.RunStream(context.Background(), p, x, []*pluginapi.StreamEvent{
				{Kind: pluginapi.EventToolCallDelta, Call: 0, Tool: "search", Text: `{"q":"<PERSON_1>"}`},
				{Kind: pluginapi.EventToolCallDelta, Call: 1, Tool: "ask", Text: `{"q":"<PERSON_1>"}`},
			})
			require.NoError(t, err)
			assert.Equal(t, tt.want["search"], res.ToolArguments[0][0])
			assert.Equal(t, tt.want["ask"], res.ToolArguments[0][1])
		})
	}
}

func TestStreamUnnamedToolKeepsPlaceholdersUnderAllowList(t *testing.T) {
	a := newAnalyzer(t, "Ann Lee")
	p := newPlugin(t, a, `{"restore": true, "restore_tools": ["ask"]}`)
	x := plugintest.Exchange(plugintest.Prompt(plugintest.Text(pluginapi.RoleUser, "m1", "Find Ann Lee.")), nil)
	_, err := p.OnPrompt(context.Background(), x)
	require.NoError(t, err)
	res, err := plugintest.RunStream(context.Background(), p, x, []*pluginapi.StreamEvent{
		{Kind: pluginapi.EventToolCallDelta, Text: `{"q":"<PERSON_1>"}`},
	})
	require.NoError(t, err)
	assert.Equal(t, `{"q":"<PERSON_1>"}`, res.ToolArguments[0][0])
}

func TestRestoreRoles(t *testing.T) {
	a := newAnalyzer(t, "Ann Lee", "Sam Ops")
	p := newPlugin(t, a, `{"restore": true, "roles": ["system", "user"], "restore_roles": ["system", "user"]}`)
	x := plugintest.Exchange(plugintest.Prompt(
		plugintest.Text(pluginapi.RoleSystem, "m0", "The user's name is Ann Lee."),
		plugintest.Text(pluginapi.RoleUser, "m1", "Ask Sam Ops."),
	), nil)
	_, err := p.OnPrompt(context.Background(), x)
	require.NoError(t, err)
	require.Equal(t, "The user's name is <PERSON_1>.", x.Prompt.Message("m0").Text())

	x.Response = plugintest.Completion("Hello <PERSON_1>, I asked <PERSON_2>.")
	_, err = p.OnResponse(context.Background(), x)
	require.NoError(t, err)
	assert.Equal(t, "Hello Ann Lee, I asked Sam Ops.", x.Response.Text(0))

	// Leaving a role out of restore_roles keeps its placeholders.
	q := newPlugin(t, a, `{"restore": true, "restore_roles": ["assistant"]}`)
	y := plugintest.Exchange(plugintest.Prompt(plugintest.Text(pluginapi.RoleUser, "m1", "I am Ann Lee.")), nil)
	_, err = q.OnPrompt(context.Background(), y)
	require.NoError(t, err)
	y.Response = plugintest.Completion("Hello <PERSON_1>.")
	_, err = q.OnResponse(context.Background(), y)
	require.NoError(t, err)
	assert.Equal(t, "Hello <PERSON_1>.", y.Response.Text(0))
}

func TestRestoreDefaults(t *testing.T) {
	p := newPlugin(t, nil, `{}`)
	assert.Equal(t, map[pluginapi.Role]bool{pluginapi.RoleUser: true, pluginapi.RoleAssistant: true, pluginapi.RoleTool: true}, p.restoreRoles)
	assert.Equal(t, DefaultPlaceholderFormat, p.placeholders.format)
	assert.Nil(t, p.restoreTools)
	assert.Nil(t, p.keepTools)
	assert.True(t, p.restoresTool("anything"))
	assert.True(t, p.restoresTool(""))
}
