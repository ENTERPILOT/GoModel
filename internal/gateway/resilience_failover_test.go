package gateway

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	goconfig "github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
	server, capture := providertest.Server(t, func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.URL.Path)
		if r.URL.Path == "/model1" {
			w.WriteHeader(524)
			return
		}
		_, _ = w.Write([]byte(`{"id":"backup","model":"model2","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"ok"}}]}`))
	})
	cfg := llmclient.DefaultConfig("cloudflare", server.URL)
	cfg.Retry.MaxRetries = 2
	cfg.Retry.InitialBackoff = time.Nanosecond
	cfg.CircuitBreaker.Scope = "model"
	cfg.CircuitBreaker.FailureThreshold = 1
	provider := &retryFailoverProvider{client: llmclient.New(cfg, nil)}
	_ = capture // uses recorded requests for audit in tests that need it
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
		require.NoError(t, err)
		require.Equal(t, "backup", response.ID, "response=%+v", response)
	}
	// First request exhausts model1; the second skips its open breaker.
	got := strings.Join(calls, ",")
	require.Equal(t, "/model1,/model1,/model1,/model2,/model2", got)
}

// quotaTripProvider is a core.Provider whose ChatCompletion/StreamChatCompletion
// delegate to an llmclient.Client.  It satisfies core.Provider and the
// embedded providerTypeResolverStub.
type quotaTripProvider struct {
	providerTypeResolverStub
	client *llmclient.Client
}

func (p *quotaTripProvider) ChatCompletion(ctx context.Context, req *core.ChatRequest) (*core.ChatResponse, error) {
	var resp core.ChatResponse
	err := p.client.Do(ctx, llmclient.Request{Method: "GET", Endpoint: req.Model, Model: req.Model}, &resp)
	if err != nil {
		return nil, err
	}
	resp.Provider = req.Provider
	return &resp, nil
}

func (p *quotaTripProvider) StreamChatCompletion(ctx context.Context, req *core.ChatRequest) (io.ReadCloser, error) {
	var resp core.ChatResponse
	err := p.client.Do(ctx, llmclient.Request{Method: "GET", Endpoint: req.Model, Model: req.Model}, &resp)
	if err != nil {
		return nil, err
	}
	return nil, nil
}

// TestTripRuleQuotaTripsPrimaryInFailoverChain exercises the full trip-on +
// failover + reset cycle: a quota error that matches a trip_on rule opens the
// breaker on the primary provider, the orchestrator falls back to the backup
// target, and a reset allows the primary to be retried.
func TestTripRuleQuotaTripsPrimaryInFailoverChain(t *testing.T) {
	t.Parallel()

	var aHits, bHits atomic.Int32
	// Combined server: /model1 returns quota 500 with usage-limit message,
	// /model2 returns 200.  We use 500 because the default failover policy
	// does not treat 403 as retryable, but the trip rule matches the
	// message text regardless of status.
	server, _ := providertest.Server(t, func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if path == "/model1" {
			aHits.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":{"message":"You've reached your weekly (7-day) usage limit, please try again after 0:02:52","code":"usage_limit_exceeded"}}`))
		} else {
			bHits.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"b-resp","model":"model2","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"ok"}}]}`))
		}
	})

	// Primary provider: quotaTripProvider with trip rules hitting /model1.
	primary := &quotaTripProvider{
		client: llmclient.New(llmclient.Config{
			ProviderName: "primary",
			BaseURL:      server.URL,
			Retry:        goconfig.DefaultRetryConfig(),
			CircuitBreaker: goconfig.CircuitBreakerConfig{
				Enabled:          true,
				FailureThreshold: 5,
				SuccessThreshold: 1,
				Timeout:          time.Minute,
				Scope:            "model",
				TripOn: goconfig.TripRuleMap{
					"weekly": {Match: `weekly.*usage limit`, TTL: time.Minute},
				},
			},
		}, nil),
	}

	// Orchestrator: primary is the tripClientProvider for /model1;
	// failover falls back to the same provider with /model2 (model-scoped
	// breaker means /model1 trip doesn't block /model2).
	orchestrator := NewInferenceOrchestrator(InferenceConfig{
		Provider: primary,
		FailoverResolver: failoverResolverFunc(func(*core.RequestModelResolution, core.Operation) []core.ModelSelector {
			return []core.ModelSelector{{Provider: "primary", Model: "/model2"}}
		}),
	})
	workflow := &core.Workflow{
		Endpoint:   core.DescribeEndpoint("POST", "/v1/chat/completions"),
		Resolution: &core.RequestModelResolution{ResolvedSelector: core.ModelSelector{Provider: "primary", Model: "/model1"}},
		Policy:     &core.ResolvedWorkflowPolicy{Features: core.WorkflowFeatures{Failover: true}},
	}

	// Request 1: trips breaker on model1, failover to model2.
	resp, _, err := orchestrator.DispatchChatCompletion(context.Background(), workflow, &core.ChatRequest{Model: "/model1"})
	require.NoError(t, err)
	require.Equal(t, "b-resp", resp.ID)
	assert.Equal(t, int32(1), aHits.Load(), "model1 should be called once (tripped)")
	assert.Equal(t, int32(1), bHits.Load(), "model2 should be called once (failover)")

	// Request 2: model1 breaker is open → tripped → skip. Failover to model2.
	resp, _, err = orchestrator.DispatchChatCompletion(context.Background(), workflow, &core.ChatRequest{Model: "/model1"})
	require.NoError(t, err)
	require.Equal(t, "b-resp", resp.ID)
	assert.Equal(t, int32(1), aHits.Load(), "model1 must not be called again while tripped")
	assert.Equal(t, int32(2), bHits.Load(), "model2 serves the second request via failover")

	// Reset breaker via the trip client's ResetBreaker method.
	// (In production the admin API calls providers.NewBreakerResetter, which
	// type-asserts the adapter and calls ResetBreaker.)
	primary.client.ResetBreaker()

	// Request 3: breaker is reset, model1 is contacted again (and trips).
	resp, _, err = orchestrator.DispatchChatCompletion(context.Background(), workflow, &core.ChatRequest{Model: "/model1"})
	require.NoError(t, err)
	require.Equal(t, "b-resp", resp.ID)
	assert.Equal(t, int32(2), aHits.Load(), "model1 must be contacted again after reset")
	assert.Equal(t, int32(3), bHits.Load(), "failover serves again as model1 trips once more")
}
