package presidio

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"unicode/utf8"

	"github.com/enterpilot/gomodel/pluginapi"
	"github.com/enterpilot/gomodel/pluginapi/plugintest"
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
	if err := json.Unmarshal([]byte(cfg), &m); err != nil {
		t.Fatal(err)
	}
	if _, ok := m["analyzer_url"]; !ok && a != nil {
		m["analyzer_url"] = a.srv.URL
	}
	raw, _ := json.Marshal(m)
	p := New()
	if err := p.Init(context.Background(), raw, plugintest.NewHost()); err != nil {
		t.Fatalf("Init: %v", err)
	}
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
			if got := newPlugin(t, nil, tt.cfg).EditsContent(); got != tt.want {
				t.Fatalf("EditsContent() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestManifest(t *testing.T) {
	m := New().Manifest()
	if m.Name != "presidio" || !m.Mutates || !m.Guardrail {
		t.Fatalf("manifest = %+v", m)
	}
	if !reflect.DeepEqual(m.Kinds, []pluginapi.Kind{pluginapi.KindPrompt, pluginapi.KindResponse, pluginapi.KindStream}) {
		t.Errorf("kinds = %v", m.Kinds)
	}
	want := []string{"analyzer_url", "api_key", "language", "entities", "block_entities", "score_threshold", "allow_list", "ad_hoc_recognizers", "roles", "action", "operator", "restore", "message", "block_status", "stream_chunk", "stream_lookbehind"}
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
	p := New()
	for name, ok := range map[string]bool{
		"PromptHook":    func() bool { _, ok := p.(pluginapi.PromptHook); return ok }(),
		"ResponseHook":  func() bool { _, ok := p.(pluginapi.ResponseHook); return ok }(),
		"StreamHook":    func() bool { _, ok := p.(pluginapi.StreamHook); return ok }(),
		"HealthChecker": func() bool { _, ok := p.(pluginapi.HealthChecker); return ok }(),
	} {
		if !ok {
			t.Errorf("must implement %s", name)
		}
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
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestDefaults(t *testing.T) {
	p := newPlugin(t, nil, `{}`)
	if p.analyzerURL != DefaultAnalyzerURL || p.language != "en" || p.action != ActionAnonymize || p.operator != OperatorReplace || p.restore || p.enforcement.Message != DefaultMessage || p.streamChunk != DefaultStreamChunk || p.lookbehind != DefaultStreamLookbehind {
		t.Errorf("settings = %+v", p.settings)
	}
	if p.entities != nil || p.scoreThreshold != nil || p.allowList != nil || p.adHocRecognizers != nil || p.blockEntities != nil {
		t.Errorf("optional settings not nil: %+v", p.settings)
	}
	wantRoles := map[pluginapi.Role]bool{pluginapi.RoleUser: true, pluginapi.RoleAssistant: true, pluginapi.RoleTool: true}
	if !reflect.DeepEqual(p.roles, wantRoles) {
		t.Errorf("roles = %v", p.roles)
	}
	// Lists as text, threshold as string, block entities merged into
	// entities, trailing slash and case normalized.
	p = newPlugin(t, nil, `{"analyzer_url": "http://presidio:3000/", "entities": "person, email_address\n", "block_entities": ["CREDIT_CARD"], "score_threshold": "0.4", "roles": "system", "restore": "yes", "ad_hoc_recognizers": "[{\"supported_entity\": \"ZIP\"}]"}`)
	if p.analyzerURL != "http://presidio:3000" || *p.scoreThreshold != 0.4 || !p.restore {
		t.Errorf("settings = %+v", p.settings)
	}
	if !reflect.DeepEqual(p.entities, []string{"PERSON", "EMAIL_ADDRESS", "CREDIT_CARD"}) || !p.blockEntities["CREDIT_CARD"] {
		t.Errorf("entities = %v, blocked = %v", p.entities, p.blockEntities)
	}
	if !p.roles[pluginapi.RoleSystem] || !p.roles[pluginapi.RoleDeveloper] || p.roles[pluginapi.RoleUser] {
		t.Errorf("roles = %v", p.roles)
	}
	if string(p.adHocRecognizers) != `[{"supported_entity":"ZIP"}]` {
		t.Errorf("recognizers = %s", p.adHocRecognizers)
	}
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
	if err != nil {
		t.Fatal(err)
	}
	if d.Action != pluginapi.ActionAllow || d.NoStore {
		t.Fatalf("decision = %+v", d)
	}
	if got := x.Prompt.Message("m1").Text(); got != "I am <PERSON_1>, mail <EMAIL_ADDRESS_1>. My friend is also <PERSON_1>." {
		t.Errorf("m1 = %q", got)
	}
	if got := x.Prompt.Message("m2").Text(); got != "Hello <PERSON_1>" {
		t.Errorf("m2 = %q", got)
	}
	if got := x.Prompt.Message("m0").Text(); !strings.Contains(got, "john@acme.com") {
		t.Errorf("system message edited: %q", got)
	}
	detail := d.Detail.(map[string]any)
	if !reflect.DeepEqual(detail["entities"], map[string]int{"PERSON": 3, "EMAIL_ADDRESS": 1}) || detail["messages"] != 2 || detail["replacements"] != 4 {
		t.Errorf("detail = %v", detail)
	}
	texts := a.texts()
	if len(texts) != 2 {
		t.Fatalf("analyzer calls = %v", texts)
	}
	req := a.requests[0]
	if req.Language != "en" || !reflect.DeepEqual(req.Entities, []string{"PERSON", "EMAIL_ADDRESS"}) || req.ScoreThreshold == nil || *req.ScoreThreshold != 0.3 || !reflect.DeepEqual(req.AllowList, []string{"ACME"}) || req.CorrelationID != "test-request" {
		t.Errorf("request = %+v", req)
	}
	if a.auth != "Bearer tok" {
		t.Errorf("authorization = %q", a.auth)
	}
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
		if _, err := p.OnPrompt(context.Background(), x); err != nil {
			t.Fatal(err)
		}
		got := x.Prompt.Message("m1").Text()
		if tt.operator == OperatorHash {
			if !regexp.MustCompile(`^Hi [0-9a-f]{64}, mail [0-9a-f]{64}$`).MatchString(got) {
				t.Errorf("%s: %q", tt.operator, got)
			}
			continue
		}
		if got != tt.want {
			t.Errorf("%s: %q, want %q", tt.operator, got, tt.want)
		}
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
			if err != nil {
				t.Fatal(err)
			}
			if d.Action != tt.action || d.Code != tt.code || d.Status != tt.status {
				t.Fatalf("decision = %+v", d)
			}
			if edited := x.Prompt.Message("m1").Text() != tt.text; edited != tt.edited {
				t.Errorf("edited = %v, text %q", edited, x.Prompt.Message("m1").Text())
			}
			if d.Action == pluginapi.ActionRespond && d.Response.Text(0) != DefaultMessage {
				t.Errorf("respond text = %q", d.Response.Text(0))
			}
			if tt.code == CodeBlocked && d.Detail.(map[string]any)["blocked_entity"] != "CREDIT_CARD" {
				t.Errorf("detail = %v", d.Detail)
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
	if _, err := p.OnPrompt(context.Background(), x); err != nil {
		t.Fatal(err)
	}
	if got := x.Prompt.Message("m1").Text(); got != "Looking up <PERSON_1>" {
		t.Errorf("text = %q", got)
	}
	if got := string(x.Prompt.ToolCalls()[0].Call.Arguments); got != `{"n":1,"name":"<PERSON_1>","tags":["vip","<EMAIL_ADDRESS_1>"]}` {
		t.Errorf("arguments = %s", got)
	}
	if got := x.Prompt.Message("m2").Parts[0].ToolResult.Parts[0].Text; got != "<PERSON_1> <<EMAIL_ADDRESS_1>>" {
		t.Errorf("result = %q", got)
	}
	// Strings inside the arguments are analyzed one by one, never the JSON.
	for _, s := range a.texts() {
		if strings.Contains(s, "{") {
			t.Errorf("analyzer saw JSON: %q", s)
		}
	}
}

func TestOnPromptAnalyzerErrorsFailWithoutEditing(t *testing.T) {
	a := newAnalyzer(t, "Ann")
	a.status = http.StatusInternalServerError
	p := newPlugin(t, a, `{}`)
	x := plugintest.Exchange(plugintest.Prompt(plugintest.Text(pluginapi.RoleUser, "m1", "Ann is here")), nil)
	_, err := p.OnPrompt(context.Background(), x)
	if err == nil || !strings.Contains(err.Error(), "analyzer returned HTTP 500") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), "Ann") {
		t.Errorf("error echoes the text: %v", err)
	}
	if x.Prompt.Message("m1").Text() != "Ann is here" || x.Prompt.Changes().Dirty {
		t.Error("prompt edited despite the error")
	}
	p = newPlugin(t, nil, `{"analyzer_url": "http://127.0.0.1:1"}`)
	if _, err := p.OnPrompt(context.Background(), x); err == nil || !strings.Contains(err.Error(), "analyzer unreachable") {
		t.Errorf("err = %v", err)
	}
}

func TestRestoreRoundTrip(t *testing.T) {
	a := newAnalyzer(t, "Ann Lee")
	in := newPlugin(t, a, `{"restore": true}`)
	out := newPlugin(t, a, `{"restore": true}`)
	x := plugintest.Exchange(plugintest.Prompt(plugintest.Text(pluginapi.RoleUser, "m1", "I am Ann Lee (ann@x.io). Draft an email to Bob.")), nil)
	d, err := in.OnPrompt(context.Background(), x)
	if err != nil {
		t.Fatal(err)
	}
	if !d.NoStore {
		t.Errorf("prompt decision = %+v, want NoStore", d)
	}
	if got := x.Prompt.Message("m1").Text(); got != "I am <PERSON_1> (<EMAIL_ADDRESS_1>). Draft an email to Bob." {
		t.Fatalf("prompt = %q", got)
	}
	// The model repeats the placeholders, reveals a new address of its
	// own, and calls a tool with a placeholder argument.
	x.Response = plugintest.Completion("Dear Bob, <PERSON_1> (<EMAIL_ADDRESS_1>) wrote; cc bob@y.io.")
	x.Response.Choices[0].Message.Parts = append(x.Response.Choices[0].Message.Parts, pluginapi.Part{Kind: pluginapi.PartToolCall, ToolCall: &pluginapi.ToolCall{ID: "c1", Name: "send", Arguments: json.RawMessage(`{"to":"<EMAIL_ADDRESS_1>"}`)}})
	d, err = out.OnResponse(context.Background(), x)
	if err != nil {
		t.Fatal(err)
	}
	if d.Action != pluginapi.ActionAllow {
		t.Fatalf("response decision = %+v", d)
	}
	if got := x.Response.Text(0); got != "Dear Bob, Ann Lee (ann@x.io) wrote; cc <EMAIL_ADDRESS_2>." {
		t.Errorf("response = %q", got)
	}
	if got := string(x.Response.Choices[0].Message.Parts[1].ToolCall.Arguments); got != `{"to":"ann@x.io"}` {
		t.Errorf("arguments = %s", got)
	}
	detail := d.Detail.(map[string]any)
	if detail["restored"] != 3 || detail["replacements"] != 1 {
		t.Errorf("detail = %v", detail)
	}
	// Without a prompt-phase mapping there is nothing to restore and the
	// model's own values are still anonymized.
	y := plugintest.Exchange(nil, plugintest.Completion("Write to <PERSON_1> at bob@y.io"))
	if _, err := out.OnResponse(context.Background(), y); err != nil {
		t.Fatal(err)
	}
	if got := y.Response.Text(0); got != "Write to <PERSON_1> at <EMAIL_ADDRESS_1>" {
		t.Errorf("response = %q", got)
	}
}

func TestOnResponseDecisions(t *testing.T) {
	a := newAnalyzer(t, "Ann")
	p := newPlugin(t, a, `{"action": "block", "block_entities": ["EMAIL_ADDRESS"]}`)
	x := plugintest.Exchange(nil, plugintest.Completion("clean", "Ann was here"))
	d, err := p.OnResponse(context.Background(), x)
	if err != nil {
		t.Fatal(err)
	}
	if d.Action != pluginapi.ActionBlock || d.Code != Code || d.Status != 0 {
		t.Errorf("decision = %+v", d)
	}
	x = plugintest.Exchange(nil, plugintest.Completion("mail ann@x.io"))
	if d, _ = p.OnResponse(context.Background(), x); d.Code != CodeBlocked {
		t.Errorf("decision = %+v", d)
	}
	p = newPlugin(t, a, `{"action": "warn"}`)
	x = plugintest.Exchange(nil, plugintest.Completion("Ann was here"))
	if d, _ = p.OnResponse(context.Background(), x); d.Action != pluginapi.ActionWarn || x.Response.Text(0) != "Ann was here" {
		t.Errorf("decision = %+v, text %q", d, x.Response.Text(0))
	}
}

func TestStreamPolicy(t *testing.T) {
	p := newPlugin(t, nil, `{"stream_chunk": 100, "stream_lookbehind": 20}`)
	if got := p.StreamPolicy(); got != (pluginapi.StreamPolicy{Mode: pluginapi.StreamTransform, LookbehindChars: 20, MinChunkChars: 100}) {
		t.Errorf("policy = %+v", got)
	}
	for _, action := range []string{ActionBlock, ActionRespond} {
		p = newPlugin(t, nil, `{"action": "`+action+`"}`)
		if got := p.StreamPolicy(); got.Mode != pluginapi.StreamBuffer {
			t.Errorf("%s policy = %+v", action, got)
		}
	}
}

func TestStreamEvents(t *testing.T) {
	a := newAnalyzer(t, "Ann Lee")
	in := newPlugin(t, a, `{"restore": true}`)
	p := newPlugin(t, a, `{"restore": true, "block_entities": ["CREDIT_CARD"]}`)
	x := plugintest.Exchange(plugintest.Prompt(plugintest.Text(pluginapi.RoleUser, "m1", "I am Ann Lee")), nil)
	if _, err := in.OnPrompt(context.Background(), x); err != nil {
		t.Fatal(err)
	}
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
		if err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
		if got.Action != st.want.Action || got.Text != st.want.Text {
			t.Errorf("step %d: %+v, want %+v", i, got, st.want)
		}
		if got.Action == pluginapi.StreamTerminate && (got.Terminate == nil || got.Terminate.Code != CodeBlocked) {
			t.Errorf("step %d: terminate = %+v", i, got.Terminate)
		}
	}
	// The end decision repeats what the stream contained, the cut included.
	d, err := p.OnStreamEnd(context.Background(), x)
	if err != nil {
		t.Fatal(err)
	}
	detail := d.Detail.(map[string]any)
	if d.Action != pluginapi.ActionBlock || d.Code != CodeBlocked || detail["restored"] != 1 || detail["replacements"] != 2 || !reflect.DeepEqual(detail["entities"], map[string]int{"EMAIL_ADDRESS": 1, "CREDIT_CARD": 1}) {
		t.Errorf("end = %+v", d)
	}
	// A clean stream ends with a plain allow.
	if d, _ := p.OnStreamEnd(context.Background(), plugintest.Exchange(nil, nil)); d.Action != pluginapi.ActionAllow || d.Detail != nil {
		t.Errorf("clean end = %+v", d)
	}
}

func TestStreamWarn(t *testing.T) {
	a := newAnalyzer(t, "Ann")
	p := newPlugin(t, a, `{"action": "warn"}`)
	x := plugintest.Exchange(nil, nil)
	got, err := p.OnStreamEvent(context.Background(), x, &pluginapi.StreamEvent{Kind: pluginapi.EventTextDelta, Text: "Ann"})
	if err != nil || got.Action != pluginapi.StreamPass {
		t.Fatalf("event = %+v, %v", got, err)
	}
	d, _ := p.OnStreamEnd(context.Background(), x)
	if d.Action != pluginapi.ActionWarn || d.Code != Code {
		t.Errorf("end = %+v", d)
	}
	p = newPlugin(t, a, `{"action": "block"}`)
	if got, _ := p.OnStreamEvent(context.Background(), x, &pluginapi.StreamEvent{Kind: pluginapi.EventTextDelta, Text: "Ann"}); got.Action != pluginapi.StreamPass {
		t.Errorf("buffered event = %+v", got)
	}
	if d, _ := p.OnStreamEnd(context.Background(), plugintest.Exchange(nil, nil)); d.Action != pluginapi.ActionAllow {
		t.Errorf("buffered end = %+v", d)
	}
}

func TestHealth(t *testing.T) {
	a := newAnalyzer(t)
	if err := newPlugin(t, a, `{}`).Health(context.Background()); err != nil {
		t.Errorf("healthy: %v", err)
	}
	err := newPlugin(t, a, `{"language": "xx"}`).Health(context.Background())
	if err == nil || !strings.Contains(err.Error(), `HTTP 500 for language "xx"`) {
		t.Errorf("unsupported language: %v", err)
	}
	if err := newPlugin(t, nil, `{"analyzer_url": "http://127.0.0.1:1"}`).Health(context.Background()); err == nil {
		t.Error("unreachable analyzer reported healthy")
	}
}

func TestSummarize(t *testing.T) {
	p := New().(*Plugin)
	for cfg, want := range map[string]string{
		`{}`: "anonymize (replace), all entities, en",
		`{"entities": ["PERSON"], "restore": true, "language": "de"}`:                      "anonymize (replace), PERSON, restore, de",
		`{"action": "block", "entities": ["PERSON", "URL"], "block_entities": ["US_SSN"]}`: "block, 3 entity types, 1 blocking, en",
		`{"bogus": 1}`: "",
	} {
		if got := p.Summarize(json.RawMessage(cfg)); got != want {
			t.Errorf("%s: %q, want %q", cfg, got, want)
		}
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
	if _, err := in.OnPrompt(context.Background(), x); err != nil {
		t.Fatal(err)
	}
	if got := x.Prompt.Message("m0").Text(); got != "Escalate to <PERSON_1> at <EMAIL_ADDRESS_1>." {
		t.Fatalf("system = %q", got)
	}
	// The model is talked into repeating the system placeholders: they
	// stay placeholders, while the user's own value comes back.
	x.Response = plugintest.Completion("Contact <PERSON_1> at <EMAIL_ADDRESS_1>, <PERSON_2>.")
	d, err := out.OnResponse(context.Background(), x)
	if err != nil {
		t.Fatal(err)
	}
	if got := x.Response.Text(0); got != "Contact <PERSON_1> at <EMAIL_ADDRESS_1>, Ann Lee." {
		t.Errorf("response = %q", got)
	}
	if !d.NoStore {
		t.Errorf("restored response must not be cached: %+v", d)
	}
	// A value the system prompt mentions first becomes restorable once the
	// user sends it too.
	z := plugintest.Exchange(plugintest.Prompt(
		plugintest.Text(pluginapi.RoleSystem, "m0", "The customer is Ann Lee."),
		plugintest.Text(pluginapi.RoleUser, "m1", "I am Ann Lee."),
	), nil)
	if _, err := in.OnPrompt(context.Background(), z); err != nil {
		t.Fatal(err)
	}
	z.Response = plugintest.Completion("Hello <PERSON_1>.")
	if _, err := out.OnResponse(context.Background(), z); err != nil {
		t.Fatal(err)
	}
	if got := z.Response.Text(0); got != "Hello Ann Lee." {
		t.Errorf("response = %q", got)
	}

	// A prompt instance without restore never hands values to a response
	// instance with restore.
	plain := newPlugin(t, a, `{}`)
	y := plugintest.Exchange(plugintest.Prompt(plugintest.Text(pluginapi.RoleUser, "m1", "I am Ann Lee.")), nil)
	d, err = plain.OnPrompt(context.Background(), y)
	if err != nil || d.NoStore {
		t.Fatalf("prompt = %+v, %v", d, err)
	}
	y.Response = plugintest.Completion("Hello <PERSON_1>.")
	if d, err = out.OnResponse(context.Background(), y); err != nil || d.NoStore {
		t.Fatalf("response = %+v, %v", d, err)
	}
	if got := y.Response.Text(0); got != "Hello <PERSON_1>." {
		t.Errorf("response = %q", got)
	}
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
	if _, err := p.OnPrompt(context.Background(), x); err == nil {
		t.Fatal("expected the phase to fail")
	}
	if x.Prompt.Changes().Dirty {
		t.Error("prompt edited despite the failure")
	}
	if m := p.mapping(x); m.hasRestorable() || len(m.byPlaceholder) != 0 {
		t.Errorf("mapping kept state from the failed phase: %v", m.byPlaceholder)
	}
	// A later response phase (fail_mode open let the request continue)
	// finds nothing to restore.
	x.Response = plugintest.Completion("Hi <PERSON_1>")
	restore := newPlugin(t, a, `{"restore": true}`)
	if _, err := restore.OnResponse(context.Background(), x); err != nil {
		t.Fatal(err)
	}
	if got := x.Response.Text(0); got != "Hi <PERSON_1>" {
		t.Errorf("response = %q", got)
	}
}

func TestPlaceholdersAreNumberedInDocumentOrder(t *testing.T) {
	a := newAnalyzer(t, "Ann", "Bob", "Cid")
	p := newPlugin(t, a, `{}`)
	for range 5 {
		x := plugintest.Exchange(plugintest.Prompt(plugintest.Text(pluginapi.RoleUser, "m1", "Cid"), plugintest.Text(pluginapi.RoleUser, "m2", "Bob"), plugintest.Text(pluginapi.RoleUser, "m3", "Ann and Cid")), nil)
		if _, err := p.OnPrompt(context.Background(), x); err != nil {
			t.Fatal(err)
		}
		got := x.Prompt.Message("m1").Text() + " " + x.Prompt.Message("m2").Text() + " " + x.Prompt.Message("m3").Text()
		if got != "<PERSON_1> <PERSON_2> <PERSON_3> and <PERSON_1>" {
			t.Fatalf("numbering = %q", got)
		}
	}
}

func TestToolArgumentsKeepLargeNumbers(t *testing.T) {
	a := newAnalyzer(t, "Ann")
	p := newPlugin(t, a, `{}`)
	call := pluginapi.Message{ID: "m1", Role: pluginapi.RoleAssistant, Parts: []pluginapi.Part{
		{Kind: pluginapi.PartToolCall, ToolCall: &pluginapi.ToolCall{ID: "c1", Name: "f", Arguments: json.RawMessage(`{"id":9007199254740993,"name":"Ann","ratio":1.10}`)}},
	}}
	x := plugintest.Exchange(plugintest.Prompt(call), nil)
	if _, err := p.OnPrompt(context.Background(), x); err != nil {
		t.Fatal(err)
	}
	if got := string(x.Prompt.ToolCalls()[0].Call.Arguments); got != `{"id":9007199254740993,"name":"<PERSON_1>","ratio":1.10}` {
		t.Errorf("arguments = %s", got)
	}
	if _, _, ok := argStrings(json.RawMessage(`{"a":"b"} {"c":"d"}`)); ok {
		t.Error("two JSON values accepted")
	}
}

func TestAPIKeyNeedsHTTPS(t *testing.T) {
	for _, cfg := range []string{
		`{"api_key": "tok", "analyzer_url": "http://presidio.internal:5002"}`,
		`{"api_key": "tok", "analyzer_url": "http://10.0.0.5:5002"}`,
	} {
		if err := New().Init(context.Background(), json.RawMessage(cfg), plugintest.NewHost()); err == nil || !strings.Contains(err.Error(), "api_key needs an https:// analyzer_url") {
			t.Errorf("%s: err = %v", cfg, err)
		}
	}
	for _, cfg := range []string{
		`{"api_key": "tok", "analyzer_url": "https://presidio.internal"}`,
		`{"api_key": "tok", "analyzer_url": "http://localhost:5002"}`,
		`{"api_key": "tok", "analyzer_url": "http://127.0.0.1:5002"}`,
		`{"api_key": "tok", "analyzer_url": "http://[::1]:5002"}`,
		`{"analyzer_url": "http://presidio.internal:5002"}`,
	} {
		if err := New().Init(context.Background(), json.RawMessage(cfg), plugintest.NewHost()); err != nil {
			t.Errorf("%s: %v", cfg, err)
		}
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
	if err == nil || !strings.Contains(err.Error(), "redirect to plain http") {
		t.Fatalf("err = %v", err)
	}
	// A loopback sidecar may still be reached over plain http, and without
	// a key any redirect is followed as usual.
	target = a.srv.URL
	if _, err := p.OnPrompt(context.Background(), x); err != nil {
		t.Fatalf("loopback redirect: %v", err)
	}
	p = newPlugin(t, nil, `{"analyzer_url": "`+front.URL+`"}`)
	p.client.http = front.Client()
	if _, err := p.OnPrompt(context.Background(), x); err != nil {
		t.Fatalf("err = %v", err)
	}
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
	if _, err := in.OnPrompt(context.Background(), x); err != nil {
		t.Fatal(err)
	}
	if got := x.Prompt.Message("m3").Text(); got != "My colleague <PERSON_4> wants to meet <PERSON_2>." {
		t.Fatalf("prompt = %q", got)
	}
	x.Response = plugintest.Completion("<PERSON_4> meets <PERSON_2>; <PERSON_1> watches.")
	if _, err := out.OnResponse(context.Background(), x); err != nil {
		t.Fatal(err)
	}
	if got := x.Response.Text(0); got != "Jan Kowalczyk meets <PERSON_2>; Jakub Nowak watches." {
		t.Errorf("response = %q", got)
	}
	// A value the model produces next to a literal placeholder gets a fresh
	// number, in the response phase and in a stream.
	y := plugintest.Exchange(nil, plugintest.Completion("<PERSON_1> meets Zoe Ray"))
	if _, err := out.OnResponse(context.Background(), y); err != nil {
		t.Fatal(err)
	}
	if got := y.Response.Text(0); got != "<PERSON_1> meets <PERSON_2>" {
		t.Errorf("response = %q", got)
	}
	z := plugintest.Exchange(nil, nil)
	got, err := out.OnStreamEvent(context.Background(), z, &pluginapi.StreamEvent{Kind: pluginapi.EventTextDelta, Text: "<PERSON_1> meets Zoe Ray"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Text != "<PERSON_1> meets <PERSON_2>" {
		t.Errorf("stream = %+v", got)
	}
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
	if _, err := in.OnPrompt(context.Background(), x); err != nil {
		t.Fatal(err)
	}
	if got := x.Prompt.Message("m2").Text(); got != "I am <PERSON_2>." {
		t.Fatalf("prompt = %q", got)
	}
	x.Response = plugintest.Completion("<PERSON_1> is not <PERSON_2>")
	if _, err := out.OnResponse(context.Background(), x); err != nil {
		t.Fatal(err)
	}
	if got := x.Response.Text(0); got != "<PERSON_1> is not Ann Lee" {
		t.Errorf("response = %q", got)
	}
	// Arguments that are a JSON string rather than an object are decoded too.
	scalar := pluginapi.Message{ID: "m1", Role: pluginapi.RoleAssistant, Parts: []pluginapi.Part{
		{Kind: pluginapi.PartToolCall, ToolCall: &pluginapi.ToolCall{ID: "c1", Name: "lookup", Arguments: json.RawMessage(`"\u003cPERSON_1\u003e"`)}},
	}}
	y := plugintest.Exchange(plugintest.Prompt(scalar, plugintest.Text(pluginapi.RoleUser, "m2", "I am Ann Lee.")), nil)
	if _, err := in.OnPrompt(context.Background(), y); err != nil {
		t.Fatal(err)
	}
	if got := y.Prompt.Message("m2").Text(); got != "I am <PERSON_2>." {
		t.Fatalf("scalar prompt = %q", got)
	}
	// Object keys are reserved as well as values.
	keyed := pluginapi.Message{ID: "m1", Role: pluginapi.RoleAssistant, Parts: []pluginapi.Part{
		{Kind: pluginapi.PartToolCall, ToolCall: &pluginapi.ToolCall{ID: "c1", Name: "lookup", Arguments: json.RawMessage(`{"\u003cPERSON_1\u003e": "x"}`)}},
	}}
	z := plugintest.Exchange(plugintest.Prompt(keyed, plugintest.Text(pluginapi.RoleUser, "m2", "I am Ann Lee.")), nil)
	if _, err := in.OnPrompt(context.Background(), z); err != nil {
		t.Fatal(err)
	}
	if got := z.Prompt.Message("m2").Text(); got != "I am <PERSON_2>." {
		t.Fatalf("keyed prompt = %q", got)
	}
	// With assistant analysis on (the default roles), a scalar argument is
	// not analyzed but still reserved.
	all := newPlugin(t, a, `{"restore": true}`)
	w := plugintest.Exchange(plugintest.Prompt(scalar, plugintest.Text(pluginapi.RoleUser, "m2", "I am Ann Lee.")), nil)
	if _, err := all.OnPrompt(context.Background(), w); err != nil {
		t.Fatal(err)
	}
	if got := w.Prompt.Message("m2").Text(); got != "I am <PERSON_2>." {
		t.Fatalf("analyzed-role scalar prompt = %q", got)
	}
}

func TestStreamRestoresToolCallArguments(t *testing.T) {
	a := newAnalyzer(t, "Ann Lee")
	in := newPlugin(t, a, `{"restore": true}`)
	out := newPlugin(t, a, `{"restore": true, "stream_lookbehind": 12}`)
	x := plugintest.Exchange(plugintest.Prompt(plugintest.Text(pluginapi.RoleUser, "m1", "Email ann@x.io for Ann Lee")), nil)
	if _, err := in.OnPrompt(context.Background(), x); err != nil {
		t.Fatal(err)
	}
	res, err := plugintest.RunStream(context.Background(), out, x, []*pluginapi.StreamEvent{
		plugintest.TextDelta("Sending to <PERSON_1>."),
		{Kind: pluginapi.EventToolCallDelta, Call: 0, Text: `{"to":"<EMAIL_ADD`},
		{Kind: pluginapi.EventToolCallDelta, Call: 0, Text: `RESS_1>","cc":"bob@y.io"}`},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text[0] != "Sending to Ann Lee." || res.ToolArguments[0][0] != `{"to":"ann@x.io","cc":"<EMAIL_ADDRESS_2>"}` {
		t.Errorf("result = %+v", res)
	}
	if res.End.Detail.(map[string]any)["restored"] != 2 {
		t.Errorf("end = %+v", res.End)
	}
}

// Gemini returns tool-call arguments with angle brackets escaped: the
// stream still restores those placeholders, and a restored value is
// JSON-escaped so the arguments stay valid.
func TestStreamRestoresEscapedToolCallArguments(t *testing.T) {
	a := newAnalyzer(t, `Ann "Lee"`)
	in := newPlugin(t, a, `{"restore": true}`)
	out := newPlugin(t, a, `{"restore": true, "stream_lookbehind": 32}`)
	x := plugintest.Exchange(plugintest.Prompt(plugintest.Text(pluginapi.RoleUser, "m1", `Email ann@x.io for Ann "Lee"`)), nil)
	if _, err := in.OnPrompt(context.Background(), x); err != nil {
		t.Fatal(err)
	}
	res, err := plugintest.RunStream(context.Background(), out, x, []*pluginapi.StreamEvent{
		{Kind: pluginapi.EventToolCallDelta, Call: 0, Text: `{"to":"\u003cEMAIL_ADD`},
		{Kind: pluginapi.EventToolCallDelta, Call: 0, Text: `RESS_1\u003e","name":"\u003cPERSON_1\u003e","raw":"\\u003cPERSON_1\u003e"}`},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := res.ToolArguments[0][0]
	if want := `{"to":"ann@x.io","name":"Ann \"Lee\"","raw":"\\u003cPERSON_1\u003e"}`; got != want {
		t.Fatalf("arguments = %s, want %s", got, want)
	}
	var args map[string]string
	if err := json.Unmarshal([]byte(got), &args); err != nil {
		t.Fatalf("restored arguments are not JSON: %v", err)
	}
}
