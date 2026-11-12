package admin

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/usage"
)

// defaultBenchmarkMonths is the default range: the current month plus the
// six complete months before it.
const defaultBenchmarkMonths = 7

// UsageBenchmark handles GET /admin/usage/benchmark
//
// @Summary      Benchmark the monthly cost per active user of a group
// @Description  Treats each direct child of parent as one user, summing its whole subtree, and returns per calendar month the number of active users (at least one request in the month, response-cache hits included), their total cost and the mean, median (p50), p90 and maximum cost per active user. Percentiles interpolate linearly between closest ranks. A user's cost is the one the invoice export reports for it; unpriced_users counts users whose cost is wholly unknown (counted at 0) and partly_priced_users those counted at a known part of it. Every user is counted; nothing is folded into an "other" bucket. Usage recorded on parent itself is reported as unattributed_cost. Months are read in the time zone of the X-GoModel-Timezone header (UTC without it); the current month is flagged partial and has no change percentages. A range covers at most 12 months. The response holds no user paths unless top_users asks for the highest-cost users of each month.
// @Tags         admin
// @Produce      json
// @Security     BearerAuth
// @Param        X-GoModel-Timezone  header  string  false  "IANA time zone months are read in, e.g. Europe/Warsaw (default UTC)"
// @Param        parent       query     string  false  "Group whose direct children are the users, e.g. /eng (default: the credential's scope root, or /)"
// @Param        months       query     int     false  "Number of months ending at end_month, 1-12 (default 7: six complete months plus the current one); ignored with start_month"
// @Param        start_month  query     string  false  "First month (YYYY-MM, default: months before end_month)"
// @Param        end_month    query     string  false  "Last month (YYYY-MM, default and latest: the current month)"
// @Param        cost_basis   query     string  false  "provider (default) or billed; billed requires pricing.markups_enabled"
// @Param        top_users    query     int     false  "List each month's N highest-cost users (0-50, default 0: aggregates only)"
// @Param        model       query     []string  false  "Filter by exact model names; repeat or comma-separate for several"  collectionFormat(multi)
// @Param        provider    query     []string  false  "Filter by provider names or types; repeat or comma-separate for several"  collectionFormat(multi)
// @Param        label       query     []string  false  "Filter by request labels (exact match); repeat for several"  collectionFormat(multi)
// @Param        endpoint    query     []string  false  "Filter by exact request endpoints, e.g. /v1/chat/completions,/v1/messages; repeat or comma-separate for several"  collectionFormat(multi)
// @Param        operation   query     string  false  "Filter by comma-separated operations, e.g. chat_completions,embeddings"
// @Param        auth_key_id query     []string  false  "Filter by managed auth key IDs; repeat or comma-separate for several"  collectionFormat(multi)
// @Success      200  {object}  usage.Benchmark
// @Failure      400  {object}  core.GatewayError
// @Failure      401  {object}  core.GatewayError
// @Failure      403  {object}  core.GatewayError
// @Failure      409  {object}  core.GatewayError
// @Router       /admin/usage/benchmark [get]
func (h *Handler) UsageBenchmark(c *echo.Context) error {
	baseParams, err := parseUsageParams(c)
	if err != nil {
		return handleError(c, err)
	}
	if parents := c.QueryParams()["parent"]; len(parents) > 1 {
		return handleError(c, core.NewInvalidRequestError("invalid parent: name one group", nil))
	}
	parent, err := scopedUserPath(c, "parent", c.QueryParam("parent"))
	if err != nil {
		return handleError(c, err)
	}
	costBasis, err := usage.NormalizeBenchmarkCostBasis(c.QueryParam("cost_basis"))
	if err != nil {
		return handleError(c, err)
	}
	if costBasis == usage.BenchmarkCostBasisBilled && !h.billing.Enabled {
		return handleError(c, markupsDisabledError(
			"a billed-cost benchmark needs pricing markups: set PRICING_MARKUPS_ENABLED=true (pricing.markups_enabled)"))
	}
	topUsers, err := optionalIntQueryParam(c, "top_users")
	if err != nil {
		return handleError(c, err)
	}
	if topUsers < 0 || topUsers > usage.MaxBenchmarkTopUsers {
		return handleError(c, core.NewInvalidRequestError(fmt.Sprintf("invalid top_users: use 0 to %d", usage.MaxBenchmarkTopUsers), nil))
	}

	months, err := benchmarkMonthsParam(c)
	if err != nil {
		return handleError(c, err)
	}

	_, location := dashboardTimeZone(c)
	now := timeNow().In(location)
	start, end, err := benchmarkMonthRange(c.QueryParam("start_month"), c.QueryParam("end_month"), months, now, location)
	if err != nil {
		return handleError(c, err)
	}
	baseParams.StartDate = start
	baseParams.EndDate = end.AddDate(0, 1, -1)
	params := usage.BenchmarkParams{UsageQueryParams: baseParams, Parent: parent, CostBasis: costBasis, TopUsers: topUsers, Now: now}

	var benchmark *usage.Benchmark
	if h.usageReader != nil {
		if benchmark, err = h.usageReader.GetUsageBenchmark(c.Request().Context(), params); err != nil {
			return handleError(c, err)
		}
	}
	if benchmark == nil {
		benchmark = usage.EmptyBenchmark(params)
	}
	return c.JSON(http.StatusOK, benchmark)
}

// benchmarkMonthsParam returns the months query parameter, or the default
// without it. With start_month the count is not used, so it is not read.
func benchmarkMonthsParam(c *echo.Context) (int, error) {
	raw := strings.TrimSpace(c.QueryParam("months"))
	if raw == "" || strings.TrimSpace(c.QueryParam("start_month")) != "" {
		return defaultBenchmarkMonths, nil
	}
	months, err := strconv.Atoi(raw)
	if err != nil || months < 1 || months > usage.MaxBenchmarkMonths {
		return 0, core.NewInvalidRequestError(fmt.Sprintf("invalid months: use a whole number from 1 to %d", usage.MaxBenchmarkMonths), nil)
	}
	return months, nil
}

// benchmarkMonthRange resolves the first days of the first and last month.
// The last month defaults to, and may not pass, the current one; the first
// defaults to months-1 months before the last.
func benchmarkMonthRange(rawStart, rawEnd string, months int, now time.Time, location *time.Location) (time.Time, time.Time, error) {
	current := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, location)
	end, err := parseBenchmarkMonth("end_month", rawEnd, current, location)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	start, err := parseBenchmarkMonth("start_month", rawStart, end.AddDate(0, -(months-1), 0), location)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	switch {
	case end.After(current):
		return time.Time{}, time.Time{}, core.NewInvalidRequestError("invalid end_month: it is after the current month", nil)
	case start.After(end):
		return time.Time{}, time.Time{}, core.NewInvalidRequestError("invalid start_month: it is after end_month", nil)
	case start.AddDate(0, usage.MaxBenchmarkMonths, 0).Before(end.AddDate(0, 1, 0)):
		return time.Time{}, time.Time{}, core.NewInvalidRequestError(fmt.Sprintf("the benchmark covers at most %d months", usage.MaxBenchmarkMonths), nil)
	}
	return start, end, nil
}

func parseBenchmarkMonth(name, raw string, fallback time.Time, location *time.Location) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback, nil
	}
	month, err := time.ParseInLocation("2006-01", raw, location)
	if err != nil {
		return time.Time{}, core.NewInvalidRequestError("invalid "+name+", expected YYYY-MM", nil)
	}
	return month, nil
}
