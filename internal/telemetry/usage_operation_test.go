package telemetry

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/echotest"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/usage"
	"github.com/enterpilot/gomodel/internal/usagemetrics"
)

// histogramOperations returns the gen_ai.operation.name of every point of an
// int64 or float64 histogram.
func histogramOperations(t *testing.T, reader *metric.ManualReader, name string) []string {
	t.Helper()
	found, ok := findMetric(t, reader, name)
	require.True(t, ok, "%s was not recorded", name)
	var sets []attribute.Set
	switch data := found.Data.(type) {
	case metricdata.Histogram[int64]:
		for _, point := range data.DataPoints {
			sets = append(sets, point.Attributes)
		}
	case metricdata.Histogram[float64]:
		for _, point := range data.DataPoints {
			sets = append(sets, point.Attributes)
		}
	default:
		require.Failf(t, "unexpected metric data", "%s is %T", name, found.Data)
	}
	operations := make([]string, 0, len(sets))
	for _, set := range sets {
		operation, _ := set.Value("gen_ai.operation.name")
		operations = append(operations, operation.AsString())
	}
	return operations
}

// The token metric names the operation of the provider call that served the
// entry, as the duration metric does, not one derived from the endpoint.
func TestTokenUsageAndDurationAgreeOnOperation(t *testing.T) {
	tests := []struct {
		name      string
		operation string
		endpoint  string
	}{
		{name: "gemini native", operation: llmclient.OperationGenerateContent, endpoint: "/v1/chat/completions"},
		{name: "gemini openai-compatible", operation: llmclient.OperationChat, endpoint: "/v1/chat/completions"},
		{name: "completions passthrough", operation: llmclient.OperationTextCompletion, endpoint: "/v1/completions"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reader := metric.NewManualReader()
			mp := metric.NewMeterProvider(metric.WithReader(reader))
			tp := sdktrace.NewTracerProvider()
			t.Cleanup(func() {
				_ = mp.Shutdown(context.Background())
				_ = tp.Shutdown(context.Background())
			})
			observer, err := newObserver(tp, mp, false)
			require.NoError(t, err)
			recorder, err := newUsageRecorder(mp)
			require.NoError(t, err)
			hooks := observer.hooks()

			// As the middleware and the gateway do: the request carries the
			// record, the provider call sets it, the usage entry reads it.
			ctx := core.WithGenAIOperation(context.Background())
			call := llmclient.RequestInfo{Provider: "gemini", Model: "gemini-2.5-flash", Operation: tt.operation}
			hooks.OnRequestEnd(hooks.OnRequestStart(ctx, call), response(call, http.StatusOK, time.Second, nil))
			usagemetrics.NewTap(discardLogger{}, nil, recorder).Write(&usage.UsageEntry{
				Provider: "gemini", Model: "gemini-2.5-flash", Endpoint: tt.endpoint,
				InputTokens: 10, OutputTokens: 5, Operation: core.GenAIOperationFromContext(ctx).Name(),
			})

			assert.Equal(t, []string{tt.operation}, histogramOperations(t, reader, "gen_ai.client.operation.duration"))
			assert.Equal(t, []string{tt.operation, tt.operation}, histogramOperations(t, reader, "gen_ai.client.token.usage"))
		})
	}
}

// The middleware attaches the operation record whether or not the request is
// sampled; core covers setting and reading it.
func TestMiddlewareAttachesOperationRecordToUnsampledRequests(t *testing.T) {
	c, _ := echotest.Get(t, "/v1/chat/completions")
	var record *core.GenAIOperation
	handler := withGenerationTracing(func(c *echo.Context) error {
		record = core.GenAIOperationFromContext(c.Request().Context())
		return nil
	}, false)

	require.NoError(t, handler(c))
	assert.NotNil(t, record)
}
