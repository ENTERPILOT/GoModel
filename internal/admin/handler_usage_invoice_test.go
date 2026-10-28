package admin

import (
	"encoding/csv"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/echotest"
	"github.com/enterpilot/gomodel/internal/usage"
)

func invoiceHandler(reader usage.UsageReader, entitled, markups bool) *Handler {
	return NewHandler(reader, nil, WithInvoiceExportEnabled(entitled), WithBilling(usage.Billing{Enabled: markups}))
}

func sampleInvoice() *usage.Invoice {
	line := usage.InvoiceLine{
		Model: "gpt-5", Provider: "primary", Requests: 2, InputTokens: 200, OutputTokens: 20,
		ProviderCost: new(3.0), BilledCost: new(4.1), EffectiveMarkupPercent: new(36.6667),
	}
	unpriced := usage.InvoiceLine{Model: "=cmd()", Provider: "anthropic", Requests: 1, InputTokens: 100}
	subtotal := line.InvoiceAmounts
	subtotal.Requests, subtotal.InputTokens = 3, 300
	return &usage.Invoice{
		StartDate: "2026-03-01", EndDate: "2026-03-31", Currency: usage.InvoiceCurrency, Parent: "/customers",
		Customers: []usage.InvoiceCustomer{{Customer: "/customers/acme", Lines: []usage.InvoiceLine{unpriced, line}, Subtotal: subtotal}},
		Total:     subtotal,
	}
}

func TestUsageInvoice_Gating(t *testing.T) {
	tests := []struct {
		name       string
		entitled   bool
		markups    bool
		query      string
		wantStatus int
		wantCode   string
	}{
		{name: "not entitled", markups: true, wantStatus: http.StatusForbidden, wantCode: "invoice_export_not_entitled"},
		{name: "not entitled wins over disabled markups", wantStatus: http.StatusForbidden, wantCode: "invoice_export_not_entitled"},
		{name: "markups disabled", entitled: true, wantStatus: http.StatusConflict, wantCode: "markups_disabled"},
		{name: "unknown format", entitled: true, markups: true, query: "format=xlsx", wantStatus: http.StatusBadRequest},
		{name: "invalid parent", entitled: true, markups: true, query: "parent=/a/../b", wantStatus: http.StatusBadRequest},
		{name: "invalid date", entitled: true, markups: true, query: "start_date=March", wantStatus: http.StatusBadRequest},
		{name: "enabled", entitled: true, markups: true, wantStatus: http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := invoiceHandler(&mockUsageReader{invoice: sampleInvoice()}, tt.entitled, tt.markups)
			c, rec := echotest.Get(t, "/admin/usage/invoice?"+tt.query)
			require.NoError(t, h.UsageInvoice(c))
			require.Equal(t, tt.wantStatus, rec.Code, rec.Body.String())
			if tt.wantCode != "" {
				assert.Contains(t, rec.Body.String(), `"code":"`+tt.wantCode+`"`)
			}
		})
	}
}

func TestUsageInvoice_CSV(t *testing.T) {
	reader := &mockUsageReader{invoice: sampleInvoice()}
	h := invoiceHandler(reader, true, true)
	c, rec := echotest.Get(t, "/admin/usage/invoice?start_date=2026-03-01&end_date=2026-03-31&parent=/customers/&model=gpt-5")
	require.NoError(t, h.UsageInvoice(c))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	assert.Equal(t, "text/csv; charset=utf-8", rec.Header().Get("Content-Type"))
	assert.Equal(t, `attachment; filename="gomodel-invoice-2026-03-01-to-2026-03-31.csv"`, rec.Header().Get("Content-Disposition"))
	assert.Equal(t, "/customers", reader.lastInvoice.Parent)
	assert.Equal(t, []string{"gpt-5"}, reader.lastInvoice.Models)
	assert.Equal(t, time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), reader.lastInvoice.StartDate)

	records, err := csv.NewReader(strings.NewReader(rec.Body.String())).ReadAll()
	require.NoError(t, err)
	assert.Equal(t, [][]string{
		{"row_type", "period_start", "period_end", "currency", "customer", "model", "provider", "requests", "input_tokens", "output_tokens", "provider_cost", "billed_cost", "effective_markup_percent"},
		{"item", "2026-03-01", "2026-03-31", "USD", "/customers/acme", "'=cmd()", "anthropic", "1", "100", "0", "", "", ""},
		{"item", "2026-03-01", "2026-03-31", "USD", "/customers/acme", "gpt-5", "primary", "2", "200", "20", "3", "4.1", "36.6667"},
		{"subtotal", "2026-03-01", "2026-03-31", "USD", "/customers/acme", "", "", "3", "300", "20", "3", "4.1", "36.6667"},
		{"total", "2026-03-01", "2026-03-31", "USD", "", "", "", "3", "300", "20", "3", "4.1", "36.6667"},
	}, records)
}

func TestUsageInvoice_JSON(t *testing.T) {
	h := invoiceHandler(&mockUsageReader{invoice: sampleInvoice()}, true, true)
	c, rec := echotest.Get(t, "/admin/usage/invoice?format=json")
	require.NoError(t, h.UsageInvoice(c))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, *sampleInvoice(), echotest.Decode[usage.Invoice](t, rec))
}

func TestUsageInvoice_DisabledReaderReturnsEmptyInvoice(t *testing.T) {
	h := invoiceHandler(nil, true, true)
	c, rec := echotest.Get(t, "/admin/usage/invoice?format=json&start_date=2026-03-01&end_date=2026-03-31")
	require.NoError(t, h.UsageInvoice(c))
	require.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, `{"start_date":"2026-03-01","end_date":"2026-03-31","currency":"USD","customers":[],
		"total":{"requests":0,"input_tokens":0,"output_tokens":0,"provider_cost":null,"billed_cost":null,"effective_markup_percent":null}}`, rec.Body.String())
}

func TestUsageInvoice_Scope(t *testing.T) {
	tests := []struct {
		name       string
		query      string
		wantStatus int
		wantScope  string
	}{
		{name: "parent inside scope", query: "parent=" + scopeAlpha + "/apps", wantStatus: http.StatusOK, wantScope: scopeAlpha},
		{name: "no parent keeps the scope", wantStatus: http.StatusOK, wantScope: scopeAlpha},
		{name: "parent outside scope", query: "parent=/team", wantStatus: http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reader := &mockUsageReader{}
			h := invoiceHandler(reader, true, true)
			c, rec := scopedRequest(t, http.MethodGet, "/admin/usage/invoice?"+tt.query, "", scopeAlpha)
			require.NoError(t, h.UsageInvoice(c))
			require.Equal(t, tt.wantStatus, rec.Code, rec.Body.String())
			if tt.wantStatus == http.StatusOK {
				assert.Equal(t, tt.wantScope, reader.lastInvoice.UserPath)
			}
		})
	}
}
