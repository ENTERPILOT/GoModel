package lago

import (
	"testing"
	"time"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/usage"
)

var testSubscriptions = subscriptions{
	"/customers/acme":          "sub_acme",
	"/customers/acme/internal": "sub_acme_internal",
	"/customers/globex":        "sub_globex",
}

// testEntry is a billable request of a mapped customer.
func testEntry(id string) *usage.UsageEntry {
	return &usage.UsageEntry{
		ID:           id,
		RequestID:    "req-" + id,
		Timestamp:    time.Unix(1_800_000_000, 0),
		Model:        "gpt-4o-mini",
		Provider:     "openai",
		ProviderName: "openai-eu",
		UserPath:     "/customers/acme/app",
		InputTokens:  1200,
		OutputTokens: 340,
		TotalTokens:  1540,
	}
}

// tokens maps each event's billable metric code to its value.
func tokens(events []Event) map[string]int64 {
	out := make(map[string]int64, len(events))
	for _, event := range events {
		out[event.Code] = event.Properties.Value
	}
	return out
}

func TestEventsWireShape(t *testing.T) {
	events := testSubscriptions.events(testEntry("e1"))
	require.Len(t, events, 2)

	body, err := json.Marshal(events[0])
	require.NoError(t, err)
	assert.Equal(t,
		`{"transaction_id":"e1_tok_input","external_subscription_id":"sub_acme","code":"llm_input_tokens",`+
			`"timestamp":1800000000,"properties":{"value":1200,"model":"gpt-4o-mini","provider":"openai",`+
			`"provider_name":"openai-eu","user_path":"/customers/acme/app"}}`,
		string(body))

	assert.Equal(t, "e1_tok_output", events[1].TransactionID)
	assert.Equal(t, CodeOutput, events[1].Code)
	assert.Equal(t, int64(340), events[1].Properties.Value)
}

func TestEventsSplitTokenKindsWithoutOverlap(t *testing.T) {
	tests := []struct {
		name  string
		entry func(*usage.UsageEntry)
		want  map[string]int64
	}{
		{
			name:  "plain prompt and completion",
			entry: func(*usage.UsageEntry) {},
			want:  map[string]int64{CodeInput: 1200, CodeOutput: 340},
		},
		{
			name: "openai counts cached tokens inside the prompt",
			entry: func(e *usage.UsageEntry) {
				e.RawData = map[string]any{"prompt_cached_tokens": 1000}
			},
			want: map[string]int64{CodeInput: 200, CodeCachedInput: 1000, CodeOutput: 340},
		},
		{
			name: "anthropic counts cache reads and writes beside the prompt",
			entry: func(e *usage.UsageEntry) {
				e.Provider = "anthropic"
				e.RawData = map[string]any{"cache_read_input_tokens": 4000, "cache_creation_input_tokens": 800}
			},
			want: map[string]int64{
				CodeInput:         1200,
				CodeCachedInput:   4000,
				CodeCacheCreation: 800,
				CodeOutput:        340,
			},
		},
		{
			name: "xai chat reports reasoning on top of the completion",
			entry: func(e *usage.UsageEntry) {
				e.Provider = "xai"
				e.TotalTokens = 1200 + 340 + 60
				e.RawData = map[string]any{"completion_reasoning_tokens": 60}
			},
			want: map[string]int64{CodeInput: 1200, CodeOutput: 400},
		},
		{
			name: "a fully cached prompt sends no input event",
			entry: func(e *usage.UsageEntry) {
				e.RawData = map[string]any{"prompt_cached_tokens": 1200}
			},
			want: map[string]int64{CodeCachedInput: 1200, CodeOutput: 340},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entry := testEntry("e1")
			tt.entry(entry)
			assert.Equal(t, tt.want, tokens(testSubscriptions.events(entry)))
		})
	}
}

func TestEventsSubscriptionMapping(t *testing.T) {
	tests := []struct {
		userPath string
		want     string // "" = not sent
	}{
		{"/customers/acme", "sub_acme"},
		{"/customers/acme/app/worker", "sub_acme"},
		{"/customers/acme/internal/ci", "sub_acme_internal"},
		{"customers/globex/", "sub_globex"},
		{"/customers/acmecorp", ""},
		{"/customers", ""},
		{"/", ""},
		{"", ""},
	}
	for _, tt := range tests {
		t.Run(tt.userPath, func(t *testing.T) {
			entry := testEntry("e1")
			entry.UserPath = tt.userPath
			events := testSubscriptions.events(entry)
			if tt.want == "" {
				assert.Empty(t, events, "a request no subscription covers is not billed to anyone")
				return
			}
			require.NotEmpty(t, events)
			for _, event := range events {
				assert.Equal(t, tt.want, event.ExternalSubscriptionID)
			}
		})
	}
}

func TestEventsSkipWhatIsNotOwed(t *testing.T) {
	tests := []struct {
		name  string
		entry func(*usage.UsageEntry)
	}{
		{"non-billable provider", func(e *usage.UsageEntry) { e.MarkNonBillable() }},
		{"response-cache hit", func(e *usage.UsageEntry) { e.CacheType = "exact" }},
		{"entry written again", func(e *usage.UsageEntry) { e.Repeated = true }},
		{"no tokens", func(e *usage.UsageEntry) { e.InputTokens, e.OutputTokens, e.TotalTokens = 0, 0, 0 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entry := testEntry("e1")
			tt.entry(entry)
			assert.Empty(t, testSubscriptions.events(entry))
		})
	}
	assert.Empty(t, testSubscriptions.events(nil))
}

func TestEventsFillMissingIDAndTimestamp(t *testing.T) {
	entry := testEntry("")
	entry.Timestamp = time.Time{}

	events := testSubscriptions.events(entry)

	require.Len(t, events, 2)
	assert.NotEqual(t, "_tok_input", events[0].TransactionID)
	assert.Positive(t, events[0].Timestamp)
}
