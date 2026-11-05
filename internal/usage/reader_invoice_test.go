package usage

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/storage/mongotest"
	"github.com/enterpilot/gomodel/internal/storage/sqlx"
	"github.com/enterpilot/gomodel/internal/storage/sqlx/sqlxtest"
)

// invoiceFixture: customers under /customers, usage on /customers itself, an
// unpriced request, a billed response-cache hit and usage outside the parent.
func invoiceFixture() []*UsageEntry {
	ts := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	entry := func(id, model, provider, providerName, userPath string, cost, billed *float64) *UsageEntry {
		return &UsageEntry{
			ID: "8b3e0000-0000-4000-8000-00000000000" + id, RequestID: "req-inv-" + id, ProviderID: "p-inv-" + id,
			Timestamp: ts, Model: model, Provider: provider, ProviderName: providerName,
			Endpoint: "/v1/chat/completions", UserPath: userPath,
			InputTokens: 100, OutputTokens: 10, TotalTokens: 110, TotalCost: cost, BilledCost: billed,
		}
	}
	hit := entry("5", "gpt-5", "openai", "primary", "/customers/acme/app1", new(9.0), new(0.5))
	hit.CacheType = CacheTypeExact
	return []*UsageEntry{
		entry("1", "gpt-5", "openai", "primary", "/customers/acme/app1", new(1.0), new(1.2)),
		entry("2", "gpt-5", "openai", "primary", "/customers/acme/app2", new(2.0), new(2.4)),
		entry("3", "claude", "anthropic", "", "/customers/acme", nil, nil),
		entry("4", "claude", "anthropic", "", "/customers/globex", new(3.0), new(3.3)),
		hit,
		entry("6", "gemini", "gemini", "", "/internal", new(4.0), new(4.0)),
		entry("7", "gpt-5", "openai", "primary", "/customers", new(1.0), new(1.0)),
	}
}

func TestReadersUsageInvoice(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		store, err := NewSQLStore(context.Background(), db, 0)
		require.NoError(t, err)
		reader, err := NewSQLReader(db)
		require.NoError(t, err)
		assertInvoiceReads(t, store, reader)
	})
	t.Run("mongodb", func(t *testing.T) {
		mongotest.Run(t, func(t *testing.T, db *mongo.Database) {
			store, reader := newMongoUsageStore(t, db, 0)
			assertInvoiceReads(t, store, reader)
		})
	})
}

// invoiceRow flattens one invoice line, or a subtotal (empty model), with
// costs rounded so float sums compare exactly.
type invoiceRow struct {
	Customer, Model, Provider string
	Requests                  int
	InputTokens               int64
	ProviderCost, BilledCost  *float64
	Markup                    *float64
}

func flattenInvoice(invoice *Invoice) []invoiceRow {
	round := func(v *float64) *float64 {
		if v == nil {
			return nil
		}
		r := math.Round(*v*1e6) / 1e6
		return &r
	}
	row := func(customer, model, provider string, a InvoiceAmounts) invoiceRow {
		return invoiceRow{customer, model, provider, a.Requests, a.InputTokens, round(a.ProviderCost), round(a.BilledCost), round(a.EffectiveMarkupPercent)}
	}
	var rows []invoiceRow
	for _, customer := range invoice.Customers {
		for _, line := range customer.Lines {
			rows = append(rows, row(customer.Customer, line.Model, line.Provider, line.InvoiceAmounts))
		}
		rows = append(rows, row(customer.Customer, "", "", customer.Subtotal))
	}
	return append(rows, row("", "", "", invoice.Total))
}

func assertInvoiceReads(t *testing.T, store usageBatchWriter, reader UsageReader) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, store.WriteBatch(ctx, invoiceFixture()))
	period := UsageQueryParams{
		StartDate: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
		EndDate:   time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC),
		TimeZone:  "UTC",
	}

	tests := []struct {
		name   string
		params InvoiceParams
		want   []invoiceRow
	}{
		{
			name:   "one invoice per direct child of the parent",
			params: InvoiceParams{UsageQueryParams: period, Parent: "/customers/"},
			want: []invoiceRow{
				{"/customers", "gpt-5", "primary", 1, 100, new(1.0), new(1.0), new(0.0)},
				{"/customers", "", "", 1, 100, new(1.0), new(1.0), new(0.0)},
				// The unpriced request keeps NULL costs; the cache hit adds
				// only its billed charge.
				{"/customers/acme", "claude", "anthropic", 1, 100, nil, nil, nil},
				{"/customers/acme", "gpt-5", "primary", 2, 200, new(3.0), new(4.1), new(36.6667)},
				{"/customers/acme", "", "", 3, 300, new(3.0), new(4.1), new(36.6667)},
				{"/customers/globex", "claude", "anthropic", 1, 100, new(3.0), new(3.3), new(10.0)},
				{"/customers/globex", "", "", 1, 100, new(3.0), new(3.3), new(10.0)},
				{"", "", "", 5, 500, new(7.0), new(8.4), new(20.0)},
			},
		},
		{
			name:   "every user path without a parent",
			params: InvoiceParams{UsageQueryParams: withUserPaths(period, "/customers/acme", "/internal")},
			want: []invoiceRow{
				{"/customers/acme", "claude", "anthropic", 1, 100, nil, nil, nil},
				{"/customers/acme", "", "", 1, 100, nil, nil, nil},
				{"/customers/acme/app1", "gpt-5", "primary", 1, 100, new(1.0), new(1.7), new(70.0)},
				{"/customers/acme/app1", "", "", 1, 100, new(1.0), new(1.7), new(70.0)},
				{"/customers/acme/app2", "gpt-5", "primary", 1, 100, new(2.0), new(2.4), new(20.0)},
				{"/customers/acme/app2", "", "", 1, 100, new(2.0), new(2.4), new(20.0)},
				{"/internal", "gemini", "gemini", 1, 100, new(4.0), new(4.0), new(0.0)},
				{"/internal", "", "", 1, 100, new(4.0), new(4.0), new(0.0)},
				{"", "", "", 4, 400, new(7.0), new(8.1), new(15.7143)},
			},
		},
		{
			name:   "cache mode is ignored: hits add billed cost only",
			params: InvoiceParams{UsageQueryParams: withCacheMode(period, CacheModeAll), Parent: "/customers/acme"},
			want: []invoiceRow{
				{"/customers/acme", "claude", "anthropic", 1, 100, nil, nil, nil},
				{"/customers/acme", "", "", 1, 100, nil, nil, nil},
				{"/customers/acme/app1", "gpt-5", "primary", 1, 100, new(1.0), new(1.7), new(70.0)},
				{"/customers/acme/app1", "", "", 1, 100, new(1.0), new(1.7), new(70.0)},
				{"/customers/acme/app2", "gpt-5", "primary", 1, 100, new(2.0), new(2.4), new(20.0)},
				{"/customers/acme/app2", "", "", 1, 100, new(2.0), new(2.4), new(20.0)},
				{"", "", "", 3, 300, new(3.0), new(4.1), new(36.6667)},
			},
		},
		{
			name:   "a scope inside the parent keeps the scope",
			params: InvoiceParams{UsageQueryParams: withScope(period, "/customers/globex"), Parent: "/customers"},
			want: []invoiceRow{
				{"/customers/globex", "claude", "anthropic", 1, 100, new(3.0), new(3.3), new(10.0)},
				{"/customers/globex", "", "", 1, 100, new(3.0), new(3.3), new(10.0)},
				{"", "", "", 1, 100, new(3.0), new(3.3), new(10.0)},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			invoice, err := reader.GetUsageInvoice(ctx, tt.params)
			require.NoError(t, err)
			require.NotNil(t, invoice)
			assert.Equal(t, "2026-03-01", invoice.StartDate)
			assert.Equal(t, "2026-03-31", invoice.EndDate)
			assert.Equal(t, InvoiceCurrency, invoice.Currency)
			assert.Equal(t, tt.want, flattenInvoice(invoice))
		})
	}

	t.Run("empty period", func(t *testing.T) {
		params := period
		params.StartDate = time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
		params.EndDate = params.StartDate
		invoice, err := reader.GetUsageInvoice(ctx, InvoiceParams{UsageQueryParams: params})
		require.NoError(t, err)
		assert.Empty(t, invoice.Customers)
		assert.NotNil(t, invoice.Customers)
		assert.Nil(t, invoice.Total.BilledCost)
	})

	t.Run("a scope outside the parent bills nothing", func(t *testing.T) {
		invoice, err := reader.GetUsageInvoice(ctx, InvoiceParams{UsageQueryParams: withScope(period, "/internal"), Parent: "/customers"})
		require.NoError(t, err)
		assert.Empty(t, invoice.Customers)
		assert.Equal(t, "/customers", invoice.Parent)
		assert.Nil(t, invoice.Total.BilledCost)
	})

	t.Run("invalid parent", func(t *testing.T) {
		_, err := reader.GetUsageInvoice(ctx, InvoiceParams{UsageQueryParams: period, Parent: "/a/../b"})
		require.Error(t, err)
	})
}

func withScope(params UsageQueryParams, scope string) UsageQueryParams {
	params.UserPath = scope
	return params
}

func TestInvoiceCustomer(t *testing.T) {
	tests := []struct {
		parent, userPath, want string
	}{
		{"", "/team/alice/laptop", "/team/alice/laptop"},
		{"/team", "/team/alice/laptop", "/team/alice"},
		{"/team", "/team", "/team"},
		{"/team", "/ops", "/ops"},
		{"/", "/team/alice", "/team"},
		{"/", "/", "/"},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, invoiceCustomer(tt.parent, tt.userPath), "parent %q, path %q", tt.parent, tt.userPath)
	}
}

func TestBuildInvoiceRoundsSummedCosts(t *testing.T) {
	billed := []float64{0.1, 0.2}
	groups := make([]breakdownGroup, 0, len(billed))
	for _, cost := range billed {
		groups = append(groups, breakdownGroup{
			Row: "/acme", Column: "gpt-5" + modelProviderSeparator + "openai",
			Requests: 1, TotalCost: new(cost / 1.2), BilledCost: new(cost),
		})
	}
	lines := invoiceLines{amounts: map[invoiceLineKey]*InvoiceAmounts{}}
	for _, group := range groups {
		require.NoError(t, lines.add(group))
	}
	invoice := lines.invoice(InvoiceParams{})
	require.Len(t, invoice.Customers, 1)
	line := invoice.Customers[0].Lines[0]
	assert.Equal(t, new(0.3), line.BilledCost, "no float noise (0.30000000000000004) reaches the export")
	assert.Equal(t, new(0.3), invoice.Total.BilledCost)
	assert.Equal(t, new(20.0), invoice.Total.EffectiveMarkupPercent)
	assert.Equal(t, 0.1, *groups[0].BilledCost, "storage groups stay untouched")
}

// setPackageInt lowers a package cap for one test.
func setPackageInt(t *testing.T, target *int, value int) {
	t.Helper()
	previous := *target
	*target = value
	t.Cleanup(func() { *target = previous })
}

// endUserFixture: three customers with four end-user paths each, two models
// per path, plus one billed cache hit: 24 storage groups that collapse into 6
// invoice lines.
func endUserFixture() []*UsageEntry {
	ts := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	var entries []*UsageEntry
	n := 0
	for _, customer := range []string{"acme", "globex", "initech"} {
		for user := range 4 {
			for _, model := range []string{"gpt-5", "claude"} {
				n++
				entries = append(entries, &UsageEntry{
					ID: fmt.Sprintf("9c4f0000-0000-4000-8000-%012d", n), RequestID: fmt.Sprintf("req-eu-%d", n), ProviderID: fmt.Sprintf("p-eu-%d", n),
					Timestamp: ts, Model: model, Provider: "openai", ProviderName: "primary", Endpoint: "/v1/chat/completions",
					UserPath:    fmt.Sprintf("/customers/%s/user-%d", customer, user),
					InputTokens: 10, OutputTokens: 1, TotalTokens: 11, TotalCost: new(1.0), BilledCost: new(1.5),
				})
			}
		}
	}
	hit := *entries[0]
	hit.ID, hit.RequestID, hit.ProviderID = "9c4f0000-0000-4000-8000-999999999999", "req-eu-hit", "p-eu-hit"
	hit.CacheType, hit.BilledCost = CacheTypeExact, new(0.25)
	return append(entries, &hit)
}

func TestReadersUsageInvoiceStreamsEndUserPaths(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		store, err := NewSQLStore(context.Background(), db, 0)
		require.NoError(t, err)
		reader, err := NewSQLReader(db)
		require.NoError(t, err)
		assertInvoiceStreamsEndUserPaths(t, store, reader)
	})
	t.Run("mongodb", func(t *testing.T) {
		mongotest.Run(t, func(t *testing.T, db *mongo.Database) {
			store, reader := newMongoUsageStore(t, db, 0)
			assertInvoiceStreamsEndUserPaths(t, store, reader)
		})
	})
}

func assertInvoiceStreamsEndUserPaths(t *testing.T, store usageBatchWriter, reader UsageReader) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, store.WriteBatch(ctx, endUserFixture()))
	period := UsageQueryParams{
		StartDate: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
		EndDate:   time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC),
		TimeZone:  "UTC",
	}
	// Far fewer groups allowed than the 24 the fixture has.
	setPackageInt(t, &maxBreakdownGroups, 5)

	t.Run("the breakdown keeps its group cap", func(t *testing.T) {
		_, err := reader.GetUsageBreakdown(ctx, UsageBreakdownParams{UsageQueryParams: period, Rows: BreakdownUserPath, Columns: BreakdownModel})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "usage breakdown has too many groups")
	})

	t.Run("the invoice streams past it", func(t *testing.T) {
		invoice, err := reader.GetUsageInvoice(ctx, InvoiceParams{UsageQueryParams: period, Parent: "/customers"})
		require.NoError(t, err)
		var want []invoiceRow
		for _, customer := range []string{"/customers/acme", "/customers/globex", "/customers/initech"} {
			billed := 6.0
			if customer == "/customers/acme" {
				billed = 6.25 // the cache hit adds its billed charge only
			}
			want = append(want,
				invoiceRow{customer, "claude", "primary", 4, 40, new(4.0), new(6.0), new(50.0)},
				invoiceRow{customer, "gpt-5", "primary", 4, 40, new(4.0), new(billed), new((billed/4 - 1) * 100)},
				invoiceRow{customer, "", "", 8, 80, new(8.0), new(6 + billed), new(((6+billed)/8 - 1) * 100)},
			)
		}
		want = append(want, invoiceRow{"", "", "", 24, 240, new(24.0), new(36.25), new(51.0417)})
		assert.Equal(t, want, flattenInvoice(invoice))
	})

	t.Run("too many invoice lines", func(t *testing.T) {
		setPackageInt(t, &maxInvoiceLines, 5)
		_, err := reader.GetUsageInvoice(ctx, InvoiceParams{UsageQueryParams: period, Parent: "/customers"})
		require.Error(t, err)
		var gatewayErr *core.GatewayError
		require.ErrorAs(t, err, &gatewayErr)
		assert.Equal(t, http.StatusBadRequest, gatewayErr.HTTPStatusCode())
		assert.Contains(t, gatewayErr.Message, "invoice has more than 5 lines")
		assert.Contains(t, gatewayErr.Message, "parent")
	})
}
