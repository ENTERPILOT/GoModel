package admin

import (
	"testing"
	"time"

	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/health"
)

func TestApplyRequestHealth(t *testing.T) {
	errorAt := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	flaggedModel := health.ProviderHealth{
		Requests: 5,
		Errors:   4,
		Models: []health.ModelHealth{
			{
				Model:    "qwen3.7-max",
				Requests: 4,
				Errors:   4,
				Flagged:  true,
				LastError: &health.ErrorInfo{
					StatusCode: 400,
					Message:    "Error from provider",
					At:         errorAt,
				},
			},
			{Model: "gpt-5-nano", Requests: 1},
		},
		LastError: &health.ErrorInfo{
			StatusCode: 400,
			Message:    "Error from provider",
			At:         errorAt,
		},
		LastErrorModel: "qwen3.7-max",
	}
	flaggedModelHalfOpen := flaggedModel
	flaggedModelHalfOpen.CircuitState = "half-open"

	cases := []struct {
		name          string
		status        string
		label         string
		lastError     string
		rh            *health.ProviderHealth
		wantStatus    string
		wantLabel     string
		wantReason    string
		wantLastError string
	}{
		{
			name:       "nil health keeps base classification",
			status:     "healthy",
			label:      "Healthy",
			rh:         nil,
			wantStatus: "healthy",
			wantLabel:  "Healthy",
			wantReason: "base reason",
		},
		{
			name:          "open breaker marks provider unhealthy",
			status:        "healthy",
			label:         "Healthy",
			rh:            &health.ProviderHealth{CircuitState: "open"},
			wantStatus:    "unhealthy",
			wantLabel:     "Circuit Open",
			wantReason:    "circuit breaker is open; recent requests to this provider failed and traffic is paused",
			wantLastError: "",
		},
		{
			name:       "open breaker does not override unhealthy base",
			status:     "unhealthy",
			label:      "Unhealthy",
			rh:         &health.ProviderHealth{CircuitState: "open"},
			wantStatus: "unhealthy",
			wantLabel:  "Unhealthy",
			wantReason: "base reason",
		},
		{
			name:       "half-open breaker degrades healthy provider",
			status:     "healthy",
			label:      "Healthy",
			rh:         &health.ProviderHealth{CircuitState: "half-open"},
			wantStatus: "degraded",
			wantLabel:  "Recovering",
			wantReason: "circuit breaker is half-open; probing whether the provider has recovered",
		},
		{
			name:       "half-open breaker keeps degraded base label",
			status:     "degraded",
			label:      "Starting",
			rh:         &health.ProviderHealth{CircuitState: "half-open"},
			wantStatus: "degraded",
			wantLabel:  "Starting",
			wantReason: "base reason",
		},
		{
			name:          "flagged model degrades healthy provider and surfaces its error",
			status:        "healthy",
			label:         "Healthy",
			rh:            &flaggedModel,
			wantStatus:    "degraded",
			wantLabel:     "Degraded",
			wantReason:    "recent requests are failing for: qwen3.7-max",
			wantLastError: "qwen3.7-max: Error from provider",
		},
		{
			name:          "breaker state takes precedence over flagged models",
			status:        "healthy",
			label:         "Healthy",
			rh:            &flaggedModelHalfOpen,
			wantStatus:    "degraded",
			wantLabel:     "Recovering",
			wantReason:    "circuit breaker is half-open; probing whether the provider has recovered",
			wantLastError: "qwen3.7-max: Error from provider",
		},
		{
			name:          "existing last error is not overwritten",
			status:        "degraded",
			label:         "Degraded",
			lastError:     "discovery failed",
			rh:            &flaggedModel,
			wantStatus:    "degraded",
			wantLabel:     "Degraded",
			wantReason:    "base reason",
			wantLastError: "discovery failed",
		},
		{
			name:       "closed breaker with clean traffic keeps healthy",
			status:     "healthy",
			label:      "Healthy",
			rh:         &health.ProviderHealth{CircuitState: "closed", Requests: 12},
			wantStatus: "healthy",
			wantLabel:  "Healthy",
			wantReason: "base reason",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, label, reason, lastError := applyRequestHealth(tc.status, tc.label, "base reason", tc.lastError, tc.rh)
			if status != tc.wantStatus {
				t.Errorf("status = %q, want %q", status, tc.wantStatus)
			}
			if label != tc.wantLabel {
				t.Errorf("label = %q, want %q", label, tc.wantLabel)
			}
			if reason != tc.wantReason {
				t.Errorf("reason = %q, want %q", reason, tc.wantReason)
			}
			if lastError != tc.wantLastError {
				t.Errorf("lastError = %q, want %q", lastError, tc.wantLastError)
			}
		})
	}
}

func TestRequestHealthForTrimsSnapshotKeys(t *testing.T) {
	healthByName := map[string]health.ProviderHealth{
		" openai ": {Requests: 2},
	}
	if got := requestHealthFor(healthByName, "openai"); got == nil || got.Requests != 2 {
		t.Fatalf("requestHealthFor() = %+v, want snapshot with 2 requests", got)
	}
	if got := requestHealthFor(healthByName, "missing"); got != nil {
		t.Fatalf("requestHealthFor(missing) = %+v, want nil", got)
	}
}

type staticRequestHealth map[string]health.ProviderHealth

func (s staticRequestHealth) Snapshot() map[string]health.ProviderHealth {
	return s
}

func TestBuildProviderStatusResponseIncludesRequestHealth(t *testing.T) {
	handler := &Handler{
		configuredProviders: []providers.SanitizedProviderConfig{{Name: "opencode-go", Type: "opencode_go"}},
		requestHealth: staticRequestHealth{
			"opencode-go": {
				Requests: 3,
				Errors:   3,
				Models: []health.ModelHealth{
					{Model: "qwen3.7-max", Requests: 3, Errors: 3, Flagged: true},
				},
			},
		},
	}

	resp := handler.buildProviderStatusResponse()
	if len(resp.Providers) != 1 {
		t.Fatalf("providers = %d, want 1", len(resp.Providers))
	}
	item := resp.Providers[0]
	if item.RequestHealth == nil {
		t.Fatalf("RequestHealth = nil, want snapshot attached")
	}
	if item.RequestHealth.Errors != 3 {
		t.Fatalf("RequestHealth.Errors = %d, want 3", item.RequestHealth.Errors)
	}
	// Provider has no discovered models (base "Configured"/degraded), so the
	// flagged model must not upgrade or further change the base status, but
	// its error visibility arrives via request_health.
	if item.Status != "degraded" {
		t.Fatalf("Status = %q, want degraded", item.Status)
	}
}
