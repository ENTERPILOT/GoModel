package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/goccy/go-json"
	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/auditlog"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/echotest"
	"github.com/enterpilot/gomodel/internal/requestparams"
	"github.com/enterpilot/gomodel/internal/users"
	"github.com/enterpilot/gomodel/internal/virtualmodels"
)

// policyTestUserStore is an in-memory users.Store.
type policyTestUserStore struct{ rows []users.User }

func (s *policyTestUserStore) List(context.Context) ([]users.User, error) { return s.rows, nil }
func (s *policyTestUserStore) Upsert(_ context.Context, user users.User) error {
	s.rows = append(s.rows, user)
	return nil
}
func (s *policyTestUserStore) Delete(context.Context, string) error { return nil }
func (s *policyTestUserStore) Close() error                         { return nil }

// policyTestProvider is failoverProvider that also records the Responses
// requests it receives.
type policyTestProvider struct {
	*failoverProvider
	responsesRequests []*core.ResponsesRequest
}

func (p *policyTestProvider) Responses(ctx context.Context, req *core.ResponsesRequest) (*core.ResponsesResponse, error) {
	p.responsesRequests = append(p.responsesRequests, req)
	return p.failoverProvider.Responses(ctx, req)
}

// policyFixture wires the real users and virtual-models services the way the
// app does: /eng defaults to the team-model balancer, capped at medium effort
// and 4096 output tokens; /eng/interns tightens the effort and takes its own
// default model. team-model forces a high effort and defaults max_tokens to
// 8000, and its second leg carries its own model policy.
type policyFixture struct {
	provider *policyTestProvider
	handler  *Handler
	resolve  echo.MiddlewareFunc
}

func newPolicyFixture(t *testing.T, rows ...users.User) *policyFixture {
	t.Helper()
	ctx := context.Background()
	catalog := &aliasesTestCatalog{
		supported:     map[string]bool{"openai/gpt-5": true, "openai/gpt-5-mini": true},
		providerTypes: map[string]string{"openai/gpt-5": "openai", "openai/gpt-5-mini": "openai"},
	}
	vmService, err := virtualmodels.NewService(newAliasesTestStore(
		virtualmodels.VirtualModel{
			Source:          "team-model",
			Strategy:        virtualmodels.StrategyFailover,
			Targets:         []virtualmodels.Target{{Model: "openai/gpt-5"}, {Model: "openai/gpt-5-mini"}},
			ReasoningEffort: "high",
			DefaultParams:   requestparams.Set{"max_tokens": json.RawMessage(`8000`)},
			Enabled:         true,
		},
		virtualmodels.VirtualModel{
			Source: "openai/gpt-5-mini", ProviderName: "openai", Model: "gpt-5-mini",
			ReasoningEffort: "minimal",
			Enabled:         true,
		},
	), catalog, true)
	require.NoError(t, err)
	require.NoError(t, vmService.Refresh(ctx))

	if rows == nil {
		rows = []users.User{
			{UserPath: "/eng", DefaultModel: "team-model", MaxReasoningEffort: "medium", MaxOutputTokens: 4096},
			{UserPath: "/eng/interns", DefaultModel: "openai/gpt-5-mini", MaxReasoningEffort: "low"},
		}
	}
	userService, err := users.NewService(&policyTestUserStore{rows: rows}, catalog)
	require.NoError(t, err)
	require.NoError(t, userService.Refresh(ctx))
	vmService.SetAccessPolicy(userService)

	chat := &core.ChatResponse{ID: "chatcmpl-1", Object: "chat.completion", Model: "gpt-5", Choices: []core.Choice{{Message: core.ResponseMessage{Role: "assistant", Content: "ok"}, FinishReason: "stop"}}}
	responses := &core.ResponsesResponse{ID: "resp-1", Object: "response", Model: "gpt-5", Status: "completed"}
	provider := &policyTestProvider{failoverProvider: &failoverProvider{
		chatResponses:      map[string]*core.ChatResponse{"openai/gpt-5": chat, "openai/gpt-5-mini": chat},
		responsesResponses: map[string]*core.ResponsesResponse{"openai/gpt-5": responses, "openai/gpt-5-mini": responses},
		supportedModels:    map[string]string{"openai/gpt-5": "openai", "openai/gpt-5-mini": "openai"},
	}}
	return &policyFixture{
		provider: provider,
		handler:  newHandlerWithAuthorizer(provider, nil, nil, nil, vmService, vmService, nil, vmService, nil),
		resolve:  WorkflowResolutionWithResolverAndPolicy(provider, vmService, nil),
	}
}

// post sends body to path as userPath through workflow resolution and the
// endpoint's handler, as the server's middleware chain does.
func (f *policyFixture) post(t *testing.T, userPath, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	c, rec := echotest.Post(t, path, body)
	c.Set(string(auditlog.LogEntryKey), &auditlog.LogEntry{Data: &auditlog.LogData{}})
	if userPath != "" {
		c.SetRequest(c.Request().WithContext(core.WithEffectiveUserPath(c.Request().Context(), userPath)))
	}
	endpoint := map[string]echo.HandlerFunc{
		"/v1/chat/completions":      f.handler.ChatCompletion,
		"/v1/responses":             f.handler.Responses,
		"/v1/messages":              f.handler.Messages,
		"/v1/messages/count_tokens": f.handler.CountMessageTokens,
		"/v1/embeddings":            f.handler.Embeddings,
	}[path]
	require.NotNil(t, endpoint, path)
	require.NoError(t, f.resolve(endpoint)(c))
	return rec
}

func (f *policyFixture) failPrimary() {
	down := core.NewProviderError("openai", http.StatusServiceUnavailable, "down", nil)
	f.provider.chatErrors = map[string]error{"openai/gpt-5": down}
	f.provider.responsesErrors = map[string]error{"openai/gpt-5": down}
}

// A request that names no model is served by the default model of the
// nearest user path that sets one, in every dialect, and resolved like a
// model the client sent: here through the team-model balancer.
func TestUserPathDefaultModel(t *testing.T) {
	bodies := map[string]string{
		"/v1/chat/completions": `{"messages":[{"role":"user","content":"hi"}]}`,
		"/v1/responses":        `{"input":"hi"}`,
		"/v1/messages":         `{"max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`,
	}
	sentModels := func(f *policyFixture, path string) []string {
		if path == "/v1/responses" {
			return f.provider.responsesCalls
		}
		return f.provider.chatCalls
	}
	tests := []struct {
		name       string
		userPath   string
		wantStatus int
		wantModel  string
	}{
		{"group default", "/eng", http.StatusOK, "openai/gpt-5"},
		{"inherited by a user below the group", "/eng/alice", http.StatusOK, "openai/gpt-5"},
		{"nearest default wins", "/eng/interns/bob", http.StatusOK, "openai/gpt-5-mini"},
		{"path without a default still requires a model", "/sales/carol", http.StatusBadRequest, ""},
		{"request without a user path still requires a model", "", http.StatusBadRequest, ""},
	}
	for path, body := range bodies {
		for _, tt := range tests {
			t.Run(path+" "+tt.name, func(t *testing.T) {
				f := newPolicyFixture(t)
				rec := f.post(t, tt.userPath, path, body)

				require.Equal(t, tt.wantStatus, rec.Code, rec.Body.String())
				if tt.wantModel == "" {
					assert.Contains(t, rec.Body.String(), "model is required")
					assert.Empty(t, sentModels(f, path))
					return
				}
				assert.Equal(t, []string{tt.wantModel}, sentModels(f, path))
			})
		}
	}
}

func TestUserPathDefaultModelLeavesOtherRequestsAlone(t *testing.T) {
	t.Run("a model the client names always wins", func(t *testing.T) {
		f := newPolicyFixture(t)
		rec := f.post(t, "/eng", "/v1/chat/completions", `{"model":"openai/gpt-5-mini","messages":[{"role":"user","content":"hi"}]}`)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assert.Equal(t, []string{"openai/gpt-5-mini"}, f.provider.chatCalls)
	})
	t.Run("an empty model string counts as none", func(t *testing.T) {
		f := newPolicyFixture(t)
		rec := f.post(t, "/eng", "/v1/chat/completions", `{"model":" ","messages":[{"role":"user","content":"hi"}]}`)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assert.Equal(t, []string{"openai/gpt-5"}, f.provider.chatCalls)
	})
	t.Run("a provider without a model is still an error", func(t *testing.T) {
		f := newPolicyFixture(t)
		rec := f.post(t, "/eng", "/v1/chat/completions", `{"provider":"openai","messages":[{"role":"user","content":"hi"}]}`)
		require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
		assert.Empty(t, f.provider.chatCalls)
	})
	t.Run("embeddings have no default model", func(t *testing.T) {
		f := newPolicyFixture(t)
		rec := f.post(t, "/eng", "/v1/embeddings", `{"input":"hi"}`)
		require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
		assert.Contains(t, rec.Body.String(), "model is required")
		assert.Empty(t, f.provider.embeddingCalls)
	})
	t.Run("a default the allowlist denies is denied like any model", func(t *testing.T) {
		f := newPolicyFixture(t, users.User{UserPath: "/eng", DefaultModel: "openai/gpt-5", AllowedModels: []string{"openai/gpt-5-mini"}})
		rec := f.post(t, "/eng", "/v1/chat/completions", `{"messages":[{"role":"user","content":"hi"}]}`)
		require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
		assert.Contains(t, rec.Body.String(), "model_access_denied")
		assert.Empty(t, f.provider.chatCalls)
	})
	t.Run("a default naming an unknown model is not found", func(t *testing.T) {
		f := newPolicyFixture(t, users.User{UserPath: "/eng", DefaultModel: "openai/retired"})
		rec := f.post(t, "/eng", "/v1/chat/completions", `{"messages":[{"role":"user","content":"hi"}]}`)
		require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
		assert.Empty(t, f.provider.chatCalls)
	})
	t.Run("token counting uses the default too", func(t *testing.T) {
		f := newPolicyFixture(t)
		rec := f.post(t, "/eng", "/v1/messages/count_tokens", `{"messages":[{"role":"user","content":"hi"}]}`)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		rec = f.post(t, "/sales", "/v1/messages/count_tokens", `{"messages":[{"role":"user","content":"hi"}]}`)
		require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	})
}

// The ceilings of the caller's user path bound every attempt in every
// dialect, after the virtual models on the route had their say: team-model
// forces "high" on its first leg and its second leg's own policy forces
// "minimal", so /eng (capped at medium) sends medium, then minimal.
func TestUserPathCeilingsBoundVirtualModelOverridesOnEveryAttempt(t *testing.T) {
	type attempt struct {
		model  string
		effort string
		tokens int
	}
	tests := []struct {
		name     string
		userPath string
		path     string
		body     string
		want     []attempt
	}{
		{
			name:     "chat: client above both ceilings",
			userPath: "/eng/alice",
			path:     "/v1/chat/completions",
			body:     `{"model":"team-model","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"max","max_tokens":32000}`,
			want:     []attempt{{"gpt-5", "medium", 4096}, {"gpt-5-mini", "minimal", 4096}},
		},
		{
			name:     "chat: silent client gets the alias default, capped",
			userPath: "/eng/alice",
			path:     "/v1/chat/completions",
			body:     `{"model":"team-model","messages":[{"role":"user","content":"hi"}]}`,
			want:     []attempt{{"gpt-5", "medium", 4096}, {"gpt-5-mini", "minimal", 4096}},
		},
		{
			name:     "chat: client below the token ceiling keeps its value",
			userPath: "/eng/alice",
			path:     "/v1/chat/completions",
			body:     `{"model":"team-model","messages":[{"role":"user","content":"hi"}],"max_tokens":100}`,
			want:     []attempt{{"gpt-5", "medium", 100}, {"gpt-5-mini", "minimal", 100}},
		},
		{
			name:     "chat: the tightest ancestor ceiling wins",
			userPath: "/eng/interns/bob",
			path:     "/v1/chat/completions",
			body:     `{"model":"team-model","messages":[{"role":"user","content":"hi"}],"max_tokens":32000}`,
			want:     []attempt{{"gpt-5", "low", 4096}, {"gpt-5-mini", "minimal", 4096}},
		},
		{
			name:     "chat: another team is not capped",
			userPath: "/sales/carol",
			path:     "/v1/chat/completions",
			body:     `{"model":"team-model","messages":[{"role":"user","content":"hi"}],"max_tokens":32000}`,
			want:     []attempt{{"gpt-5", "high", 32000}, {"gpt-5-mini", "minimal", 32000}},
		},
		{
			name:     "responses: client above both ceilings",
			userPath: "/eng/alice",
			path:     "/v1/responses",
			body:     `{"model":"team-model","input":"hi","reasoning":{"effort":"xhigh"},"max_output_tokens":32000}`,
			want:     []attempt{{"gpt-5", "medium", 4096}, {"gpt-5-mini", "minimal", 4096}},
		},
		{
			name:     "messages: thinking budget and max_tokens above both ceilings",
			userPath: "/eng/alice",
			path:     "/v1/messages",
			body:     `{"model":"team-model","max_tokens":32000,"thinking":{"type":"enabled","budget_tokens":30000},"messages":[{"role":"user","content":"hi"}]}`,
			want:     []attempt{{"gpt-5", "medium", 4096}, {"gpt-5-mini", "minimal", 4096}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newPolicyFixture(t)
			f.failPrimary()

			rec := f.post(t, tt.userPath, tt.path, tt.body)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

			var got []attempt
			for _, sent := range f.provider.chatRequests {
				require.NotNil(t, sent.Reasoning)
				require.NotNil(t, sent.MaxTokens)
				got = append(got, attempt{sent.Model, sent.Reasoning.Effort, *sent.MaxTokens})
			}
			for _, sent := range f.provider.responsesRequests {
				require.NotNil(t, sent.Reasoning)
				require.NotNil(t, sent.MaxOutputTokens)
				got = append(got, attempt{sent.Model, sent.Reasoning.Effort, *sent.MaxOutputTokens})
			}
			assert.Equal(t, tt.want, got)
		})
	}
}

// Without a virtual-model override the ceiling is the only thing in play: it
// lowers a client value above it and adds nothing to a request that sets none.
func TestUserPathCeilingsOnADirectModelRequest(t *testing.T) {
	f := newPolicyFixture(t)

	rec := f.post(t, "/eng/alice", "/v1/chat/completions", `{"model":"openai/gpt-5","messages":[{"role":"user","content":"hi"}]}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	rec = f.post(t, "/eng/alice", "/v1/chat/completions", `{"model":"openai/gpt-5","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"low","max_completion_tokens":512}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	rec = f.post(t, "/eng/alice", "/v1/chat/completions", `{"model":"openai/gpt-5","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"high","max_completion_tokens":32000}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	require.Len(t, f.provider.chatRequests, 3)
	silent, within, above := f.provider.chatRequests[0], f.provider.chatRequests[1], f.provider.chatRequests[2]

	assert.Nil(t, silent.Reasoning, "a silent client is not raised to the effort ceiling")
	require.NotNil(t, silent.MaxTokens, "an omitted token limit is set to the token ceiling")
	assert.Equal(t, 4096, *silent.MaxTokens)
	assert.Nil(t, silent.ExtraFields.Lookup("max_completion_tokens"))

	assert.Nil(t, within.Reasoning)
	assert.JSONEq(t, `"low"`, string(within.ExtraFields.Lookup("reasoning_effort")), "a value within the ceiling is sent as the client wrote it")
	assert.JSONEq(t, `512`, string(within.ExtraFields.Lookup("max_completion_tokens")))

	require.NotNil(t, above.Reasoning)
	assert.Equal(t, "medium", above.Reasoning.Effort)
	assert.Nil(t, above.ExtraFields.Lookup("reasoning_effort"))
	assert.JSONEq(t, `4096`, string(above.ExtraFields.Lookup("max_completion_tokens")), "the limit is capped in the client's spelling")
}

// An Anthropic Messages request is forwarded verbatim while it stays within
// the caller's ceilings and takes the translated pipeline, which can lower
// it, once it exceeds them or names no model.
func TestUserPathPolicyAndNativeMessagesForwarding(t *testing.T) {
	newFixture := func(t *testing.T) (*capturingProvider, func(userPath, body string) *httptest.ResponseRecorder) {
		t.Helper()
		ctx := context.Background()
		catalog := &aliasesTestCatalog{
			supported:     map[string]bool{"anthropic/claude-test": true},
			providerTypes: map[string]string{"anthropic/claude-test": "anthropic"},
		}
		vmService, err := virtualmodels.NewService(newAliasesTestStore(), catalog, true)
		require.NoError(t, err)
		require.NoError(t, vmService.Refresh(ctx))
		userService, err := users.NewService(&policyTestUserStore{rows: []users.User{
			{UserPath: "/eng", DefaultModel: "anthropic/claude-test", MaxReasoningEffort: "medium", MaxOutputTokens: 4096},
		}}, catalog)
		require.NoError(t, err)
		require.NoError(t, userService.Refresh(ctx))
		vmService.SetAccessPolicy(userService)

		provider := &capturingProvider{
			supportedModels: []string{"claude-test"},
			providerTypes:   map[string]string{"anthropic/claude-test": "anthropic", "claude-test": "anthropic"},
			response: &core.ChatResponse{ID: "resp-1", Object: "chat.completion", Model: "claude-test", Choices: []core.Choice{{
				Message: core.ResponseMessage{Role: "assistant", Content: "ok"}, FinishReason: "stop",
			}}}}
		handler := newHandlerWithAuthorizer(provider, nil, nil, nil, vmService, vmService, nil, vmService, nil)
		resolve := WorkflowResolutionWithResolverAndPolicy(provider, vmService, nil)
		return provider, func(userPath, body string) *httptest.ResponseRecorder {
			provider.passthroughResponse = &core.PassthroughResponse{
				StatusCode: http.StatusOK,
				Headers:    map[string][]string{"Content-Type": {"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"id":"msg_1","type":"message","role":"assistant","content":[],"model":"claude-test","usage":{"input_tokens":1,"output_tokens":1}}`)),
			}
			c, rec := echotest.Post(t, "/v1/messages", body)
			c.SetRequest(c.Request().WithContext(core.WithEffectiveUserPath(c.Request().Context(), userPath)))
			require.NoError(t, resolve(handler.Messages)(c))
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			return rec
		}
	}

	t.Run("within the ceilings is forwarded verbatim", func(t *testing.T) {
		provider, post := newFixture(t)
		post("/eng/alice", `{"model":"anthropic/claude-test","max_tokens":1024,"thinking":{"type":"enabled","budget_tokens":2000},"messages":[{"role":"user","content":"hi"}]}`)
		require.NotNil(t, provider.lastPassthroughReq, "the request should have been forwarded natively")
		assert.Nil(t, provider.capturedChatReq)
	})
	t.Run("above the token ceiling is translated and capped", func(t *testing.T) {
		provider, post := newFixture(t)
		post("/eng/alice", `{"model":"anthropic/claude-test","max_tokens":32000,"messages":[{"role":"user","content":"hi"}]}`)
		assert.Nil(t, provider.lastPassthroughReq)
		require.NotNil(t, provider.capturedChatReq)
		require.NotNil(t, provider.capturedChatReq.MaxTokens)
		assert.Equal(t, 4096, *provider.capturedChatReq.MaxTokens)
	})
	t.Run("above the effort ceiling is translated and lowered", func(t *testing.T) {
		provider, post := newFixture(t)
		post("/eng/alice", `{"model":"anthropic/claude-test","max_tokens":1024,"output_config":{"effort":"max"},"messages":[{"role":"user","content":"hi"}]}`)
		assert.Nil(t, provider.lastPassthroughReq)
		require.NotNil(t, provider.capturedChatReq)
		require.NotNil(t, provider.capturedChatReq.Reasoning)
		assert.Equal(t, "medium", provider.capturedChatReq.Reasoning.Effort)
	})
	t.Run("a request served by the default model is forwarded with the model added", func(t *testing.T) {
		provider, post := newFixture(t)
		post("/eng/alice", `{"max_tokens":1024,"messages":[{"role":"user","content":"hi"}]}`)
		require.NotNil(t, provider.lastPassthroughReq)
		assert.Nil(t, provider.capturedChatReq)
		forwarded, err := io.ReadAll(provider.lastPassthroughReq.Body)
		require.NoError(t, err)
		assert.Equal(t, `{"model":"claude-test","max_tokens":1024,"messages":[{"role":"user","content":"hi"}]}`, string(forwarded))
	})
	t.Run("another team is forwarded verbatim whatever it asks for", func(t *testing.T) {
		provider, post := newFixture(t)
		post("/sales/carol", `{"model":"anthropic/claude-test","max_tokens":32000,"output_config":{"effort":"max"},"messages":[{"role":"user","content":"hi"}]}`)
		require.NotNil(t, provider.lastPassthroughReq)
		assert.Nil(t, provider.capturedChatReq)
	})
}

// The default model also holds behind the real ingress chain, where the
// request snapshot and the captured body sit in front of workflow resolution
// and the user path comes from the header of an authenticated request.
func TestUserPathDefaultModelThroughTheIngressChain(t *testing.T) {
	for path, body := range map[string]string{
		"/v1/chat/completions": `{"messages":[{"role":"user","content":"hi"}],"max_tokens":32000}`,
		"/v1/responses":        `{"input":"hi","max_output_tokens":32000}`,
		"/v1/messages":         `{"max_tokens":32000,"messages":[{"role":"user","content":"hi"}]}`,
	} {
		t.Run(path, func(t *testing.T) {
			f := newPolicyFixture(t)
			endpoint := map[string]echo.HandlerFunc{
				"/v1/chat/completions": f.handler.ChatCompletion,
				"/v1/responses":        f.handler.Responses,
				"/v1/messages":         f.handler.Messages,
			}[path]
			chain := RequestSnapshotCapture("")(AuthMiddleware("master-key", nil)(RequestBodyCapture()(f.resolve(endpoint))))

			c, rec := echotest.Post(t, path, body,
				echotest.WithHeader("Authorization", "Bearer master-key"),
				echotest.WithHeader(core.UserPathHeaderName(""), "/eng/alice"))
			require.NoError(t, chain(c))
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

			if path == "/v1/responses" {
				require.Len(t, f.provider.responsesRequests, 1)
				sent := f.provider.responsesRequests[0]
				assert.Equal(t, "gpt-5", sent.Model)
				assert.Equal(t, 4096, *sent.MaxOutputTokens)
				assert.Equal(t, "medium", sent.Reasoning.Effort)
				return
			}
			require.Len(t, f.provider.chatRequests, 1)
			sent := f.provider.chatRequests[0]
			assert.Equal(t, "gpt-5", sent.Model)
			assert.Equal(t, 4096, *sent.MaxTokens)
			assert.Equal(t, "medium", sent.Reasoning.Effort)
		})
	}
}
