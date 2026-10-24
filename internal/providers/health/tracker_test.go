package health

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestTracker(start time.Time) (*Tracker, *time.Time) {
	now := start
	tracker := NewTracker()
	tracker.now = func() time.Time { return now }
	return tracker, &now
}

func TestTrackerSnapshot(t *testing.T) {
	start := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name   string
		record func(tracker *Tracker, now *time.Time)
		want   map[string]ProviderHealth
	}{
		{
			name:   "no traffic yields empty snapshot",
			record: func(*Tracker, *time.Time) {},
			want:   map[string]ProviderHealth{},
		},
		{
			name: "successes only",
			record: func(tracker *Tracker, _ *time.Time) {
				for range 3 {
					tracker.Record(llmclient.ResponseInfo{Provider: "openai", Model: "gpt-4o", StatusCode: 200, CircuitState: "closed"})
				}
			},
			want: map[string]ProviderHealth{
				"openai": {
					CircuitState:  "closed",
					WindowSeconds: 600,
					Requests:      3,
					Models:        []ModelHealth{{Model: "gpt-4o", Requests: 3}},
				},
			},
		},
		{
			name: "repeated errors flag the model",
			record: func(tracker *Tracker, _ *time.Time) {
				for range 3 {
					tracker.Record(llmclient.ResponseInfo{
						Provider:   "opencode-go",
						Model:      "qwen3.7-max",
						StatusCode: 400,
						Error:      errors.New("Error from provider"),
					})
				}
				tracker.Record(llmclient.ResponseInfo{Provider: "opencode-go", Model: "gpt-5-nano", StatusCode: 200})
			},
			want: map[string]ProviderHealth{
				"opencode-go": {
					WindowSeconds: 600,
					Requests:      4,
					Errors:        3,
					Models: []ModelHealth{
						{Model: "qwen3.7-max", Requests: 3, Errors: 3, Flagged: true, LastError: &ErrorInfo{StatusCode: 400, Message: "Error from provider", At: start}},
						{Model: "gpt-5-nano", Requests: 1},
					},
				},
			},
		},
		{
			name: "two errors stay under the flag threshold",
			record: func(tracker *Tracker, _ *time.Time) {
				for range 2 {
					tracker.Record(llmclient.ResponseInfo{Provider: "openai", Model: "gpt-4o", StatusCode: 500, Error: errors.New("boom")})
				}
			},
			want: map[string]ProviderHealth{
				"openai": {
					WindowSeconds: 600,
					Requests:      2,
					Errors:        2,
					Models: []ModelHealth{
						{Model: "gpt-4o", Requests: 2, Errors: 2, LastError: &ErrorInfo{StatusCode: 500, Message: "boom", At: start}},
					},
				},
			},
		},
		{
			name: "minority errors on busy model are not flagged",
			record: func(tracker *Tracker, _ *time.Time) {
				for range 7 {
					tracker.Record(llmclient.ResponseInfo{Provider: "openai", Model: "gpt-4o", StatusCode: 200})
				}
				for range 3 {
					tracker.Record(llmclient.ResponseInfo{Provider: "openai", Model: "gpt-4o", StatusCode: 429, Error: errors.New("rate limited")})
				}
			},
			want: map[string]ProviderHealth{
				"openai": {
					WindowSeconds: 600,
					Requests:      10,
					Errors:        3,
					Models: []ModelHealth{
						{Model: "gpt-4o", Requests: 10, Errors: 3, LastError: &ErrorInfo{StatusCode: 429, Message: "rate limited", At: start}},
					},
				},
			},
		},
		{
			name: "events outside the window are dropped",
			record: func(tracker *Tracker, now *time.Time) {
				for range 5 {
					tracker.Record(llmclient.ResponseInfo{Provider: "openai", Model: "gpt-4o", StatusCode: 500, Error: errors.New("boom")})
				}
				*now = now.Add(Window + time.Minute)
				tracker.Record(llmclient.ResponseInfo{Provider: "openai", Model: "gpt-4o", StatusCode: 200})
			},
			want: map[string]ProviderHealth{
				"openai": {
					WindowSeconds: 600,
					Requests:      1,
					Models:        []ModelHealth{{Model: "gpt-4o", Requests: 1}},
				},
			},
		},
		{
			name: "model-less requests only update circuit state",
			record: func(tracker *Tracker, _ *time.Time) {
				tracker.Record(llmclient.ResponseInfo{Provider: "openai", StatusCode: 200, CircuitState: "open"})
			},
			want: map[string]ProviderHealth{
				"openai": {CircuitState: "open", WindowSeconds: 600},
			},
		},
		{
			// Caller-side cancellations prove nothing about provider health;
			// like the circuit breaker, the tracker treats them as neutral.
			name: "client cancellations are not counted",
			record: func(tracker *Tracker, _ *time.Time) {
				for range 3 {
					tracker.Record(llmclient.ResponseInfo{
						Provider:     "openai",
						Model:        "gpt-4o",
						CircuitState: "closed",
						Error:        fmt.Errorf("request aborted: %w", context.Canceled),
					})
				}
			},
			want: map[string]ProviderHealth{
				"openai": {CircuitState: "closed", WindowSeconds: 600},
			},
		},
		{
			// llmclient labels body-less requests (discovery GETs, availability
			// probes) as model "unknown"; they must not count as traffic.
			name: "unknown-model probes only update circuit state",
			record: func(tracker *Tracker, _ *time.Time) {
				for range 3 {
					tracker.Record(llmclient.ResponseInfo{
						Provider:     "ollama",
						Model:        "unknown",
						CircuitState: "closed",
						Error:        errors.New("connection refused"),
					})
				}
			},
			want: map[string]ProviderHealth{
				"ollama": {CircuitState: "closed", WindowSeconds: 600},
			},
		},
		{
			name: "circuit state follows the latest request",
			record: func(tracker *Tracker, _ *time.Time) {
				tracker.Record(llmclient.ResponseInfo{Provider: "openai", Model: "gpt-4o", StatusCode: 200, CircuitState: "closed"})
				tracker.Record(llmclient.ResponseInfo{Provider: "openai", Model: "gpt-4o", StatusCode: 503, Error: errors.New("open"), CircuitState: "open"})
			},
			want: map[string]ProviderHealth{
				"openai": {
					CircuitState:  "open",
					WindowSeconds: 600,
					Requests:      2,
					Errors:        1,
					Models: []ModelHealth{
						{Model: "gpt-4o", Requests: 2, Errors: 1, LastError: &ErrorInfo{StatusCode: 503, Message: "open", At: start}},
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tracker, now := newTestTracker(start)
			tt.record(tracker, now)
			got := tracker.Snapshot()
			assertSnapshotsEqual(t, got, tt.want)
		})
	}
}

func TestTrackerHooksFeedRecord(t *testing.T) {
	tracker, _ := newTestTracker(time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC))
	hooks := tracker.Hooks()
	hooks.OnRequestEnd(t.Context(), llmclient.ResponseInfo{Provider: "openai", Model: "gpt-4o", StatusCode: 200})

	snapshot := tracker.Snapshot()
	require.Equal(t, 1, snapshot["openai"].Requests)
}

func TestTrackerEmptyResponsesFlagModel(t *testing.T) {
	tests := []struct {
		name         string
		successes    int
		empties      int
		wantRequests int
		wantErrors   int
		wantFlagged  bool
	}{
		{name: "empty responses replace their recorded successes", successes: 4, empties: 3, wantRequests: 4, wantErrors: 3, wantFlagged: true},
		{name: "a single empty response does not flag", successes: 4, empties: 1, wantRequests: 4, wantErrors: 1},
		{name: "empty response without a recorded request adds a failure", empties: 1, wantRequests: 1, wantErrors: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tracker, _ := newTestTracker(time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC))
			hooks := tracker.Hooks()
			for range tt.successes {
				hooks.OnRequestEnd(t.Context(), llmclient.ResponseInfo{Provider: "openai", Model: "gpt-4o", StatusCode: 200})
			}
			for range tt.empties {
				hooks.OnEmptyResponse(t.Context(), llmclient.EmptyResponseInfo{Provider: "openai", Model: "gpt-4o", Reason: llmclient.EmptyReasonNoChoices})
			}

			snapshot := tracker.Snapshot()["openai"]
			require.Len(t, snapshot.Models, 1)

			row := snapshot.Models[0]
			assert.Equal(t, tt.wantRequests, row.Requests)
			assert.Equal(t, tt.wantErrors, row.Errors)
			assert.Equal(t, tt.wantFlagged, row.Flagged)
			require.NotNil(t, row.LastError)
			assert.Equal(t, 200, row.LastError.StatusCode)
			assert.Contains(t, row.LastError.Message, "no_choices")
		})
	}
}

func TestTrackerEmptyResponseInterleavedWithOtherRequests(t *testing.T) {
	tracker, now := newTestTracker(time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC))
	success := llmclient.ResponseInfo{Provider: "openai", Model: "gpt-4o", StatusCode: 200}
	empty := llmclient.EmptyResponseInfo{Provider: "openai", Model: "gpt-4o", Reason: llmclient.EmptyReasonNoChoices}

	// A and B both end before A is classified empty; C fails outright
	// before B is classified empty.
	tracker.Record(success) // A
	*now = now.Add(time.Millisecond)
	tracker.Record(success) // B
	tracker.RecordEmptyResponse(empty)
	tracker.Record(llmclient.ResponseInfo{Provider: "openai", Model: "gpt-4o", StatusCode: 500}) // C
	tracker.RecordEmptyResponse(empty)
	tracker.Record(success) // D

	row := tracker.Snapshot()["openai"].Models[0]
	assert.Equal(t, 4, row.Requests)
	assert.Equal(t, 3, row.Errors)
	assert.True(t, row.Flagged)
}

func TestTrackerEvictsStalestModel(t *testing.T) {
	tracker, now := newTestTracker(time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC))
	for i := range maxTrackedModels {
		tracker.Record(llmclient.ResponseInfo{Provider: "openai", Model: fmt.Sprintf("model-%03d", i), StatusCode: 200})
		*now = now.Add(time.Millisecond)
	}
	tracker.Record(llmclient.ResponseInfo{Provider: "openai", Model: "one-too-many", StatusCode: 200})

	models := tracker.providers["openai"].models
	require.Len(t, models, maxTrackedModels)
	assert.NotContains(t, models, "model-000")
	assert.Contains(t, models, "one-too-many")
}

func TestTrackerCapsEventsPerModel(t *testing.T) {
	tracker, _ := newTestTracker(time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC))
	for range maxEventsPerModel + 50 {
		tracker.Record(llmclient.ResponseInfo{Provider: "openai", Model: "gpt-4o", StatusCode: 200})
	}
	require.Len(t, tracker.providers["openai"].models["gpt-4o"].events, maxEventsPerModel)
}

func TestTrackerTruncatesLongErrorMessages(t *testing.T) {
	tracker, _ := newTestTracker(time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC))
	tracker.Record(llmclient.ResponseInfo{
		Provider:   "openai",
		Model:      "gpt-4o",
		StatusCode: 500,
		Error:      errors.New(strings.Repeat("x", maxErrorMessageLen+100)),
	})
	message := tracker.Snapshot()["openai"].Models[0].LastError.Message
	assert.LessOrEqual(t, len(message), maxErrorMessageLen+len("…"))
	assert.True(t, strings.HasSuffix(message, "…"), "message = %q, want truncation marker", message)
}

func TestTrackerSnapshotCapsModelRowsTroubledFirst(t *testing.T) {
	tracker, _ := newTestTracker(time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC))
	for i := range maxSnapshotModels + 5 {
		tracker.Record(llmclient.ResponseInfo{Provider: "openai", Model: fmt.Sprintf("healthy-%02d", i), StatusCode: 200})
	}
	for range 4 {
		tracker.Record(llmclient.ResponseInfo{Provider: "openai", Model: "broken", StatusCode: 400, Error: errors.New("bad")})
	}

	snapshot := tracker.Snapshot()["openai"]
	require.Len(t, snapshot.Models, maxSnapshotModels)
	assert.Equal(t, "broken", snapshot.Models[0].Model)
	assert.True(t, snapshot.Models[0].Flagged, "expected flagged model first")

	// Provider totals still cover every tracked model, not just listed rows.
	assert.Equal(t, maxSnapshotModels+5+4, snapshot.Requests)
}

func TestTrackerProviderLastErrorSurvivesModelCap(t *testing.T) {
	start := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	tracker, now := newTestTracker(start)
	// Many models with more errors dominate the capped, errors-first listing…
	for i := range maxSnapshotModels + 5 {
		for range 3 {
			tracker.Record(llmclient.ResponseInfo{
				Provider:   "router",
				Model:      fmt.Sprintf("busy-%02d", i),
				StatusCode: 500,
				Error:      errors.New("old failure"),
			})
		}
	}
	// …while the most recent failure happens on a low-error model that gets
	// dropped from the model list.
	*now = now.Add(time.Minute)
	tracker.Record(llmclient.ResponseInfo{
		Provider:   "router",
		Model:      "quiet-model",
		StatusCode: 400,
		Error:      errors.New("newest failure"),
	})

	snapshot := tracker.Snapshot()["router"]
	for _, row := range snapshot.Models {
		assert.NotEqual(t, "quiet-model", row.Model, "quiet-model should be dropped from the capped listing")
	}
	require.NotNil(t, snapshot.LastError)
	assert.Equal(t, "newest failure", snapshot.LastError.Message)
	assert.Equal(t, "quiet-model", snapshot.LastErrorModel)
}

func TestErrorMessageTruncationIsRuneSafe(t *testing.T) {
	tracker, _ := newTestTracker(time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC))
	// Multi-byte runes positioned so a byte-count cut would split one.
	tracker.Record(llmclient.ResponseInfo{
		Provider:   "openai",
		Model:      "gpt-4o",
		StatusCode: 500,
		Error:      errors.New(strings.Repeat("é", maxErrorMessageLen)),
	})
	message := tracker.Snapshot()["openai"].Models[0].LastError.Message
	assert.True(t, strings.HasSuffix(message, "…"), "expected truncated message, got %q", message)
	assert.True(t, utf8.ValidString(message), "truncated message is not valid UTF-8: %q", message)
}

func TestProviderHealthFlaggedModels(t *testing.T) {
	snapshot := ProviderHealth{Models: []ModelHealth{
		{Model: "a", Flagged: true},
		{Model: "b"},
		{Model: "c", Flagged: true},
	}}
	assert.Equal(t, []string{"a", "c"}, snapshot.FlaggedModels())
}

func assertSnapshotsEqual(t *testing.T, got, want map[string]ProviderHealth) {
	t.Helper()
	require.Len(t, got, len(want), "snapshot providers = %v", got)

	for name, wantProvider := range want {
		gotProvider, ok := got[name]
		require.True(t, ok, "missing provider %q in snapshot", name)
		assert.Equal(t, wantProvider.CircuitState, gotProvider.CircuitState, "provider %q", name)
		assert.Equal(t, wantProvider.WindowSeconds, gotProvider.WindowSeconds, "provider %q", name)
		assert.Equal(t, wantProvider.Requests, gotProvider.Requests, "provider %q", name)
		assert.Equal(t, wantProvider.Errors, gotProvider.Errors, "provider %q", name)
		require.Len(t, gotProvider.Models, len(wantProvider.Models), "provider %q models = %+v", name, gotProvider.Models)

		for i, wantModel := range wantProvider.Models {
			gotModel := gotProvider.Models[i]
			assert.Equal(t, wantModel.Model, gotModel.Model, "provider %q model[%d]", name, i)
			assert.Equal(t, wantModel.Requests, gotModel.Requests, "provider %q model[%d]", name, i)
			assert.Equal(t, wantModel.Errors, gotModel.Errors, "provider %q model[%d]", name, i)
			assert.Equal(t, wantModel.Flagged, gotModel.Flagged, "provider %q model[%d]", name, i)
			if wantModel.LastError == nil {
				assert.Nil(t, gotModel.LastError, "provider %q model[%d] last_error", name, i)
				continue
			}
			require.NotNil(t, gotModel.LastError, "provider %q model[%d] last_error", name, i)
			assert.Equal(t, *wantModel.LastError, *gotModel.LastError, "provider %q model[%d] last_error", name, i)
		}
	}
}
