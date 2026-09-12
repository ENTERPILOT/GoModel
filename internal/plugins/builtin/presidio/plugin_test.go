package presidio

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"unicode/utf8"

	"github.com/enterpilot/gomodel/pluginapi"
	"github.com/enterpilot/gomodel/pluginapi/plugintest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// analyzer is a fake Presidio analyzer: it finds e-mail addresses, credit
// card numbers, and the names listed in persons, reporting code-point
// offsets like the real one. It records every request body.
type analyzer struct {
	srv      *httptest.Server
	mu       sync.Mutex
	requests []analyzerRequest
	status   int
	persons  []string
	auth     string
}

var (
	emailRe = regexp.MustCompile(`[a-z0-9.]+@[a-z0-9.]+\.[a-z]+`)
	cardRe  = regexp.MustCompile(`\b4[0-9]{15}\b`)
)

func newAnalyzer(t *testing.T, persons ...string) *analyzer {
	t.Helper()
	a := &analyzer{persons: persons, status: http.StatusOK}
	a.srv = httptest.NewServer(http.HandlerFunc(a.handle))
	t.Cleanup(a.srv.Close)
	return a
}

func (a *analyzer) handle(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if r.URL.Path == "/supportedentities" {
		if r.URL.Query().Get("language") != "en" {
			http.Error(w, `{"error":"No matching recognizers were found to serve the request."}`, http.StatusInternalServerError)
			return
		}
		_, _ = io.WriteString(w, `["PERSON","EMAIL_ADDRESS","CREDIT_CARD"]`)
		return
	}
	a.auth = r.Header.Get("Authorization")
	var req analyzerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	a.requests = append(a.requests, req)
	if a.status != http.StatusOK {
		http.Error(w, `{"error":"the text: `+req.Text+`"}`, a.status)
		return
	}
	results := []analyzerResult{}
	add := func(entity string, loc []int, score float64) {
		start := utf8.RuneCountInString(req.Text[:loc[0]])
		end := start + utf8.RuneCountInString(req.Text[loc[0]:loc[1]])
		results = append(results, analyzerResult{EntityType: entity, Start: start, End: end, Score: score})
	}
	for _, loc := range emailRe.FindAllStringIndex(req.Text, -1) {
		add("EMAIL_ADDRESS", loc, 1)
	}
	for _, loc := range cardRe.FindAllStringIndex(req.Text, -1) {
		add("CREDIT_CARD", loc, 1)
	}
	for _, name := range a.persons {
		for i := 0; ; {
			idx := strings.Index(req.Text[i:], name)
			if idx < 0 {
				break
			}
			add("PERSON", []int{i + idx, i + idx + len(name)}, 0.85)
			i += idx + len(name)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(results)
}

func (a *analyzer) texts() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []string
	for _, r := range a.requests {
		out = append(out, r.Text)
	}
	return out
}

func newPlugin(t *testing.T, a *analyzer, cfg string) *Plugin {
	t.Helper()
	if cfg == "" {
		cfg = "{}"
	}
	var m map[string]any
	err := json.Unmarshal([]byte(cfg), &m)
	require.NoError(t, err)

	if _, ok := m["analyzer_url"]; !ok && a != nil {
		m["analyzer_url"] = a.srv.URL
	}
	raw, _ := json.Marshal(m)
	p := New()
	err = p.Init(context.Background(), raw, plugintest.NewHost())
	require.NoError(t, err)

	return p.(*Plugin)
}

// A chained Responses request replays its stored history through the prompt
// phase only when an instance edits content, so an instance that only flags
// or blocks must say so.
func TestEditsContent(t *testing.T) {
	tests := []struct {
		name string
		cfg  string
		want bool
	}{
		{name: "default anonymizes", cfg: `{}`, want: true},
		{name: "warn only flags", cfg: `{"action":"warn"}`},
		{name: "block only rejects", cfg: `{"action":"block"}`},
		{name: "restore still edits", cfg: `{"action":"warn","restore":true}`, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := newPlugin(t, nil, tt.cfg).EditsContent()
			require.Equal(t, tt.want, got)
		})
	}
}

func TestManifest(t *testing.T) {
	m := New().Manifest()
	require.Equal(t, "presidio", m.Name)
	require.True(t, m.Mutates)
	require.True(t, m.Guardrail)
	assert.Equal(t, []pluginapi.Kind{pluginapi.KindPrompt, pluginapi.KindResponse, pluginapi.KindStream}, m.Kinds)

	want := []string{"analyzer_url", "api_key", "language", "entities", "block_entities", "score_threshold", "allow_list", "ad_hoc_recognizers", "roles", "action", "operator", "restore", "message", "block_status", "stream_chunk", "stream_lookbehind"}
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

	p := New()
	for name, ok := range map[string]bool{
		"PromptHook":    func() bool { _, ok := p.(pluginapi.PromptHook); return ok }(),
		"ResponseHook":  func() bool { _, ok := p.(pluginapi.ResponseHook); return ok }(),
		"StreamHook":    func() bool { _, ok := p.(pluginapi.StreamHook); return ok }(),
		"HealthChecker": func() bool { _, ok := p.(pluginapi.HealthChecker); return ok }(),
	} {
		assert.True(t, ok, "must implement %s", name)
	}
}

func TestInitErrors(t *testing.T) {
	tests := []struct {
		name string
		cfg  string
		want string
	}{
		{"unknown key", `{"bogus": 1}`, `unknown field "bogus"`},
		{"url scheme", `{"analyzer_url": "localhost:5002"}`, "analyzer_url must start with http"},
		{"url type", `{"analyzer_url": 5}`, "analyzer_url must be a string"},
		{"bad action", `{"action": "drop"}`, "action must be one of anonymize, block, respond, warn"},
		{"bad operator", `{"operator": "encrypt"}`, "operator must be one of replace, mask, redact, hash"},
		{"restore needs replace", `{"restore": true, "operator": "mask"}`, "restore needs operator replace"},
		{"bad role", `{"roles": ["robot"]}`, "unknown role"},
		{"threshold high", `{"score_threshold": 2}`, "score_threshold must be between 0 and 1"},
		{"threshold type", `{"score_threshold": "abc"}`, "score_threshold must be a number"},
		{"status low", `{"block_status": 302}`, "block_status must be an HTTP status between 400 and 599"},
		{"recognizers not array", `{"ad_hoc_recognizers": "{\"a\":1}"}`, "ad_hoc_recognizers must be a JSON array"},
		{"entities type", `{"entities": 5}`, "entities must be a list of strings"},
		{"chunk high", `{"stream_chunk": 100000}`, "stream_chunk must be between 0 and 16384"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := New().Init(context.Background(), json.RawMessage(tt.cfg), plugintest.NewHost())
			require.ErrorContains(t, err, tt.want)
		})
	}
}

func TestDefaults(t *testing.T) {
	p := newPlugin(t, nil, `{}`)
	assert.Equal(t, DefaultAnalyzerURL, p.analyzerURL)
	assert.Equal(t, "en", p.language)
	assert.Equal(t, ActionAnonymize, p.action)
	assert.Equal(t, OperatorReplace, p.operator)
	assert.False(t, p.restore)
	assert.Equal(t, DefaultMessage, p.enforcement.Message)
	assert.Equal(t, DefaultStreamChunk, p.streamChunk)
	assert.Equal(t, DefaultStreamLookbehind, p.lookbehind)
	assert.Nil(t, p.entities)
	assert.Nil(t, p.scoreThreshold)
	assert.Nil(t, p.allowList)
	assert.Nil(t, p.adHocRecognizers)
	assert.Nil(t, p.blockEntities, "optional settings not nil: %+v", p.settings)

	wantRoles := map[pluginapi.Role]bool{pluginapi.RoleUser: true, pluginapi.RoleAssistant: true, pluginapi.RoleTool: true}
	assert.Equal(t, wantRoles, p.roles)

	// Lists as text, threshold as string, block entities merged into
	// entities, trailing slash and case normalized.
	p = newPlugin(t, nil, `{"analyzer_url": "http://presidio:3000/", "entities": "person, email_address\n", "block_entities": ["CREDIT_CARD"], "score_threshold": "0.4", "roles": "system", "restore": "yes", "ad_hoc_recognizers": "[{\"supported_entity\": \"ZIP\"}]"}`)
	assert.Equal(t, "http://presidio:3000", p.analyzerURL)
	assert.Equal(t, 0.4, *p.scoreThreshold)
	assert.True(t, p.restore)
	assert.Equal(t, []string{"PERSON", "EMAIL_ADDRESS", "CREDIT_CARD"}, p.entities)
	assert.True(t, p.blockEntities["CREDIT_CARD"], "entities = %v, blocked = %v", p.entities, p.blockEntities)

	assert.True(t, p.roles[pluginapi.RoleSystem])
	assert.True(t, p.roles[pluginapi.RoleDeveloper])
	assert.False(t, p.roles[pluginapi.RoleUser])
	assert.Equal(t, `[{"supported_entity":"ZIP"}]`, string(p.adHocRecognizers), "recognizers = %s", p.adHocRecognizers)
}

func TestOnPromptAnonymizes(t *testing.T) {
	a := newAnalyzer(t, "John Smith")
	p := newPlugin(t, a, `{"api_key": "tok", "entities": ["PERSON", "EMAIL_ADDRESS"], "score_threshold": 0.3, "allow_list": ["ACME"]}`)
	x := plugintest.Exchange(plugintest.Prompt(
		plugintest.Text(pluginapi.RoleSystem, "m0", "Reply to john@acme.com politely."),
		plugintest.Text(pluginapi.RoleUser, "m1", "I am John Smith, mail john@acme.com. My friend is also John Smith."),
		plugintest.Text(pluginapi.RoleAssistant, "m2", "Hello John Smith"),
		plugintest.Text(pluginapi.RoleUser, "m3", "   "),
	), nil)
	d, err := p.OnPrompt(context.Background(), x)
	require.NoError(t, err)
	require.Equal(t, pluginapi.ActionAllow, d.Action)
	require.False(t, d.NoStore)
	got := x.Prompt.Message("m1").Text()
	assert.Equal(t, "I am <PERSON_1>, mail <EMAIL_ADDRESS_1>. My friend is also <PERSON_1>.", got)
	got = x.Prompt.Message("m2").Text()
	assert.Equal(t, "Hello <PERSON_1>", got)
	got = x.Prompt.Message("m0").Text()
	assert.Contains(t, got, "john@acme.com")

	detail := d.Detail.(map[string]any)
	assert.Equal(t, map[string]int{"PERSON": 3, "EMAIL_ADDRESS": 1}, detail["entities"])
	assert.Equal(t, 2, detail["messages"])
	assert.Equal(t, 4, detail["replacements"], "detail = %v", detail)

	texts := a.texts()
	require.Len(t, texts, 2)

	req := a.requests[0]
	assert.Equal(t, "en", req.Language)
	assert.Equal(t, []string{"PERSON", "EMAIL_ADDRESS"}, req.Entities)
	require.NotNil(t, req.ScoreThreshold)
	assert.Equal(t, 0.3, *req.ScoreThreshold)
	assert.Equal(t, []string{"ACME"}, req.AllowList)
	assert.Equal(t, "test-request", req.CorrelationID)
	assert.Equal(t, "Bearer tok", a.auth)
}

func TestOnPromptOperators(t *testing.T) {
	a := newAnalyzer(t, "Zoë")
	for _, tt := range []struct{ operator, want string }{
		{OperatorMask, "Hi ***, mail ***************"},
		{OperatorRedact, "Hi , mail "},
		{OperatorHash, "Hi 8be13b5f5b46f0ffe89b4a6ec43ed6cab28c2b6ac5bc4df1a7ee9c3ffd0a6db6, mail 2ab0e5ff9e6acd6de6fc2d14ff0dc5a4d2a5c6f8fb0a66a45c9ac9a1a2b9e82b"},
	} {
		p := newPlugin(t, a, `{"operator": "`+tt.operator+`"}`)
		x := plugintest.Exchange(plugintest.Prompt(plugintest.Text(pluginapi.RoleUser, "m1", "Hi Zoë, mail zoe@example.org")), nil)
		_, err := p.OnPrompt(context.Background(), x)
		require.NoError(t, err)

		got := x.Prompt.Message("m1").Text()
		if tt.operator == OperatorHash {
			assert.True(t, regexp.MustCompile(`^Hi [0-9a-f]{64}, mail [0-9a-f]{64}$`).MatchString(got), "%s: %q", tt.operator, got)
			continue
		}
		assert.Equal(t, tt.want, got, "%s: %q, want %q", tt.operator, got, tt.want)
	}
}

func TestOnPromptDecisions(t *testing.T) {
	a := newAnalyzer(t, "Ann")
	tests := []struct {
		name   string
		cfg    string
		text   string
		action pluginapi.Action
		code   string
		status int
		edited bool
	}{
		{"block", `{"action": "block", "block_status": 451}`, "Ann is here", pluginapi.ActionBlock, Code, 451, false},
		{"respond", `{"action": "respond"}`, "Ann is here", pluginapi.ActionRespond, Code, 0, false},
		{"warn", `{"action": "warn"}`, "Ann is here", pluginapi.ActionWarn, Code, 0, false},
		{"warn nothing found", `{"action": "warn"}`, "nobody is here", pluginapi.ActionAllow, "", 0, false},
		{"anonymize with blocking entity", `{"block_entities": ["CREDIT_CARD"]}`, "Ann pays with 4111111111111111", pluginapi.ActionBlock, CodeBlocked, 0, false},
		{"respond with blocking entity", `{"action": "respond", "block_entities": ["credit_card"]}`, "card 4111111111111111", pluginapi.ActionRespond, CodeBlocked, 0, false},
		{"warn with blocking entity", `{"action": "warn", "block_entities": ["CREDIT_CARD"]}`, "card 4111111111111111", pluginapi.ActionBlock, CodeBlocked, 0, false},
		{"anonymize", `{}`, "Ann is here", pluginapi.ActionAllow, "", 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := newPlugin(t, a, tt.cfg)
			x := plugintest.Exchange(plugintest.Prompt(plugintest.Text(pluginapi.RoleUser, "m1", tt.text)), nil)
			d, err := p.OnPrompt(context.Background(), x)
			require.NoError(t, err)
			require.Equal(t, tt.action, d.Action)
			require.Equal(t, tt.code, d.Code)
			require.Equal(t, tt.status, d.Status)
			edited := x.Prompt.Message("m1").Text() != tt.text
			assert.Equal(t, tt.edited, edited)

			if d.Action == pluginapi.ActionRespond {
				assert.Equal(t, DefaultMessage, d.Response.Text(0))
			}
			if tt.code == CodeBlocked {
				assert.Equal(t, "CREDIT_CARD", d.Detail.(map[string]any)["blocked_entity"])
			}
		})
	}
}

func TestOnPromptToolCallsAndResults(t *testing.T) {
	a := newAnalyzer(t, "Ann")
	p := newPlugin(t, a, `{}`)
	call := pluginapi.Message{ID: "m1", Role: pluginapi.RoleAssistant, Parts: []pluginapi.Part{
		{Kind: pluginapi.PartText, Text: "Looking up Ann"},
		{Kind: pluginapi.PartToolCall, ToolCall: &pluginapi.ToolCall{ID: "c1", Name: "lookup", Arguments: json.RawMessage(`{"name": "Ann", "tags": ["vip", "ann@x.io"], "n": 1}`)}},
	}}
	result := pluginapi.Message{ID: "m2", Role: pluginapi.RoleTool, Parts: []pluginapi.Part{
		{Kind: pluginapi.PartToolResult, ToolResult: &pluginapi.ToolResult{CallID: "c1", Parts: []pluginapi.Part{{Kind: pluginapi.PartText, Text: "Ann <ann@x.io>"}}}},
	}}
	x := plugintest.Exchange(plugintest.Prompt(call, result), nil)
	_, err := p.OnPrompt(context.Background(), x)
	require.NoError(t, err)
	got := x.Prompt.Message("m1").Text()
	assert.Equal(t, "Looking up <PERSON_1>", got)
	got = string(x.Prompt.ToolCalls()[0].Call.Arguments)
	assert.Equal(t, `{"n":1,"name":"<PERSON_1>","tags":["vip","<EMAIL_ADDRESS_1>"]}`, got)
	got = x.Prompt.Message("m2").Parts[0].ToolResult.Parts[0].Text
	assert.Equal(t, "<PERSON_1> <<EMAIL_ADDRESS_1>>", got)

	// Strings inside the arguments are analyzed one by one, never the JSON.
	for _, s := range a.texts() {
		assert.NotContains(t, s, "{", "analyzer saw JSON")
	}
}

func TestOnPromptAnalyzerErrorsFailWithoutEditing(t *testing.T) {
	a := newAnalyzer(t, "Ann")
	a.status = http.StatusInternalServerError
	p := newPlugin(t, a, `{}`)
	x := plugintest.Exchange(plugintest.Prompt(plugintest.Text(pluginapi.RoleUser, "m1", "Ann is here")), nil)
	_, err := p.OnPrompt(context.Background(), x)
	require.ErrorContains(t, err, "analyzer returned HTTP 500")
	assert.NotContains(t, err.Error(), "Ann")
	assert.Equal(t, "Ann is here", x.Prompt.Message("m1").Text())
	assert.False(t, x.Prompt.Changes().Dirty)

	p = newPlugin(t, nil, `{"analyzer_url": "http://127.0.0.1:1"}`)
	_, err = p.OnPrompt(context.Background(), x)
	require.ErrorContains(t, err, "analyzer unreachable")
}

func TestRestoreRoundTrip(t *testing.T) {
	a := newAnalyzer(t, "Ann Lee")
	in := newPlugin(t, a, `{"restore": true}`)
	out := newPlugin(t, a, `{"restore": true}`)
	x := plugintest.Exchange(plugintest.Prompt(plugintest.Text(pluginapi.RoleUser, "m1", "I am Ann Lee (ann@x.io). Draft an email to Bob.")), nil)
	d, err := in.OnPrompt(context.Background(), x)
	require.NoError(t, err)
	assert.True(t, d.NoStore, "prompt decision = %+v, want NoStore", d)
	got := x.Prompt.Message("m1").Text()
	require.Equal(t, "I am <PERSON_1> (<EMAIL_ADDRESS_1>). Draft an email to Bob.", got)

	// The model repeats the placeholders, reveals a new address of its
	// own, and calls a tool with a placeholder argument.
	x.Response = plugintest.Completion("Dear Bob, <PERSON_1> (<EMAIL_ADDRESS_1>) wrote; cc bob@y.io.")
	x.Response.Choices[0].Message.Parts = append(x.Response.Choices[0].Message.Parts, pluginapi.Part{Kind: pluginapi.PartToolCall, ToolCall: &pluginapi.ToolCall{ID: "c1", Name: "send", Arguments: json.RawMessage(`{"to":"<EMAIL_ADDRESS_1>"}`)}})
	d, err = out.OnResponse(context.Background(), x)
	require.NoError(t, err)
	require.Equal(t, pluginapi.ActionAllow, d.Action)
	got = x.Response.Text(0)
	assert.Equal(t, "Dear Bob, Ann Lee (ann@x.io) wrote; cc <EMAIL_ADDRESS_2>.", got)
	got = string(x.Response.Choices[0].Message.Parts[1].ToolCall.Arguments)
	assert.Equal(t, `{"to":"ann@x.io"}`, got)

	detail := d.Detail.(map[string]any)
	assert.Equal(t, 3, detail["restored"])
	assert.Equal(t, 1, detail["replacements"], "detail = %v", detail)

	// Without a prompt-phase mapping there is nothing to restore and the
	// model's own values are still anonymized.
	y := plugintest.Exchange(nil, plugintest.Completion("Write to <PERSON_1> at bob@y.io"))
	_, err = out.OnResponse(context.Background(), y)
	require.NoError(t, err)
	got = y.Response.Text(0)
	assert.Equal(t, "Write to <PERSON_1> at <EMAIL_ADDRESS_1>", got)
}

func TestOnResponseDecisions(t *testing.T) {
	a := newAnalyzer(t, "Ann")
	p := newPlugin(t, a, `{"action": "block", "block_entities": ["EMAIL_ADDRESS"]}`)
	x := plugintest.Exchange(nil, plugintest.Completion("clean", "Ann was here"))
	d, err := p.OnResponse(context.Background(), x)
	require.NoError(t, err)
	assert.Equal(t, pluginapi.ActionBlock, d.Action)
	assert.Equal(t, Code, d.Code)
	assert.Equal(t, 0, d.Status)

	x = plugintest.Exchange(nil, plugintest.Completion("mail ann@x.io"))
	d, _ = p.OnResponse(context.Background(), x)
	assert.Equal(t, CodeBlocked, d.Code)

	p = newPlugin(t, a, `{"action": "warn"}`)
	x = plugintest.Exchange(nil, plugintest.Completion("Ann was here"))
	d, _ = p.OnResponse(context.Background(), x)
	assert.Equal(t, pluginapi.ActionWarn, d.Action)
	assert.Equal(t, "Ann was here", x.Response.Text(0), "decision = %+v, text %q", d, x.Response.Text(0))
}

func TestStreamPolicy(t *testing.T) {
	p := newPlugin(t, nil, `{"stream_chunk": 100, "stream_lookbehind": 20}`)
	got := p.StreamPolicy()
	assert.Equal(t, pluginapi.StreamPolicy{Mode: pluginapi.StreamTransform, LookbehindChars: 20, MinChunkChars: 100}, got)

	for _, action := range []string{ActionBlock, ActionRespond} {
		p = newPlugin(t, nil, `{"action": "`+action+`"}`)
		got := p.StreamPolicy()
		assert.Equal(t, pluginapi.StreamBuffer, got.Mode, "%s policy = %+v", action, got)
	}
}

func TestStreamEvents(t *testing.T) {
	a := newAnalyzer(t, "Ann Lee")
	in := newPlugin(t, a, `{"restore": true}`)
	p := newPlugin(t, a, `{"restore": true, "block_entities": ["CREDIT_CARD"]}`)
	x := plugintest.Exchange(plugintest.Prompt(plugintest.Text(pluginapi.RoleUser, "m1", "I am Ann Lee")), nil)
	_, err := in.OnPrompt(context.Background(), x)
	require.NoError(t, err)

	ev := func(s string, overlap int) *pluginapi.StreamEvent {
		return &pluginapi.StreamEvent{Kind: pluginapi.EventTextDelta, Text: s, Overlap: overlap}
	}
	steps := []struct {
		ev   *pluginapi.StreamEvent
		want pluginapi.StreamDecision
	}{
		{ev("Hello <PERSON_1>, ", 0), pluginapi.Replace("Hello Ann Lee, ")},
		// The tail comes back restored; the new text has a value.
		{ev("Ann Lee, your friend bob@y.io", 8), pluginapi.Replace("Ann Lee, your friend <EMAIL_ADDRESS_1>")},
		// A value that ends inside the overlap was handled last time.
		{ev("<EMAIL_ADDRESS_1> is fine", 17), pluginapi.Pass()},
		{&pluginapi.StreamEvent{Kind: pluginapi.EventFinish}, pluginapi.Pass()},
		{ev("card 4111111111111111", 0), pluginapi.Terminate(pluginapi.Decision{})},
	}
	for i, st := range steps {
		got, err := p.OnStreamEvent(context.Background(), x, st.ev)
		require.NoError(t, err, "step %d: %v", i, err)
		assert.Equal(t, st.want.Action, got.Action)
		assert.Equal(t, st.want.Text, got.Text, "step %d: %+v, want %+v", i, got, st.want)

		if got.Action == pluginapi.StreamTerminate {
			require.NotNil(t, got.Terminate, "step %d", i)
			assert.Equal(t, CodeBlocked, got.Terminate.Code, "step %d", i)
		}
	}
	// The end decision repeats what the stream contained, the cut included.
	d, err := p.OnStreamEnd(context.Background(), x)
	require.NoError(t, err)

	detail := d.Detail.(map[string]any)
	assert.Equal(t, pluginapi.ActionBlock, d.Action)
	assert.Equal(t, CodeBlocked, d.Code)
	assert.Equal(t, 1, detail["restored"])
	assert.Equal(t, 2, detail["replacements"])
	assert.Equal(t, map[string]int{"EMAIL_ADDRESS": 1, "CREDIT_CARD": 1}, detail["entities"])
	// A clean stream ends with a plain allow.
	d, _ = p.OnStreamEnd(context.Background(), plugintest.Exchange(nil, nil))
	assert.Equal(t, pluginapi.ActionAllow, d.Action)
	assert.Nil(t, d.Detail)
}

func TestStreamWarn(t *testing.T) {
	a := newAnalyzer(t, "Ann")
	p := newPlugin(t, a, `{"action": "warn"}`)
	x := plugintest.Exchange(nil, nil)
	got, err := p.OnStreamEvent(context.Background(), x, &pluginapi.StreamEvent{Kind: pluginapi.EventTextDelta, Text: "Ann"})
	require.NoError(t, err)
	require.Equal(t, pluginapi.StreamPass, got.Action)

	d, _ := p.OnStreamEnd(context.Background(), x)
	assert.Equal(t, pluginapi.ActionWarn, d.Action)
	assert.Equal(t, Code, d.Code)

	p = newPlugin(t, a, `{"action": "block"}`)
	got, _ = p.OnStreamEvent(context.Background(), x, &pluginapi.StreamEvent{Kind: pluginapi.EventTextDelta, Text: "Ann"})
	assert.Equal(t, pluginapi.StreamPass, got.Action)
	d, _ = p.OnStreamEnd(context.Background(), plugintest.Exchange(nil, nil))
	assert.Equal(t, pluginapi.ActionAllow, d.Action)
}

func TestHealth(t *testing.T) {
	a := newAnalyzer(t)
	err := newPlugin(t, a, `{}`).Health(context.Background())
	assert.NoError(t, err)

	err = newPlugin(t, a, `{"language": "xx"}`).Health(context.Background())
	require.ErrorContains(t, err, `HTTP 500 for language "xx"`)
	assert.Error(t, newPlugin(t, nil, `{"analyzer_url": "http://127.0.0.1:1"}`).Health(context.Background()))
}

func TestSummarize(t *testing.T) {
	p := New().(*Plugin)
	for cfg, want := range map[string]string{
		`{}`: "anonymize (replace), all entities, en",
		`{"entities": ["PERSON"], "restore": true, "language": "de"}`:                      "anonymize (replace), PERSON, restore, de",
		`{"action": "block", "entities": ["PERSON", "URL"], "block_entities": ["US_SSN"]}`: "block, 3 entity types, 1 blocking, en",
		`{"bogus": 1}`: "",
	} {
		got := p.Summarize(json.RawMessage(cfg))
		assert.Equal(t, want, got)
	}
}

func TestRestoreProvenance(t *testing.T) {
	a := newAnalyzer(t, "Ann Lee", "Sam Ops")
	in := newPlugin(t, a, `{"restore": true, "roles": ["system", "user"]}`)
	out := newPlugin(t, a, `{"restore": true}`)
	x := plugintest.Exchange(plugintest.Prompt(
		plugintest.Text(pluginapi.RoleSystem, "m0", "Escalate to Sam Ops at ops@corp.io."),
		plugintest.Text(pluginapi.RoleUser, "m1", "I am Ann Lee."),
	), nil)
	_, err := in.OnPrompt(context.Background(), x)
	require.NoError(t, err)
	got := x.Prompt.Message("m0").Text()
	require.Equal(t, "Escalate to <PERSON_1> at <EMAIL_ADDRESS_1>.", got)

	// The model is talked into repeating the system placeholders: they
	// stay placeholders, while the user's own value comes back.
	x.Response = plugintest.Completion("Contact <PERSON_1> at <EMAIL_ADDRESS_1>, <PERSON_2>.")
	d, err := out.OnResponse(context.Background(), x)
	require.NoError(t, err)
	got = x.Response.Text(0)
	assert.Equal(t, "Contact <PERSON_1> at <EMAIL_ADDRESS_1>, Ann Lee.", got)
	assert.True(t, d.NoStore, "restored response must not be cached: %+v", d)

	// A value the system prompt mentions first becomes restorable once the
	// user sends it too.
	z := plugintest.Exchange(plugintest.Prompt(
		plugintest.Text(pluginapi.RoleSystem, "m0", "The customer is Ann Lee."),
		plugintest.Text(pluginapi.RoleUser, "m1", "I am Ann Lee."),
	), nil)
	_, err = in.OnPrompt(context.Background(), z)
	require.NoError(t, err)

	z.Response = plugintest.Completion("Hello <PERSON_1>.")
	_, err = out.OnResponse(context.Background(), z)
	require.NoError(t, err)
	got = z.Response.Text(0)
	assert.Equal(t, "Hello Ann Lee.", got)

	// A prompt instance without restore never hands values to a response
	// instance with restore.
	plain := newPlugin(t, a, `{}`)
	y := plugintest.Exchange(plugintest.Prompt(plugintest.Text(pluginapi.RoleUser, "m1", "I am Ann Lee.")), nil)
	d, err = plain.OnPrompt(context.Background(), y)
	require.NoError(t, err)
	require.False(t, d.NoStore)

	y.Response = plugintest.Completion("Hello <PERSON_1>.")
	d, err = out.OnResponse(context.Background(), y)
	require.NoError(t, err)
	require.False(t, d.NoStore)
	got = y.Response.Text(0)
	assert.Equal(t, "Hello <PERSON_1>.", got)
}

func TestPartialAnalyzerFailureLeavesNoState(t *testing.T) {
	a := newAnalyzer(t, "Ann")
	p := newPlugin(t, a, `{"restore": true}`)
	// One of the two parts hits an analyzer failure.
	var calls atomic.Int64
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1)%2 == 0 {
			http.Error(w, `{"error":"down"}`, http.StatusBadGateway)
			return
		}
		a.handle(w, r)
	}))
	t.Cleanup(proxy.Close)
	p.client.baseURL = proxy.URL

	x := plugintest.Exchange(plugintest.Prompt(plugintest.Text(pluginapi.RoleUser, "m1", "Ann one"), plugintest.Text(pluginapi.RoleUser, "m2", "Ann two")), nil)
	_, err := p.OnPrompt(context.Background(), x)
	require.Error(t, err)
	assert.False(t, x.Prompt.Changes().Dirty)
	m := p.mapping(x)
	assert.False(t, m.hasRestorable())
	assert.Empty(t, m.byPlaceholder)

	// A later response phase (fail_mode open let the request continue)
	// finds nothing to restore.
	x.Response = plugintest.Completion("Hi <PERSON_1>")
	restore := newPlugin(t, a, `{"restore": true}`)
	_, err = restore.OnResponse(context.Background(), x)
	require.NoError(t, err)
	got := x.Response.Text(0)
	assert.Equal(t, "Hi <PERSON_1>", got)
}

func TestPlaceholdersAreNumberedInDocumentOrder(t *testing.T) {
	a := newAnalyzer(t, "Ann", "Bob", "Cid")
	p := newPlugin(t, a, `{}`)
	for range 5 {
		x := plugintest.Exchange(plugintest.Prompt(plugintest.Text(pluginapi.RoleUser, "m1", "Cid"), plugintest.Text(pluginapi.RoleUser, "m2", "Bob"), plugintest.Text(pluginapi.RoleUser, "m3", "Ann and Cid")), nil)
		_, err := p.OnPrompt(context.Background(), x)
		require.NoError(t, err)

		got := x.Prompt.Message("m1").Text() + " " + x.Prompt.Message("m2").Text() + " " + x.Prompt.Message("m3").Text()
		require.Equal(t, "<PERSON_1> <PERSON_2> <PERSON_3> and <PERSON_1>", got)
	}
}

func TestToolArgumentsKeepLargeNumbers(t *testing.T) {
	a := newAnalyzer(t, "Ann")
	p := newPlugin(t, a, `{}`)
	call := pluginapi.Message{ID: "m1", Role: pluginapi.RoleAssistant, Parts: []pluginapi.Part{
		{Kind: pluginapi.PartToolCall, ToolCall: &pluginapi.ToolCall{ID: "c1", Name: "f", Arguments: json.RawMessage(`{"id":9007199254740993,"name":"Ann","ratio":1.10}`)}},
	}}
	x := plugintest.Exchange(plugintest.Prompt(call), nil)
	_, err := p.OnPrompt(context.Background(), x)
	require.NoError(t, err)
	got := string(x.Prompt.ToolCalls()[0].Call.Arguments)
	assert.Equal(t, `{"id":9007199254740993,"name":"<PERSON_1>","ratio":1.10}`, got)
	_, _, ok := argStrings(json.RawMessage(`{"a":"b"} {"c":"d"}`))
	assert.False(t, ok)
}

func TestAPIKeyNeedsHTTPS(t *testing.T) {
	for _, cfg := range []string{
		`{"api_key": "tok", "analyzer_url": "http://presidio.internal:5002"}`,
		`{"api_key": "tok", "analyzer_url": "http://10.0.0.5:5002"}`,
	} {
		err := New().Init(context.Background(), json.RawMessage(cfg), plugintest.NewHost())
		require.ErrorContains(t, err, "api_key needs an https:// analyzer_url")
	}
	for _, cfg := range []string{
		`{"api_key": "tok", "analyzer_url": "https://presidio.internal"}`,
		`{"api_key": "tok", "analyzer_url": "http://localhost:5002"}`,
		`{"api_key": "tok", "analyzer_url": "http://127.0.0.1:5002"}`,
		`{"api_key": "tok", "analyzer_url": "http://[::1]:5002"}`,
		`{"analyzer_url": "http://presidio.internal:5002"}`,
	} {
		err := New().Init(context.Background(), json.RawMessage(cfg), plugintest.NewHost())
		assert.NoError(t, err, "%s: %v", cfg, err)
	}
}

func TestAPIKeyIsNotFollowedToPlainHTTP(t *testing.T) {
	a := newAnalyzer(t, "Ann")
	// A TLS front that redirects to a plain-http host.
	target := "http://presidio.invalid"
	front := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target+r.URL.RequestURI(), http.StatusTemporaryRedirect)
	}))
	t.Cleanup(front.Close)
	p := newPlugin(t, nil, `{"analyzer_url": "`+front.URL+`", "api_key": "tok"}`)
	p.client.http = front.Client()
	x := plugintest.Exchange(plugintest.Prompt(plugintest.Text(pluginapi.RoleUser, "m1", "Ann is here")), nil)
	_, err := p.OnPrompt(context.Background(), x)
	require.ErrorContains(t, err, "redirect to plain http")

	// A loopback sidecar may still be reached over plain http, and without
	// a key any redirect is followed as usual.
	target = a.srv.URL
	_, err = p.OnPrompt(context.Background(), x)
	require.NoError(t, err)

	p = newPlugin(t, nil, `{"analyzer_url": "`+front.URL+`"}`)
	p.client.http = front.Client()
	_, err = p.OnPrompt(context.Background(), x)
	require.NoError(t, err)
}

func TestPlaceholdersSkipLiteralTokens(t *testing.T) {
	a := newAnalyzer(t, "Jakub Nowak", "Jan Kowalczyk", "Zoe Ray")
	in := newPlugin(t, a, `{"restore": true}`)
	out := newPlugin(t, a, `{"restore": true}`)
	// The history carries a placeholder an earlier response left unrestored
	// (a person the model invented), and the unanalyzed system message a
	// literal one; neither number may be handed to a new value.
	x := plugintest.Exchange(plugintest.Prompt(
		plugintest.Text(pluginapi.RoleSystem, "m0", "Template slot: <PERSON_3>."),
		plugintest.Text(pluginapi.RoleUser, "m1", "I am Jakub Nowak."),
		plugintest.Text(pluginapi.RoleAssistant, "m2", "Meet <PERSON_2>, a historian."),
		plugintest.Text(pluginapi.RoleUser, "m3", "My colleague Jan Kowalczyk wants to meet <PERSON_2>."),
	), nil)
	_, err := in.OnPrompt(context.Background(), x)
	require.NoError(t, err)

	require.Equal(t, "My colleague <PERSON_4> wants to meet <PERSON_2>.", x.Prompt.Message("m3").Text())
	x.Response = plugintest.Completion("<PERSON_4> meets <PERSON_2>; <PERSON_1> watches.")
	_, err = out.OnResponse(context.Background(), x)
	require.NoError(t, err)

	assert.Equal(t, "Jan Kowalczyk meets <PERSON_2>; Jakub Nowak watches.", x.Response.Text(0))
	// A value the model produces next to a literal placeholder gets a fresh
	// number, in the response phase and in a stream.
	y := plugintest.Exchange(nil, plugintest.Completion("<PERSON_1> meets Zoe Ray"))
	_, err = out.OnResponse(context.Background(), y)
	require.NoError(t, err)

	assert.Equal(t, "<PERSON_1> meets <PERSON_2>", y.Response.Text(0))
	z := plugintest.Exchange(nil, nil)
	got, err := out.OnStreamEvent(context.Background(), z, &pluginapi.StreamEvent{Kind: pluginapi.EventTextDelta, Text: "<PERSON_1> meets Zoe Ray"})
	require.NoError(t, err)
	assert.Equal(t, "<PERSON_1> meets <PERSON_2>", got.Text)
}

func TestPlaceholdersSkipUnanalyzedToolArguments(t *testing.T) {
	a := newAnalyzer(t, "Ann Lee")
	in := newPlugin(t, a, `{"restore": true, "roles": ["user"]}`)
	out := newPlugin(t, a, `{"restore": true}`)
	// Assistant arguments are not analyzed with roles [user], but their
	// placeholder, JSON-escaped here, must still keep its number.
	call := pluginapi.Message{ID: "m1", Role: pluginapi.RoleAssistant, Parts: []pluginapi.Part{
		{Kind: pluginapi.PartToolCall, ToolCall: &pluginapi.ToolCall{ID: "c1", Name: "lookup", Arguments: json.RawMessage(`{"name": "\u003cPERSON_1\u003e"}`)}},
	}}
	x := plugintest.Exchange(plugintest.Prompt(call, plugintest.Text(pluginapi.RoleUser, "m2", "I am Ann Lee.")), nil)
	_, err := in.OnPrompt(context.Background(), x)
	require.NoError(t, err)
	got := x.Prompt.Message("m2").Text()
	require.Equal(t, "I am <PERSON_2>.", got)

	x.Response = plugintest.Completion("<PERSON_1> is not <PERSON_2>")
	_, err = out.OnResponse(context.Background(), x)
	require.NoError(t, err)
	got = x.Response.Text(0)
	assert.Equal(t, "<PERSON_1> is not Ann Lee", got)

	// Arguments that are a JSON string rather than an object are decoded too.
	scalar := pluginapi.Message{ID: "m1", Role: pluginapi.RoleAssistant, Parts: []pluginapi.Part{
		{Kind: pluginapi.PartToolCall, ToolCall: &pluginapi.ToolCall{ID: "c1", Name: "lookup", Arguments: json.RawMessage(`"\u003cPERSON_1\u003e"`)}},
	}}
	y := plugintest.Exchange(plugintest.Prompt(scalar, plugintest.Text(pluginapi.RoleUser, "m2", "I am Ann Lee.")), nil)
	_, err = in.OnPrompt(context.Background(), y)
	require.NoError(t, err)
	got = y.Prompt.Message("m2").Text()
	require.Equal(t, "I am <PERSON_2>.", got)

	// Object keys are reserved as well as values.
	keyed := pluginapi.Message{ID: "m1", Role: pluginapi.RoleAssistant, Parts: []pluginapi.Part{
		{Kind: pluginapi.PartToolCall, ToolCall: &pluginapi.ToolCall{ID: "c1", Name: "lookup", Arguments: json.RawMessage(`{"\u003cPERSON_1\u003e": "x"}`)}},
	}}
	z := plugintest.Exchange(plugintest.Prompt(keyed, plugintest.Text(pluginapi.RoleUser, "m2", "I am Ann Lee.")), nil)
	_, err = in.OnPrompt(context.Background(), z)
	require.NoError(t, err)
	got = z.Prompt.Message("m2").Text()
	require.Equal(t, "I am <PERSON_2>.", got)

	// With assistant analysis on (the default roles), a scalar argument is
	// not analyzed but still reserved.
	all := newPlugin(t, a, `{"restore": true}`)
	w := plugintest.Exchange(plugintest.Prompt(scalar, plugintest.Text(pluginapi.RoleUser, "m2", "I am Ann Lee.")), nil)
	_, err = all.OnPrompt(context.Background(), w)
	require.NoError(t, err)
	got = w.Prompt.Message("m2").Text()
	require.Equal(t, "I am <PERSON_2>.", got)
}

func TestStreamRestoresToolCallArguments(t *testing.T) {
	a := newAnalyzer(t, "Ann Lee")
	in := newPlugin(t, a, `{"restore": true}`)
	out := newPlugin(t, a, `{"restore": true, "stream_lookbehind": 12}`)
	x := plugintest.Exchange(plugintest.Prompt(plugintest.Text(pluginapi.RoleUser, "m1", "Email ann@x.io for Ann Lee")), nil)
	_, err := in.OnPrompt(context.Background(), x)
	require.NoError(t, err)

	res, err := plugintest.RunStream(context.Background(), out, x, []*pluginapi.StreamEvent{
		plugintest.TextDelta("Sending to <PERSON_1>."),
		{Kind: pluginapi.EventToolCallDelta, Call: 0, Text: `{"to":"<EMAIL_ADD`},
		{Kind: pluginapi.EventToolCallDelta, Call: 0, Text: `RESS_1>","cc":"bob@y.io"}`},
	})
	require.NoError(t, err)
	assert.Equal(t, "Sending to Ann Lee.", res.Text[0])
	assert.Equal(t, `{"to":"ann@x.io","cc":"<EMAIL_ADDRESS_2>"}`, res.ToolArguments[0][0])
	assert.Equal(t, 2, res.End.Detail.(map[string]any)["restored"])
}

// Gemini returns tool-call arguments with angle brackets escaped: the
// stream still restores those placeholders, and a restored value is
// JSON-escaped so the arguments stay valid.
func TestStreamRestoresEscapedToolCallArguments(t *testing.T) {
	a := newAnalyzer(t, `Ann "Lee"`)
	in := newPlugin(t, a, `{"restore": true}`)
	out := newPlugin(t, a, `{"restore": true, "stream_lookbehind": 32}`)
	x := plugintest.Exchange(plugintest.Prompt(plugintest.Text(pluginapi.RoleUser, "m1", `Email ann@x.io for Ann "Lee"`)), nil)
	_, err := in.OnPrompt(context.Background(), x)
	require.NoError(t, err)

	res, err := plugintest.RunStream(context.Background(), out, x, []*pluginapi.StreamEvent{
		{Kind: pluginapi.EventToolCallDelta, Call: 0, Text: `{"to":"\u003cEMAIL_ADD`},
		{Kind: pluginapi.EventToolCallDelta, Call: 0, Text: `RESS_1\u003e","name":"\u003cPERSON_1\u003e","raw":"\\u003cPERSON_1\u003e"}`},
	})
	require.NoError(t, err)

	got := res.ToolArguments[0][0]
	want := `{"to":"ann@x.io","name":"Ann \"Lee\"","raw":"\\u003cPERSON_1\u003e"}`
	require.Equal(t, want, got)

	var args map[string]string
	err = json.Unmarshal([]byte(got), &args)
	require.NoError(t, err)
}
