package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
)

type retryFailoverProvider struct {
	providerTypeResolverStub
	client *llmclient.Client
}

func (p *retryFailoverProvider) ChatCompletion(ctx context.Context, req *core.ChatRequest) (*core.ChatResponse, error) {
	model := strings.TrimPrefix(req.Model, "cloudflare/")
	var response core.ChatResponse
	err := p.client.Do(ctx, llmclient.Request{Method: "GET", Endpoint: "/" + model, Model: model}, &response)
	return &response, err
}

func TestCloudflareTimeoutRetriesBeforeModelFailover(t *testing.T) {
	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.URL.Path)
		if r.URL.Path == "/model1" {
			w.WriteHeader(524)
			return
		}
		_, _ = w.Write([]byte(`{"id":"backup","model":"model2","choices":[]}`))
	}))
	defer server.Close()
	cfg := llmclient.DefaultConfig("cloudflare", server.URL)
	cfg.Retry.MaxRetries = 2
	cfg.Retry.InitialBackoff = time.Nanosecond
	cfg.CircuitBreaker.Scope = "model"
	cfg.CircuitBreaker.FailureThreshold = 1
	provider := &retryFailoverProvider{client: llmclient.New(cfg, nil)}
	orchestrator := NewInferenceOrchestrator(InferenceConfig{
		Provider: provider,
		FailoverResolver: failoverResolverFunc(func(*core.RequestModelResolution, core.Operation) []core.ModelSelector {
			return []core.ModelSelector{{Provider: "cloudflare", Model: "model2"}}
		}),
	})
	workflow := &core.Workflow{
		Endpoint:   core.DescribeEndpoint("POST", "/v1/chat/completions"),
		Resolution: &core.RequestModelResolution{ResolvedSelector: core.ModelSelector{Provider: "cloudflare", Model: "model1"}},
		Policy:     &core.ResolvedWorkflowPolicy{Features: core.WorkflowFeatures{Failover: true}},
	}
	for range 2 {
		response, _, err := orchestrator.DispatchChatCompletion(context.Background(), workflow, &core.ChatRequest{Model: "model1"})
		if err != nil {
			t.Fatal(err)
		}
		if response.ID != "backup" {
			t.Fatalf("response=%+v", response)
		}
	}
	// First request exhausts model1; the second skips its open breaker.
	if got := strings.Join(calls, ","); got != "/model1,/model1,/model1,/model2,/model2" {
		t.Fatalf("upstream calls=%s", got)
	}
}
