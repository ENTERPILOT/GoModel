// Package health tracks recent request outcomes per provider and model.
//
// A Tracker is fed through llmclient observability hooks and keeps a sliding
// window of per-model request/error counts plus each provider's last observed
// circuit-breaker state. The admin provider-status endpoint folds its
// snapshots into the dashboard so real-traffic failures are visible even when
// model discovery still succeeds.
//
// Known limitation: hooks fire when a streaming response is established, so a
// stream that starts with HTTP 200 and fails mid-body is recorded as a
// success (the same blind spot applies to the Prometheus hooks).
package health

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/enterpilot/gomodel/internal/llmclient"
)

const (
	// Window is how far back request outcomes count toward model health.
	Window = 10 * time.Minute

	// maxTrackedModels bounds per-provider memory; the least recently active
	// model is evicted first.
	maxTrackedModels = 100
	// maxEventsPerModel bounds per-model memory under sustained traffic.
	maxEventsPerModel = 500
	// maxErrorMessageLen keeps snapshot payloads compact.
	maxErrorMessageLen = 240
	// maxSnapshotModels bounds the per-provider model list in a snapshot.
	maxSnapshotModels = 25

	// flagMinErrors is the minimum number of windowed errors before a model
	// can be flagged; together with the ≥50% error-rate rule it avoids
	// flagging a model on a single malformed user request.
	flagMinErrors = 3
)

// ErrorInfo describes the most recent failed request for a model.
type ErrorInfo struct {
	StatusCode int       `json:"status_code,omitempty"`
	Message    string    `json:"message,omitempty"`
	At         time.Time `json:"at"`
}

// ModelHealth summarizes windowed traffic for one model.
type ModelHealth struct {
	Model     string     `json:"model"`
	Requests  int        `json:"requests"`
	Errors    int        `json:"errors"`
	Flagged   bool       `json:"flagged"`
	LastError *ErrorInfo `json:"last_error,omitempty"`
}

// ProviderHealth summarizes windowed traffic for one provider.
type ProviderHealth struct {
	// CircuitState is the circuit-breaker state as of the provider's most
	// recent request ("closed", "half-open", "open"); empty when the breaker
	// is disabled or no request has run yet.
	CircuitState  string        `json:"circuit_state,omitempty"`
	WindowSeconds int           `json:"window_seconds"`
	Requests      int           `json:"requests"`
	Errors        int           `json:"errors"`
	Models        []ModelHealth `json:"models,omitempty"`
	// LastError is the most recent windowed failure across every tracked
	// model, computed before Models is capped, with LastErrorModel naming the
	// model it came from.
	LastError      *ErrorInfo `json:"last_error,omitempty"`
	LastErrorModel string     `json:"last_error_model,omitempty"`
}

type event struct {
	at     time.Time
	failed bool
}

type modelState struct {
	events       []event
	lastError    *ErrorInfo
	lastActivity time.Time
}

type providerState struct {
	circuitState string
	models       map[string]*modelState
}

// Tracker records request outcomes and serves health snapshots. The zero
// value is not usable; construct with NewTracker.
type Tracker struct {
	mu        sync.Mutex
	now       func() time.Time
	providers map[string]*providerState
}

// NewTracker returns an empty tracker.
func NewTracker() *Tracker {
	return &Tracker{
		now:       time.Now,
		providers: make(map[string]*providerState),
	}
}

// Hooks returns llmclient hooks that feed this tracker. Compose them with
// other hooks via llmclient.JoinHooks.
func (t *Tracker) Hooks() llmclient.Hooks {
	return llmclient.Hooks{
		OnRequestEnd: func(_ context.Context, info llmclient.ResponseInfo) {
			t.Record(info)
		},
	}
}

// Record ingests one completed request. Requests without a model (e.g. model
// discovery) only update the provider's circuit state.
func (t *Tracker) Record(info llmclient.ResponseInfo) {
	if info.Provider == "" {
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	provider := t.providers[info.Provider]
	if provider == nil {
		provider = &providerState{models: make(map[string]*modelState)}
		t.providers[info.Provider] = provider
	}
	if info.CircuitState != "" {
		provider.circuitState = info.CircuitState
	}
	// Body-less requests (model discovery GETs, availability probes,
	// multipart uploads) are not model-attributed client traffic, so they
	// only update circuit state.
	if info.Model == "" || info.Model == llmclient.UnknownModel {
		return
	}
	// A caller-side cancellation proves nothing about provider health; the
	// circuit breaker treats it as neutral and so does this tracker. Client
	// deadlines (context.DeadlineExceeded) still count as failures.
	if errors.Is(info.Error, context.Canceled) {
		return
	}

	now := t.now()
	model := provider.models[info.Model]
	if model == nil {
		if len(provider.models) >= maxTrackedModels {
			evictStalestModel(provider.models)
		}
		model = &modelState{}
		provider.models[info.Model] = model
	}

	failed := info.Error != nil || info.StatusCode >= 400
	model.events = append(model.events, event{at: now, failed: failed})
	model.lastActivity = now
	if failed {
		model.lastError = &ErrorInfo{
			StatusCode: info.StatusCode,
			Message:    errorMessage(info),
			At:         now,
		}
	}
	model.prune(now)
}

// Snapshot returns windowed health per provider name. Providers without any
// recorded requests are omitted.
func (t *Tracker) Snapshot() map[string]ProviderHealth {
	t.mu.Lock()
	defer t.mu.Unlock()

	now := t.now()
	result := make(map[string]ProviderHealth, len(t.providers))
	for name, provider := range t.providers {
		snapshot := ProviderHealth{
			CircuitState:  provider.circuitState,
			WindowSeconds: int(Window / time.Second),
		}
		for modelName, model := range provider.models {
			model.prune(now)
			requests, errors := model.counts()
			if requests == 0 {
				continue
			}
			row := ModelHealth{
				Model:    modelName,
				Requests: requests,
				Errors:   errors,
				Flagged:  errors >= flagMinErrors && errors*2 >= requests,
			}
			if model.lastError != nil && now.Sub(model.lastError.At) <= Window {
				lastError := *model.lastError
				row.LastError = &lastError
				if snapshot.LastError == nil || lastError.At.After(snapshot.LastError.At) {
					snapshot.LastError = &lastError
					snapshot.LastErrorModel = modelName
				}
			}
			snapshot.Requests += requests
			snapshot.Errors += errors
			snapshot.Models = append(snapshot.Models, row)
		}
		sortModelHealth(snapshot.Models)
		if len(snapshot.Models) > maxSnapshotModels {
			snapshot.Models = snapshot.Models[:maxSnapshotModels]
		}
		if snapshot.Requests == 0 && snapshot.CircuitState == "" {
			continue
		}
		result[name] = snapshot
	}
	return result
}

// FlaggedModels returns the flagged model names from a snapshot, sorted as
// the snapshot lists them.
func (p ProviderHealth) FlaggedModels() []string {
	var flagged []string
	for _, model := range p.Models {
		if model.Flagged {
			flagged = append(flagged, model.Model)
		}
	}
	return flagged
}

func (m *modelState) prune(now time.Time) {
	cutoff := now.Add(-Window)
	drop := 0
	for drop < len(m.events) && m.events[drop].at.Before(cutoff) {
		drop++
	}
	if excess := len(m.events) - drop - maxEventsPerModel; excess > 0 {
		drop += excess
	}
	if drop > 0 {
		m.events = append(m.events[:0], m.events[drop:]...)
	}
}

func (m *modelState) counts() (requests, errors int) {
	for _, e := range m.events {
		requests++
		if e.failed {
			errors++
		}
	}
	return requests, errors
}

func evictStalestModel(models map[string]*modelState) {
	var stalest string
	var stalestAt time.Time
	for name, model := range models {
		if stalest == "" || model.lastActivity.Before(stalestAt) {
			stalest = name
			stalestAt = model.lastActivity
		}
	}
	delete(models, stalest)
}

func errorMessage(info llmclient.ResponseInfo) string {
	message := ""
	if info.Error != nil {
		message = info.Error.Error()
	} else {
		message = fmt.Sprintf("provider returned HTTP %d", info.StatusCode)
	}
	if len(message) > maxErrorMessageLen {
		cut := maxErrorMessageLen
		for cut > 0 && !utf8.RuneStart(message[cut]) {
			cut--
		}
		message = message[:cut] + "…"
	}
	return message
}

// sortModelHealth orders rows flagged-first, then by errors, requests, and
// name so the most troubled models surface before the snapshot cap applies.
func sortModelHealth(models []ModelHealth) {
	sort.Slice(models, func(i, j int) bool {
		a, b := models[i], models[j]
		if a.Flagged != b.Flagged {
			return a.Flagged
		}
		if a.Errors != b.Errors {
			return a.Errors > b.Errors
		}
		if a.Requests != b.Requests {
			return a.Requests > b.Requests
		}
		return a.Model < b.Model
	})
}
