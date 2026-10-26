package usage

import (
	"cmp"
	"context"
	"math"
	"slices"
	"strings"

	"github.com/enterpilot/gomodel/internal/core"
)

// InvoiceCurrency is the currency every invoice amount is stated in.
const InvoiceCurrency = "USD"

// InvoiceParams configures a per-customer invoice export.
type InvoiceParams struct {
	UsageQueryParams
	// Parent bills each direct child of this user path as one customer, the
	// way per-child budgets meter them, and keeps the export inside its
	// subtree. Usage recorded on Parent itself stays under Parent. Empty
	// bills every tracked user path as its own customer.
	Parent string
}

// Invoice is billed usage for a period, one block per customer. Requests,
// tokens and provider cost cover provider-served requests; billed cost also
// includes billed response-cache hits, like every other billed view.
type Invoice struct {
	StartDate string            `json:"start_date,omitempty"`
	EndDate   string            `json:"end_date,omitempty"`
	Currency  string            `json:"currency"`
	Parent    string            `json:"parent,omitempty"`
	Customers []InvoiceCustomer `json:"customers"`
	Total     InvoiceAmounts    `json:"total"`
}

// InvoiceCustomer is one customer's line items and subtotal.
type InvoiceCustomer struct {
	Customer string         `json:"customer"`
	Lines    []InvoiceLine  `json:"lines"`
	Subtotal InvoiceAmounts `json:"subtotal"`
}

// InvoiceLine is one customer's usage of one model through one provider.
type InvoiceLine struct {
	Model    string `json:"model"`
	Provider string `json:"provider"`
	InvoiceAmounts
}

// InvoiceAmounts holds the figures of a line, subtotal or total. Costs are
// nil when no row had a known cost. EffectiveMarkupPercent is billed cost
// over provider cost, minus one, in percent; it is nil without a positive
// provider cost.
type InvoiceAmounts struct {
	Requests               int      `json:"requests"`
	InputTokens            int64    `json:"input_tokens"`
	OutputTokens           int64    `json:"output_tokens"`
	ProviderCost           *float64 `json:"provider_cost" extensions:"x-nullable"`
	BilledCost             *float64 `json:"billed_cost" extensions:"x-nullable"`
	EffectiveMarkupPercent *float64 `json:"effective_markup_percent" extensions:"x-nullable"`
}

func (a *InvoiceAmounts) add(other InvoiceAmounts) {
	a.Requests += other.Requests
	a.InputTokens += other.InputTokens
	a.OutputTokens += other.OutputTokens
	a.ProviderCost = addCost(a.ProviderCost, other.ProviderCost)
	a.BilledCost = addCost(a.BilledCost, other.BilledCost)
}

func (a *InvoiceAmounts) setMarkup() {
	a.EffectiveMarkupPercent = nil
	if a.ProviderCost == nil || a.BilledCost == nil || *a.ProviderCost <= 0 {
		return
	}
	// Rounded to 1/10000 of a percent so float noise (19.999999999999996)
	// does not leak into the export.
	markup := math.Round((*a.BilledCost / *a.ProviderCost - 1)*100*1e4) / 1e4
	a.EffectiveMarkupPercent = &markup
}

// invoiceCostPrecision is the step costs are rounded to once every sum and
// markup is computed from the raw values, so float noise (4.1000000000000005)
// does not reach the export.
const invoiceCostPrecision = 1e9

func roundInvoiceCost(cost *float64) *float64 {
	if cost == nil {
		return nil
	}
	return new(math.Round(*cost*invoiceCostPrecision) / invoiceCostPrecision)
}

// roundCosts rounds every cost into new pointers, leaving the storage
// groups the amounts were summed from untouched.
func (inv *Invoice) roundCosts() {
	round := func(a *InvoiceAmounts) {
		a.ProviderCost = roundInvoiceCost(a.ProviderCost)
		a.BilledCost = roundInvoiceCost(a.BilledCost)
	}
	for i := range inv.Customers {
		for j := range inv.Customers[i].Lines {
			round(&inv.Customers[i].Lines[j].InvoiceAmounts)
		}
		round(&inv.Customers[i].Subtotal)
	}
	round(&inv.Total)
}

// readUsageInvoice groups usage by user path and model/provider with the
// breakdown query, so billed cache hits are folded exactly like the
// breakdown folds them, and arranges the groups into an invoice. Unlike the
// breakdown it keeps every group: an invoice may not fold customers away.
func readUsageInvoice(ctx context.Context, params InvoiceParams, query func(context.Context, UsageBreakdownParams) ([]breakdownGroup, error)) (*Invoice, error) {
	parent, err := core.NormalizeUserPath(params.Parent)
	if err != nil {
		return nil, core.NewInvalidRequestError("invalid parent: "+err.Error(), err)
	}
	params.Parent = parent
	// Always uncached: a cache hit's stored cost is the provider cost it
	// avoided, which an invoice must not report as provider cost. Billed
	// cache hits are still added to billed cost below.
	params.CacheMode = CacheModeUncached
	breakdown := UsageBreakdownParams{
		UsageQueryParams: params.UsageQueryParams,
		Rows:             BreakdownUserPath,
		Columns:          breakdownModelProvider,
		Metric:           BreakdownMetricCost,
	}
	// Narrow to the parent's subtree unless the scope is already narrower.
	if parent != "" && (breakdown.UserPath == "" || core.UserPathContains(breakdown.UserPath, parent)) {
		breakdown.UserPath = parent
	}
	groups, err := readBilledBreakdownGroups(ctx, breakdown, query)
	if err != nil {
		return nil, err
	}
	return buildInvoice(groups, params), nil
}

// buildInvoice arranges (user path, model/provider) groups into customers
// ordered by customer, then lines ordered by model and provider.
func buildInvoice(groups []breakdownGroup, params InvoiceParams) *Invoice {
	type lineKey struct{ customer, model, provider string }
	lines := map[lineKey]*InvoiceAmounts{}
	for _, group := range groups {
		model, provider, _ := strings.Cut(group.Column, modelProviderSeparator)
		key := lineKey{customer: invoiceCustomer(params.Parent, group.Row), model: model, provider: provider}
		amounts, ok := lines[key]
		if !ok {
			amounts = &InvoiceAmounts{}
			lines[key] = amounts
		}
		amounts.add(InvoiceAmounts{
			Requests: group.Requests, InputTokens: group.InputTokens, OutputTokens: group.OutputTokens,
			ProviderCost: group.TotalCost, BilledCost: group.BilledCost,
		})
	}

	keys := make([]lineKey, 0, len(lines))
	for key := range lines {
		keys = append(keys, key)
	}
	slices.SortFunc(keys, func(a, b lineKey) int {
		return cmp.Or(cmp.Compare(a.customer, b.customer), cmp.Compare(a.model, b.model), cmp.Compare(a.provider, b.provider))
	})

	invoice := EmptyInvoice(params)
	for _, key := range keys {
		if n := len(invoice.Customers); n == 0 || invoice.Customers[n-1].Customer != key.customer {
			invoice.Customers = append(invoice.Customers, InvoiceCustomer{Customer: key.customer, Lines: []InvoiceLine{}})
		}
		customer := &invoice.Customers[len(invoice.Customers)-1]
		line := InvoiceLine{Model: key.model, Provider: key.provider, InvoiceAmounts: *lines[key]}
		line.setMarkup()
		customer.Lines = append(customer.Lines, line)
		customer.Subtotal.add(line.InvoiceAmounts)
	}
	for i := range invoice.Customers {
		invoice.Customers[i].Subtotal.setMarkup()
		invoice.Total.add(invoice.Customers[i].Subtotal)
	}
	invoice.Total.setMarkup()
	invoice.roundCosts()
	return invoice
}

// EmptyInvoice returns an invoice for params' period without customers.
func EmptyInvoice(params InvoiceParams) *Invoice {
	invoice := &Invoice{Currency: InvoiceCurrency, Parent: params.Parent, Customers: []InvoiceCustomer{}}
	if !params.StartDate.IsZero() {
		invoice.StartDate = params.StartDate.Format("2006-01-02")
	}
	if !params.EndDate.IsZero() {
		invoice.EndDate = params.EndDate.Format("2006-01-02")
	}
	return invoice
}

// invoiceCustomer names the customer billed for usage on userPath: the
// direct child of parent it falls under, or the path itself without a
// parent or outside the parent's children.
func invoiceCustomer(parent, userPath string) string {
	if parent != "" {
		if child, ok := core.UserPathChild(parent, userPath); ok {
			return child
		}
	}
	return userPath
}
