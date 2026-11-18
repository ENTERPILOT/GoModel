package admin

import (
	"net/http"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/echotest"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/usage"
)

// writeDeadlineRecorder records the write deadlines a handler sets through
// http.ResponseController.
type writeDeadlineRecorder struct {
	http.ResponseWriter
	deadlines []time.Time
}

func (w *writeDeadlineRecorder) SetWriteDeadline(deadline time.Time) error {
	w.deadlines = append(w.deadlines, deadline)
	return nil
}

// These actions can outlast the server-wide write timeout, which would drop
// the response of work that still completes; they clear it for their response.
func TestLongAdminActionsClearTheWriteDeadline(t *testing.T) {
	tests := []struct {
		name   string
		target string
		body   any
		handle func(*Handler) echo.HandlerFunc
	}{
		{
			name:   "runtime refresh",
			target: "/admin/runtime/refresh",
			handle: func(h *Handler) echo.HandlerFunc { return h.RefreshRuntime },
		},
		{
			name:   "usage pricing recalculation",
			target: "/admin/usage/recalculate-pricing",
			body:   `{"confirmation":"recalculate"}`,
			handle: func(h *Handler) echo.HandlerFunc { return h.RecalculateUsagePricing },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := NewHandler(nil, providers.NewModelRegistry(),
				WithRuntimeRefresher(&mockRuntimeRefresher{}),
				WithUsagePricingRecalculator(&mockPricingRecalculator{result: usage.RecalculatePricingResult{Status: "ok"}}),
			)
			var writer *writeDeadlineRecorder
			c, rec := echotest.Post(t, tt.target, tt.body, echotest.WithResponseWriter(func(w http.ResponseWriter) http.ResponseWriter {
				writer = &writeDeadlineRecorder{ResponseWriter: w}
				return writer
			}))

			require.NoError(t, tt.handle(h)(c))
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			assert.Equal(t, []time.Time{{}}, writer.deadlines, "the write deadline is cleared once and never set again")
		})
	}
}
