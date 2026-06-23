package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v5"

	"gomodel/internal/core"
	"gomodel/internal/providers"
	"gomodel/internal/usage"
	"gomodel/internal/virtualmodels"
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
	if h.pricingResolver != nil {
		t.Fatal("pricingResolver is non-nil for nil registry")
	}
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

	body := bytes.NewBufferString(`{
		"start_date":"2026-04-01",
		"end_date":"2026-04-02",
		"user_path":"team/alpha",
		"selector":"smart",
		"confirmation":"recalculate"
	}`)
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/admin/usage/recalculate-pricing", body)
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	req.Header.Set(dashboardTimeZoneHeader, "Europe/Warsaw")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := h.RecalculateUsagePricing(c); err != nil {
		t.Fatalf("RecalculateUsagePricing() error = %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if recalculator.calls != 1 {
		t.Fatalf("recalculator calls = %d, want 1", recalculator.calls)
	}

	params := recalculator.params
	if params.Provider != "openai" || params.Model != "gpt-4o" {
		t.Fatalf("selector params = %q/%q, want openai/gpt-4o", params.Provider, params.Model)
	}
	if params.UserPath != "/team/alpha" {
		t.Fatalf("UserPath = %q, want /team/alpha", params.UserPath)
	}
	if params.CacheMode != usage.CacheModeAll {
		t.Fatalf("CacheMode = %q, want %q", params.CacheMode, usage.CacheModeAll)
	}

	location, err := time.LoadLocation("Europe/Warsaw")
	if err != nil {
		t.Fatalf("LoadLocation() error = %v", err)
	}
	wantStart := time.Date(2026, 4, 1, 0, 0, 0, 0, location)
	wantEnd := time.Date(2026, 4, 2, 0, 0, 0, 0, location)
	if !params.StartDate.Equal(wantStart) || !params.EndDate.Equal(wantEnd) {
		t.Fatalf("date range = %s to %s, want %s to %s", params.StartDate, params.EndDate, wantStart, wantEnd)
	}

	var result usage.RecalculatePricingResult
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if result.Recalculated != 2 || result.WithPricing != 2 {
		t.Fatalf("result = %+v, want recalculated=2 with_pricing=2", result)
	}
}

func TestRecalculateUsagePricingRequiresConfirmation(t *testing.T) {
	recalculator := &mockPricingRecalculator{}
	h := NewHandler(nil, providers.NewModelRegistry(), WithUsagePricingRecalculator(recalculator))

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/admin/usage/recalculate-pricing", bytes.NewBufferString(`{"confirmation":"nope"}`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := h.RecalculateUsagePricing(c); err != nil {
		t.Fatalf("RecalculateUsagePricing() returned handler error: %v", err)
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if recalculator.calls != 0 {
		t.Fatalf("recalculator calls = %d, want 0", recalculator.calls)
	}
}

func TestRecalculateUsagePricingAcceptsConfirmAlias(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		wantCode  int
		wantCalls int
	}{
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

			e := echo.New()
			req := httptest.NewRequest(http.MethodPost, "/admin/usage/recalculate-pricing", bytes.NewBufferString(test.body))
			req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
			rec := httptest.NewRecorder()
			c := e.NewContext(req, rec)

			if err := h.RecalculateUsagePricing(c); err != nil {
				t.Fatalf("RecalculateUsagePricing() returned handler error: %v", err)
			}
			if rec.Code != test.wantCode {
				t.Fatalf("status = %d, want %d body=%s", rec.Code, test.wantCode, rec.Body.String())
			}
			if recalculator.calls != test.wantCalls {
				t.Fatalf("recalculator calls = %d, want %d", recalculator.calls, test.wantCalls)
			}
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

			e := echo.New()
			req := httptest.NewRequest(http.MethodPost, "/admin/usage/recalculate-pricing", bytes.NewBufferString(`{"confirmation":"recalculate"}`))
			req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
			rec := httptest.NewRecorder()
			c := e.NewContext(req, rec)

			if err := h.RecalculateUsagePricing(c); err != nil {
				t.Fatalf("RecalculateUsagePricing() returned handler error: %v", err)
			}
			if rec.Code != http.StatusServiceUnavailable {
				t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusServiceUnavailable, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), test.wantError) {
				t.Fatalf("response body = %s, want %q", rec.Body.String(), test.wantError)
			}
			if recalculator.calls != 0 {
				t.Fatalf("recalculator calls = %d, want 0", recalculator.calls)
			}
		})
	}
}

func TestRecalculateUsagePricingInvalidSelector(t *testing.T) {
	recalculator := &mockPricingRecalculator{}
	h := NewHandler(nil, providers.NewModelRegistry(), WithUsagePricingRecalculator(recalculator))

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/admin/usage/recalculate-pricing", bytes.NewBufferString(`{"confirmation":"recalculate","selector":"invalid"}`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := h.RecalculateUsagePricing(c); err != nil {
		t.Fatalf("RecalculateUsagePricing() returned handler error: %v", err)
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "invalid selector") {
		t.Fatalf("response body = %s, want invalid selector message", rec.Body.String())
	}
	if recalculator.calls != 0 {
		t.Fatalf("recalculator calls = %d, want 0", recalculator.calls)
	}
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

			e := echo.New()
			req := httptest.NewRequest(http.MethodPost, "/admin/usage/recalculate-pricing", bytes.NewBufferString(test.body))
			req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
			rec := httptest.NewRecorder()
			c := e.NewContext(req, rec)

			if err := h.RecalculateUsagePricing(c); err != nil {
				t.Fatalf("RecalculateUsagePricing() returned handler error: %v", err)
			}
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), test.wantError) {
				t.Fatalf("response body = %s, want %q", rec.Body.String(), test.wantError)
			}
			if recalculator.calls != 0 {
				t.Fatalf("recalculator calls = %d, want 0", recalculator.calls)
			}
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

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/admin/usage/recalculate-pricing", bytes.NewBufferString(`{"confirmation":"recalculate"}`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := h.RecalculateUsagePricing(c); err != nil {
		t.Fatalf("RecalculateUsagePricing() returned handler error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if recalculator.calls != 1 {
		t.Fatalf("recalculator calls = %d, want 1", recalculator.calls)
	}

	expectedEnd := time.Date(2026, 4, 28, 0, 0, 0, 0, time.UTC)
	expectedStart := expectedEnd.AddDate(0, 0, -(defaultDateRangeDays - 1))
	if !recalculator.params.EndDate.Equal(expectedEnd) || !recalculator.params.StartDate.Equal(expectedStart) {
		t.Fatalf("date range = %s to %s, want %s to %s",
			recalculator.params.StartDate, recalculator.params.EndDate, expectedStart, expectedEnd)
	}
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

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/admin/usage/recalculate-pricing", bytes.NewBufferString(`{"confirmation":"recalculate","days":9999}`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := h.RecalculateUsagePricing(c); err != nil {
		t.Fatalf("RecalculateUsagePricing() returned handler error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if recalculator.calls != 1 {
		t.Fatalf("recalculator calls = %d, want 1", recalculator.calls)
	}

	expectedEnd := time.Date(2026, 4, 28, 0, 0, 0, 0, time.UTC)
	expectedStart := expectedEnd.AddDate(0, 0, -(maxDateRangeDays - 1))
	if !recalculator.params.EndDate.Equal(expectedEnd) || !recalculator.params.StartDate.Equal(expectedStart) {
		t.Fatalf("date range = %s to %s, want %s to %s",
			recalculator.params.StartDate, recalculator.params.EndDate, expectedStart, expectedEnd)
	}
}

func TestRecalculateUsagePricingReturnsInternalErrorOnRecalculatorFailure(t *testing.T) {
	recalculator := &mockPricingRecalculator{err: errors.New("storage write failed")}
	h := NewHandler(nil, providers.NewModelRegistry(), WithUsagePricingRecalculator(recalculator))

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/admin/usage/recalculate-pricing", bytes.NewBufferString(`{"confirmation":"recalculate"}`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := h.RecalculateUsagePricing(c); err != nil {
		t.Fatalf("RecalculateUsagePricing() returned handler error: %v", err)
	}
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusInternalServerError, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "failed to recalculate usage pricing") {
		t.Fatalf("response body = %s, want recalculation failure message", rec.Body.String())
	}
	if recalculator.calls != 1 {
		t.Fatalf("recalculator calls = %d, want 1", recalculator.calls)
	}
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

			e := echo.New()
			req := httptest.NewRequest(http.MethodPost, "/admin/usage/recalculate-pricing", bytes.NewBufferString(`{"confirmation":"recalculate"}`))
			req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
			rec := httptest.NewRecorder()
			c := e.NewContext(req, rec)

			if err := h.RecalculateUsagePricing(c); err != nil {
				t.Fatalf("RecalculateUsagePricing() returned handler error: %v", err)
			}
			if rec.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d body=%s", rec.Code, test.wantStatus, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), test.wantBodyString) {
				t.Fatalf("response body = %s, want %q", rec.Body.String(), test.wantBodyString)
			}
			if recalculator.calls != 1 {
				t.Fatalf("recalculator calls = %d, want 1", recalculator.calls)
			}
		})
	}
}
