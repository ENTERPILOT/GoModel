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
	if snapshot["openai"].Requests != 1 {
		t.Fatalf("Snapshot()[openai].Requests = %d, want 1", snapshot["openai"].Requests)
	}
}

func TestTrackerEvictsStalestModel(t *testing.T) {
	tracker, now := newTestTracker(time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC))
	for i := range maxTrackedModels {
		tracker.Record(llmclient.ResponseInfo{Provider: "openai", Model: fmt.Sprintf("model-%03d", i), StatusCode: 200})
		*now = now.Add(time.Millisecond)
	}
	tracker.Record(llmclient.ResponseInfo{Provider: "openai", Model: "one-too-many", StatusCode: 200})

	models := tracker.providers["openai"].models
	if len(models) != maxTrackedModels {
		t.Fatalf("tracked models = %d, want %d", len(models), maxTrackedModels)
	}
	if _, ok := models["model-000"]; ok {
		t.Fatalf("expected stalest model model-000 to be evicted")
	}
	if _, ok := models["one-too-many"]; !ok {
		t.Fatalf("expected newest model to be tracked")
	}
}

func TestTrackerCapsEventsPerModel(t *testing.T) {
	tracker, _ := newTestTracker(time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC))
	for range maxEventsPerModel + 50 {
		tracker.Record(llmclient.ResponseInfo{Provider: "openai", Model: "gpt-4o", StatusCode: 200})
	}
	if got := len(tracker.providers["openai"].models["gpt-4o"].events); got != maxEventsPerModel {
		t.Fatalf("events kept = %d, want %d", got, maxEventsPerModel)
	}
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
	if len(message) > maxErrorMessageLen+len("…") {
		t.Fatalf("error message length = %d, want <= %d", len(message), maxErrorMessageLen+len("…"))
	}
	if !strings.HasSuffix(message, "…") {
		t.Fatalf("expected truncated message to end with ellipsis")
	}
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
	if len(snapshot.Models) != maxSnapshotModels {
		t.Fatalf("model rows = %d, want %d", len(snapshot.Models), maxSnapshotModels)
	}
	if snapshot.Models[0].Model != "broken" || !snapshot.Models[0].Flagged {
		t.Fatalf("expected flagged model first, got %+v", snapshot.Models[0])
	}
	// Provider totals still cover every tracked model, not just listed rows.
	if snapshot.Requests != maxSnapshotModels+5+4 {
		t.Fatalf("provider requests = %d, want %d", snapshot.Requests, maxSnapshotModels+5+4)
	}
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
	listed := false
	for _, row := range snapshot.Models {
		if row.Model == "quiet-model" {
			listed = true
		}
	}
	if listed {
		t.Fatalf("expected quiet-model to be dropped by the snapshot cap")
	}
	if snapshot.LastError == nil || snapshot.LastError.Message != "newest failure" {
		t.Fatalf("provider LastError = %+v, want newest failure", snapshot.LastError)
	}
	if snapshot.LastErrorModel != "quiet-model" {
		t.Fatalf("LastErrorModel = %q, want quiet-model", snapshot.LastErrorModel)
	}
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
	if !strings.HasSuffix(message, "…") {
		t.Fatalf("expected truncated message, got %q", message)
	}
	if !utf8.ValidString(message) {
		t.Fatalf("truncated message is not valid UTF-8: %q", message)
	}
}

func TestProviderHealthFlaggedModels(t *testing.T) {
	snapshot := ProviderHealth{Models: []ModelHealth{
		{Model: "a", Flagged: true},
		{Model: "b"},
		{Model: "c", Flagged: true},
	}}
	got := snapshot.FlaggedModels()
	if len(got) != 2 || got[0] != "a" || got[1] != "c" {
		t.Fatalf("FlaggedModels() = %v, want [a c]", got)
	}
}

func assertSnapshotsEqual(t *testing.T, got, want map[string]ProviderHealth) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("snapshot providers = %d (%v), want %d", len(got), got, len(want))
	}
	for name, wantProvider := range want {
		gotProvider, ok := got[name]
		if !ok {
			t.Fatalf("missing provider %q in snapshot", name)
		}
		if gotProvider.CircuitState != wantProvider.CircuitState ||
			gotProvider.WindowSeconds != wantProvider.WindowSeconds ||
			gotProvider.Requests != wantProvider.Requests ||
			gotProvider.Errors != wantProvider.Errors {
			t.Fatalf("provider %q = %+v, want %+v", name, gotProvider, wantProvider)
		}
		if len(gotProvider.Models) != len(wantProvider.Models) {
			t.Fatalf("provider %q models = %+v, want %+v", name, gotProvider.Models, wantProvider.Models)
		}
		for i, wantModel := range wantProvider.Models {
			gotModel := gotProvider.Models[i]
			if gotModel.Model != wantModel.Model ||
				gotModel.Requests != wantModel.Requests ||
				gotModel.Errors != wantModel.Errors ||
				gotModel.Flagged != wantModel.Flagged {
				t.Fatalf("provider %q model[%d] = %+v, want %+v", name, i, gotModel, wantModel)
			}
			if (gotModel.LastError == nil) != (wantModel.LastError == nil) {
				t.Fatalf("provider %q model[%d] last_error = %+v, want %+v", name, i, gotModel.LastError, wantModel.LastError)
			}
			if wantModel.LastError != nil && *gotModel.LastError != *wantModel.LastError {
				t.Fatalf("provider %q model[%d] last_error = %+v, want %+v", name, i, *gotModel.LastError, *wantModel.LastError)
			}
		}
	}
}
