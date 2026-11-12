package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/cache"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/markups"
	"github.com/enterpilot/gomodel/internal/responsecache"
	"github.com/enterpilot/gomodel/internal/usage"
)

// Every fixture below reports 10 input and 5 output tokens, priced at $1 and
// $2 per million: a provider cost of $0.00002.
const responseCostAmount = "0.00002"

var responseCostPricing = &core.ModelPricing{InputPerMtok: new(1.0), OutputPerMtok: new(2.0)}

const (
	chatCostStream = `data: {"id":"chatcmpl-1","object":"chat.completion.chunk","model":"gpt-4o-mini","choices":[{"index":0,"delta":{"content":"Hi"},"finish_reason":null}]}

data: {"id":"chatcmpl-1","object":"chat.completion.chunk","model":"gpt-4o-mini","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}

data: {"id":"chatcmpl-1","object":"chat.completion.chunk","model":"gpt-4o-mini","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}

data: [DONE]

`
	responsesCostStream = `event: response.created
data: {"type":"response.created","response":{"id":"resp_1","object":"response","model":"gpt-4o-mini","status":"in_progress","usage":null}}

event: response.output_text.delta
data: {"type":"response.output_text.delta","delta":"Hi"}

event: response.completed
data: {"type":"response.completed","response":{"id":"resp_1","object":"response","model":"gpt-4o-mini","status":"completed","usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}}

`
	anthropicCostStream = `event: message_start
data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","content":[],"model":"claude-fable-5","usage":{"input_tokens":10,"output_tokens":1}}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hi"}}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":5}}

event: message_stop
data: {"type":"message_stop"}

`
	anthropicCostMessage = `{"id":"msg_1","type":"message","role":"assistant","model":"claude-fable-5","content":[{"type":"text","text":"Hi"}],"stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":5}}`
)

func costChatProvider() *mockProvider {
	return &mockProvider{
		supportedModels: []string{"gpt-4o-mini"},
		streamData:      chatCostStream,
		response: &core.ChatResponse{
			ID:      "chatcmpl-1",
			Object:  "chat.completion",
			Model:   "gpt-4o-mini",
			Choices: []core.Choice{{Message: core.ResponseMessage{Role: "assistant", Content: "Hi"}, FinishReason: "stop"}},
			Usage:   core.Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
		},
	}
}

func costResponsesProvider() *mockProvider {
	return &mockProvider{
		supportedModels: []string{"gpt-4o-mini"},
		streamData:      responsesCostStream,
		responsesResponse: &core.ResponsesResponse{
			ID:     "resp_1",
			Object: "response",
			Model:  "gpt-4o-mini",
			Status: "completed",
			Usage:  &core.ResponsesUsage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15},
		},
	}
}

func costEmbeddingsProvider() *mockProvider {
	return &mockProvider{
		supportedModels: []string{"text-embedding-3-small"},
		embeddingResponse: &core.EmbeddingResponse{
			Object: "list",
			Model:  "text-embedding-3-small",
			Data:   []core.EmbeddingData{{Object: "embedding", Embedding: json.RawMessage(`[0.1]`)}},
			Usage:  core.EmbeddingUsage{PromptTokens: 10, TotalTokens: 10},
		},
	}
}

// costNativeMessagesProvider answers /v1/messages the way the Anthropic
// provider does: the gateway forwards the request and relays the answer.
func costNativeMessagesProvider(contentType, body string) func() *mockProvider {
	return func() *mockProvider {
		return &mockProvider{
			supportedModels: []string{"claude-fable-5"},
			providerTypes:   map[string]string{"claude-fable-5": "anthropic"},
			passthroughResponse: &core.PassthroughResponse{
				StatusCode: http.StatusOK,
				Headers:    map[string][]string{"Content-Type": {contentType}},
				Body:       io.NopCloser(strings.NewReader(body)),
			},
		}
	}
}

// costGateway builds a gateway that tracks usage at responseCostPricing.
func costGateway(t *testing.T, provider core.RoutableProvider, configure ...func(*Config)) (*Server, *collectingUsageLogger) {
	t.Helper()
	logger := &collectingUsageLogger{config: usage.Config{Enabled: true, EnforceReturningUsageData: true}}
	cfg := &Config{UsageLogger: logger, PricingResolver: &mockPricingResolver{pricing: responseCostPricing}}
	for _, apply := range configure {
		apply(cfg)
	}
	return New(provider, cfg), logger
}

func returnCosts(cfg *Config) { cfg.ResponseCostEnabled = true }

// billCosts returns billed costs, marked up by percent over the provider cost.
func billCosts(t *testing.T, percent float64, billCacheHits bool) func(*Config) {
	t.Helper()
	service, err := markups.New(config.PricingConfig{
		MarkupsEnabled: true,
		Markups:        map[string]float64{"/": percent},
		BillCacheHits:  billCacheHits,
	}, costProviderNames{}, false)
	require.NoError(t, err)
	return func(cfg *Config) {
		cfg.ResponseCostEnabled = true
		cfg.ResponseCostBilled = true
		cfg.UsageLogger = markups.NewUsageTap(cfg.UsageLogger, service)
	}
}

type costProviderNames struct{}

func (costProviderNames) ProviderNames() []string { return []string{"mock"} }

func cachedResponses(t *testing.T) func(*Config) {
	t.Helper()
	store := cache.NewMapStore()
	t.Cleanup(func() { _ = store.Close() })
	return func(cfg *Config) {
		cfg.ResponseCacheMiddleware = responsecache.NewResponseCacheMiddlewareWithStoreAndUsage(store, time.Hour, cfg.UsageLogger, cfg.PricingResolver)
	}
}

// streamedCost returns the cost of the last event that carries one, and the
// number of events that do.
func streamedCost(body string) (gjson.Result, int) {
	var cost gjson.Result
	count := 0
	for line := range strings.SplitSeq(body, "\n") {
		data, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			continue
		}
		for _, path := range []string{"usage.cost", "response.usage.cost"} {
			if found := gjson.Get(data, path); found.Exists() {
				cost = found
				count++
			}
		}
	}
	return cost, count
}

// costMember is the member the gateway appends to a non-empty usage object.
func costMember(amount string) string {
	return `,"cost":` + amount
}

type costRequest struct {
	name     string
	path     string
	body     string
	provider func() *mockProvider
}

var bufferedCostRequests = []costRequest{
	{
		name: "chat completions", path: "/v1/chat/completions", provider: costChatProvider,
		body: `{"model":"gpt-4o-mini","messages":[{"role":"user","content":"Hi"}]}`,
	},
	{
		name: "responses", path: "/v1/responses", provider: costResponsesProvider,
		body: `{"model":"gpt-4o-mini","input":"Hi"}`,
	},
	{
		name: "embeddings", path: "/v1/embeddings", provider: costEmbeddingsProvider,
		body: `{"model":"text-embedding-3-small","input":"Hi"}`,
	},
	{
		name: "messages translated", path: "/v1/messages", provider: costChatProvider,
		body: `{"model":"gpt-4o-mini","max_tokens":64,"messages":[{"role":"user","content":"Hi"}]}`,
	},
	{
		name: "messages relayed natively", path: "/v1/messages",
		provider: costNativeMessagesProvider("application/json", anthropicCostMessage),
		body:     `{"model":"claude-fable-5","max_tokens":64,"messages":[{"role":"user","content":"Hi"}]}`,
	},
}

var streamedCostRequests = []costRequest{
	{
		name: "chat completions", path: "/v1/chat/completions", provider: costChatProvider,
		body: `{"model":"gpt-4o-mini","stream":true,"messages":[{"role":"user","content":"Hi"}]}`,
	},
	{
		name: "responses", path: "/v1/responses", provider: costResponsesProvider,
		body: `{"model":"gpt-4o-mini","stream":true,"input":"Hi"}`,
	},
	{
		name: "messages translated", path: "/v1/messages", provider: costChatProvider,
		body: `{"model":"gpt-4o-mini","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"Hi"}]}`,
	},
	{
		name: "messages relayed natively", path: "/v1/messages",
		provider: costNativeMessagesProvider("text/event-stream", anthropicCostStream),
		body:     `{"model":"claude-fable-5","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"Hi"}]}`,
	},
}

// Off by default: no header, and every response body stays as it is.
func TestResponseCost_OffByDefault(t *testing.T) {
	for _, tt := range append(append([]costRequest{}, bufferedCostRequests...), streamedCostRequests...) {
		t.Run(tt.name, func(t *testing.T) {
			srv, logger := costGateway(t, tt.provider())

			rec := postJSON(t, srv, tt.path, tt.body)

			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			require.Len(t, logger.entries, 1)
			require.NotNil(t, logger.entries[0].TotalCost)
			assert.Empty(t, rec.Header().Values(responseCostHeader))
			assert.Empty(t, rec.Header().Values(responseCostCurrencyHeader))
			assert.NotContains(t, rec.Body.String(), `"cost"`)
		})
	}
}

// A buffered response carries its cost as usage.cost and in the headers, the
// same rounded value as the usage record. Nothing else in the body changes.
func TestResponseCost_BufferedResponsesCarryCost(t *testing.T) {
	for _, tt := range bufferedCostRequests {
		t.Run(tt.name, func(t *testing.T) {
			plain, _ := costGateway(t, tt.provider())
			want := postJSON(t, plain, tt.path, tt.body)
			srv, logger := costGateway(t, tt.provider(), returnCosts)

			rec := postJSON(t, srv, tt.path, tt.body)

			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			wantAmount := responseCostAmount
			if tt.path == "/v1/embeddings" {
				wantAmount = "0.00001" // 10 input tokens, no output
			}
			assert.Equal(t, wantAmount, gjson.Get(rec.Body.String(), "usage.cost").Raw)
			assert.Equal(t, want.Body.String(), strings.Replace(rec.Body.String(), costMember(wantAmount), "", 1),
				"only the cost member may differ from the body sent with response costs off")
			assert.Equal(t, wantAmount, rec.Header().Get(responseCostHeader))
			assert.Equal(t, "USD", rec.Header().Get(responseCostCurrencyHeader))
			require.Len(t, logger.entries, 1)
			require.NotNil(t, logger.entries[0].TotalCost)
			assert.Equal(t, formatResponseCost(*logger.entries[0].TotalCost), rec.Header().Get(responseCostHeader),
				"the header and the usage record must agree")
		})
	}
}

// A streamed response starts before its cost is known, so the cost rides in
// the usage object of the event that carries the final token counts. Nothing
// else in the stream changes.
func TestResponseCost_StreamsCarryCostInFinalUsage(t *testing.T) {
	for _, tt := range streamedCostRequests {
		t.Run(tt.name, func(t *testing.T) {
			plain, _ := costGateway(t, tt.provider())
			want := postJSON(t, plain, tt.path, tt.body)
			srv, logger := costGateway(t, tt.provider(), returnCosts)

			rec := postJSON(t, srv, tt.path, tt.body)

			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			assert.Empty(t, rec.Header().Values(responseCostHeader), "the headers are sent before a stream's cost is known")
			cost, events := streamedCost(rec.Body.String())
			require.Equal(t, 1, events, rec.Body.String())
			assert.Equal(t, responseCostAmount, cost.Raw)
			assert.Equal(t, want.Body.String(), strings.Replace(rec.Body.String(), costMember(responseCostAmount), "", 1),
				"only the cost member may differ from the stream sent with response costs off")
			require.Len(t, logger.entries, 1)
			require.NotNil(t, logger.entries[0].TotalCost)
			assert.Equal(t, responseCostAmount, formatResponseCost(*logger.entries[0].TotalCost))
		})
	}
}

// With markups enabled the caller sees the billed cost and nothing else.
func TestResponseCost_BilledCostReplacesProviderCost(t *testing.T) {
	const billed = "0.00004" // $0.00002 plus the 100% markup

	t.Run("buffered", func(t *testing.T) {
		srv, logger := costGateway(t, costChatProvider(), billCosts(t, 100, true))

		rec := postJSON(t, srv, "/v1/chat/completions", bufferedCostRequests[0].body)

		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assert.Equal(t, []string{billed}, rec.Header().Values(responseCostHeader))
		assert.Equal(t, billed, gjson.Get(rec.Body.String(), "usage.cost").Raw)
		require.Len(t, logger.entries, 1)
		assert.Equal(t, responseCostAmount, formatResponseCost(*logger.entries[0].TotalCost))
		assert.Equal(t, billed, formatResponseCost(*logger.entries[0].BilledCost))
	})

	t.Run("streamed", func(t *testing.T) {
		srv, _ := costGateway(t, costChatProvider(), billCosts(t, 100, true))

		rec := postJSON(t, srv, "/v1/chat/completions", streamedCostRequests[0].body)

		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		cost, events := streamedCost(rec.Body.String())
		require.Equal(t, 1, events, rec.Body.String())
		assert.Equal(t, billed, cost.Raw)
		assert.NotContains(t, rec.Body.String(), responseCostAmount)
	})

	// A usage logger that stamps no billed cost must not make the gateway
	// fall back to the provider cost.
	t.Run("no billed cost recorded", func(t *testing.T) {
		srv, _ := costGateway(t, costChatProvider(), func(cfg *Config) {
			cfg.ResponseCostEnabled = true
			cfg.ResponseCostBilled = true
		})

		rec := postJSON(t, srv, "/v1/chat/completions", bufferedCostRequests[0].body)

		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assert.Empty(t, rec.Header().Values(responseCostHeader))
		assert.False(t, gjson.Get(rec.Body.String(), "usage.cost").Exists())
	})

	// A wrapper above the markup tap that does not forward StampBilling hides
	// the billed cost until the entry is written. The response carries no
	// cost, never the provider cost; the usage log still holds the billed cost
	// (usage.CostReport.Record logs the gap as an error).
	t.Run("wrapper without billing stamp", func(t *testing.T) {
		billed := billCosts(t, 100, true)
		srv, logger := costGateway(t, costChatProvider(), func(cfg *Config) {
			billed(cfg)
			cfg.UsageLogger = opaqueUsageLogger{cfg.UsageLogger}
		})

		rec := postJSON(t, srv, "/v1/chat/completions", bufferedCostRequests[0].body)

		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assert.Empty(t, rec.Header().Values(responseCostHeader))
		assert.False(t, gjson.Get(rec.Body.String(), "usage.cost").Exists())
		require.Len(t, logger.entries, 1)
		assert.Equal(t, "0.00004", formatResponseCost(*logger.entries[0].BilledCost))
	})
}

// opaqueUsageLogger wraps a usage logger without implementing
// usage.BillingStamper.
type opaqueUsageLogger struct{ usage.LoggerInterface }

// A model without pricing has an unknown cost, which is not a cost of zero:
// the response carries nothing.
func TestResponseCost_UnknownPricingCarriesNothing(t *testing.T) {
	unpriced := func(cfg *Config) {
		cfg.ResponseCostEnabled = true
		cfg.PricingResolver = &mockPricingResolver{}
	}
	for _, tt := range []costRequest{bufferedCostRequests[0], streamedCostRequests[0], streamedCostRequests[3]} {
		t.Run(tt.name, func(t *testing.T) {
			plain, _ := costGateway(t, tt.provider())
			want := postJSON(t, plain, tt.path, tt.body)
			srv, logger := costGateway(t, tt.provider(), unpriced)

			rec := postJSON(t, srv, tt.path, tt.body)

			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			require.Len(t, logger.entries, 1)
			require.Nil(t, logger.entries[0].TotalCost)
			assert.Empty(t, rec.Header().Values(responseCostHeader))
			assert.Empty(t, rec.Header().Values(responseCostCurrencyHeader))
			assert.Equal(t, want.Body.String(), rec.Body.String())
		})
	}
}

// A response served from the response cache called no provider: its provider
// cost is zero, and its billed cost is whatever the hit is billed at. The
// cache never holds the cost of the request that filled it.
func TestResponseCost_CacheHits(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*testing.T) func(*Config)
		miss      string
		hit       string
	}{
		{name: "provider cost", configure: func(*testing.T) func(*Config) { return returnCosts }, miss: responseCostAmount, hit: "0"},
		{name: "billed hits", configure: func(t *testing.T) func(*Config) { return billCosts(t, 100, true) }, miss: "0.00004", hit: "0.00004"},
		{name: "unbilled hits", configure: func(t *testing.T) func(*Config) { return billCosts(t, 100, false) }, miss: "0.00004", hit: "0"},
	}
	for _, tt := range tests {
		t.Run(tt.name+"/buffered", func(t *testing.T) {
			srv, _ := costGateway(t, costChatProvider(), tt.configure(t), cachedResponses(t))
			body := bufferedCostRequests[0].body

			miss := postJSON(t, srv, "/v1/chat/completions", body)
			require.Equal(t, http.StatusOK, miss.Code, miss.Body.String())
			require.Empty(t, miss.Header().Get("X-Cache"))
			assert.Equal(t, tt.miss, miss.Header().Get(responseCostHeader))
			assert.Equal(t, tt.miss, gjson.Get(miss.Body.String(), "usage.cost").Raw)

			var hit *httptest.ResponseRecorder
			require.Eventually(t, func() bool { // the cache is written off the request path
				hit = postJSON(t, srv, "/v1/chat/completions", body)
				return hit.Header().Get("X-Cache") != ""
			}, 2*time.Second, 5*time.Millisecond)
			assert.Equal(t, tt.hit, hit.Header().Get(responseCostHeader))
			assert.Equal(t, "USD", hit.Header().Get(responseCostCurrencyHeader))
			assert.Equal(t, tt.hit, gjson.Get(hit.Body.String(), "usage.cost").Raw)
			assert.Equal(t, strings.Replace(miss.Body.String(), costMember(tt.miss), "", 1),
				strings.Replace(hit.Body.String(), costMember(tt.hit), "", 1), "the hit replays the answer, with its own cost")
		})

		t.Run(tt.name+"/streamed", func(t *testing.T) {
			srv, _ := costGateway(t, costChatProvider(), tt.configure(t), cachedResponses(t))
			body := streamedCostRequests[0].body

			miss := postJSON(t, srv, "/v1/chat/completions", body)
			require.Equal(t, http.StatusOK, miss.Code, miss.Body.String())
			cost, events := streamedCost(miss.Body.String())
			require.Equal(t, 1, events, miss.Body.String())
			assert.Equal(t, tt.miss, cost.Raw)

			var hit *httptest.ResponseRecorder
			require.Eventually(t, func() bool {
				hit = postJSON(t, srv, "/v1/chat/completions", body)
				return hit.Header().Get("X-Cache") != ""
			}, 2*time.Second, 5*time.Millisecond)
			// A stream carries its cost in its events only, even where it
			// is known before the replay starts.
			assert.Empty(t, hit.Header().Values(responseCostHeader))
			cost, events = streamedCost(hit.Body.String())
			require.Equal(t, 1, events, hit.Body.String())
			assert.Equal(t, tt.hit, cost.Raw)
		})
	}
}

// Images and buffered audio record their usage before they answer, so they
// carry the cost header like every other buffered response; it is their only
// channel, since their answers carry no usage object. Audio the gateway
// relays as it arrives is measured, and priced, only once it has been sent.
func TestResponseCost_MediaEndpoints(t *testing.T) {
	speech := func(resp *core.AudioResponse) core.RoutableProvider {
		return &audioMockProvider{mockProvider: &mockProvider{supportedModels: []string{"gpt-4o-mini-tts"}}, speechResp: resp}
	}
	const speechBody = `{"model":"gpt-4o-mini-tts","input":"hello","voice":"alloy"}`
	tests := []struct {
		name     string
		provider core.RoutableProvider
		pricing  *core.ModelPricing
		path     string
		body     string
		want     string
	}{
		{
			name: "image generation", provider: newImageMock(), pricing: &core.ModelPricing{PerImage: new(0.04)},
			path: "/v1/images/generations", body: `{"model":"dall-e-3","prompt":"a cat"}`, want: "0.04",
		},
		{
			name:     "buffered speech",
			provider: speech(&core.AudioResponse{ContentType: "audio/mpeg", Data: []byte("synthetic-audio")}),
			pricing:  &core.ModelPricing{PerCharacterInput: new(0.001)},
			path:     "/v1/audio/speech", body: speechBody, want: "0.005",
		},
		{
			name:     "relayed speech",
			provider: speech(&core.AudioResponse{ContentType: "audio/mpeg", Stream: io.NopCloser(strings.NewReader("synthetic-audio"))}),
			pricing:  &core.ModelPricing{PerCharacterInput: new(0.001)},
			path:     "/v1/audio/speech", body: speechBody,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, logger := costGateway(t, tt.provider, func(cfg *Config) {
				cfg.ResponseCostEnabled = true
				cfg.PricingResolver = &mockPricingResolver{pricing: tt.pricing}
			})

			rec := postJSON(t, srv, tt.path, tt.body)

			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			require.Len(t, logger.entries, 1)
			require.NotNil(t, logger.entries[0].TotalCost)
			assert.Equal(t, tt.want, rec.Header().Get(responseCostHeader))
			if tt.want != "" {
				assert.Equal(t, "USD", rec.Header().Get(responseCostCurrencyHeader))
			}
		})
	}
}

// A gateway error answers before any usage is recorded and carries no cost.
func TestResponseCost_ErrorsCarryNothing(t *testing.T) {
	provider := costChatProvider()
	provider.err = core.NewProviderError("mock", http.StatusBadGateway, "upstream failed", nil)
	srv, logger := costGateway(t, provider, returnCosts)

	rec := postJSON(t, srv, "/v1/chat/completions", bufferedCostRequests[0].body)

	require.Equal(t, http.StatusBadGateway, rec.Code, rec.Body.String())
	assert.Empty(t, logger.entries)
	assert.Empty(t, rec.Header().Values(responseCostHeader))
}

// Answers the gateway relays in the provider's own dialect (/v1/decisions,
// /p/{provider}/..., a natively forwarded /v1/messages) are read before they
// are relayed when costs are returned, so the header can carry the cost.
func TestResponseCost_RelayedJSONAnswers(t *testing.T) {
	t.Run("decisions", func(t *testing.T) {
		srv, logger := costGateway(t, newDecisionsTestProvider(), returnCosts, func(cfg *Config) {
			cfg.ModelResolver = decisionAliases
		})

		rec := postJSON(t, srv, "/v1/decisions", decisionBody("classifier"))

		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		require.Len(t, logger.entries, 1)
		require.NotNil(t, logger.entries[0].TotalCost)
		assert.Equal(t, formatResponseCost(*logger.entries[0].TotalCost), rec.Header().Get(responseCostHeader))
		if usageObject := gjson.Get(rec.Body.String(), "usage"); usageObject.IsObject() {
			assert.Equal(t, rec.Header().Get(responseCostHeader), usageObject.Get("cost").Raw)
		}
	})

	// The answer is not committed yet when the provider body fails, so the
	// client gets a gateway error instead of a truncated answer.
	t.Run("provider body fails", func(t *testing.T) {
		provider := costNativeMessagesProvider("application/json", "")()
		provider.passthroughResponse.Body = io.NopCloser(io.MultiReader(strings.NewReader(anthropicCostMessage[:40]), &failingReader{}))
		srv, logger := costGateway(t, provider, returnCosts)

		rec := postJSON(t, srv, "/v1/messages", bufferedCostRequests[4].body)

		assert.Equal(t, http.StatusBadGateway, rec.Code, rec.Body.String())
		assert.Empty(t, logger.entries)
		assert.Empty(t, rec.Header().Values(responseCostHeader))
	})
}

// Unknown pricing returns nothing in every mode, even where the bill is fixed
// at zero: 0 only ever means a priced request that cost nothing.
func TestResponseCost_UnpricedCacheHitsCarryNothing(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*testing.T) func(*Config)
	}{
		{name: "provider cost", configure: func(*testing.T) func(*Config) { return returnCosts }},
		{name: "unbilled hits", configure: func(t *testing.T) func(*Config) { return billCosts(t, 100, false) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, _ := costGateway(t, costChatProvider(), func(cfg *Config) {
				cfg.PricingResolver = &mockPricingResolver{}
			}, tt.configure(t), cachedResponses(t))
			body := bufferedCostRequests[0].body

			miss := postJSON(t, srv, "/v1/chat/completions", body)
			require.Equal(t, http.StatusOK, miss.Code, miss.Body.String())
			var hit *httptest.ResponseRecorder
			require.Eventually(t, func() bool {
				hit = postJSON(t, srv, "/v1/chat/completions", body)
				return hit.Header().Get("X-Cache") != ""
			}, 2*time.Second, 5*time.Millisecond)

			assert.Empty(t, miss.Header().Values(responseCostHeader))
			assert.Empty(t, hit.Header().Values(responseCostHeader))
			assert.Empty(t, hit.Header().Values(responseCostCurrencyHeader))
			assert.False(t, gjson.Get(hit.Body.String(), "usage.cost").Exists())
		})
	}
}
