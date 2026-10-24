package admin

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/echotest"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/usage"
	"github.com/enterpilot/gomodel/internal/virtualmodels"
)

type mockPricingRecalculator struct {
	calls  int
	params usage.RecalculatePricingParams
	result usage.RecalculatePricingResult
	err    error
}

func (m *mockPricingRecalculator) RecalculatePricing(_ context.Context, params usage.RecalculatePricingParams, _ usage.PricingResolver) (usage.RecalculatePricingResult, error) {
	m.calls++
	m.params = params
	if m.err != nil {
		return usage.RecalculatePricingResult{}, m.err
	}
	return m.result, nil
}

func TestNewHandlerDoesNotWrapNilRegistryAsPricingResolver(t *testing.T) {
	h := NewHandler(nil, nil)
	require.Nil(t, h.pricingResolver)
}

func TestRecalculateUsagePricingResolvesAliasAndFilters(t *testing.T) {
	catalog := newVMTestCatalog()
	catalog.add("openai/gpt-4o", "openai")
	service := newVMService(t, catalog, newVMTestStore(virtualmodels.VirtualModel{
		Source:  "smart",
		Targets: []virtualmodels.Target{{Provider: "openai", Model: "gpt-4o"}},
		Enabled: true,
	}), true)

	recalculator := &mockPricingRecalculator{
		result: usage.RecalculatePricingResult{
			Status:       "ok",
			Matched:      2,
			Recalculated: 2,
			WithPricing:  2,
		},
	}
	h := NewHandler(nil, providers.NewModelRegistry(),
		WithVirtualModels(service),
		WithUsagePricingRecalculator(recalculator),
	)

	c, rec := echotest.Post(t, "/admin/usage/recalculate-pricing", `{
		"start_date":"2026-04-01",
		"end_date":"2026-04-02",
		"user_path":"team/alpha",
		"selector":"smart",
		"confirmation":"recalculate"
	}`, echotest.WithHeader(dashboardTimeZoneHeader, "Europe/Warsaw"))
	err := h.RecalculateUsagePricing(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, 1, recalculator.calls)

	params := recalculator.params
	assert.Equal(t, "openai", params.Provider)
	assert.Equal(t, "gpt-4o", params.Model)
	assert.Equal(t, "/team/alpha", params.UserPath)
	assert.Equal(t, usage.CacheModeAll, params.CacheMode)

	location, err := time.LoadLocation("Europe/Warsaw")
	require.NoError(t, err)
	assert.WithinDuration(t, time.Date(2026, 4, 1, 0, 0, 0, 0, location), params.StartDate, 0)
	assert.WithinDuration(t, time.Date(2026, 4, 2, 0, 0, 0, 0, location), params.EndDate, 0)

	result := echotest.Decode[usage.RecalculatePricingResult](t, rec)
	assert.Equal(t, int64(2), result.Recalculated)
	assert.Equal(t, int64(2), result.WithPricing)
}

func TestRecalculateUsagePricingConfirmation(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		wantCode  int
		wantCalls int
	}{
		{
			name:      "confirmation rejected",
			body:      `{"confirmation":"nope"}`,
			wantCode:  http.StatusBadRequest,
			wantCalls: 0,
		},
		{
			name:      "confirm alias accepted",
			body:      `{"confirm":"recalculate"}`,
			wantCode:  http.StatusOK,
			wantCalls: 1,
		},
		{
			name:      "confirm alias rejected",
			body:      `{"confirm":"nope"}`,
			wantCode:  http.StatusBadRequest,
			wantCalls: 0,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recalculator := &mockPricingRecalculator{
				result: usage.RecalculatePricingResult{Status: "ok"},
			}
			h := NewHandler(nil, providers.NewModelRegistry(), WithUsagePricingRecalculator(recalculator))

			c, rec := echotest.Post(t, "/admin/usage/recalculate-pricing", test.body)
			err := h.RecalculateUsagePricing(c)
			require.NoError(t, err)
			require.Equal(t, test.wantCode, rec.Code, rec.Body.String())
			assert.Equal(t, test.wantCalls, recalculator.calls)
		})
	}
}

func TestRecalculateUsagePricingFeatureUnavailable(t *testing.T) {
	tests := []struct {
		name      string
		handler   func(*mockPricingRecalculator) *Handler
		wantError string
	}{
		{
			name: "missing recalculator",
			handler: func(*mockPricingRecalculator) *Handler {
				return NewHandler(nil, providers.NewModelRegistry())
			},
			wantError: "usage pricing recalculation is unavailable",
		},
		{
			name: "missing model registry",
			handler: func(recalculator *mockPricingRecalculator) *Handler {
				return NewHandler(nil, nil, WithUsagePricingRecalculator(recalculator))
			},
			wantError: "model pricing metadata is unavailable",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recalculator := &mockPricingRecalculator{}
			h := test.handler(recalculator)

			c, rec := echotest.Post(t, "/admin/usage/recalculate-pricing", `{"confirmation":"recalculate"}`)
			err := h.RecalculateUsagePricing(c)
			require.NoError(t, err)
			require.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
			assert.Contains(t, rec.Body.String(), test.wantError)
			assert.Equal(t, 0, recalculator.calls)
		})
	}
}

func TestRecalculateUsagePricingInvalidSelector(t *testing.T) {
	recalculator := &mockPricingRecalculator{}
	h := NewHandler(nil, providers.NewModelRegistry(), WithUsagePricingRecalculator(recalculator))

	c, rec := echotest.Post(t, "/admin/usage/recalculate-pricing", `{"confirmation":"recalculate","selector":"invalid"}`)
	err := h.RecalculateUsagePricing(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "invalid selector")
	assert.Equal(t, 0, recalculator.calls)
}

func TestRecalculateUsagePricingRejectsInvalidDateAndUserPath(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		wantError string
	}{
		{
			name:      "invalid start date",
			body:      `{"confirmation":"recalculate","start_date":"2026/04/01"}`,
			wantError: "invalid start_date format, expected YYYY-MM-DD",
		},
		{
			name:      "invalid end date",
			body:      `{"confirmation":"recalculate","end_date":"tomorrow"}`,
			wantError: "invalid end_date format, expected YYYY-MM-DD",
		},
		{
			name:      "inverted date range",
			body:      `{"confirmation":"recalculate","start_date":"2026-04-29","end_date":"2026-04-28"}`,
			wantError: "start_date must be on or before end_date",
		},
		{
			name:      "invalid user path",
			body:      `{"confirmation":"recalculate","user_path":"/team/../alpha"}`,
			wantError: "invalid user_path: user path cannot contain '.' or '..' segments",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recalculator := &mockPricingRecalculator{}
			h := NewHandler(nil, providers.NewModelRegistry(), WithUsagePricingRecalculator(recalculator))

			c, rec := echotest.Post(t, "/admin/usage/recalculate-pricing", test.body)
			err := h.RecalculateUsagePricing(c)
			require.NoError(t, err)
			require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			assert.Contains(t, rec.Body.String(), test.wantError)
			assert.Equal(t, 0, recalculator.calls)
		})
	}
}

func TestRecalculateUsagePricingDefaultsDateRange(t *testing.T) {
	originalTimeNow := timeNow
	timeNow = func() time.Time {
		return time.Date(2026, 4, 28, 12, 0, 0, 0, time.UTC)
	}
	defer func() {
		timeNow = originalTimeNow
	}()

	recalculator := &mockPricingRecalculator{
		result: usage.RecalculatePricingResult{Status: "ok"},
	}
	h := NewHandler(nil, providers.NewModelRegistry(), WithUsagePricingRecalculator(recalculator))

	c, rec := echotest.Post(t, "/admin/usage/recalculate-pricing", `{"confirmation":"recalculate"}`)
	err := h.RecalculateUsagePricing(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, 1, recalculator.calls)

	expectedEnd := time.Date(2026, 4, 28, 0, 0, 0, 0, time.UTC)
	assert.WithinDuration(t, expectedEnd, recalculator.params.EndDate, 0)
	assert.WithinDuration(t, expectedEnd.AddDate(0, 0, -(defaultDateRangeDays-1)), recalculator.params.StartDate, 0)
}

func TestRecalculateUsagePricingClampsRequestedDays(t *testing.T) {
	originalTimeNow := timeNow
	timeNow = func() time.Time {
		return time.Date(2026, 4, 28, 12, 0, 0, 0, time.UTC)
	}
	defer func() {
		timeNow = originalTimeNow
	}()

	recalculator := &mockPricingRecalculator{
		result: usage.RecalculatePricingResult{Status: "ok"},
	}
	h := NewHandler(nil, providers.NewModelRegistry(), WithUsagePricingRecalculator(recalculator))

	c, rec := echotest.Post(t, "/admin/usage/recalculate-pricing", `{"confirmation":"recalculate","days":9999}`)
	err := h.RecalculateUsagePricing(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, 1, recalculator.calls)

	expectedEnd := time.Date(2026, 4, 28, 0, 0, 0, 0, time.UTC)
	assert.WithinDuration(t, expectedEnd, recalculator.params.EndDate, 0)
	assert.WithinDuration(t, expectedEnd.AddDate(0, 0, -(maxDateRangeDays-1)), recalculator.params.StartDate, 0)
}

func TestRecalculateUsagePricingReturnsInternalErrorOnRecalculatorFailure(t *testing.T) {
	recalculator := &mockPricingRecalculator{err: errors.New("storage write failed")}
	h := NewHandler(nil, providers.NewModelRegistry(), WithUsagePricingRecalculator(recalculator))

	c, rec := echotest.Post(t, "/admin/usage/recalculate-pricing", `{"confirmation":"recalculate"}`)
	err := h.RecalculateUsagePricing(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusInternalServerError, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "failed to recalculate usage pricing")
	assert.Equal(t, 1, recalculator.calls)
}

func TestRecalculateUsagePricingPreservesExpectedRecalculatorErrors(t *testing.T) {
	tests := []struct {
		name           string
		err            error
		wantStatus     int
		wantBodyString string
	}{
		{
			name:           "context canceled",
			err:            context.Canceled,
			wantStatus:     statusClientClosedRequest,
			wantBodyString: "request_canceled",
		},
		{
			name:           "context deadline exceeded",
			err:            context.DeadlineExceeded,
			wantStatus:     http.StatusGatewayTimeout,
			wantBodyString: "request_timeout",
		},
		{
			name:           "gateway error",
			err:            core.NewRateLimitError("usage", "pricing recalculation is rate limited"),
			wantStatus:     http.StatusTooManyRequests,
			wantBodyString: "rate_limit_error",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recalculator := &mockPricingRecalculator{err: test.err}
			h := NewHandler(nil, providers.NewModelRegistry(), WithUsagePricingRecalculator(recalculator))

			c, rec := echotest.Post(t, "/admin/usage/recalculate-pricing", `{"confirmation":"recalculate"}`)
			err := h.RecalculateUsagePricing(c)
			require.NoError(t, err)
			require.Equal(t, test.wantStatus, rec.Code, rec.Body.String())
			assert.Contains(t, rec.Body.String(), test.wantBodyString)
			assert.Equal(t, 1, recalculator.calls)
		})
	}
}
