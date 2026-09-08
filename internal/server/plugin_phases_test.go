package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/auditlog"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/guardrails"
	"github.com/enterpilot/gomodel/internal/plugins"
	"github.com/enterpilot/gomodel/pluginapi"
)

// phasePlugin is a configurable test plugin covering prompt, response and
// stream hooks. Its behaviour is selected by config keys.
type phasePlugin struct {
	prompt    string
	response  string
	stream    string
	text      string
	maxBuffer int
}

func newPhasePlugin() pluginapi.Plugin { return &phasePlugin{} }

func (p *phasePlugin) Manifest() pluginapi.Manifest {
	return pluginapi.Manifest{
		Name:    "phase_test",
		Kinds:   []pluginapi.Kind{pluginapi.KindPrompt, pluginapi.KindResponse, pluginapi.KindStream},
		Mutates: true,
		ConfigSchema: []pluginapi.Field{
			{Key: "prompt", Input: pluginapi.InputText},
			{Key: "response", Input: pluginapi.InputText},
			{Key: "stream", Input: pluginapi.InputText},
			{Key: "text", Input: pluginapi.InputText},
			{Key: "max_buffer", Input: pluginapi.InputText},
		},
	}
}

func (p *phasePlugin) Init(_ context.Context, raw json.RawMessage, _ pluginapi.Host) error {
	var cfg struct {
		Prompt, Response, Stream, Text string
		MaxBuffer                      string `json:"max_buffer"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return err
	}
	p.prompt, p.response, p.stream, p.text = cfg.Prompt, cfg.Response, cfg.Stream, cfg.Text
	if cfg.MaxBuffer != "" {
		p.maxBuffer, _ = strconv.Atoi(cfg.MaxBuffer)
	}
	return nil
}

func (p *phasePlugin) Close(context.Context) error { return nil }

func (p *phasePlugin) decide(mode string, x *pluginapi.Exchange) (pluginapi.Decision, error) {
	switch mode {
	case "respond":
		return pluginapi.Respond(p.text), nil
	case "block":
		return pluginapi.Block(0, "policy", "blocked by test"), nil
	case "warn":
		return pluginapi.Warn("pii", "found", nil), nil
	case "edit":
		if x.Response != nil {
			return pluginapi.Allow(), x.Response.ReplaceText(0, p.text)
		}
	}
	return pluginapi.Allow(), nil
}

func (p *phasePlugin) OnPrompt(_ context.Context, x *pluginapi.Exchange) (pluginapi.Decision, error) {
	if p.prompt == "edit" {
		if last := x.Prompt.LastUser(); last != nil {
			return pluginapi.Allow(), x.Prompt.SetText(last.ID, 0, p.text)
		}
	}
	return p.decide(p.prompt, x)
}

func (p *phasePlugin) OnResponse(_ context.Context, x *pluginapi.Exchange) (pluginapi.Decision, error) {
	return p.decide(p.response, x)
}

func (p *phasePlugin) StreamPolicy() pluginapi.StreamPolicy {
	if p.stream == "buffer" {
		return pluginapi.StreamPolicy{Mode: pluginapi.StreamBuffer, MaxBufferBytes: p.maxBuffer}
	}
	return pluginapi.StreamPolicy{Mode: pluginapi.StreamTransform}
}

func (p *phasePlugin) OnStreamEvent(_ context.Context, _ *pluginapi.Exchange, ev *pluginapi.StreamEvent) (pluginapi.StreamDecision, error) {
	switch p.stream {
	case "replace":
		if ev.Kind == pluginapi.EventTextDelta {
			return pluginapi.Replace(strings.ReplaceAll(ev.Text, "secret", p.text)), nil
		}
	case "terminate":
		if ev.Kind == pluginapi.EventTextDelta && strings.Contains(ev.Text, "secret") {
			return pluginapi.Terminate(pluginapi.Block(0, "policy", "cut")), nil
		}
	}
	return pluginapi.Pass(), nil
}

func (p *phasePlugin) OnStreamEnd(_ context.Context, x *pluginapi.Exchange) (pluginapi.Decision, error) {
	if p.stream == "end_block" && strings.Contains(x.Stream.Text(0), "secret") {
		return pluginapi.Block(0, "policy", "ended"), nil
	}
	return pluginapi.Allow(), nil
}

func phaseChains(t *testing.T, cfg map[string]string, steps ...guardrails.StepReference) *plugins.Chains {
	t.Helper()
	raw, _ := json.Marshal(cfg)
	return newGuardrailChains(t, nil, steps, []func() pluginapi.Plugin{newPhasePlugin},
		guardrails.Definition{Name: "phase", Type: "phase_test", Config: raw})
}

func phaseHandler(t *testing.T, inner core.RoutableProvider, chains *plugins.Chains) *Handler {
	t.Helper()
	return phaseHandlerWithLogger(t, inner, nil, chains)
}

func phaseHandlerWithLogger(t *testing.T, inner core.RoutableProvider, logger auditlog.LoggerInterface, chains *plugins.Chains) *Handler {
	t.Helper()
	handler := newHandler(inner, logger, nil, nil, nil, nil, nil, guardrails.NewWorkflowRequestPatcher(staticChainsResolver{chains: chains}))
	handler.pluginChains = staticChainsResolver{chains: chains}
	return handler
}

func doChat(t *testing.T, handler *Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("Content-Type", "application/json")
	req.Body = &explodingReadCloser{}
	frame := core.NewRequestSnapshot(http.MethodPost, "/v1/chat/completions", nil, nil, nil, "application/json", []byte(body), false, "", nil)
	req = withRequestSnapshotAndPrompt(req, frame)
	rec := httptest.NewRecorder()
	if err := handler.ChatCompletion(e.NewContext(req, rec)); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	return rec
}

func phaseProvider() *capturingProvider {
	return &capturingProvider{
		supportedModels: []string{"gpt-5-nano"},
		providerTypes:   map[string]string{"gpt-5-nano": "mock"},
		response: &core.ChatResponse{
			ID: "chatcmpl_1", Object: "chat.completion", Model: "gpt-5-nano", Provider: "mock",
			Choices: []core.Choice{{Index: 0, FinishReason: "stop", Message: core.ResponseMessage{Role: "assistant", Content: "the secret answer"}}},
			Usage:   core.Usage{PromptTokens: 3, CompletionTokens: 5, TotalTokens: 8},
		},
		streamData: "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"model\":\"gpt-5-nano\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"the \"}}]}\n\n" +
			"data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"model\":\"gpt-5-nano\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"secret answer\"}}]}\n\n" +
			"data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"model\":\"gpt-5-nano\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
			"data: [DONE]\n\n",
	}
}

const chatBody = `{"model":"gpt-5-nano","messages":[{"role":"user","content":"hi"}]}`
const chatStreamBody = `{"model":"gpt-5-nano","stream":true,"messages":[{"role":"user","content":"hi"}]}`

func TestChatCompletion_PromptPluginRespondShortCircuits(t *testing.T) {
	chains := phaseChains(t, map[string]string{"prompt": "respond", "text": "I cannot help"}, guardrails.StepReference{Ref: "phase", Step: 1})
	for _, tt := range []struct {
		name string
		body string
		want string
	}{
		{"json", chatBody, `"content":"I cannot help"`},
		{"stream", chatStreamBody, `"content":"I cannot help"`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			inner := phaseProvider()
			rec := doChat(t, phaseHandler(t, inner, chains), tt.body)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d (%s)", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), tt.want) {
				t.Fatalf("body = %s, want %s", rec.Body.String(), tt.want)
			}
			if inner.capturedChatReq != nil {
				t.Fatal("provider was called despite short-circuit")
			}
			if tt.name == "stream" && (!strings.HasSuffix(strings.TrimSpace(rec.Body.String()), "[DONE]") || rec.Header().Get("Content-Type") != "text/event-stream") {
				t.Fatalf("stream body = %s", rec.Body.String())
			}
		})
	}
}

func TestChatCompletion_ResponsePhaseDecisions(t *testing.T) {
	tests := []struct {
		name       string
		cfg        map[string]string
		wantStatus int
		wantBody   string
		wantHeader string
	}{
		{"edit", map[string]string{"response": "edit", "text": "redacted"}, 200, `"content":"redacted"`, ""},
		{"respond", map[string]string{"response": "respond", "text": "canned"}, 200, `"content":"canned"`, ""},
		{"block", map[string]string{"response": "block"}, 502, `"code":"policy"`, ""},
		{"warn", map[string]string{"response": "warn"}, 200, `"content":"the secret answer"`, "warn; code=pii"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chains := phaseChains(t, tt.cfg, guardrails.StepReference{Ref: "phase", Phase: pluginapi.KindResponse, Step: 1})
			rec := doChat(t, phaseHandler(t, phaseProvider(), chains), chatBody)
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (%s)", rec.Code, tt.wantStatus, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), tt.wantBody) {
				t.Fatalf("body = %s, want %s", rec.Body.String(), tt.wantBody)
			}
			if got := rec.Header().Get(plugins.GuardrailHeader); got != tt.wantHeader {
				t.Fatalf("header = %q, want %q", got, tt.wantHeader)
			}
			// The provider billed the original completion; every 200 keeps its usage.
			if rec.Code == http.StatusOK && !strings.Contains(rec.Body.String(), `"total_tokens":8`) {
				t.Fatalf("body = %s, want provider usage", rec.Body.String())
			}
		})
	}
}

func TestChatCompletion_StreamPhase(t *testing.T) {
	tests := []struct {
		name    string
		cfg     map[string]string
		phase   pluginapi.Kind
		want    []string
		notWant string
	}{
		{"transform replace", map[string]string{"stream": "replace", "text": "[x]"}, pluginapi.KindStream, []string{`[x] answer`, "[DONE]"}, "secret"},
		{"transform terminate", map[string]string{"stream": "terminate"}, pluginapi.KindStream, []string{`"policy"`, "content_filter", "[DONE]"}, "secret answer"},
		{"end block", map[string]string{"stream": "end_block"}, pluginapi.KindStream, []string{"secret answer", `"policy"`, "[DONE]"}, ""},
		{"buffered response edit", map[string]string{"stream": "buffer", "response": "edit", "text": "assembled"}, pluginapi.KindStream, []string{`"content":"assembled"`, "[DONE]"}, "secret"},
		{"response chain block on stream", map[string]string{"response": "block"}, pluginapi.KindResponse, []string{`"policy"`, "[DONE]"}, "secret"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chains := phaseChains(t, tt.cfg, guardrails.StepReference{Ref: "phase", Phase: tt.phase, Step: 1})
			rec := doChat(t, phaseHandler(t, phaseProvider(), chains), chatStreamBody)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d (%s)", rec.Code, rec.Body.String())
			}
			body := rec.Body.String()
			for _, want := range tt.want {
				if !strings.Contains(body, want) {
					t.Fatalf("body = %s, want %q", body, want)
				}
			}
			if tt.notWant != "" && strings.Contains(body, tt.notWant) {
				t.Fatalf("body = %s, must not contain %q", body, tt.notWant)
			}
		})
	}
}

func TestCanForwardMessagesNatively_DisabledByPostResponsePlugins(t *testing.T) {
	chains := phaseChains(t, map[string]string{"response": "warn"}, guardrails.StepReference{Ref: "phase", Phase: pluginapi.KindResponse, Step: 1})
	svc := &translatedInferenceService{provider: &capturingProvider{}, pluginChains: staticChainsResolver{chains: chains}}
	workflow := &core.Workflow{ProviderType: anthropicProviderType}
	if svc.canForwardMessagesNatively(context.Background(), workflow) {
		t.Fatal("native fast path allowed with a response chain")
	}
	svc.pluginChains = staticChainsResolver{chains: &plugins.Chains{}}
	if svc.hasPostResponsePlugins(context.Background()) {
		t.Fatal("empty chains reported as post-response plugins")
	}
}

// usageStreamProvider streams a completion whose final chunk carries usage,
// as providers do when include_usage is forced upstream.
func usageStreamProvider() *capturingProvider {
	p := phaseProvider()
	p.streamData = "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"model\":\"gpt-5-nano\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"the secret answer\"}}]}\n\n" +
		"data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"model\":\"gpt-5-nano\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"model\":\"gpt-5-nano\",\"choices\":[],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":5,\"total_tokens\":8}}\n\n" +
		"data: [DONE]\n\n"
	return p
}

func TestChatCompletion_BufferedStreamKeepsProviderUsage(t *testing.T) {
	tests := []struct {
		name string
		cfg  map[string]string
		want []string
	}{
		{"block", map[string]string{"stream": "buffer", "response": "block"}, []string{`"policy"`, `"total_tokens":8`}},
		{"edit", map[string]string{"stream": "buffer", "response": "edit", "text": "clean"}, []string{`"content":"clean"`, `"total_tokens":8`}},
		{"respond", map[string]string{"stream": "buffer", "response": "respond", "text": "canned"}, []string{`"content":"canned"`, `"total_tokens":8`}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chains := phaseChains(t, tt.cfg, guardrails.StepReference{Ref: "phase", Phase: pluginapi.KindStream, Step: 1})
			// The client did not ask for usage; the provider chunk is relayed anyway.
			rec := doChat(t, phaseHandler(t, usageStreamProvider(), chains), chatStreamBody)
			body := rec.Body.String()
			for _, want := range tt.want {
				if !strings.Contains(body, want) {
					t.Fatalf("body = %s, want %q", body, want)
				}
			}
			if strings.Count(body, "[DONE]") != 1 {
				t.Fatalf("body = %s, want exactly one [DONE]", body)
			}
		})
	}
}

func TestChatCompletion_TwoBufferedMutatingInstances(t *testing.T) {
	first, _ := json.Marshal(map[string]string{"stream": "buffer", "response": "edit", "text": "first"})
	second, _ := json.Marshal(map[string]string{"stream": "buffer", "response": "edit", "text": "second"})
	chains := newGuardrailChains(t, nil, []guardrails.StepReference{
		{Ref: "one", Phase: pluginapi.KindStream, Step: 1},
		{Ref: "two", Phase: pluginapi.KindStream, Step: 2},
	}, []func() pluginapi.Plugin{newPhasePlugin},
		guardrails.Definition{Name: "one", Type: "phase_test", Config: first},
		guardrails.Definition{Name: "two", Type: "phase_test", Config: second})
	rec := doChat(t, phaseHandler(t, phaseProvider(), chains), chatStreamBody)
	body := rec.Body.String()
	if strings.Contains(body, "plugin_failure") || !strings.Contains(body, `"content":"second"`) {
		t.Fatalf("body = %s, want the second editor's text and no plugin_failure", body)
	}
}

func TestChatCompletion_StreamWarnHeaderCommitsWithFirstBytes(t *testing.T) {
	chains := phaseChains(t, map[string]string{"response": "warn"}, guardrails.StepReference{Ref: "phase", Phase: pluginapi.KindResponse, Step: 1})
	rec := doChat(t, phaseHandler(t, phaseProvider(), chains), chatStreamBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get(plugins.GuardrailHeader); got != "warn; code=pii" {
		t.Fatalf("header = %q, want the buffered response's warn", got)
	}
	if body := rec.Body.String(); !strings.Contains(body, "secret answer") || !strings.HasSuffix(strings.TrimSpace(body), "[DONE]") {
		t.Fatalf("body = %s", body)
	}
}

// Requests hold their plugin instances only while a phase runs (the whole
// stream, for the stream phase) and release them afterwards, so a replaced
// guardrail is not closed underneath a request and does not leak a hold.
func TestChatCompletion_PluginChainsReleasedAfterRequest(t *testing.T) {
	for _, tt := range []struct{ name, body string }{{"json", chatBody}, {"stream", chatStreamBody}} {
		t.Run(tt.name, func(t *testing.T) {
			chains := phaseChains(t, map[string]string{"prompt": "warn", "response": "warn", "stream": "replace", "text": "x"},
				guardrails.StepReference{Ref: "phase", Phase: pluginapi.KindPrompt, Step: 1},
				guardrails.StepReference{Ref: "phase", Phase: pluginapi.KindResponse, Step: 1},
				guardrails.StepReference{Ref: "phase", Phase: pluginapi.KindStream, Step: 1})
			rec := doChat(t, phaseHandler(t, phaseProvider(), chains), tt.body)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d (%s)", rec.Code, rec.Body.String())
			}
			for _, chain := range []*plugins.Chain{chains.Prompt, chains.Response, chains.Stream} {
				for _, inst := range chain.Instances() {
					if inst.Held() {
						t.Fatalf("instance %q still held after the request", inst.Name)
					}
				}
			}
		})
	}
}

// phaseChainsNamed builds chains from several instances of the phase plugin,
// one definition per name.
func phaseChainsNamed(t *testing.T, cfgs map[string]map[string]string, steps ...guardrails.StepReference) *plugins.Chains {
	t.Helper()
	definitions := make([]guardrails.Definition, 0, len(cfgs))
	for name, cfg := range cfgs {
		raw, _ := json.Marshal(cfg)
		definitions = append(definitions, guardrails.Definition{Name: name, Type: "phase_test", Config: raw})
	}
	return newGuardrailChains(t, nil, steps, []func() pluginapi.Plugin{newPhasePlugin}, definitions...)
}

// An instance later in the stream chain must see, at the end of the stream,
// the text an earlier instance replaced, not the original.
func TestChatCompletion_StreamEndSeesTransformedText(t *testing.T) {
	chains := phaseChainsNamed(t, map[string]map[string]string{
		"scrub": {"stream": "replace", "text": "[x]"},
		"watch": {"stream": "end_block"},
	}, guardrails.StepReference{Ref: "scrub", Phase: pluginapi.KindStream, Step: 1}, guardrails.StepReference{Ref: "watch", Phase: pluginapi.KindStream, Step: 2})
	rec := doChat(t, phaseHandler(t, phaseProvider(), chains), chatStreamBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "[x] answer") || strings.Contains(body, "secret") {
		t.Fatalf("body = %s, want the scrubbed text only", body)
	}
	if strings.Contains(body, `"policy"`) {
		t.Fatalf("body = %s: the observing instance blocked over already-scrubbed text", body)
	}
}

// A buffered instance's MaxBufferBytes must not cap the buffer the response
// chain shares; without a response chain the cap applies.
func TestChatCompletion_BufferCapIsNotBorrowedByResponseChain(t *testing.T) {
	steps := func(withResponse bool) []guardrails.StepReference {
		s := []guardrails.StepReference{{Ref: "small", Phase: pluginapi.KindStream, Step: 1}}
		if withResponse {
			s = append(s, guardrails.StepReference{Ref: "later", Phase: pluginapi.KindResponse, Step: 1})
		}
		return s
	}
	cfgs := map[string]map[string]string{
		"small": {"stream": "buffer", "max_buffer": "16"},
		"later": {"response": "warn"},
	}
	t.Run("response chain uses the default", func(t *testing.T) {
		rec := doChat(t, phaseHandler(t, phaseProvider(), phaseChainsNamed(t, cfgs, steps(true)...)), chatStreamBody)
		body := rec.Body.String()
		if rec.Code != http.StatusOK || !strings.Contains(body, "secret answer") || !strings.Contains(body, "[DONE]") {
			t.Fatalf("status = %d body = %s, want the full answer", rec.Code, body)
		}
	})
	t.Run("alone the cap applies", func(t *testing.T) {
		rec := doChat(t, phaseHandler(t, phaseProvider(), phaseChainsNamed(t, cfgs, steps(false)...)), chatStreamBody)
		body := rec.Body.String()
		if strings.Contains(body, "secret answer") {
			t.Fatalf("body = %s, want the stream cut by the buffer cap", body)
		}
	})
}

// A blocked request belongs to its resolved workflow like any other outcome,
// so the audit entry can carry it.
func TestChatCompletion_BlockedRequestKeepsWorkflow(t *testing.T) {
	chains := phaseChains(t, map[string]string{"prompt": "block"}, guardrails.StepReference{Ref: "phase", Phase: pluginapi.KindPrompt, Step: 1})
	handler := phaseHandler(t, phaseProvider(), chains)
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("Content-Type", "application/json")
	req.Body = &explodingReadCloser{}
	frame := core.NewRequestSnapshot(http.MethodPost, "/v1/chat/completions", nil, nil, nil, "application/json", []byte(chatBody), false, "", nil)
	req = withRequestSnapshotAndPrompt(req, frame)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	if err := handler.ChatCompletion(c); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d (%s), want the block", rec.Code, rec.Body.String())
	}
	if core.GetWorkflow(c.Request().Context()) == nil {
		t.Fatal("blocked request lost its resolved workflow")
	}
}

// A prompt edit is applied back to the request once, after the whole chain;
// the audit revision of the editing instance carries that applied body under
// the same gates as the ingress rewriters.
func TestChatCompletion_PromptEditRecordsRevisionBody(t *testing.T) {
	run := func(t *testing.T, logBodies, logRevisionBodies bool) *auditlog.LogEntry {
		t.Helper()
		chains := phaseChains(t, map[string]string{"prompt": "edit", "text": "rewritten by guardrail"},
			guardrails.StepReference{Ref: "phase", Phase: pluginapi.KindPrompt, Step: 1})
		auditLogger := &capturingAuditLogger{config: auditlog.Config{Enabled: true, LogBodies: logBodies, LogRevisionBodies: logRevisionBodies}}
		handler := phaseHandlerWithLogger(t, phaseProvider(), auditLogger, chains)
		e := echo.New()
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		req.Header.Set("Content-Type", "application/json")
		req.Body = &explodingReadCloser{}
		frame := core.NewRequestSnapshot(http.MethodPost, "/v1/chat/completions", nil, nil, nil, "application/json", []byte(chatBody), false, "", nil)
		req = withRequestSnapshotAndPrompt(req, frame)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		entry := &auditlog.LogEntry{Data: &auditlog.LogData{}}
		c.Set(string(auditlog.LogEntryKey), entry)
		if err := handler.ChatCompletion(c); err != nil {
			t.Fatalf("handler returned error: %v", err)
		}
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d (%s)", rec.Code, rec.Body.String())
		}
		revisions := entry.Data.RequestRevisions
		if len(revisions) != 1 {
			t.Fatalf("expected 1 revision, got %d: %+v", len(revisions), revisions)
		}
		revision := revisions[0]
		if revision.Seq != 1 || revision.Rewriter != "phase" || revision.NoChange {
			t.Errorf("revision must name the editing instance as a change: %+v", revision)
		}
		if revision.BytesBefore == 0 || revision.BytesAfter == 0 {
			t.Errorf("revision sizes missing: %+v", revision)
		}
		return entry
	}

	t.Run("with body logging", func(t *testing.T) {
		revision := run(t, true, true).Data.RequestRevisions[0]
		body, _ := json.Marshal(revision.Body)
		if !strings.Contains(string(body), "rewritten by guardrail") || strings.Contains(string(body), `"hi"`) {
			t.Errorf("revision body must be the applied request: %s", body)
		}
	})

	t.Run("without body logging", func(t *testing.T) {
		if revision := run(t, false, true).Data.RequestRevisions[0]; revision.Body != nil {
			t.Errorf("body must not be captured when body logging is off: %+v", revision)
		}
	})

	t.Run("without revision body logging", func(t *testing.T) {
		if revision := run(t, true, false).Data.RequestRevisions[0]; revision.Body != nil {
			t.Errorf("body must not be captured when revision body logging is off: %+v", revision)
		}
	})
}

// A prompt chain's edits are one revision naming the instances that actually
// edited the prompt, with the applied body; a mutating instance that changed
// nothing is not an editor, and an objection is its own no-change entry.
func TestChatCompletion_PromptEditsRecordOneRevision(t *testing.T) {
	run := func(t *testing.T, cfgs map[string]map[string]string, steps ...guardrails.StepReference) []auditlog.RequestRevisionSnapshot {
		t.Helper()
		chains := phaseChainsNamed(t, cfgs, steps...)
		auditLogger := &capturingAuditLogger{config: auditlog.Config{Enabled: true, LogBodies: true, LogRevisionBodies: true}}
		handler := phaseHandlerWithLogger(t, phaseProvider(), auditLogger, chains)
		e := echo.New()
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		req.Header.Set("Content-Type", "application/json")
		req.Body = &explodingReadCloser{}
		frame := core.NewRequestSnapshot(http.MethodPost, "/v1/chat/completions", nil, nil, nil, "application/json", []byte(chatBody), false, "", nil)
		req = withRequestSnapshotAndPrompt(req, frame)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		entry := &auditlog.LogEntry{Data: &auditlog.LogData{}}
		c.Set(string(auditlog.LogEntryKey), entry)
		if err := handler.ChatCompletion(c); err != nil {
			t.Fatalf("handler returned error: %v", err)
		}
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d (%s)", rec.Code, rec.Body.String())
		}
		return entry.Data.RequestRevisions
	}
	bodyText := func(revision auditlog.RequestRevisionSnapshot) string {
		body, _ := json.Marshal(revision.Body)
		return string(body)
	}

	t.Run("editor then no-op mutator", func(t *testing.T) {
		revisions := run(t, map[string]map[string]string{
			"editor": {"prompt": "edit", "text": "rewritten by editor"},
			"noop":   {},
		}, guardrails.StepReference{Ref: "editor", Phase: pluginapi.KindPrompt, Step: 1},
			guardrails.StepReference{Ref: "noop", Phase: pluginapi.KindPrompt, Step: 2})
		if len(revisions) != 1 || revisions[0].Rewriter != "editor" || revisions[0].NoChange {
			t.Fatalf("expected one changed revision for the editor, got %+v", revisions)
		}
		if !strings.Contains(bodyText(revisions[0]), "rewritten by editor") {
			t.Errorf("editor revision must carry the applied body: %+v", revisions[0])
		}
	})

	t.Run("editor then editor", func(t *testing.T) {
		revisions := run(t, map[string]map[string]string{
			"first":  {"prompt": "edit", "text": "rewritten first"},
			"second": {"prompt": "edit", "text": "rewritten second"},
		}, guardrails.StepReference{Ref: "first", Phase: pluginapi.KindPrompt, Step: 1},
			guardrails.StepReference{Ref: "second", Phase: pluginapi.KindPrompt, Step: 2})
		if len(revisions) != 1 || revisions[0].Rewriter != "first, second" || revisions[0].NoChange {
			t.Fatalf("expected one changed revision naming both editors in order, got %+v", revisions)
		}
		if revisions[0].BytesBefore == 0 || revisions[0].BytesAfter == 0 || !strings.Contains(bodyText(revisions[0]), "rewritten second") {
			t.Errorf("the revision must carry the phase sizes and the applied body: %+v", revisions[0])
		}
		detail, _ := json.Marshal(revisions[0].Detail)
		if string(detail) != `{"phase":"prompt","edited":["first","second"]}` {
			t.Errorf("detail = %s", detail)
		}
	})

	t.Run("warning then editor", func(t *testing.T) {
		revisions := run(t, map[string]map[string]string{
			"watch":  {"prompt": "warn"},
			"editor": {"prompt": "edit", "text": "rewritten by editor"},
		}, guardrails.StepReference{Ref: "watch", Phase: pluginapi.KindPrompt, Step: 1},
			guardrails.StepReference{Ref: "editor", Phase: pluginapi.KindPrompt, Step: 2})
		if len(revisions) != 2 || revisions[0].Rewriter != "watch" || !revisions[0].NoChange || revisions[0].Body != nil {
			t.Fatalf("expected the warning as a no-change entry first, got %+v", revisions)
		}
		if revisions[1].Rewriter != "editor" || revisions[1].NoChange || !strings.Contains(bodyText(revisions[1]), "rewritten by editor") {
			t.Errorf("the edit revision must follow with the applied body: %+v", revisions[1])
		}
	})
}
