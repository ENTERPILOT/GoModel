package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/auditlog"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/echotest"
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
	case "fail":
		return pluginapi.Allow(), errors.New("hook failed")
	case "edit":
		if x.Response != nil {
			return pluginapi.Allow(), x.Response.ReplaceText(0, p.text)
		}
	}
	return pluginapi.Allow(), nil
}

func (p *phasePlugin) OnPrompt(_ context.Context, x *pluginapi.Exchange) (pluginapi.Decision, error) {
	if p.prompt == "edit" || p.prompt == "edit_fail" {
		if last := x.Prompt.LastUser(); last != nil {
			if err := x.Prompt.SetText(last.ID, 0, p.text); err != nil {
				return pluginapi.Allow(), err
			}
			if p.prompt == "edit_fail" {
				return pluginapi.Allow(), errors.New("failed after editing")
			}
			return pluginapi.Allow(), nil
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
	case "fail_event":
		if ev.Kind == pluginapi.EventTextDelta {
			return pluginapi.StreamDecision{}, errors.New("event hook failed")
		}
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
	c, rec := chatContext(t, body)
	err := handler.ChatCompletion(c)
	require.NoError(t, err)

	return rec
}

// chatContext builds a chat completion context whose body is only reachable
// through the request snapshot, as it is after ingress middleware ran.
func chatContext(t *testing.T, body string, opts ...echotest.Option) (*echo.Context, *httptest.ResponseRecorder) {
	t.Helper()
	c, rec := echotest.Post(t, "/v1/chat/completions", &explodingReadCloser{}, opts...)
	frame := core.NewRequestSnapshot(http.MethodPost, "/v1/chat/completions", nil, nil, nil, "application/json", []byte(body), false, "", nil)
	c.SetRequest(withRequestSnapshotAndPrompt(c.Request(), frame))
	return c, rec
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
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			require.Contains(t, rec.Body.String(), tt.want)
			require.Nil(t, inner.capturedChatReq)

			if tt.name == "stream" {
				require.Equal(t, "text/event-stream", rec.Header().Get("Content-Type"))
				require.True(t, strings.HasSuffix(strings.TrimSpace(rec.Body.String()), "[DONE]"), "stream body = %s", rec.Body.String())
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
			require.Equal(t, tt.wantStatus, rec.Code, rec.Body.String())
			require.Contains(t, rec.Body.String(), tt.wantBody)
			got := rec.Header().Get(plugins.GuardrailHeader)
			require.Equal(t, tt.wantHeader, got)

			// The provider billed the original completion; every 200 keeps its usage.
			if rec.Code == http.StatusOK {
				require.Contains(t, rec.Body.String(), `"total_tokens":8`, "want provider usage")
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
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

			body := rec.Body.String()
			for _, want := range tt.want {
				require.Contains(t, body, want)
			}
			if tt.notWant != "" {
				require.NotContains(t, body, tt.notWant)
			}
		})
	}
}

func TestCanForwardMessagesNatively_DisabledByPostResponsePlugins(t *testing.T) {
	chains := phaseChains(t, map[string]string{"response": "warn"}, guardrails.StepReference{Ref: "phase", Phase: pluginapi.KindResponse, Step: 1})
	svc := &translatedInferenceService{provider: &capturingProvider{}, pluginChains: staticChainsResolver{chains: chains}}
	workflow := &core.Workflow{ProviderType: anthropicProviderType}
	require.False(t, svc.canForwardMessagesNatively(context.Background(), workflow, false))

	svc.pluginChains = staticChainsResolver{chains: &plugins.Chains{}}
	require.False(t, svc.hasPostResponsePlugins(context.Background()))
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
				require.Contains(t, body, want)
			}
			require.Equal(t, 1, strings.Count(body, "[DONE]"), "body = %s, want exactly one [DONE]", body)
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
	require.NotContains(t, body, "plugin_failure")
	require.Contains(t, body, `"content":"second"`)
}

func TestChatCompletion_StreamWarnHeaderCommitsWithFirstBytes(t *testing.T) {
	chains := phaseChains(t, map[string]string{"response": "warn"}, guardrails.StepReference{Ref: "phase", Phase: pluginapi.KindResponse, Step: 1})
	rec := doChat(t, phaseHandler(t, phaseProvider(), chains), chatStreamBody)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	got := rec.Header().Get(plugins.GuardrailHeader)
	require.Equal(t, "warn; code=pii", got)
	body := rec.Body.String()
	require.Contains(t, body, "secret answer")
	require.True(t, strings.HasSuffix(strings.TrimSpace(body), "[DONE]"))
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
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

			for _, chain := range []*plugins.Chain{chains.Prompt, chains.Response, chains.Stream} {
				for _, inst := range chain.Instances() {
					require.False(t, inst.Held(), "instance %q still held after the request", inst.Name)
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
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	body := rec.Body.String()
	require.Contains(t, body, "[x] answer")
	require.NotContains(t, body, "secret")
	require.NotContains(t, body, `"policy"`, "body = %s: the observing instance blocked over already-scrubbed text", body)
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
		require.Equal(t, http.StatusOK, rec.Code)
		require.Contains(t, body, "secret answer")
		require.Contains(t, body, "[DONE]")
	})
	t.Run("alone the cap applies", func(t *testing.T) {
		rec := doChat(t, phaseHandler(t, phaseProvider(), phaseChainsNamed(t, cfgs, steps(false)...)), chatStreamBody)
		body := rec.Body.String()
		require.NotContains(t, body, "secret answer", "body = %s, want the stream cut by the buffer cap", body)
	})
}

// A blocked request belongs to its resolved workflow like any other outcome,
// so the audit entry can carry it.
func TestChatCompletion_BlockedRequestKeepsWorkflow(t *testing.T) {
	chains := phaseChains(t, map[string]string{"prompt": "block"}, guardrails.StepReference{Ref: "phase", Phase: pluginapi.KindPrompt, Step: 1})
	handler := phaseHandler(t, phaseProvider(), chains)
	c, rec := chatContext(t, chatBody)
	err := handler.ChatCompletion(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	require.NotNil(t, core.GetWorkflow(c.Request().Context()))
}

// A prompt edit is applied back to the request once, after the whole chain;
// the audit revision of the editing instance carries that applied body under
// the same gates as the ingress rewriters.
func TestChatCompletion_PromptEditRecordsRevisionBody(t *testing.T) {
	run := func(t *testing.T, logBodies, logRevisionBodies bool) *auditlog.LogEntry {
		t.Helper()
		chains := phaseChains(t, map[string]string{"prompt": "edit", "text": "rewritten by guardrail"},
			guardrails.StepReference{Ref: "phase", Phase: pluginapi.KindPrompt, Step: 1})
		auditLogger := &capturingAuditLogger{config: auditlog.Config{Enabled: true, LogBodies: logBodies, LogRevisionBodies: logRevisionBodies, LogGuardrailSteps: true}}
		handler := phaseHandlerWithLogger(t, phaseProvider(), auditLogger, chains)
		entry := &auditlog.LogEntry{Data: &auditlog.LogData{}}
		c, rec := chatContext(t, chatBody, echotest.WithValue(string(auditlog.LogEntryKey), entry))
		err := handler.ChatCompletion(c)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

		entry.CompleteRequestRevisions()
		revisions := entry.Data.RequestRevisions
		require.Len(t, revisions, 1)

		revision := revisions[0]
		assert.Equal(t, 1, revision.Seq)
		assert.Equal(t, "phase", revision.Rewriter)
		assert.False(t, revision.NoChange, "revision must name the editing instance as a change: %+v", revision)
		assert.NotZero(t, revision.BytesBefore)
		assert.NotZero(t, revision.BytesAfter, "revision sizes missing: %+v", revision)

		return entry
	}

	t.Run("with body logging", func(t *testing.T) {
		revision := run(t, true, true).Data.RequestRevisions[0]
		body, _ := json.Marshal(revision.Body)
		assert.Contains(t, string(body), "rewritten by guardrail")
		assert.NotContains(t, string(body), `"hi"`, "revision body must be the applied request: %s", body)
	})

	t.Run("without body logging", func(t *testing.T) {
		revision := run(t, false, true).Data.RequestRevisions[0]
		assert.Nil(t, revision.Body, "body must not be captured when body logging is off: %+v", revision)
	})

	t.Run("without revision body logging", func(t *testing.T) {
		revision := run(t, true, false).Data.RequestRevisions[0]
		assert.Nil(t, revision.Body, "body must not be captured when revision body logging is off: %+v", revision)
	})
}

// runPromptRevisions runs one chat request through the prompt chains built
// from cfgs under the given audit config and returns the audit entry's
// revision chain, complete.
func runPromptRevisions(t *testing.T, body string, cfg auditlog.Config, cfgs map[string]map[string]string, steps ...guardrails.StepReference) []auditlog.RequestRevisionSnapshot {
	t.Helper()
	return runPromptRevisionsExpecting(t, http.StatusOK, body, cfg, cfgs, steps...)
}

func runPromptRevisionsExpecting(t *testing.T, wantStatus int, body string, cfg auditlog.Config, cfgs map[string]map[string]string, steps ...guardrails.StepReference) []auditlog.RequestRevisionSnapshot {
	t.Helper()
	chains := phaseChainsNamed(t, cfgs, steps...)
	auditLogger := &capturingAuditLogger{config: cfg}
	handler := phaseHandlerWithLogger(t, phaseProvider(), auditLogger, chains)
	entry := &auditlog.LogEntry{Data: &auditlog.LogData{}}
	c, rec := chatContext(t, body, echotest.WithValue(string(auditlog.LogEntryKey), entry))
	err := handler.ChatCompletion(c)
	require.NoError(t, err)
	require.Equal(t, wantStatus, rec.Code, rec.Body.String())

	if len(auditLogger.entries) > 0 {
		// A streamed request is written by the stream observer.
		return auditLogger.entries[0].Data.RequestRevisions
	}
	entry.CompleteRequestRevisions()
	return entry.Data.RequestRevisions
}

func revisionBodyText(revision auditlog.RequestRevisionSnapshot) string {
	body, _ := json.Marshal(revision.Body)
	return string(body)
}

// Every editing step of a prompt chain is its own changed revision carrying
// the request as that step left it, so the chain reads step by step and the
// last body is what was forwarded; a mutating instance that changed nothing
// is a no-op, and an objection is a no-change entry in its place.
func TestChatCompletion_PromptEditsRecordEachStep(t *testing.T) {
	run := func(t *testing.T, body string, cfgs map[string]map[string]string, steps ...guardrails.StepReference) []auditlog.RequestRevisionSnapshot {
		t.Helper()
		return runPromptRevisions(t, body, auditlog.Config{Enabled: true, LogBodies: true, LogRevisionBodies: true, LogGuardrailSteps: true}, cfgs, steps...)
	}
	bodyText := revisionBodyText

	t.Run("editor then no-op mutator", func(t *testing.T) {
		revisions := run(t, chatBody, map[string]map[string]string{
			"editor": {"prompt": "edit", "text": "rewritten by editor"},
			"noop":   {},
		}, guardrails.StepReference{Ref: "editor", Phase: pluginapi.KindPrompt, Step: 1},
			guardrails.StepReference{Ref: "noop", Phase: pluginapi.KindPrompt, Step: 2})
		require.Len(t, revisions, 1)
		require.Equal(t, "editor", revisions[0].Rewriter)
		require.False(t, revisions[0].NoChange)
		assert.Contains(t, bodyText(revisions[0]), "rewritten by editor", "editor revision must carry the applied body: %+v", revisions[0])
	})

	for _, tt := range []struct{ name, body string }{{"editor then editor", chatBody}, {"editor then editor, streamed", chatStreamBody}} {
		t.Run(tt.name, func(t *testing.T) {
			revisions := run(t, tt.body, map[string]map[string]string{
				"first":  {"prompt": "edit", "text": "rewritten first"},
				"second": {"prompt": "edit", "text": "rewritten second"},
			}, guardrails.StepReference{Ref: "first", Phase: pluginapi.KindPrompt, Step: 1},
				guardrails.StepReference{Ref: "second", Phase: pluginapi.KindPrompt, Step: 2})
			require.Len(t, revisions, 2)
			require.Equal(t, "first", revisions[0].Rewriter)
			require.Equal(t, "second", revisions[1].Rewriter)
			require.False(t, revisions[0].NoChange)
			require.False(t, revisions[1].NoChange)
			assert.Equal(t, 1, revisions[0].Seq)
			assert.Equal(t, 2, revisions[1].Seq)

			first, second := bodyText(revisions[0]), bodyText(revisions[1])
			assert.Contains(t, first, "rewritten first")
			assert.NotContains(t, first, "rewritten second")
			assert.Contains(t, second, "rewritten second")
			assert.NotZero(t, revisions[0].BytesBefore)
			assert.NotZero(t, revisions[0].BytesAfter)
			assert.Equal(t, revisions[0].BytesAfter, revisions[1].BytesBefore)
			assert.NotZero(t, revisions[1].BytesAfter, "each step must be measured against the previous one: %+v", revisions)
		})
	}

	t.Run("warning then editor", func(t *testing.T) {
		revisions := run(t, chatBody, map[string]map[string]string{
			"watch":  {"prompt": "warn"},
			"editor": {"prompt": "edit", "text": "rewritten by editor"},
		}, guardrails.StepReference{Ref: "watch", Phase: pluginapi.KindPrompt, Step: 1},
			guardrails.StepReference{Ref: "editor", Phase: pluginapi.KindPrompt, Step: 2})
		require.Len(t, revisions, 2)
		require.Equal(t, "watch", revisions[0].Rewriter)
		require.True(t, revisions[0].NoChange)
		require.Nil(t, revisions[0].Body)
		assert.Equal(t, "editor", revisions[1].Rewriter)
		assert.False(t, revisions[1].NoChange)
		assert.Contains(t, bodyText(revisions[1]), "rewritten by editor", "the edit revision must follow with the applied body: %+v", revisions[1])

		// The warning inspected the original request: sized as such on both sides.
		assert.NotZero(t, revisions[0].BytesBefore)
		assert.Equal(t, revisions[0].BytesAfter, revisions[0].BytesBefore)
		assert.Equal(t, revisions[0].BytesAfter, revisions[1].BytesBefore, "no-change sizes must be the request the step saw: %+v", revisions)
	})

	t.Run("editor then warning", func(t *testing.T) {
		revisions := run(t, chatBody, map[string]map[string]string{
			"editor": {"prompt": "edit", "text": "rewritten by editor"},
			"watch":  {"prompt": "warn"},
		}, guardrails.StepReference{Ref: "editor", Phase: pluginapi.KindPrompt, Step: 1},
			guardrails.StepReference{Ref: "watch", Phase: pluginapi.KindPrompt, Step: 2})
		require.Len(t, revisions, 2)
		require.Equal(t, "watch", revisions[1].Rewriter)
		require.True(t, revisions[1].NoChange)

		// The warning inspected the edited request.
		assert.Equal(t, revisions[0].BytesAfter, revisions[1].BytesBefore)
		assert.Equal(t, revisions[0].BytesAfter, revisions[1].BytesAfter, "no-change sizes must follow the edit: %+v", revisions)
	})

	t.Run("editor then block", func(t *testing.T) {
		revisions := runPromptRevisionsExpecting(t, http.StatusBadRequest, chatBody, auditlog.Config{Enabled: true, LogBodies: true, LogRevisionBodies: true, LogGuardrailSteps: true}, map[string]map[string]string{
			"editor": {"prompt": "edit", "text": "rewritten by editor"},
			"gate":   {"prompt": "block"},
		}, guardrails.StepReference{Ref: "editor", Phase: pluginapi.KindPrompt, Step: 1},
			guardrails.StepReference{Ref: "gate", Phase: pluginapi.KindPrompt, Step: 2})
		require.Len(t, revisions, 2)
		require.Equal(t, "editor", revisions[0].Rewriter)
		require.False(t, revisions[0].NoChange)
		require.Equal(t, "gate", revisions[1].Rewriter)
		require.True(t, revisions[1].NoChange)

		// Nothing was forwarded, but the edit still shows what the block saw.
		assert.Contains(t, bodyText(revisions[0]), "rewritten by editor")
		assert.Equal(t, revisions[0].BytesAfter, revisions[1].BytesBefore, "the edit snapshot must survive a later block: %+v", revisions)
	})

	t.Run("edit then fail closed", func(t *testing.T) {
		revisions := runPromptRevisionsExpecting(t, http.StatusInternalServerError, chatBody, auditlog.Config{Enabled: true, LogBodies: true, LogRevisionBodies: true, LogGuardrailSteps: true}, map[string]map[string]string{
			"editor": {"prompt": "edit_fail", "text": "rewritten then failed"},
		}, guardrails.StepReference{Ref: "editor", Phase: pluginapi.KindPrompt, Step: 1})
		require.Len(t, revisions, 1)
		require.Equal(t, "editor", revisions[0].Rewriter)
		require.False(t, revisions[0].NoChange)

		// The failure came after the edit, so the trail shows the edited request.
		assert.Contains(t, bodyText(revisions[0]), "rewritten then failed")
		assert.NotEqual(t, revisions[0].BytesBefore, revisions[0].BytesAfter, "the edit snapshot must survive the instance's own failure: %+v", revisions[0])
		detail := revisions[0].Detail.(pluginDecisionDetail)
		assert.Contains(t, detail.Error, "failed after editing", "detail must carry the failure: %+v", detail)
	})
}

// A snapshot that cannot be applied leaves its revision a change with the
// error in its detail, and the size chain carries on from the previous step.
func TestPromptStepRevisionsKeepSizesWhenASnapshotFails(t *testing.T) {
	records := []plugins.DecisionRecord{
		{Phase: pluginapi.KindPrompt, Instance: "first", Decision: pluginapi.Allow(), Edited: true},
		{Phase: pluginapi.KindPrompt, Instance: "second", Decision: pluginapi.Allow(), Edited: true},
		{Phase: pluginapi.KindPrompt, Instance: "watch", Decision: pluginapi.Warn("pii", "found", nil)},
	}
	edits := []plugins.PromptEdit{
		{Instance: "first", Apply: func() (any, error) { return nil, errors.New("boom") }},
		{Instance: "second", Apply: func() (any, error) {
			return &core.ChatRequest{Model: "m", Messages: []core.Message{{Role: "user", Content: "rewritten"}}}, nil
		}},
	}
	revisions := promptStepRevisions(records, edits, []byte(`{"model":"m"}`), true)
	require.Len(t, revisions, 3)

	failed := revisions[0]
	assert.False(t, failed.NoChange)
	assert.Equal(t, 13, failed.BytesBefore)
	assert.Equal(t, 13, failed.BytesAfter)
	assert.Nil(t, failed.Body)
	assert.Contains(t, failed.Detail.(pluginDecisionDetail).Error, "boom", "failed snapshot revision = %+v", failed)

	applied := revisions[1]
	assert.False(t, applied.NoChange)
	assert.Equal(t, 13, applied.BytesBefore)
	assert.NotZero(t, applied.BytesAfter)
	assert.NotNil(t, applied.Body, "applied snapshot revision = %+v", applied)
	assert.Equal(t, applied.BytesAfter, revisions[2].BytesBefore)
	assert.Equal(t, applied.BytesAfter, revisions[2].BytesAfter)
	assert.True(t, revisions[2].NoChange, "no-change revision after the chain = %+v", revisions[2])
}

// With step logging off the phase keeps no snapshots: the chain's edits are
// one changed revision naming the editors and carrying the request as
// forwarded, after the objections.
func TestChatCompletion_PromptEditsRecordOneRevisionWithoutStepLogging(t *testing.T) {
	revisions := runPromptRevisions(t, chatBody, auditlog.Config{Enabled: true, LogBodies: true, LogRevisionBodies: true}, map[string]map[string]string{
		"watch":  {"prompt": "warn"},
		"first":  {"prompt": "edit", "text": "rewritten first"},
		"second": {"prompt": "edit", "text": "rewritten second"},
	}, guardrails.StepReference{Ref: "first", Phase: pluginapi.KindPrompt, Step: 1},
		guardrails.StepReference{Ref: "watch", Phase: pluginapi.KindPrompt, Step: 2},
		guardrails.StepReference{Ref: "second", Phase: pluginapi.KindPrompt, Step: 3})
	require.Len(t, revisions, 2)
	require.Equal(t, "watch", revisions[0].Rewriter)
	require.True(t, revisions[0].NoChange)

	chain := revisions[1]
	assert.Equal(t, "first, second", chain.Rewriter)
	assert.False(t, chain.NoChange)
	assert.Equal(t, 2, chain.Seq)
	assert.NotZero(t, chain.BytesBefore)
	assert.NotZero(t, chain.BytesAfter, "chain revision = %+v", chain)
	body := revisionBodyText(chain)
	assert.Contains(t, body, "rewritten second")
	assert.NotContains(t, body, "rewritten first")

	detail, _ := json.Marshal(chain.Detail)
	assert.Equal(t, `{"phase":"prompt","edited":["first","second"]}`, string(detail), "detail = %s", detail)
}
