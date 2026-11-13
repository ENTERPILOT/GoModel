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
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/echotest"
	"github.com/enterpilot/gomodel/internal/llmclient"
)

func TestObserverLabelsProviderSpansWithUserPath(t *testing.T) {
	tests := []struct {
		name string
		call llmclient.RequestInfo
		err  error
	}{
		{name: "buffered", call: llmclient.RequestInfo{Provider: "openai", Model: "gpt-5", Operation: "chat"}},
		{name: "stream that failed to establish", call: llmclient.RequestInfo{Provider: "openai", Model: "gpt-5", Operation: "chat", Stream: true}, err: context.DeadlineExceeded},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hooks, recorder, reader := newTestHooks(t)
			ctx := core.WithEffectiveUserPath(t.Context(), "/team/alpha")
			ctx = hooks.OnRequestStart(ctx, tt.call)
			hooks.OnRequestEnd(ctx, response(tt.call, http.StatusOK, 10*time.Millisecond, tt.err))

			spans := recorder.Ended()
			require.Len(t, spans, 1)
			assert.Equal(t, "/team/alpha", attributeMap(spans[0].Attributes())[userPathAttribute])

			found, ok := findMetric(t, reader, "gen_ai.client.operation.duration")
			require.True(t, ok)
			histogram, ok := found.Data.(metricdata.Histogram[float64])
			require.True(t, ok)
			for _, point := range histogram.DataPoints {
				_, has := point.Attributes.Value(attribute.Key(userPathAttribute))
				assert.False(t, has, "user path is span-only")
			}
		})
	}
}

func TestObserverOmitsUserPathWhenUnset(t *testing.T) {
	hooks, recorder, _ := newTestHooks(t)
	call := llmclient.RequestInfo{Provider: "openai", Model: "gpt-5", Operation: "chat"}
	ctx := hooks.OnRequestStart(t.Context(), call)
	hooks.OnRequestEnd(ctx, response(call, http.StatusOK, 10*time.Millisecond, nil))

	spans := recorder.Ended()
	require.Len(t, spans, 1)
	assert.NotContains(t, attributeMap(spans[0].Attributes()), userPathAttribute)
}

func TestServerSpanGetsUserPathResolvedByInnerMiddleware(t *testing.T) {
	tests := []struct {
		name  string
		panic bool
	}{
		{name: "handler returns"},
		{name: "handler panics after authentication", panic: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := tracetest.NewSpanRecorder()
			tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
			t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

			c, _ := echotest.Request(t, http.MethodPost, "/v1/chat/completions", nil)
			ctx, span := tp.Tracer("test").Start(c.Request().Context(), "POST /v1/chat/completions")
			c.SetRequest(c.Request().WithContext(ctx))

			auth := func(c *echo.Context) error {
				c.SetRequest(c.Request().WithContext(core.WithEffectiveUserPath(c.Request().Context(), "/team/alpha/service")))
				if tt.panic {
					panic("handler failed")
				}
				return nil
			}
			handler := withGenerationTracing(auth, false)
			if tt.panic {
				assert.Panics(t, func() { _ = handler(c) })
			} else {
				require.NoError(t, handler(c))
			}
			span.End()

			spans := recorder.Ended()
			require.Len(t, spans, 1)
			assert.Equal(t, "/team/alpha/service", attributeMap(spans[0].Attributes())[userPathAttribute])
		})
	}
}
