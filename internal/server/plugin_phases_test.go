package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/guardrails"
	"github.com/enterpilot/gomodel/internal/plugins"
	"github.com/enterpilot/gomodel/pluginapi"
)

// phasePlugin is a configurable test plugin covering prompt, response and
// stream hooks. Its behaviour is selected by config keys.
type phasePlugin struct {
	prompt   string
	response string
	stream   string
	text     string
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
		},
	}
}

func (p *phasePlugin) Init(_ context.Context, raw json.RawMessage, _ pluginapi.Host) error {
	var cfg struct{ Prompt, Response, Stream, Text string }
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return err
	}
	p.prompt, p.response, p.stream, p.text = cfg.Prompt, cfg.Response, cfg.Stream, cfg.Text
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
	return p.decide(p.prompt, x)
}

func (p *phasePlugin) OnResponse(_ context.Context, x *pluginapi.Exchange) (pluginapi.Decision, error) {
	return p.decide(p.response, x)
}

func (p *phasePlugin) StreamPolicy() pluginapi.StreamPolicy {
	if p.stream == "buffer" {
		return pluginapi.StreamPolicy{Mode: pluginapi.StreamBuffer}
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
	handler := newHandler(inner, nil, nil, nil, nil, nil, nil, guardrails.NewWorkflowRequestPatcher(staticChainsResolver{chains: chains}))
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
