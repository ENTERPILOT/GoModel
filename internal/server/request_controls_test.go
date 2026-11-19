package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/requestlog"
)

func TestControlSwitch(t *testing.T) {
	tests := []struct {
		name     string
		values   []string
		wantOn   bool
		wantSent bool
		wantErr  bool
	}{
		{name: "absent"},
		{name: "off", values: []string{"off"}, wantSent: true},
		{name: "false", values: []string{"false"}, wantSent: true},
		{name: "zero", values: []string{"0"}, wantSent: true},
		{name: "any case and padding", values: []string{" OFF "}, wantSent: true},
		{name: "on", values: []string{"on"}, wantOn: true, wantSent: true},
		{name: "true", values: []string{"True"}, wantOn: true, wantSent: true},
		{name: "one", values: []string{"1"}, wantOn: true, wantSent: true},
		{name: "empty", values: []string{""}, wantErr: true},
		{name: "unknown word", values: []string{"never"}, wantErr: true},
		{name: "list in one value", values: []string{"off, on"}, wantErr: true},
		{name: "repeated", values: []string{"off", "off"}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			on, sent, err := controlSwitch(failoverControl(tt.values...), core.FailoverControlHeader)

			if tt.wantErr {
				var gatewayErr *core.GatewayError
				require.ErrorAs(t, err, &gatewayErr)
				assert.Equal(t, http.StatusBadRequest, gatewayErr.HTTPStatusCode())
				assert.Contains(t, gatewayErr.Message, core.FailoverControlHeader)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantOn, on)
			assert.Equal(t, tt.wantSent, sent)
		})
	}
}

// failoverControlServer serves gpt-4o from a failing primary with a working
// azure failover target, under a workflow whose failover feature is
// workflowFailover.
func failoverControlServer(cfg Config, workflowFailover bool) (*Server, *failoverProvider) {
	provider := &failoverProvider{
		chatResponses: map[string]*core.ChatResponse{
			"azure/gpt-4o": {
				ID:       "chatcmpl-failover",
				Object:   "chat.completion",
				Model:    "gpt-4o",
				Provider: "azure",
				Choices: []core.Choice{{
					Message:      core.ResponseMessage{Role: "assistant", Content: "failover ok"},
					FinishReason: "stop",
				}},
			},
		},
		chatErrors: map[string]error{
			"gpt-4o": core.NewProviderError("openai", http.StatusServiceUnavailable, "model temporarily unavailable", nil),
		},
		supportedModels: map[string]string{
			"gpt-4o":       "openai",
			"azure/gpt-4o": "azure",
		},
	}
	features := core.DefaultWorkflowFeatures()
	features.Failover = workflowFailover
	cfg.FailoverResolver = failoverResolverStub{selectors: []core.ModelSelector{{Provider: "azure", Model: "gpt-4o"}}}
	cfg.WorkflowPolicyResolver = requestWorkflowPolicyResolverFunc(func(core.WorkflowSelector) (*core.ResolvedWorkflowPolicy, error) {
		return &core.ResolvedWorkflowPolicy{VersionID: "workflow-1", Features: features}, nil
	})
	return New(provider, &cfg), provider
}

// failoverControl builds the headers of a request that sends the failover
// control header once per value, under the canonical key net/http gives it.
func failoverControl(values ...string) http.Header {
	header := http.Header{}
	for _, value := range values {
		header.Add(core.FailoverControlHeader, value)
	}
	return header
}

func TestRequestControls_Failover(t *testing.T) {
	const chatBody = `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`
	allowed := []string{core.FailoverControlHeader}
	primaryOnly := []string{"gpt-4o"}
	failedOver := []string{"gpt-4o", "azure/gpt-4o"}

	tests := []struct {
		name             string
		allowed          []string
		workflowFailover bool
		header           http.Header
		wantStatus       int
		wantCalls        []string
	}{
		{
			name:             "off serves the request without failover",
			allowed:          allowed,
			workflowFailover: true,
			header:           failoverControl("off"),
			wantStatus:       http.StatusServiceUnavailable,
			wantCalls:        primaryOnly,
		},
		{
			name:             "on keeps the configured failover",
			allowed:          allowed,
			workflowFailover: true,
			header:           failoverControl("on"),
			wantStatus:       http.StatusOK,
			wantCalls:        failedOver,
		},
		{
			name:             "on cannot enable failover the workflow turned off",
			allowed:          allowed,
			workflowFailover: false,
			header:           failoverControl("on"),
			wantStatus:       http.StatusServiceUnavailable,
			wantCalls:        primaryOnly,
		},
		{
			name:             "allowed name in any case",
			allowed:          []string{"x-gomodel-failover"},
			workflowFailover: true,
			header:           failoverControl("FALSE"),
			wantStatus:       http.StatusServiceUnavailable,
			wantCalls:        primaryOnly,
		},
		{
			name:             "no header",
			allowed:          allowed,
			workflowFailover: true,
			wantStatus:       http.StatusOK,
			wantCalls:        failedOver,
		},
		{
			name:             "ignored unless the operator allowed it",
			workflowFailover: true,
			header:           failoverControl("off"),
			wantStatus:       http.StatusOK,
			wantCalls:        failedOver,
		},
		{
			name:             "a value that is not allowed to steer is not validated either",
			workflowFailover: true,
			header:           failoverControl("never"),
			wantStatus:       http.StatusOK,
			wantCalls:        failedOver,
		},
		{
			name:             "unusable value is refused before any provider call",
			allowed:          allowed,
			workflowFailover: true,
			header:           failoverControl("never"),
			wantStatus:       http.StatusBadRequest,
		},
		{
			name:             "repeated header is refused",
			allowed:          allowed,
			workflowFailover: true,
			header:           failoverControl("off", "on"),
			wantStatus:       http.StatusBadRequest,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, provider := failoverControlServer(Config{RequestControlHeaders: tt.allowed}, tt.workflowFailover)

			rec := postWithHeaders(t, srv, "/v1/chat/completions", chatBody, tt.header)

			require.Equal(t, tt.wantStatus, rec.Code, rec.Body.String())
			assert.Equal(t, tt.wantCalls, provider.chatCalls)
			if tt.wantStatus == http.StatusBadRequest {
				assert.Contains(t, rec.Body.String(), "invalid_request_error")
				assert.Contains(t, rec.Body.String(), core.FailoverControlHeader)
			}
		})
	}
}

// The request log records the setting the request was served with, so an
// operator can tell a client's choice from the configured policy.
func TestRequestControls_FailoverOffShowsInRequestLog(t *testing.T) {
	requestLogger := &capturingRequestLogger{config: requestlog.Config{Enabled: true}}
	srv, _ := failoverControlServer(Config{
		RequestLogger:         requestLogger,
		RequestControlHeaders: []string{core.FailoverControlHeader},
	}, true)

	rec := postWithHeaders(t, srv, "/v1/chat/completions", `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`,
		failoverControl("off"))

	require.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
	require.Len(t, requestLogger.entries, 1)
	require.NotNil(t, requestLogger.entries[0].Data)
	require.NotNil(t, requestLogger.entries[0].Data.WorkflowFeatures)
	assert.False(t, requestLogger.entries[0].Data.WorkflowFeatures.Failover)
	assert.True(t, requestLogger.entries[0].Data.WorkflowFeatures.Guardrails)
}

// Control headers are read after authentication: a caller without a valid
// credential gets 401 whatever the header holds, never the 400 that would
// tell it the header is enabled here.
func TestRequestControls_RunAfterAuthentication(t *testing.T) {
	srv, provider := failoverControlServer(Config{
		MasterKey:             "master",
		RequestControlHeaders: []string{core.FailoverControlHeader},
	}, true)

	rec := postWithHeaders(t, srv, "/v1/chat/completions", `{"model":"gpt-4o","messages":[]}`,
		failoverControl("never"))

	require.Equal(t, http.StatusUnauthorized, rec.Code, rec.Body.String())
	assert.Empty(t, provider.chatCalls)
}

// The header steers model requests only; other routes do not read it.
func TestRequestControls_IgnoredOutsideModelRoutes(t *testing.T) {
	srv, _ := failoverControlServer(Config{RequestControlHeaders: []string{core.FailoverControlHeader}}, true)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	req.Header.Set(core.FailoverControlHeader, "never")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}

func TestBuildPassthroughHeadersSkipsRequestControlHeaders(t *testing.T) {
	headers := http.Header{}
	headers.Set(core.FailoverControlHeader, "off")
	headers.Set("OpenAI-Beta", "responses=v1")

	got := buildPassthroughHeaders(t.Context(), headers)

	assert.Empty(t, got.Values(core.FailoverControlHeader), "request control headers stay at the gateway")
	assert.Equal(t, "responses=v1", got.Get("OpenAI-Beta"))
}
