package edenai

import (
	"bytes"
	"math"
	"strings"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
)

const (
	// costField is Eden's per-request charge in USD. Eden reports it at the
	// response root, next to "choices", rather than inside "usage" where
	// OpenRouter and xAI put theirs.
	costField = "cost"
	// upstreamProviderField carries Eden's own "provider" member — the
	// upstream that actually served the request — after it is moved off the
	// typed field. See normalizeUpstreamProvider.
	upstreamProviderField = "edenai_upstream_provider"
)

// normalizeChatResponse reconciles Eden's response extensions with GoModel's
// response semantics. It is applied to every chat completion, which also
// covers the Responses surface because that is translated through chat.
func normalizeChatResponse(resp *core.ChatResponse) {
	if resp == nil {
		return
	}
	liftResponseCost(resp)
	normalizeUpstreamProvider(resp)
}

// liftResponseCost copies Eden's root-level "cost" into Usage.RawUsage, where
// the usage pipeline already looks for a provider-reported exact cost.
//
// internal/usage builds its rawData exclusively from the usage object, so a
// root-level member is invisible to cost accounting. Moving the value one
// level down here — rather than teaching the usage pipeline to read response
// roots — keeps the Eden-specific knowledge inside the Eden provider and lets
// the existing, well-tested usage.cost path do the accounting.
//
// The value is left in ExtraFields as well, so clients still receive Eden's
// cost member verbatim. An existing usage.cost wins: if Eden ever also
// reports it in the conventional place, that reading is the more specific one.
func liftResponseCost(resp *core.ChatResponse) {
	cost, ok := decodeCost(resp.ExtraFields.Lookup(costField))
	if !ok {
		return
	}
	if resp.Usage.RawUsage == nil {
		resp.Usage.RawUsage = make(map[string]any, 1)
	}
	if _, exists := resp.Usage.RawUsage[costField]; exists {
		return
	}
	resp.Usage.RawUsage[costField] = cost
}

// decodeCost parses a raw JSON cost member, rejecting anything that would
// corrupt downstream accounting: absent, null, non-numeric, negative, NaN, or
// infinite.
//
// The null check is load-bearing rather than defensive: unmarshalling a JSON
// null into a float64 is a no-op that reports no error, so without it a
// `"cost": null` would be lifted as a real $0.00 charge and silently
// understate spend.
func decodeCost(raw json.RawMessage) (float64, bool) {
	trimmed := bytes.TrimSpace(raw)
	if core.IsJSONNull(trimmed) {
		return 0, false
	}
	var cost float64
	if err := json.Unmarshal(trimmed, &cost); err != nil {
		return 0, false
	}
	if cost < 0 || math.IsNaN(cost) || math.IsInf(cost, 0) {
		return 0, false
	}
	return cost, true
}

// normalizeUpstreamProvider clears Eden's "provider" member off the typed
// field and re-exposes it under an Eden-namespaced key.
//
// Eden reports the upstream that served the request ("openai", "deepinfra"),
// but core.ChatResponse.Provider means the provider GoModel executed against.
// The gateway treats a populated value as authoritative: it feeds
// gateway.ResponseProviderType, which labels provider-attempt telemetry and
// failover metadata, and it is echoed to the client. Leaving Eden's value
// there would report "openai" as the executing provider for a request that
// actually ran through Eden, mislabeling both. Clearing it lets
// ResponseProviderType fall back to the configured provider type ("edenai"),
// which is the accurate answer, while the namespaced extra field keeps the
// upstream visible to clients that want it.
func normalizeUpstreamProvider(resp *core.ChatResponse) {
	upstream := strings.TrimSpace(resp.Provider)
	resp.Provider = ""
	if upstream == "" {
		return
	}
	encoded, err := json.Marshal(upstream)
	if err != nil {
		return
	}
	merged, err := core.MergeUnknownJSONFields(resp.ExtraFields, map[string]json.RawMessage{
		upstreamProviderField: encoded,
	})
	if err != nil {
		return
	}
	resp.ExtraFields = merged
}

// normalizeEmbeddingResponse applies the same provider-field reasoning to
// embeddings, which Eden also annotates with the upstream provider and which
// feeds the same gateway.ResponseProviderType labeling. core.EmbeddingResponse
// models no unknown-field container, so the upstream value is dropped rather
// than relocated.
func normalizeEmbeddingResponse(resp *core.EmbeddingResponse) {
	if resp == nil {
		return
	}
	resp.Provider = ""
}
