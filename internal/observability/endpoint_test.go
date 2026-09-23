package observability

import (
	"context"
	"strconv"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"

	"github.com/enterpilot/gomodel/internal/llmclient"
)

func TestMetricEndpoint(t *testing.T) {
	tests := []struct {
		endpoint string
		want     string
	}{
		{"/chat/completions", "/chat/completions"},
		{"chat/completions?trace=true", "/chat/completions"},
		{"", "/"},
		{"/v1/models", "/v1/models"},
		{"/models?pageToken=abc", "/models"},
		{"/files/file-abc123", "/files/{id}"},
		{"/files/file-abc123/content", "/files/{id}/content"},
		{"/batches/batch_1/cancel", "/batches/{id}/cancel"},
		{"/openai/batches/batch_1", "/openai/batches/{id}"},
		{"/messages/batches", "/messages/batches"},
		{"/messages/batches/msgbatch_01/results", "/messages/batches/{id}/results"},
		{"/v1/messages/batches", "/v1/messages/batches"},
		{"/responses/resp_123", "/responses/{id}"},
		{"/responses/input_tokens", "/responses/input_tokens"},
		{"/responses/compact", "/responses/compact"},
		{"/messages/count_tokens", "/messages/count_tokens"},
		{"/models/gemini-2.5-flash:generateContent", "/models/{id}:generateContent"},
		{"/v1/text-to-speech/21m00Tcm4TlvDq8ikWAM?output_format=mp3", "/v1/text-to-speech/{id}"},
		{"/v1/custom/12345/run", "/v1/custom/{id}/run"},
		{"/v1/custom/0f8fad5b-d9cb-469f-a165-70867728950e", "/v1/custom/{id}"},
		{"/v2/chat", "/v2/chat"},
	}
	for _, tt := range tests {
		t.Run(tt.endpoint, func(t *testing.T) {
			assert.Equal(t, tt.want, metricEndpoint(tt.endpoint))
		})
	}
}

func TestPrometheusHooksBoundEndpointCardinality(t *testing.T) {
	ResetMetrics()
	hooks := NewPrometheusHooks()

	for _, id := range []string{"file-a1", "file-b2", "file-c3"} {
		info := llmclient.RequestInfo{Provider: "openai", Endpoint: "/files/" + id}
		ctx := hooks.OnRequestStart(context.Background(), info)
		hooks.OnRequestEnd(ctx, llmclient.ResponseInfo{Provider: "openai", Endpoint: info.Endpoint, StatusCode: 200})
	}

	assert.Equal(t, 1, testutil.CollectAndCount(RequestsTotal))
	assert.Equal(t, 1, testutil.CollectAndCount(RequestDuration))
	assert.Equal(t, 1, testutil.CollectAndCount(InFlightRequests))
	assert.InDelta(t, 3, testutil.ToFloat64(RequestsTotal.WithLabelValues("openai", "", "/files/{id}", "200", "success", "false")), 0)
}

func TestMetricEndpointCapsDistinctLabels(t *testing.T) {
	ResetMetrics()
	t.Cleanup(ResetMetrics)

	for i := range maxEndpointLabels {
		assert.Equal(t, "/custom/resource-"+strconv.Itoa(i), metricEndpoint("/custom/resource-"+strconv.Itoa(i)))
	}
	assert.Equal(t, otherEndpointLabel, metricEndpoint("/custom/resource-alpha"), "new paths past the cap collapse")
	assert.Equal(t, "/custom/resource-0", metricEndpoint("/custom/resource-0"), "admitted paths keep their label")
}
