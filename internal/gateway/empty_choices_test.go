package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/stretchr/testify/require"
)

func TestDispatchChatCompletionEmptyChoices(t *testing.T) {
	tests := []struct {
		name      string
		failovers []core.ModelSelector
		wantID    string
		wantCalls []string
	}{
		{
			name:      "fails over to a target with choices",
			failovers: []core.ModelSelector{{Provider: "cloudflare", Model: "model2"}},
			wantID:    "backup",
			wantCalls: []string{"/model1", "/model2"},
		},
		{
			name:      "returns 502 without a failover target",
			wantCalls: []string{"/model1"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls = append(calls, r.URL.Path)
				if r.URL.Path == "/model1" {
					_, _ = w.Write([]byte(`{"id":"empty","model":"model1","choices":[]}`))
					return
				}
				_, _ = w.Write([]byte(`{"id":"backup","model":"model2","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"ok"}}]}`))
			}))
			defer server.Close()

			provider := &retryFailoverProvider{client: llmclient.New(llmclient.DefaultConfig("cloudflare", server.URL), nil)}
			orchestrator := NewInferenceOrchestrator(InferenceConfig{
				Provider: provider,
				FailoverResolver: failoverResolverFunc(func(*core.RequestModelResolution, core.Operation) []core.ModelSelector {
					return tt.failovers
				}),
			})
			workflow := &core.Workflow{
				Endpoint:   core.DescribeEndpoint("POST", "/v1/chat/completions"),
				Resolution: &core.RequestModelResolution{ResolvedSelector: core.ModelSelector{Provider: "cloudflare", Model: "model1"}},
				Policy:     &core.ResolvedWorkflowPolicy{Features: core.WorkflowFeatures{Failover: true}},
			}

			response, _, err := orchestrator.DispatchChatCompletion(context.Background(), workflow, &core.ChatRequest{Model: "model1"})

			if tt.wantID != "" {
				require.NoError(t, err)
				require.Equal(t, tt.wantID, response.ID)
			} else {
				var gatewayErr *core.GatewayError
				require.ErrorAs(t, err, &gatewayErr)
				require.Equal(t, http.StatusBadGateway, gatewayErr.HTTPStatusCode())
				require.Equal(t, "provider returned no choices", gatewayErr.Message)
			}
			require.Equal(t, tt.wantCalls, calls)
		})
	}
}
