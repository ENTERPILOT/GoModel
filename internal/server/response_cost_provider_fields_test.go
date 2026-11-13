package server

import (
	"net/http"
	"strings"
	"testing"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/usage"
)

// providerCostDigits appears in every provider-reported cost of these
// fixtures and in nothing else, so one search finds a cost that survived.
const providerCostDigits = "987654321"

// providerCostValue is the value a provider reports in field.
func providerCostValue(field usage.ProviderCostField) any {
	if field.Object {
		return map[string]any{"upstream_inference_cost": 0.987654321}
	}
	return 0.987654321
}

// providerCostMember is field as a JSON member, `"name":value`.
func providerCostMember(t *testing.T, field usage.ProviderCostField) string {
	t.Helper()
	value, err := json.Marshal(providerCostValue(field))
	require.NoError(t, err)
	return `"` + field.Name + `":` + string(value)
}

// withProviderCost returns payload, a JSON object with one usage object that
// reports pricedUsage, with field where a provider reports it: among the
// usage members or, for a root field, in front of the usage object. nested
// puts a usage member under raw_usage instead.
func withProviderCost(t *testing.T, payload string, field usage.ProviderCostField, nested bool) string {
	t.Helper()
	member := providerCostMember(t, field)
	require.Contains(t, payload, `"usage":{`)
	switch {
	case field.Root:
		return strings.Replace(payload, `"usage":{`, member+`,"usage":{`, 1)
	case nested:
		return strings.Replace(payload, `"usage":{`, `"usage":{"`+usage.RawUsageMember+`":{`+member+`},`, 1)
	}
	return strings.Replace(payload, `"usage":{`, `"usage":{`+member+`,`, 1)
}

// providerCostPath is one way a provider's response reaches the client.
type providerCostPath struct {
	name string
	path string
	body string
	// nested reports the usage members under raw_usage.
	nested bool
	// billed is the cost a 50% markup makes of the request; empty when the
	// response carries none.
	billed string
	// provider answers with field reported; nil when the path cannot carry it.
	provider func(t *testing.T, field usage.ProviderCostField) *mockProvider
}

func providerCostPaths() []providerCostPath {
	const (
		relayedChat   = `{"id":"x","object":"chat.completion","choices":[],"usage":{` + pricedUsage + `}}`
		relayedChunk  = `{"id":"x","object":"chat.completion.chunk","choices":[],"usage":{` + pricedUsage + `}}`
		relayedDelta  = `data: {"id":"x","choices":[{"index":0,"delta":{"content":"Hi"}}]}` + "\n\n"
		relayedEnding = "\n\ndata: [DONE]\n\n"
	)
	relayed := func(header http.Header, wrap func(payload string) string, payload string, nested bool) func(*testing.T, usage.ProviderCostField) *mockProvider {
		return func(t *testing.T, field usage.ProviderCostField) *mockProvider {
			return costPassthroughProvider(header, strings.NewReader(wrap(withProviderCost(t, payload, field, nested))))
		}
	}
	whole := func(payload string) string { return payload }
	event := func(payload string) string { return relayedDelta + "data: " + payload + relayedEnding }
	return []providerCostPath{
		{
			name: "typed chat", path: "/v1/chat/completions", body: bufferedCostRequests[0].body, billed: "0.00003",
			provider: func(t *testing.T, field usage.ProviderCostField) *mockProvider {
				provider := costChatProvider()
				if !field.Root {
					provider.response.Usage.RawUsage = map[string]any{field.Name: providerCostValue(field)}
					return provider
				}
				value, err := json.Marshal(providerCostValue(field))
				require.NoError(t, err)
				provider.response.ExtraFields = core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{field.Name: value})
				return provider
			},
		},
		{
			name: "typed chat stream", path: "/v1/chat/completions", body: streamedCostRequests[0].body, billed: "0.00003",
			provider: func(t *testing.T, field usage.ProviderCostField) *mockProvider {
				provider := costChatProvider()
				provider.streamData = event(withProviderCost(t, relayedChunk, field, false))
				return provider
			},
		},
		{
			name: "typed embeddings", path: "/v1/embeddings", body: bufferedCostRequests[2].body, nested: true, billed: "0.000015",
			provider: func(_ *testing.T, field usage.ProviderCostField) *mockProvider {
				if field.Root {
					return nil // an embedding response has no member the provider's root can ride in
				}
				provider := costEmbeddingsProvider()
				provider.embeddingResponse.Usage.RawUsage = map[string]any{field.Name: providerCostValue(field)}
				return provider
			},
		},
		{
			name: "relayed JSON", path: passthroughChatPath, body: passthroughChatBody, billed: "0.00003",
			provider: relayed(jsonHeader(), whole, relayedChat, false),
		},
		{
			name: "relayed JSON, raw_usage", path: passthroughChatPath, body: passthroughChatBody, nested: true, billed: "0.00003",
			provider: relayed(jsonHeader(), whole, relayedChat, true),
		},
		{
			name: "relayed stream", path: passthroughChatPath, body: passthroughStreamBody, billed: "0.00003",
			provider: relayed(sseHeader(), event, relayedChunk, false),
		},
		{
			name: "relayed stream, raw_usage", path: passthroughChatPath, body: passthroughStreamBody, nested: true, billed: "0.00003",
			provider: relayed(sseHeader(), event, relayedChunk, true),
		},
		{
			// The usage reader knows LF and CRLF framing only, so this stream
			// is not priced; its events are cleaned all the same.
			name: "relayed stream, bare CR framing", path: passthroughChatPath, body: passthroughStreamBody,
			provider: relayed(sseHeader(), func(payload string) string {
				return strings.ReplaceAll(event(payload), "\n", "\r")
			}, relayedChunk, false),
		},
		{
			name: "native messages", path: "/v1/messages", body: bufferedCostRequests[4].body, billed: "0.00003",
			provider: func(t *testing.T, field usage.ProviderCostField) *mockProvider {
				return costNativeMessagesProvider("application/json", withProviderCost(t, anthropicCostMessage, field, false))()
			},
		},
		{
			name: "native messages stream", path: "/v1/messages", body: streamedCostRequests[3].body, billed: "0.00003",
			provider: func(t *testing.T, field usage.ProviderCostField) *mockProvider {
				// A usage member goes into message_start, whose usage sits
				// under "message"; a root member to the root of message_delta.
				stream := withProviderCost(t, anthropicCostStream, usage.ProviderCostField{Name: field.Name, Object: field.Object}, false)
				if field.Root {
					stream = strings.Replace(anthropicCostStream, `{"type":"message_delta",`, `{`+providerCostMember(t, field)+`,"type":"message_delta",`, 1)
				}
				return costNativeMessagesProvider("text/event-stream", stream)()
			},
		},
	}
}

// With markups on, a client learns the billed cost and nothing the provider
// reported: every member the usage log reads a provider's cost from
// (usage.ProviderCostFields) is gone from every kind of response. With markups
// off the same members pass through, except usage.cost, which is this
// gateway's whenever costs are returned.
func TestResponseCost_ProviderCostFields(t *testing.T) {
	fields := usage.ProviderCostFields()
	require.NotEmpty(t, fields)
	for _, route := range providerCostPaths() {
		for _, field := range fields {
			name := route.name + "/usage." + field.Name
			if field.Root {
				name = route.name + "/root " + field.Name
			}
			if route.provider(t, field) == nil {
				continue
			}
			t.Run(name+"/markups on", func(t *testing.T) {
				srv, _ := costGateway(t, route.provider(t, field), billCosts(t, 50, true))

				rec := postJSON(t, srv, route.path, route.body)

				require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
				body := rec.Body.String()
				assert.NotContains(t, body, providerCostDigits)
				assert.NotContains(t, body, responseCostAmount, "the provider cost the gateway calculated")
				if !field.Root {
					assert.NotContains(t, strings.ToLower(body), `"`+field.Name+`":`+providerCostDigits)
				}
				if route.billed == "" {
					assert.NotContains(t, strings.ToLower(body), `"cost"`)
					return
				}
				assert.Contains(t, body, `"cost":`+route.billed, "the provider cost plus 50%%")
			})

			t.Run(name+"/markups off", func(t *testing.T) {
				srv, _ := costGateway(t, route.provider(t, field), returnCosts)

				rec := postJSON(t, srv, route.path, route.body)

				require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
				// usage.cost is the gateway's own member; every other one is
				// the provider's and is relayed as it was.
				owned := !field.Root && !route.nested && field.Name == usageCostMember
				assert.Equal(t, !owned, strings.Contains(rec.Body.String(), providerCostDigits), rec.Body.String())
			})
		}
	}
}

// The name raw_usage is the one core's usage types serialise RawUsage under.
func TestResponseCost_RawUsageMemberIsCoreJSONName(t *testing.T) {
	encoded, err := json.Marshal(core.EmbeddingUsage{RawUsage: map[string]any{"cost": 1}})
	require.NoError(t, err)

	assert.True(t, gjson.GetBytes(encoded, usage.RawUsageMember+".cost").Exists(), "%s", encoded)
}

// A cost the provider reports under a name the gateway does not read is not
// known to be one, and stays: the guarantee covers usage.ProviderCostFields.
func TestResponseCost_UnknownProviderCostFieldStays(t *testing.T) {
	const answer = `{"id":"x","choices":[],"usage":{` + pricedUsage + `,"estimated_cost":0.987654321}}`
	srv, _ := costGateway(t, costPassthroughProvider(jsonHeader(), strings.NewReader(answer)), billCosts(t, 50, true))

	rec := postJSON(t, srv, passthroughChatPath, passthroughChatBody)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "0.987654321", gjson.Get(rec.Body.String(), "usage.estimated_cost").Raw)
	assert.Equal(t, "0.00003", gjson.Get(rec.Body.String(), "usage.cost").Raw)
}
