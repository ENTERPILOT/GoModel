package server

import (
	"context"
	"io"
	"net/http"
	"strings"
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
	}{
		{
			name:        "json response waits after the upstream answered",
			reqBody:     `{"model":"claude-test","max_tokens":64,"messages":[{"role":"user","content":"Hi"}]}`,
			contentType: "application/json",
			upstream:    nativeJSON,
		},
		{
			name:        "stream is paced on the scaled timeline",
			reqBody:     `{"model":"claude-test","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"Hi"}]}`,
			contentType: "text/event-stream",
			upstream:    nativeSSE,
		},
	}
	for _, tt := range tests {
		// A synctest bubble turns the fake upstream's latency and the
		// slowdown wait into fake-clock time, so the assertion is exact.
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) { runNativeSlowdownCase(t, tt.reqBody, tt.contentType, tt.upstream) })
		})
	}
}

func runNativeSlowdownCase(t *testing.T, reqBody, contentType, upstream string) {
	t.Helper()
	provider := &slowPassthroughProvider{
		mockProvider: &mockProvider{
			supportedModels: []string{"claude-test"},
			providerTypes:   map[string]string{"claude-test": "anthropic"},
			passthroughResponse: &core.PassthroughResponse{
				StatusCode: http.StatusOK,
				Headers:    map[string][]string{"Content-Type": {contentType}},
				Body:       io.NopCloser(strings.NewReader(upstream)),
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
	assert.Equal(t, 30*time.Millisecond, elapsed, "slowdown factor 1 doubles the upstream time")
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
			resp := &core.PassthroughResponse{StatusCode: tt.status, Body: body()}
			started := time.Now().Add(-time.Second)
			done := time.Now()
			require.NoError(t, slowDownNativeMessages(context.Background(), tt.workflow, false, resp, started))
			assert.Less(t, time.Since(done), 500*time.Millisecond, "must return without waiting")
		})
	}
}
