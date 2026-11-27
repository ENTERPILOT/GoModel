// Package lago exports billable token usage to Lago (https://getlago.com) as
// usage events, so customers can be invoiced from there. It reads each usage
// entry once, on its way to the usage logger, and never touches storage.
package lago

import (
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/usage"
)

// Billable metric codes, one per token kind. They are the default codes of
// Lago's own LLM SDKs, so one set of billable metrics serves both.
const (
	CodeInput         = "llm_input_tokens"
	CodeOutput        = "llm_output_tokens"
	CodeCachedInput   = "llm_cached_input_tokens"
	CodeCacheCreation = "llm_cache_creation_tokens"
)

// Event is one usage event of Lago's POST /events/batch.
type Event struct {
	// TransactionID is Lago's idempotency key: an event sent again with the
	// same ID is not counted twice.
	TransactionID          string     `json:"transaction_id"`
	ExternalSubscriptionID string     `json:"external_subscription_id"`
	Code                   string     `json:"code"`
	Timestamp              int64      `json:"timestamp"` // Unix seconds
	Properties             Properties `json:"properties"`
}

// Properties is what a billable metric aggregates (Value) and what a charge
// can filter or group by (the rest).
type Properties struct {
	Value        int64  `json:"value"`
	Model        string `json:"model"`
	Provider     string `json:"provider"`
	ProviderName string `json:"provider_name,omitempty"`
	UserPath     string `json:"user_path"`
}

// subscriptions maps a canonical user path to an external_subscription_id.
type subscriptions map[string]string

// resolve returns the subscription of the deepest configured path covering
// userPath, or "" when none does.
func (s subscriptions) resolve(userPath string) string {
	for _, candidate := range core.UserPathAncestors(userPath) {
		if id := s[candidate]; id != "" {
			return id
		}
	}
	return ""
}

// events returns the events one usage entry is billed with: one per token
// kind with a positive count, or none when nothing is owed for the entry.
//
// The four counts never overlap: input is the uncached prompt, and output
// contains reasoning. A response-cache hit repeats the usage of the cached
// answer without reaching a provider, so it sends nothing.
func (s subscriptions) events(entry *usage.UsageEntry) []Event {
	if entry == nil || entry.Repeated || !entry.IsBillable() || entry.CacheType != "" {
		return nil
	}
	subscription := s.resolve(entry.UserPath)
	if subscription == "" {
		return nil
	}

	logged := usage.UsageLogEntry{
		Provider:     entry.Provider,
		InputTokens:  entry.InputTokens,
		OutputTokens: entry.OutputTokens,
		TotalTokens:  entry.TotalTokens,
		RawData:      entry.RawData,
	}
	input, cachedInput, cacheCreation := usage.EntryInputSegments(logged)
	output := int64(entry.OutputTokens)
	if reasoning, included := usage.EntryReasoningTokens(logged); !included {
		output += reasoning
	}

	id := entry.ID
	if id == "" {
		id = uuid.NewString()
	}
	timestamp := entry.Timestamp
	if timestamp.IsZero() {
		timestamp = time.Now()
	}
	properties := Properties{
		Model:        property(entry.Model),
		Provider:     property(entry.Provider),
		ProviderName: property(entry.ProviderName),
		UserPath:     property(entry.UserPath),
	}

	var events []Event
	for _, kind := range []struct {
		name, code string
		tokens     int64
	}{
		{"input", CodeInput, input},
		{"output", CodeOutput, output},
		{"cache_read", CodeCachedInput, cachedInput},
		{"cache_write", CodeCacheCreation, cacheCreation},
	} {
		if kind.tokens <= 0 {
			continue
		}
		properties.Value = kind.tokens
		events = append(events, Event{
			TransactionID:          id + "_tok_" + kind.name,
			ExternalSubscriptionID: subscription,
			Code:                   kind.code,
			Timestamp:              timestamp.Unix(),
			Properties:             properties,
		})
	}
	return events
}

// property cleans a value sent as an event property. Model names can come
// from request paths and upstream responses; invalid UTF-8 would not survive
// JSON encoding unchanged.
func property(value string) string {
	return strings.ToValidUTF8(strings.TrimSpace(value), "�")
}
