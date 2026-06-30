package gateway

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"gomodel/internal/core"
)

const (
	AttemptKindPrimary  = "primary"
	AttemptKindFailover = "failover"
	AttemptKindRetry    = "retry"
)

type attemptRecorderKey struct{}

type attemptObserverKey struct{}

// AttemptObserver is invoked after a failed provider attempt is recorded, so the
// audit/live layer can surface it before the overall request finishes (e.g. a
// failed primary while failover is still in flight).
type AttemptObserver func()

// WithAttemptObserver registers an observer notified after each failed attempt.
// It is independent of (and additive to) the attempt recorder.
func WithAttemptObserver(ctx context.Context, observe AttemptObserver) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if observe == nil {
		return ctx
	}
	return context.WithValue(ctx, attemptObserverKey{}, observe)
}

func attemptObserverFromContext(ctx context.Context) AttemptObserver {
	if ctx == nil {
		return nil
	}
	observe, _ := ctx.Value(attemptObserverKey{}).(AttemptObserver)
	return observe
}

// ProviderAttempt describes one external provider call made while serving a
// logical request. It is intentionally storage-agnostic; server/audit layers
// decide how to persist it.
type ProviderAttempt struct {
	Seq          int
	Kind         string
	ProviderType string
	ProviderName string
	Model        string
	StatusCode   int
	Success      bool
	ErrorType    string
	ErrorCode    string
	ErrorMessage string
	StartedAt    time.Time
	DurationNs   int64
	// ResponseBody and ResponseHeaders hold the raw upstream error response for
	// a failed attempt. They are persisted only when audit body/header logging
	// is enabled; the audit layer parses and redacts them before storage.
	ResponseBody    []byte
	ResponseHeaders http.Header
}

type AttemptRecorder struct {
	mu       sync.Mutex
	attempts []ProviderAttempt
}

// WithAttemptRecorder ensures ctx carries a request-scoped attempt recorder.
func WithAttemptRecorder(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if AttemptRecorderFromContext(ctx) != nil {
		return ctx
	}
	return context.WithValue(ctx, attemptRecorderKey{}, &AttemptRecorder{})
}

func AttemptRecorderFromContext(ctx context.Context) *AttemptRecorder {
	if ctx == nil {
		return nil
	}
	recorder, _ := ctx.Value(attemptRecorderKey{}).(*AttemptRecorder)
	return recorder
}

func AttemptsFromContext(ctx context.Context) []ProviderAttempt {
	recorder := AttemptRecorderFromContext(ctx)
	if recorder == nil {
		return nil
	}
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	if len(recorder.attempts) == 0 {
		return nil
	}
	attempts := make([]ProviderAttempt, len(recorder.attempts))
	copy(attempts, recorder.attempts)
	return attempts
}

func recordProviderAttempt(ctx context.Context, attempt ProviderAttempt) {
	recorder := AttemptRecorderFromContext(ctx)
	if recorder == nil {
		return
	}
	attempt.Kind = normalizeProviderAttemptKind(attempt.Kind)
	if attempt.Kind == "" {
		return
	}
	attempt.ProviderType = strings.TrimSpace(attempt.ProviderType)
	attempt.ProviderName = strings.TrimSpace(attempt.ProviderName)
	attempt.Model = strings.TrimSpace(attempt.Model)
	attempt.ErrorType = strings.TrimSpace(attempt.ErrorType)
	attempt.ErrorCode = strings.TrimSpace(attempt.ErrorCode)
	attempt.ErrorMessage = strings.TrimSpace(attempt.ErrorMessage)

	recorder.mu.Lock()
	if attempt.Seq <= 0 {
		attempt.Seq = len(recorder.attempts) + 1
	}
	recorder.attempts = append(recorder.attempts, attempt)
	recorder.mu.Unlock()

	// Surface failures live as they happen (a failed primary/attempt before
	// failover completes). Successful attempts take the normal end-of-request
	// path, so the hot path stays free of extra live publishes.
	if !attempt.Success {
		if observe := attemptObserverFromContext(ctx); observe != nil {
			observe()
		}
	}
}

func normalizeProviderAttemptKind(kind string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case AttemptKindPrimary:
		return AttemptKindPrimary
	case AttemptKindFailover:
		return AttemptKindFailover
	case AttemptKindRetry:
		return AttemptKindRetry
	default:
		return ""
	}
}

func providerAttemptFromResult(kind, providerType, providerName, model string, started time.Time, err error) ProviderAttempt {
	attempt := ProviderAttempt{
		Kind:         kind,
		ProviderType: providerType,
		ProviderName: providerName,
		Model:        model,
		StartedAt:    started,
		DurationNs:   time.Since(started).Nanoseconds(),
		Success:      err == nil,
	}
	if err == nil {
		attempt.StatusCode = http.StatusOK
		return attempt
	}

	var gatewayErr *core.GatewayError
	if errors.As(err, &gatewayErr) && gatewayErr != nil {
		attempt.StatusCode = gatewayErr.HTTPStatusCode()
		attempt.ErrorType = string(gatewayErr.Type)
		attempt.ErrorMessage = gatewayErr.Message
		if gatewayErr.Code != nil {
			attempt.ErrorCode = *gatewayErr.Code
		}
		attempt.ResponseBody = gatewayErr.ResponseBody
		attempt.ResponseHeaders = gatewayErr.ResponseHeaders
		return attempt
	}

	attempt.StatusCode = http.StatusInternalServerError
	attempt.ErrorType = string(core.ErrorTypeProvider)
	attempt.ErrorMessage = err.Error()
	return attempt
}
