package admin

import (
	"bytes"
	"encoding/csv"
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/usage"
)

// invoiceCSVHeader is the stable column order of the CSV invoice export.
var invoiceCSVHeader = []string{
	"row_type", "period_start", "period_end", "currency", "customer", "model", "provider",
	"requests", "input_tokens", "output_tokens", "provider_cost", "billed_cost", "effective_markup_percent",
}

// UsageInvoice handles GET /admin/usage/invoice
//
// @Summary      Export billed usage as per-customer invoices
// @Description  Returns billed usage for the period, one block of model/provider line items per customer user path, with subtotals and a grand total. Amounts are USD. With parent, each direct child of that user path is one customer (usage on the parent itself stays under the parent); without it every user path is its own customer. Requests, tokens and provider cost cover provider-served requests; billed cost also includes billed response-cache hits. CSV rows carry row_type item, subtotal or total. Requires a GoModel Pro license with the billing entitlement and pricing.markups_enabled.
// @Tags         admin
// @Produce      text/csv
// @Produce      json
// @Security     BearerAuth
// @Param        format      query     string  false  "Export format: csv (default, as an attachment) or json"
// @Param        parent      query     string  false  "Bill each direct child of this user path as one customer, e.g. /customers"
// @Param        days        query     int     false  "Number of days (default 30)"
// @Param        start_date  query     string  false  "Start date (YYYY-MM-DD)"
// @Param        end_date    query     string  false  "End date (YYYY-MM-DD)"
// @Param        model       query     []string  false  "Filter by exact model names; repeat or comma-separate for several"  collectionFormat(multi)
// @Param        provider    query     []string  false  "Filter by provider names or types; repeat or comma-separate for several"  collectionFormat(multi)
// @Param        label       query     []string  false  "Filter by request labels (exact match); repeat for several"  collectionFormat(multi)
// @Param        endpoint    query     []string  false  "Filter by exact request endpoints, e.g. /v1/chat/completions,/v1/messages; repeat or comma-separate for several"  collectionFormat(multi)
// @Param        operation   query     string  false  "Filter by comma-separated operations, e.g. chat_completions,embeddings"
// @Param        session_id  query     string  false  "Filter by exact detected session ID"
// @Param        auth_key_id query     []string  false  "Filter by managed auth key IDs; repeat or comma-separate for several"  collectionFormat(multi)
// @Param        user_path   query     []string  false  "Filter by tracked user path subtrees; repeat for several"  collectionFormat(multi)
// @Success      200  {object}  usage.Invoice
// @Failure      400  {object}  core.GatewayError
// @Failure      401  {object}  core.GatewayError
// @Failure      403  {object}  core.GatewayError
// @Failure      409  {object}  core.GatewayError
// @Router       /admin/usage/invoice [get]
func (h *Handler) UsageInvoice(c *echo.Context) error {
	if !h.invoiceExport {
		return handleError(c, invoiceExportUnavailableError())
	}
	if !h.billing.Enabled {
		return handleError(c, core.NewInvalidRequestErrorWithStatus(
			http.StatusConflict,
			"invoice export needs billed cost: enable markups with PRICING_MARKUPS_ENABLED=true (pricing.markups_enabled)",
			nil,
		).WithCode("markups_disabled"))
	}
	format := strings.ToLower(strings.TrimSpace(c.QueryParam("format")))
	if format != "" && format != "csv" && format != "json" {
		return handleError(c, core.NewInvalidRequestError("invalid format: use csv or json", nil))
	}
	baseParams, err := parseUsageParams(c)
	if err != nil {
		return handleError(c, err)
	}
	parent, err := normalizeUserPathQueryParam("parent", c.QueryParam("parent"))
	if err != nil {
		return handleError(c, err)
	}
	if parent != "" && !requestScope(c).Allows(parent) {
		return handleError(c, userPathOutOfScopeError("parent"))
	}
	params := usage.InvoiceParams{UsageQueryParams: baseParams, Parent: parent}

	var invoice *usage.Invoice
	if h.usageReader != nil {
		if invoice, err = h.usageReader.GetUsageInvoice(c.Request().Context(), params); err != nil {
			return handleError(c, err)
		}
	}
	if invoice == nil {
		invoice = usage.EmptyInvoice(params)
	}

	if format == "json" {
		return c.JSON(http.StatusOK, invoice)
	}
	body, err := invoiceCSV(invoice)
	if err != nil {
		return handleError(c, err)
	}
	c.Response().Header().Set(echo.HeaderContentDisposition, `attachment; filename="`+invoiceFilename(invoice)+`"`)
	return c.Blob(http.StatusOK, "text/csv; charset=utf-8", body)
}

// invoiceCSV renders one row per line item, then each customer's subtotal,
// then the grand total, told apart by row_type.
func invoiceCSV(invoice *usage.Invoice) ([]byte, error) {
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	row := func(rowType, customer, model, provider string, a usage.InvoiceAmounts) []string {
		return []string{
			rowType, invoice.StartDate, invoice.EndDate, invoice.Currency,
			csvText(customer), csvText(model), csvText(provider),
			strconv.Itoa(a.Requests), strconv.FormatInt(a.InputTokens, 10), strconv.FormatInt(a.OutputTokens, 10),
			csvDecimal(a.ProviderCost), csvDecimal(a.BilledCost), csvDecimal(a.EffectiveMarkupPercent),
		}
	}
	records := [][]string{invoiceCSVHeader}
	for _, customer := range invoice.Customers {
		for _, line := range customer.Lines {
			records = append(records, row("item", customer.Customer, line.Model, line.Provider, line.InvoiceAmounts))
		}
		records = append(records, row("subtotal", customer.Customer, "", "", customer.Subtotal))
	}
	records = append(records, row("total", "", "", "", invoice.Total))
	if err := w.WriteAll(records); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// csvDecimal prints an amount as a plain decimal (never an exponent),
// rounded to 1e-9 so float sums print cleanly; unknown amounts are empty.
func csvDecimal(value *float64) string {
	if value == nil {
		return ""
	}
	return strconv.FormatFloat(math.Round(*value*1e9)/1e9, 'f', -1, 64)
}

// csvText keeps a spreadsheet from reading a model or provider name as a
// formula: values starting with a formula character get a leading quote.
func csvText(value string) string {
	if value != "" && strings.ContainsRune("=+-@\t\r", rune(value[0])) {
		return "'" + value
	}
	return value
}

func invoiceFilename(invoice *usage.Invoice) string {
	if invoice.StartDate == "" || invoice.EndDate == "" {
		return "gomodel-invoice.csv"
	}
	return "gomodel-invoice-" + invoice.StartDate + "-to-" + invoice.EndDate + ".csv"
}
