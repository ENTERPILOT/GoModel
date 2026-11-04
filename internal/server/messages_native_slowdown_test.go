package server

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/echotest"
)

// slowPassthroughProvider answers native passthrough after a fixed delay, so a
// slowdown factor has a measurable inference time to scale.
type slowPassthroughProvider struct {
	*mockProvider
	delay time.Duration
}

// delayedReader hands out its content only after delay, like a body whose
// bytes trail the response headers.
type delayedReader struct {
	delay time.Duration
	r     io.Reader
	once  sync.Once
}

func (d *delayedReader) Read(p []byte) (int, error) {
	d.once.Do(func() { <-time.After(d.delay) })
	return d.r.Read(p)
}

func (p *slowPassthroughProvider) Passthrough(ctx context.Context, providerType string, req *core.PassthroughRequest) (*core.PassthroughResponse, error) {
	select {
	case <-time.After(p.delay):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return p.mockProvider.Passthrough(ctx, providerType, req)
}

func TestMessages_NativeForwardingAppliesSlowdown(t *testing.T) {
	const nativeJSON = `{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"native"}],"model":"claude-test","stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`
	const nativeSSE = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"

	tests := []struct {
		name        string
		reqBody     string
		contentType string
		upstream    string
		bodyDelay   time.Duration
		wantElapsed time.Duration
	}{
		{
			name:        "json response waits after the upstream answered",
			reqBody:     `{"model":"claude-test","max_tokens":64,"messages":[{"role":"user","content":"Hi"}]}`,
			contentType: "application/json",
			upstream:    nativeJSON,
			wantElapsed: 30 * time.Millisecond,
		},
		{
			name:        "json body transfer counts toward the wait",
			reqBody:     `{"model":"claude-test","max_tokens":64,"messages":[{"role":"user","content":"Hi"}]}`,
			contentType: "application/json",
			upstream:    nativeJSON,
			bodyDelay:   10 * time.Millisecond,
			wantElapsed: 50 * time.Millisecond,
		},
		{
			name:        "stream is paced on the scaled timeline",
			reqBody:     `{"model":"claude-test","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"Hi"}]}`,
			contentType: "text/event-stream",
			upstream:    nativeSSE,
			wantElapsed: 30 * time.Millisecond,
		},
	}
	for _, tt := range tests {
		// A synctest bubble turns the fake upstream's latency and the
		// slowdown wait into fake-clock time, so the assertion is exact.
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				runNativeSlowdownCase(t, tt.reqBody, tt.contentType, tt.upstream, tt.bodyDelay, tt.wantElapsed)
			})
		})
	}
}

func runNativeSlowdownCase(t *testing.T, reqBody, contentType, upstream string, bodyDelay, wantElapsed time.Duration) {
	t.Helper()
	provider := &slowPassthroughProvider{
		mockProvider: &mockProvider{
			supportedModels: []string{"claude-test"},
			providerTypes:   map[string]string{"claude-test": "anthropic"},
			passthroughResponse: &core.PassthroughResponse{
				StatusCode: http.StatusOK,
				Headers:    map[string][]string{"Content-Type": {contentType}},
				Body:       io.NopCloser(&delayedReader{delay: bodyDelay, r: strings.NewReader(upstream)}),
			},
		},
		delay: 15 * time.Millisecond,
	}
	resolver := &audioSlowdownResolver{factor: 1}
	handler := newHandler(provider, nil, nil, nil, resolver, nil, nil, nil)
	c, rec := echotest.Post(t, "/v1/messages", reqBody)

	started := time.Now()
	err := handler.Messages(c)
	require.NoError(t, err)
	elapsed := time.Since(started)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, upstream, rec.Body.String())
	require.NotNil(t, provider.lastPassthroughReq, "a slowed request must still forward natively")
	assert.Equal(t, wantElapsed, elapsed, "slowdown factor 1 doubles the upstream time")
	assert.Equal(t, "claude-test", resolver.resolved)
}

func TestSlowDownNativeMessages_SkipsErrorsAndUnslowedModels(t *testing.T) {
	body := func() io.ReadCloser { return io.NopCloser(strings.NewReader("{}")) }
	tests := []struct {
		name     string
		workflow *core.Workflow
		status   int
	}{
		{name: "no slowdown", workflow: &core.Workflow{Resolution: &core.RequestModelResolution{}}, status: http.StatusOK},
		{name: "no resolution", workflow: &core.Workflow{}, status: http.StatusOK},
		{name: "upstream error", workflow: &core.Workflow{Resolution: &core.RequestModelResolution{Slowdown: 10}}, status: http.StatusTooManyRequests},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				resp := &core.PassthroughResponse{StatusCode: tt.status, Body: body()}
				started := time.Now().Add(-time.Second)
				before := time.Now()
				require.NoError(t, slowDownNativeMessages(context.Background(), tt.workflow, false, resp, started))
				assert.Equal(t, time.Duration(0), time.Since(before), "must return without waiting")
			})
		})
	}
}

// failingBody errors on its first read, like an upstream connection that
// drops after the headers.
type failingBody struct{}

func (failingBody) Read([]byte) (int, error) { return 0, errors.New("connection reset") }
func (failingBody) Close() error             { return nil }

func TestSlowDownNativeMessages_ReportsBodyReadFailure(t *testing.T) {
	workflow := &core.Workflow{Resolution: &core.RequestModelResolution{Slowdown: 1}}
	resp := &core.PassthroughResponse{StatusCode: http.StatusOK, Body: failingBody{}}
	err := slowDownNativeMessages(context.Background(), workflow, false, resp, time.Now())
	require.Error(t, err)
	var gatewayErr *core.GatewayError
	require.ErrorAs(t, err, &gatewayErr)
	assert.Equal(t, http.StatusBadGateway, gatewayErr.StatusCode)
}
