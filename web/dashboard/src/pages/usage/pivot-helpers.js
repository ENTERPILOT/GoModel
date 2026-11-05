// Breakdown (pivot) table helpers: turn a /admin/usage/breakdown response
// into a grid with totals, label its keys, format values, export CSV and
// build the stacked bar chart. Pure functions for node:test.

import { formatCost, formatCostShort, formatTokensShort } from "../../lib/utils/format.js";
import { barColors } from "../../lib/utils/chartTheme.js";
import { stackedBarOptions } from "./usage-chart-config.js";
import * as m from "../../lib/paraglide/messages.js";

// pivotCellValue reads the chosen metric from a breakdown cell. Cost stays
// null when no request in the cell had pricing: unknown is not free.
export function pivotCellValue(cell, metric) {
  if (metric === "tokens") return cell.total_tokens || 0;
  if (metric === "requests") return cell.requests || 0;
  return cell.total_cost ?? null;
}

// addPivotValue sums like SQL SUM: undefined (no traffic) adds nothing, and
// a total is null (unknown) only when every value in it is null.
function addPivotValue(total, value) {
  if (value === undefined) return total;
  if (value === null) return total === undefined ? null : total;
  if (total === undefined || total === null) return value;
  return total + value;
}

// pivotTable lays the cells out as grid[row][column] with row, column and
// grand totals of the metric. A pair without traffic is undefined; an
// unpriced cost is null.
export function pivotTable(data, metric) {
  const rows = (data && data.rows) || [];
  const columns = (data && data.columns) || [];
  const grid = rows.map(() => columns.map(() => undefined));
  const rowTotals = rows.map(() => undefined);
  const columnTotals = columns.map(() => undefined);
  let total;
  for (const cell of (data && data.cells) || []) {
    if (!grid[cell.row] || cell.column >= columns.length) continue;
    const value = pivotCellValue(cell, metric);
    grid[cell.row][cell.column] = value;
    rowTotals[cell.row] = addPivotValue(rowTotals[cell.row], value);
    columnTotals[cell.column] = addPivotValue(columnTotals[cell.column], value);
    total = addPivotValue(total, value);
  }
  return { rows, columns, grid, rowTotals, columnTotals, total };
}

// pivotKeyLabel names a row or column key for display: the "other" bucket
// with its size, blank keys as "(none)", API keys by name.
export function pivotKeyLabel(dimension, key, authKeyNames) {
  if (!key) return "";
  if (key.other) return m.usage_pivot_other({ count: key.folded || 0 });
  if (!dimension) return m.usage_column_total();
  if (key.key === "") return dimension === "user_path" ? "/" : m.usage_pivot_none();
  if (dimension === "auth_key") return (authKeyNames && authKeyNames[key.key]) || key.key;
  return key.key;
}

// formatPivotValue renders a metric value for the table: "–" where there
// was no traffic, "---" for an unknown (unpriced) cost.
export function formatPivotValue(value, metric) {
  if (value === undefined) return "–";
  if (metric === "cost") return formatCost(value);
  if (value === null) return "---";
  return Number(value).toLocaleString();
}

// pivotCsv renders the table, totals included, as RFC 4180 CSV. Values are
// raw numbers so spreadsheets can sum them: no traffic is 0 and an unknown
// cost is left empty. Without a column dimension only the totals column is
// written.
export function pivotCsv(table, { cornerLabel, rowLabels, columnLabels, hasColumns }) {
  const field = (value) => (value === undefined ? 0 : value === null ? "" : value);
  const lines = [[cornerLabel, ...(hasColumns ? columnLabels : []), m.usage_column_total()]];
  table.grid.forEach((cells, i) => {
    lines.push([rowLabels[i], ...(hasColumns ? cells.map(field) : []), field(table.rowTotals[i])]);
  });
  lines.push([m.usage_column_total(), ...(hasColumns ? table.columnTotals.map(field) : []), field(table.total)]);
  return lines.map((line) => line.map(csvField).join(",")).join("\r\n") + "\r\n";
}

function csvField(value) {
  let text = String(value ?? "");
  // A leading formula character would run as a formula when the file is
  // opened in a spreadsheet; quote it as text instead.
  if (/^[=+\-@]/.test(text) && Number.isNaN(Number(text))) text = "'" + text;
  return /[",\r\n]/.test(text) ? '"' + text.replace(/"/g, '""') + '"' : text;
}

// pivotChartConfig builds a stacked bar chart: one bar per column (periods
// read left to right) stacking the rows, or one bar per row when the
// breakdown has no column dimension.
export function pivotChartConfig(colors, table, { rowLabels, columnLabels, metric, hasColumns }) {
  const palette = barColors();
  const fmtShort = (v) => (metric === "cost" ? formatCostShort(v) : formatTokensShort(v));
  const datasets = hasColumns
    ? table.rows.map((_, i) => ({
        label: rowLabels[i],
        data: table.grid[i].map((value) => value || 0),
        backgroundColor: palette[i % palette.length],
        borderWidth: 0,
        maxBarThickness: 48,
      }))
    : [
        {
          label: m.usage_column_total(),
          data: table.rowTotals.map((value) => value || 0),
          backgroundColor: table.rows.map((_, i) => palette[i % palette.length]),
          borderWidth: 0,
          maxBarThickness: 48,
        },
      ];
  return {
    type: "bar",
    data: { labels: hasColumns ? columnLabels : rowLabels, datasets },
    options: stackedBarOptions(colors, {
      formatValue: fmtShort,
      legend: hasColumns,
      tooltip: { label: (c) => c.dataset.label + ": " + formatPivotValue(c.parsed.y, metric) },
    }),
  };
}
