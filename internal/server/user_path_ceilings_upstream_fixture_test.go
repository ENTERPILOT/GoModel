package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/auditlog"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/echotest"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/anthropic"
	"github.com/enterpilot/gomodel/internal/providers/cohere"
	"github.com/enterpilot/gomodel/internal/providers/gemini"
	"github.com/enterpilot/gomodel/internal/providers/openai"
	"github.com/enterpilot/gomodel/internal/providers/opencodego"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
	"github.com/enterpilot/gomodel/internal/users"
	"github.com/enterpilot/gomodel/internal/virtualmodels"
)

// Upstream answers for each real adapter the fixture wires.
const (
	anthropicMessageJSON = `{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`
	geminiContentJSON    = `{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}`
	cohereChatJSON       = `{"id":"c1","finish_reason":"COMPLETE","message":{"role":"assistant","content":[{"type":"text","text":"ok"}]},"usage":{"tokens":{"input_tokens":1,"output_tokens":1}}}`
)

// upstreamModels are the models the fixture serves, by provider.
var upstreamModels = map[string]string{
	"anthropic/claude-sonnet-4-5":  "anthropic",
	"openai/gpt-5":                 "openai",
	"openai/gpt-4o":                "openai",
	"openai/gpt-5.1":               "openai",
	"gemini/gemini-2.5-flash":      "gemini",
	"cohere/command-a-reasoning":   "cohere",
	"opencode_go/kimi-k2-thinking": "opencode_go",
}

// upstreamRouter is a RoutableProvider over the real provider adapters, each
// pointed at a recording fake upstream, so a test sees the exact body the
// gateway sends after every override, ceiling and adapter translation.
type upstreamRouter struct {
	adapters map[string]core.Provider
}

func (r *upstreamRouter) adapter(req string, providerName string) core.Provider {
	selector, err := core.ParseModelSelector(req, providerName)
	if err != nil {
		return nil
	}
	return r.adapters[selector.Provider]
}

func bareModel(model, providerName string) string {
	selector, err := core.ParseModelSelector(model, providerName)
	if err != nil {
		return model
	}
	return selector.Model
}

func (r *upstreamRouter) ChatCompletion(ctx context.Context, req *core.ChatRequest) (*core.ChatResponse, error) {
	adapter := r.adapter(req.Model, req.Provider)
	routed := *req
	routed.Model, routed.Provider = bareModel(req.Model, req.Provider), ""
	return adapter.ChatCompletion(ctx, &routed)
}

func (r *upstreamRouter) StreamChatCompletion(ctx context.Context, req *core.ChatRequest) (io.ReadCloser, error) {
	adapter := r.adapter(req.Model, req.Provider)
	routed := *req
	routed.Model, routed.Provider = bareModel(req.Model, req.Provider), ""
	return adapter.StreamChatCompletion(ctx, &routed)
}

func (r *upstreamRouter) Responses(ctx context.Context, req *core.ResponsesRequest) (*core.ResponsesResponse, error) {
	adapter := r.adapter(req.Model, req.Provider)
	routed := *req
	routed.Model, routed.Provider = bareModel(req.Model, req.Provider), ""
	return adapter.Responses(ctx, &routed)
}

func (r *upstreamRouter) StreamResponses(ctx context.Context, req *core.ResponsesRequest) (io.ReadCloser, error) {
	adapter := r.adapter(req.Model, req.Provider)
	routed := *req
	routed.Model, routed.Provider = bareModel(req.Model, req.Provider), ""
	return adapter.StreamResponses(ctx, &routed)
}

func (r *upstreamRouter) Embeddings(context.Context, *core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
	return nil, core.NewInvalidRequestError("not served", nil)
}

func (r *upstreamRouter) ListModels(context.Context) (*core.ModelsResponse, error) {
	return &core.ModelsResponse{Object: "list"}, nil
}

func (r *upstreamRouter) Supports(model string) bool { return r.GetProviderType(model) != "" }

func (r *upstreamRouter) GetProviderType(model string) string {
	selector, err := core.ParseModelSelector(model, "")
	if err != nil {
		return ""
	}
	return upstreamModels[selector.QualifiedModel()]
}

// Passthrough forwards a native Messages body to the Anthropic adapter.
func (r *upstreamRouter) Passthrough(ctx context.Context, providerType string, req *core.PassthroughRequest) (*core.PassthroughResponse, error) {
	native, ok := r.adapters[providerType].(interface {
		Passthrough(context.Context, *core.PassthroughRequest) (*core.PassthroughResponse, error)
	})
	if !ok {
		return nil, core.NewInvalidRequestError("no passthrough for "+providerType, nil)
	}
	return native.Passthrough(ctx, req)
}

// upstreamFixture serves the real request path, from workflow resolution
// through the handler, the gateway and the provider adapters, to one fake
// upstream per provider.
type upstreamFixture struct {
	upstreams map[string]*providertest.Capture
	handler   *Handler
	resolve   echo.MiddlewareFunc
}

// newUpstreamFixture wires the user rows and virtual models the way the app
// does. A provider listed in failing answers 503 to everything.
func newUpstreamFixture(t *testing.T, rows []users.User, models []virtualmodels.VirtualModel, failing ...string) *upstreamFixture {
	t.Helper()
	ctx := context.Background()
	supported := make(map[string]bool, len(upstreamModels))
	for model := range upstreamModels {
		supported[model] = true
	}
	catalog := &aliasesTestCatalog{supported: supported, providerTypes: upstreamModels}
	vmService, err := virtualmodels.NewService(newAliasesTestStore(models...), catalog, true)
	require.NoError(t, err)
	require.NoError(t, vmService.Refresh(ctx))
	userService, err := users.NewService(&policyTestUserStore{rows: rows}, catalog)
	require.NoError(t, err)
	require.NoError(t, userService.Refresh(ctx))
	vmService.SetAccessPolicy(userService)

	f := &upstreamFixture{upstreams: map[string]*providertest.Capture{}}
	server := func(name, body string) string {
		down := slices.Contains(failing, name)
		srv, capture := providertest.Server(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch {
			case down:
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = io.WriteString(w, `{"error":{"message":"down","type":"server_error"}}`)
			case name == "openai" && strings.HasSuffix(r.URL.Path, "/responses"):
				_, _ = io.WriteString(w, providertest.ResponsesJSON)
			default:
				_, _ = io.WriteString(w, body)
			}
		})
		f.upstreams[name] = capture
		return srv.URL
	}
	opts := providertest.Options(llmclient.Hooks{})
	opts.Resilience.Retry.MaxRetries = 0
	router := &upstreamRouter{adapters: map[string]core.Provider{
		"anthropic":   anthropic.New(providers.ProviderConfig{APIKey: "k", BaseURL: server("anthropic", anthropicMessageJSON)}, opts),
		"openai":      openai.New(providers.ProviderConfig{APIKey: "k", BaseURL: server("openai", providertest.ChatCompletionJSON)}, opts),
		"gemini":      gemini.New(providers.ProviderConfig{APIKey: "k", BaseURL: server("gemini", geminiContentJSON), APIMode: "native"}, opts),
		"cohere":      cohere.New(providers.ProviderConfig{APIKey: "k", BaseURL: server("cohere", cohereChatJSON)}, opts),
		"opencode_go": opencodego.New(providers.ProviderConfig{APIKey: "k", BaseURL: server("opencode_go", providertest.ChatCompletionJSON)}, opts),
	}}
	f.handler = newHandlerWithAuthorizer(router, nil, nil, nil, vmService, vmService, nil, vmService, nil)
	f.resolve = WorkflowResolutionWithResolverAndPolicy(router, vmService, nil)
	return f
}

// post sends body to path as a caller on userPath ("" for none) and returns
// the response.
func (f *upstreamFixture) post(t *testing.T, userPath, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	c, rec := echotest.Post(t, path, body)
	c.Set(string(auditlog.LogEntryKey), &auditlog.LogEntry{Data: &auditlog.LogData{}})
	if userPath != "" {
		c.SetRequest(c.Request().WithContext(core.WithEffectiveUserPath(c.Request().Context(), userPath)))
	}
	endpoint := map[string]echo.HandlerFunc{
		"/v1/chat/completions": f.handler.ChatCompletion,
		"/v1/responses":        f.handler.Responses,
		"/v1/messages":         f.handler.Messages,
	}[path]
	require.NotNil(t, endpoint, path)
	require.NoError(t, f.resolve(endpoint)(c))
	return rec
}

// openAIEffort returns the effort an OpenAI upstream body carries, on Chat
// Completions or on the Responses API the adapter may serve chat through.
func openAIEffort(sent map[string]any) any {
	if reasoning, ok := sent["reasoning"].(map[string]any); ok {
		return reasoning["effort"]
	}
	return sent["reasoning_effort"]
}

// openAILimit returns the output token limit an OpenAI upstream body
// carries, in whichever field the model and API take.
func openAILimit(t *testing.T, sent map[string]any) int {
	t.Helper()
	for _, key := range []string{"max_output_tokens", "max_completion_tokens", "max_tokens"} {
		if value, ok := sent[key].(float64); ok {
			return int(value)
		}
	}
	require.Failf(t, "no output token limit upstream", "%v", sent)
	return 0
}

// sent returns the body the named upstream received last.
func (f *upstreamFixture) sent(t *testing.T, upstream string) map[string]any {
	t.Helper()
	return f.upstreams[upstream].Last(t).JSON(t)
}
