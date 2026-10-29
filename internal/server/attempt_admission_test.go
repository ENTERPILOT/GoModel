package server

import (
	"context"
	"github.com/enterpilot/gomodel/internal/budget"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/echotest"
	"github.com/enterpilot/gomodel/internal/ratelimit"
	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/require"
)

func TestAttemptAdmissionPreservesConsumerAcrossRoutes(t *testing.T) {
	service := newTestRateLimitService(t,
		rateLimitRuleWithRequests("/", 5),
		rateLimitProviderRule("first", 1),
		rateLimitProviderRule("backup", 100),
	)
	ctx := context.Background()
	adm, err := admit(ctx, service, nil, rateLimitRoute{provider: "first", model: "first/model"}.withFailovers(1), nil)
	require.NoError(t, err)
	defer adm.release()
	firstRelease, err := adm.attempts.Acquire(ctx, "first", "first/model")
	require.NoError(t, err)
	firstRelease()
	adm.attempts.consumerAt = time.Now().Add(-2 * time.Minute)
	backupRelease, err := adm.attempts.Acquire(ctx, "backup", "backup/model")
	require.NoError(t, err)
	defer backupRelease()
	require.Equal(t, int64(5), adm.attempts.headers.RequestLimit)
	require.Equal(t, int64(4), adm.attempts.headers.RequestRemaining)
	require.Zero(t, adm.attempts.headers.RequestResetAfter, "a fallback must not restart the original consumer reset countdown")
	for _, status := range service.Statuses(time.Now().UTC()) {
		if status.Rule.Scope == ratelimit.ScopeUserPath {
			require.Equal(t, int64(1), status.RequestsUsed)
		}
	}
}

func TestBudgetRejectionDoesNotConsumeRequestQuota(t *testing.T) {
	for _, scope := range []budget.Scope{budget.ScopeUserPath, budget.ScopeProvider} {
		t.Run(string(scope), func(t *testing.T) {
			service := newTestRateLimitService(t, rateLimitRuleWithRequests("/", 5), rateLimitProviderRule("first", 5))
			adm, err := admit(context.Background(), service, exceededBudgetChecker{scope: scope}, rateLimitRoute{provider: "first", model: "first/model"}, nil)
			require.Error(t, err)
			adm.release()
			statuses := service.Statuses(time.Now().UTC())
			require.Len(t, statuses, 2)
			for _, status := range statuses {
				require.Zero(t, status.RequestsUsed)
			}
		})
	}
}

func TestCancellationAfterAdmissionRetainsRequestChargeAndReleasesConcurrency(t *testing.T) {
	limit := int64(1)
	service := newTestRateLimitService(t, rateLimitRuleWithRequests("/", 5),
		ratelimit.Rule{Scope: ratelimit.ScopeUserPath, Subject: "/", PeriodSeconds: ratelimit.PeriodConcurrent, MaxRequests: &limit})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	adm, err := admit(ctx, service, nil, rateLimitRoute{provider: "first", model: "first/model"}, nil)
	require.NoError(t, err)
	cancel()
	_, err = adm.attempts.Acquire(ctx, "first", "first/model")
	require.ErrorIs(t, err, context.Canceled)
	adm.release()
	for _, status := range service.Statuses(time.Now().UTC()) {
		if status.Rule.PeriodSeconds != ratelimit.PeriodConcurrent {
			require.Equal(t, int64(1), status.RequestsUsed)
		}
	}
	reservation, err := service.Acquire(ratelimit.Subjects{UserPath: "/"}, time.Now().UTC())
	require.NoError(t, err, "cancelled request must release its consumer concurrency")
	reservation.Release()
}

func TestAttemptAdmissionReleasesSameProviderSlotBeforeFallback(t *testing.T) {
	limit := int64(1)
	service := newTestRateLimitService(t,
		ratelimit.Rule{Scope: ratelimit.ScopeProvider, Subject: "same", PeriodSeconds: ratelimit.PeriodConcurrent, MaxRequests: &limit},
	)
	ctx := context.Background()
	adm, err := admit(ctx, service, nil, rateLimitRoute{provider: "same", model: "same/a"}.withFailovers(1), nil)
	require.NoError(t, err)
	defer adm.release()
	firstRelease, err := adm.attempts.Acquire(ctx, "same", "same/a")
	require.NoError(t, err)
	_, err = service.Acquire(ratelimit.Subjects{Provider: "same"}, time.Now().UTC())
	require.Error(t, err)
	firstRelease()
	backupRelease, err := adm.attempts.Acquire(ctx, "same", "same/b")
	require.NoError(t, err)
	backupRelease()
	backupRelease()
}

type namedAttemptProvider struct{ *failoverProvider }

type attemptStreamProvider struct {
	namedAttemptProvider
	stream func(context.Context) io.ReadCloser
}

func (p attemptStreamProvider) StreamChatCompletion(ctx context.Context, _ *core.ChatRequest) (io.ReadCloser, error) {
	return p.stream(ctx), nil
}

type admissionHookReader struct {
	io.ReadCloser
	once sync.Once
	hook func() error
	err  error
}

func (r *admissionHookReader) Read(p []byte) (int, error) {
	r.once.Do(func() { r.err = r.hook() })
	if r.err != nil {
		return 0, r.err
	}
	return r.ReadCloser.Read(p)
}

func TestStreamingInternalJudgeDoesNotInheritOuterAdmission(t *testing.T) {
	base := namedAttemptProvider{&failoverProvider{
		supportedModels: map[string]string{"first/model": "openai"},
		chatResponses:   map[string]*core.ChatResponse{"first/model": {Model: "model", Provider: "openai", Choices: []core.Choice{{Message: core.ResponseMessage{Role: "assistant", Content: "allow"}, FinishReason: "stop"}}}},
	}}
	judge := NewInternalChatCompletionExecutor(base, InternalChatCompletionExecutorConfig{})
	judged := make(chan error, 1)
	provider := attemptStreamProvider{namedAttemptProvider: base, stream: func(ctx context.Context) io.ReadCloser {
		return &admissionHookReader{ReadCloser: io.NopCloser(strings.NewReader("data: [DONE]\n\n")), hook: func() error {
			_, err := judge.ChatCompletion(ctx, &core.ChatRequest{Model: "model", Provider: "first", Messages: []core.Message{{Role: "user", Content: "judge"}}})
			judged <- err
			return err
		}}
	}}
	handler := newHandler(provider, nil, nil, nil, nil, nil, nil, nil)
	handler.rateLimiter = newTestRateLimitService(t, rateLimitProviderRule("first", 1), rateLimitRuleWithRequests("/", 5))
	c, rec := echotest.Post(t, "/v1/chat/completions", `{"model":"first/model","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	require.NoError(t, handler.ChatCompletion(c))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NoError(t, <-judged)
	require.Equal(t, []string{"first/model"}, base.chatCalls)
	for _, status := range handler.rateLimiter.(*ratelimit.Service).Statuses(time.Now().UTC()) {
		require.Equal(t, int64(1), status.RequestsUsed, "the internal judge must not spend outer quotas")
	}
}

func TestStreamingHandlerRetainsConsumerConcurrency(t *testing.T) {
	started, finish := make(chan struct{}), make(chan struct{})
	var startOnce, finishOnce sync.Once
	unblock := func() { finishOnce.Do(func() { close(finish) }) }
	t.Cleanup(unblock)
	base := namedAttemptProvider{&failoverProvider{supportedModels: map[string]string{"first/model": "openai"}}}
	provider := attemptStreamProvider{namedAttemptProvider: base, stream: func(context.Context) io.ReadCloser {
		return &admissionHookReader{ReadCloser: io.NopCloser(strings.NewReader("data: [DONE]\n\n")), hook: func() error {
			startOnce.Do(func() { close(started) })
			<-finish
			return nil
		}}
	}}
	handler := newHandler(provider, nil, nil, nil, nil, nil, nil, nil)
	limit := int64(1)
	handler.rateLimiter = newTestRateLimitService(t, ratelimit.Rule{Scope: ratelimit.ScopeUserPath, Subject: "/team", PeriodSeconds: ratelimit.PeriodConcurrent, MaxRequests: &limit})
	request := func() (*echo.Context, *httptest.ResponseRecorder) {
		c, rec := echotest.Post(t, "/v1/chat/completions", `{"model":"first/model","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
		c.SetRequest(c.Request().WithContext(core.WithEffectiveUserPath(c.Request().Context(), "/team")))
		return c, rec
	}
	first, firstRec := request()
	done := make(chan error, 1)
	go func() { done <- handler.ChatCompletion(first) }()
	<-started
	second, secondRec := request()
	require.NoError(t, handler.ChatCompletion(second))
	require.Equal(t, http.StatusTooManyRequests, secondRec.Code)
	unblock()
	require.NoError(t, <-done)
	require.Equal(t, http.StatusOK, firstRec.Code)
	third, thirdRec := request()
	require.NoError(t, handler.ChatCompletion(third))
	require.Equal(t, http.StatusOK, thirdRec.Code)
}

func TestAttemptHeadersReplacePrimaryAndAgeFinalSnapshot(t *testing.T) {
	service := newTestRateLimitService(t, rateLimitProviderRule("first", 5))
	c, recorder := echotest.Post(t, "/v1/chat/completions", `{}`)
	adm, err := enforceAdmission(c, service, nil, rateLimitRoute{provider: "first", model: "first/a"})
	require.NoError(t, err)
	defer adm.release()
	require.NotEmpty(t, recorder.Header().Get(rateLimitLimitRequestsHeader))
	release, err := adm.attempts.Acquire(c.Request().Context(), "first", "first/a")
	require.NoError(t, err)
	release()
	release, err = adm.attempts.Acquire(c.Request().Context(), "backup", "backup/a")
	require.NoError(t, err)
	defer release()
	adm.applyHeaders(c)
	require.Empty(t, recorder.Header().Get(rateLimitLimitRequestsHeader))
	require.Empty(t, recorder.Header().Get(rateLimitResetRequestsHeader))
	adm.attempts.headers = ratelimit.HeaderSnapshot{HasRequests: true, RequestLimit: 5, RequestResetAfter: 30 * time.Second, HasTokens: true, TokenLimit: 100, TokenResetAfter: 30 * time.Second}
	adm.attempts.headersAt = time.Now().Add(-25 * time.Second)
	adm.applyHeaders(c)
	require.Equal(t, "5", recorder.Header().Get(rateLimitResetRequestsHeader))
	require.Equal(t, "5", recorder.Header().Get(rateLimitResetTokensHeader))
	adm.attempts.headersAt = time.Now().Add(-time.Minute)
	adm.applyHeaders(c)
	require.Equal(t, "0", recorder.Header().Get(rateLimitResetRequestsHeader))
	require.Equal(t, "0", recorder.Header().Get(rateLimitResetTokensHeader))
	require.Equal(t, int64(1), retryAfterSeconds(0), "Retry-After retains its minimum delay")
}

func TestPassthroughHeadersPreserveGatewayQuota(t *testing.T) {
	dst := http.Header{rateLimitLimitRequestsHeader: {"5"}, rateLimitRemainingRequestsHeader: {"4"}}
	src := http.Header{rateLimitLimitRequestsHeader: {"1000"}, rateLimitRemainingRequestsHeader: {"999"}, "Content-Type": {"application/json"}}
	copyPassthroughResponseHeaders(dst, src)
	require.Equal(t, "5", dst.Get(rateLimitLimitRequestsHeader))
	require.Equal(t, "4", dst.Get(rateLimitRemainingRequestsHeader))
	require.Equal(t, "application/json", dst.Get("Content-Type"))
}

func (p namedAttemptProvider) GetProviderName(model string) string {
	selector, err := core.ParseModelSelector(model, "")
	if err != nil {
		return ""
	}
	return selector.Provider
}

func TestTranslatedFailoverReservesBackupRequests(t *testing.T) {
	for _, path := range []string{"/v1/chat/completions", "/v1/responses", "/v1/messages"} {
		t.Run(path, func(t *testing.T) {
			primary, backup := "first/model", "backup/model"
			provider := &failoverProvider{
				chatErrors:         map[string]error{primary: core.NewProviderError("openai", http.StatusServiceUnavailable, "unavailable", nil)},
				chatResponses:      map[string]*core.ChatResponse{backup: {Model: "model", Provider: "openai", Choices: []core.Choice{{Message: core.ResponseMessage{Role: "assistant", Content: "ok"}, FinishReason: "stop"}}}},
				supportedModels:    map[string]string{primary: "openai", backup: "openai"},
				responsesErrors:    map[string]error{primary: core.NewProviderError("openai", http.StatusServiceUnavailable, "unavailable", nil)},
				responsesResponses: map[string]*core.ResponsesResponse{backup: {Model: "model", Provider: "openai", Status: "completed", Output: []core.ResponsesOutputItem{{Type: "message", Role: "assistant", Status: "completed", Content: []core.ResponsesContentItem{{Type: "output_text", Text: "ok"}}}}}},
			}
			handler := newHandler(namedAttemptProvider{provider}, nil, nil, nil, nil, nil, failoverResolverStub{
				selectors: []core.ModelSelector{{Provider: "backup", Model: "model"}},
			}, nil)
			limit := int64(1)
			handler.rateLimiter = newTestRateLimitService(t,
				rateLimitRuleWithRequests("/", 5),
				ratelimit.Rule{Scope: ratelimit.ScopeProvider, Subject: "backup", PeriodSeconds: ratelimit.PeriodMinuteSeconds, MaxRequests: &limit},
			)
			for _, want := range []int{http.StatusOK, http.StatusServiceUnavailable} {
				body := `{"model":"first/model","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`
				call := handler.ChatCompletion
				switch path {
				case "/v1/responses":
					body = `{"model":"first/model","input":"hi"}`
					call = handler.Responses
				case "/v1/messages":
					call = handler.Messages
				}
				c, recorder := echotest.Post(t, path, body)
				require.NoError(t, call(c))
				require.Equal(t, want, recorder.Code, recorder.Body.String())
			}
			if path == "/v1/responses" {
				require.Equal(t, []string{primary, backup, primary}, provider.responsesCalls)
			} else {
				require.Equal(t, []string{primary, backup, primary}, provider.chatCalls)
			}
		})
	}
}
