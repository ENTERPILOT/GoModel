package perf

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
)

// countingResolver counts the model resolutions one request performs.
type countingResolver struct {
	inner benchAliasResolver
	calls atomic.Int64
}

func (r *countingResolver) ResolveModel(requested core.RequestedModelSelector) (core.ModelSelector, bool, error) {
	r.calls.Add(1)
	return r.inner.ResolveModel(requested)
}

// Model resolution is reachable from the request middleware, the inference
// orchestrator and the router, and each of those falls back to resolving when
// it finds no workflow on the request. The middleware stores the workflow it
// resolved and the orchestrator reuses it, so a request resolves once; this
// pins that, because the duplication only reappears when one of those
// hand-offs quietly stops working.
func TestChatCompletionResolvesTheModelOnce(t *testing.T) {
	for _, tt := range []struct {
		name string
		body string
	}{
		{name: "alias", body: sampleAliasChatRequest},
		{name: "plain", body: sampleChatRequest},
		{name: "provider qualified", body: sampleQualifiedChatRequest},
	} {
		t.Run(tt.name, func(t *testing.T) {
			counter := &countingResolver{inner: benchAliasResolver{"fast": {Model: "gpt-4o-mini", Provider: "mock"}}}
			srv := newRoutedBenchServerWithResolver(t, routedCatalogSize, counter)

			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader([]byte(tt.body)))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, req)

			require.Equal(t, http.StatusOK, rec.Code)
			assert.Equal(t, int64(1), counter.calls.Load(), "model resolutions for one request")
		})
	}
}
