package server

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/openai"
	"github.com/enterpilot/gomodel/internal/usage"
)

const fastPathChatRequestBody = `{"model":"gpt-4o-mini","messages":[{"role":"user","content":"Hi"}]}`

// fastPathUpstreamBody carries members the typed core.ChatResponse does not
// model (annotations, native_finish_reason, usage.cost) so the test proves the
// fast path relays the provider body verbatim instead of re-encoding it.
const fastPathUpstreamBody = `{"id":"chatcmpl-123","object":"chat.completion","model":"gpt-4o-mini","choices":[{"index":0,"message":{"role":"assistant","content":"Hello","annotations":[]},"native_finish_reason":"stop","finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":3,"total_tokens":10,"cost":0.0001}}`

func fastPathJSONMock(body string) *mockProvider {
	return &mockProvider{
		supportedModels: []string{"gpt-4o-mini"},
		providerTypes:   map[string]string{"gpt-4o-mini": "openai"},
		providerNames:   map[string]string{"gpt-4o-mini": "openai_test"},
		response:        &core.ChatResponse{ID: "typed", Object: "chat.completion", Model: "gpt-4o-mini"},
		streamData:      "data: {\"id\":\"typed\"}\n\ndata: [DONE]\n\n",
		passthroughResponse: &core.PassthroughResponse{
			StatusCode: http.StatusOK,
			Headers:    map[string][]string{"Content-Type": {"application/json"}},
			Body:       io.NopCloser(strings.NewReader(body)),
		},
	}
}

func postChatCompletion(t *testing.T, handler *Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	if err := handler.ChatCompletion(e.NewContext(req, rec)); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	return rec
}

// postChatCompletionThroughStack sends the request through the full server
// middleware stack (request snapshot, whitebox ingress capture, workflow
// resolution) the way production requests arrive, instead of calling the
// handler directly.
func postChatCompletionThroughStack(t *testing.T, mock *mockProvider, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	New(mock, &Config{}).ServeHTTP(rec, req)
	return rec
}

// chatEntryPoints lists the two ways a chat request reaches the handler in
// tests: the bare handler (no ingress capture, so the buffered body is the
// only raw-model source) and the full middleware stack (whitebox hints
// present, and already overwritten with the resolved model by dispatch time).
var chatEntryPoints = []struct {
	name string
	post func(t *testing.T, mock *mockProvider, body string) *httptest.ResponseRecorder
}{
	{name: "handler only", post: func(t *testing.T, mock *mockProvider, body string) *httptest.ResponseRecorder {
		return postChatCompletion(t, NewHandler(mock, nil, nil, nil), body)
	}},
	{name: "middleware stack", post: postChatCompletionThroughStack},
}

func TestChatCompletion_FastPathProxiesNonStreamingBodyVerbatim(t *testing.T) {
	mock := fastPathJSONMock(fastPathUpstreamBody)
	rec := postChatCompletion(t, NewHandler(mock, nil, nil, nil), fastPathChatRequestBody)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}
	want := strings.TrimSuffix(fastPathUpstreamBody, "}") + `,"provider":"openai"}`
	if got := rec.Body.String(); got != want {
		t.Fatalf("body = %s\nwant %s", got, want)
	}
	if mock.chatCompletionCalls != 0 {
		t.Fatalf("ChatCompletion calls = %d, want 0 (fast path)", mock.chatCompletionCalls)
	}
	if mock.lastPassthroughReq == nil {
		t.Fatal("passthrough request = nil, want fast path dispatch")
	}
	if mock.lastPassthroughReq.Stream {
		t.Fatal("passthrough request marked as streaming")
	}
	if got := mock.lastPassthroughReq.Model; got != "gpt-4o-mini" {
		t.Fatalf("passthrough model = %q, want gpt-4o-mini", got)
	}
	if got := readPassthroughRequestBody(t, mock.lastPassthroughReq.Body); got != fastPathChatRequestBody {
		t.Fatalf("passthrough body = %q, want %q", got, fastPathChatRequestBody)
	}
}

func TestChatCompletion_FastPathLogsUsageFromJSONBody(t *testing.T) {
	usageLog := &collectingUsageLogger{config: usage.Config{Enabled: true}}
	mock := fastPathJSONMock(fastPathUpstreamBody)
	rec := postChatCompletion(t, NewHandler(mock, nil, usageLog, nil), fastPathChatRequestBody)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if len(usageLog.entries) != 1 {
		t.Fatalf("usage entries = %d, want 1", len(usageLog.entries))
	}
	entry := usageLog.entries[0]
	if entry.InputTokens != 7 || entry.OutputTokens != 3 {
		t.Fatalf("usage tokens = %d/%d, want 7/3", entry.InputTokens, entry.OutputTokens)
	}
	if entry.ProviderName != "openai_test" {
		t.Fatalf("ProviderName = %q, want openai_test", entry.ProviderName)
	}
}

func TestChatCompletion_FastPathReplacesUpstreamProviderAndDropsValidators(t *testing.T) {
	upstream := `{"id":"gen-1","object":"chat.completion","model":"gpt-4o-mini","provider":"OpenAI","choices":[{"provider":"nested"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`
	mock := fastPathJSONMock(upstream)
	mock.passthroughResponse.Headers["ETag"] = []string{`"abc"`}
	mock.passthroughResponse.Headers["Content-MD5"] = []string{"md5"}
	mock.passthroughResponse.Headers["Digest"] = []string{"sha-256=x"}
	mock.passthroughResponse.Headers["X-Request-Id"] = []string{"req-1"}
	rec := postChatCompletion(t, NewHandler(mock, nil, nil, nil), fastPathChatRequestBody)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	want := strings.Replace(upstream, `"provider":"OpenAI"`, `"provider":"openai"`, 1)
	if got := rec.Body.String(); got != want {
		t.Fatalf("body = %s\nwant %s", got, want)
	}
	if strings.Count(rec.Body.String(), `"provider":`) != 2 {
		t.Fatalf("body has duplicate or missing provider members: %s", rec.Body.String())
	}
	for _, header := range []string{"ETag", "Content-MD5", "Digest"} {
		if got := rec.Header().Get(header); got != "" {
			t.Fatalf("%s = %q, want dropped after body rewrite", header, got)
		}
	}
	if got := rec.Header().Get("X-Request-Id"); got != "req-1" {
		t.Fatalf("X-Request-Id = %q, want forwarded", got)
	}
}

func TestChatCompletionStreaming_FastPathRelaysSSEWhenNoPlanApplies(t *testing.T) {
	streamData := "data: {\"id\":\"chatcmpl-123\",\"choices\":[{\"delta\":{\"content\":\"Hello\"}}]}\n\ndata: [DONE]\n\n"
	mock := fastPathJSONMock(streamData)
	mock.passthroughResponse.Headers["Content-Type"] = []string{"text/event-stream"}
	reqBody := `{"model":"gpt-4o-mini","stream":true,"messages":[{"role":"user","content":"Hi"}]}`
	rec := postChatCompletion(t, NewHandler(mock, nil, nil, nil), reqBody)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); got != streamData {
		t.Fatalf("stream body = %q, want %q", got, streamData)
	}
	if mock.chatCompletionCalls != 0 {
		t.Fatalf("ChatCompletion calls = %d, want 0", mock.chatCompletionCalls)
	}
	if mock.lastPassthroughReq == nil || !mock.lastPassthroughReq.Stream {
		t.Fatalf("passthrough request = %+v, want Stream: true", mock.lastPassthroughReq)
	}
}

func TestChatCompletion_FastPathSkippedWhenRawModelIsNotCanonical(t *testing.T) {
	for _, entry := range chatEntryPoints {
		t.Run(entry.name, func(t *testing.T) {
			mock := fastPathJSONMock(fastPathUpstreamBody)
			rec := entry.post(t, mock, `{"model":" gpt-4o-mini ","messages":[{"role":"user","content":"Hi"}]}`)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
			}
			if mock.lastPassthroughReq != nil {
				t.Fatal("padded model took the fast path; the upstream would have received it unnormalized")
			}
			if mock.chatCompletionCalls != 1 {
				t.Fatalf("ChatCompletion calls = %d, want 1", mock.chatCompletionCalls)
			}
		})
	}
}

func TestChatCompletion_FastPathSkippedWhenPlannerApplies(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "non-streaming", body: fastPathChatRequestBody},
		{name: "streaming", body: `{"model":"gpt-4o-mini","stream":true,"messages":[{"role":"user","content":"Hi"}]}`},
	}
	for _, entry := range chatEntryPoints {
		for _, tt := range tests {
			t.Run(entry.name+"/"+tt.name, func(t *testing.T) {
				mock := fastPathJSONMock(fastPathUpstreamBody)
				mock.promptCachePlanApplies = true
				rec := entry.post(t, mock, tt.body)

				if rec.Code != http.StatusOK {
					t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
				}
				if mock.lastPassthroughReq != nil {
					t.Fatal("request took the passthrough fast path although the cache planner applies")
				}
				if !strings.Contains(rec.Body.String(), `"typed"`) {
					t.Fatalf("body = %s, want the translated provider response", rec.Body.String())
				}
			})
		}
	}
}

func TestChatCompletion_FastPathSkippedWithFailoverSelectors(t *testing.T) {
	mock := fastPathJSONMock(fastPathUpstreamBody)
	mock.supportedModels = append(mock.supportedModels, "azure/gpt-4o-mini")
	mock.providerTypes["azure/gpt-4o-mini"] = "azure"
	handler := newHandler(mock, nil, nil, nil, nil, nil, failoverResolverStub{
		selectors: []core.ModelSelector{{Provider: "azure", Model: "gpt-4o-mini"}},
	}, nil)
	rec := postChatCompletion(t, handler, fastPathChatRequestBody)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if mock.lastPassthroughReq != nil {
		t.Fatal("request took the passthrough fast path although failover routes exist")
	}
	if mock.chatCompletionCalls != 1 {
		t.Fatalf("ChatCompletion calls = %d, want 1", mock.chatCompletionCalls)
	}
}

func TestStampJSONObjectProvider(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "no provider key", body: `{"id":"x"}`, want: `{"id":"x","provider":"openai"}`},
		{name: "trailing newline", body: "{\"id\":\"x\"}\n", want: "{\"id\":\"x\",\"provider\":\"openai\"}\n"},
		{name: "empty object", body: `{}`, want: `{"provider":"openai"}`},
		{name: "top-level provider replaced in place", body: `{"id":"x","provider":"OpenAI","model":"m"}`, want: `{"id":"x","provider":"openai","model":"m"}`},
		{name: "top-level non-string provider replaced", body: `{"provider":{"name":"OpenAI"},"id":"x"}`, want: `{"provider":"openai","id":"x"}`},
		{name: "nested provider inside choices untouched", body: `{"choices":[{"provider":"OpenAI"}]}`, want: `{"choices":[{"provider":"OpenAI"}],"provider":"openai"}`},
		{name: "array untouched", body: `[1]`, want: `[1]`},
		{name: "empty untouched", body: ``, want: ``},
		{name: "truncated untouched", body: `{"id":`, want: `{"id":`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := string(stampJSONObjectProvider([]byte(tt.body), "openai")); got != tt.want {
				t.Fatalf("stamp(%q) = %q, want %q", tt.body, got, tt.want)
			}
		})
	}
}

// newOpenAIRouter builds a real providers.Router with one OpenAI provider whose
// upstream is an in-process server answering /models and /chat/completions
// with responseBody.
func newOpenAIRouter(tb testing.TB, responseBody string) *providers.Router {
	tb.Helper()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/models"):
			_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"gpt-4o-mini","object":"model"}]}`))
		default:
			_, _ = io.Copy(io.Discard, r.Body)
			_, _ = w.Write([]byte(responseBody))
		}
	}))
	tb.Cleanup(upstream.Close)

	provider := openai.NewWithHTTPClient("test-key", upstream.Client(), llmclient.Hooks{})
	provider.SetBaseURL(upstream.URL)
	registry := providers.NewModelRegistry()
	registry.RegisterProviderWithNameAndType(provider, "openai", "openai")
	if err := registry.Initialize(context.Background()); err != nil {
		tb.Fatalf("registry initialize: %v", err)
	}
	router, err := providers.NewRouter(registry)
	if err != nil {
		tb.Fatalf("new router: %v", err)
	}
	return router
}

// TestChatCompletion_FastPathFiresThroughMiddlewareStack drives a bare model
// id through the full server stack with a real router. The fast path used to
// be unreachable here: preparation writes the resolved provider into the
// request, and the gate mistook that for a client-supplied provider.
func TestChatCompletion_FastPathFiresThroughMiddlewareStack(t *testing.T) {
	srv := New(newOpenAIRouter(t, fastPathUpstreamBody), &Config{})
	for _, stream := range []bool{false, true} {
		body := fastPathChatRequestBody
		if stream {
			body = `{"model":"gpt-4o-mini","stream":true,"messages":[{"role":"user","content":"Hi"}]}`
		}
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("stream=%v status = %d, want 200; body=%s", stream, rec.Code, rec.Body.String())
		}
		// The translated path re-encodes core.ChatResponse (provider after
		// model, no unknown members). A proxied streaming body is the upstream
		// bytes untouched; a proxied non-streaming body keeps the upstream's
		// native_finish_reason and carries provider as its last member.
		want := fastPathUpstreamBody
		if !stream {
			want = strings.TrimSuffix(fastPathUpstreamBody, "}") + `,"provider":"openai"}`
		}
		if got := rec.Body.String(); got != want {
			t.Fatalf("stream=%v body was translated instead of proxied:\n got %s\nwant %s", stream, got, want)
		}
	}
}

// BenchmarkChatCompletionNonStreaming compares the translated path (forced by
// enforced usage data, which the fast path declines) against the passthrough
// fast path for a ~30 KB request and ~30 KB response through a real router
// and OpenAI provider talking to an in-process upstream.
func BenchmarkChatCompletionNonStreaming(b *testing.B) {
	content := strings.Repeat("The quick brown fox jumps over the lazy dog. ", 660) // ~30 KB
	requestBody := `{"model":"gpt-4o-mini","messages":[{"role":"user","content":"` + content + `"}]}`
	responseBody := `{"id":"chatcmpl-bench","object":"chat.completion","model":"gpt-4o-mini","choices":[{"index":0,"message":{"role":"assistant","content":"` + content + `"},"finish_reason":"stop"}],"usage":{"prompt_tokens":7000,"completion_tokens":7000,"total_tokens":14000}}`
	router := newOpenAIRouter(b, responseBody)

	run := func(b *testing.B, usageCfg usage.Config) {
		handler := NewHandler(router, nil, &collectingUsageLogger{config: usageCfg}, nil)
		e := echo.New()
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader([]byte(requestBody)))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			if err := handler.ChatCompletion(e.NewContext(req, rec)); err != nil {
				b.Fatalf("handler error: %v", err)
			}
			if rec.Code != http.StatusOK {
				b.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
			}
		}
	}
	b.Run("translated", func(b *testing.B) {
		run(b, usage.Config{Enabled: true, EnforceReturningUsageData: true})
	})
	b.Run("fast_path", func(b *testing.B) {
		run(b, usage.Config{Enabled: true})
	})
}
