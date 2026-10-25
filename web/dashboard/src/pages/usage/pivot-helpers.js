// Breakdown (pivot) table helpers: turn a /admin/usage/breakdown response
// into a grid with totals, label its keys, format values, export CSV and
// build the stacked bar chart. Pure functions for node:test.

import { formatTokensShort } from "../../lib/utils/format.js";
import { barColors, chartTickFont, chartTooltip } from "../../lib/utils/chartTheme.js";
import * as m from "../../lib/paraglide/messages.js";

// pivotCellValue reads the chosen metric from a breakdown cell; unpriced
// cost counts as zero.
export function pivotCellValue(cell, metric) {
  if (!cell) return 0;
  if (metric === "tokens") return cell.total_tokens || 0;
  if (metric === "requests") return cell.requests || 0;
  return cell.total_cost || 0;
}

// pivotTable lays the cells out as grid[row][column] (null where a pair had
// no traffic) with row, column and grand totals of the metric.
export function pivotTable(data, metric) {
  const rows = (data && data.rows) || [];
  const columns = (data && data.columns) || [];
  const grid = rows.map(() => columns.map(() => null));
  const rowTotals = rows.map(() => 0);
  const columnTotals = columns.map(() => 0);
  let total = 0;
  for (const cell of (data && data.cells) || []) {
    if (!grid[cell.row] || cell.column >= columns.length) continue;
    const value = pivotCellValue(cell, metric);
    grid[cell.row][cell.column] = value;
    rowTotals[cell.row] += value;
    columnTotals[cell.column] += value;
    total += value;
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

// formatPivotValue renders a metric value for the table.
export function formatPivotValue(value, metric) {
  if (value === null || value === undefined) return "–";
  if (metric === "cost") return "$" + Number(value).toFixed(value !== 0 && Math.abs(value) < 0.01 ? 4 : 2);
  return Number(value).toLocaleString();
}

// pivotCsv renders the table, totals included, as RFC 4180 CSV. Values are
// raw numbers so spreadsheets can sum them.
export function pivotCsv(table, { cornerLabel, rowLabels, columnLabels }) {
  const lines = [[cornerLabel, ...columnLabels, m.usage_column_total()]];
  table.grid.forEach((cells, i) => {
    lines.push([rowLabels[i], ...cells.map((value) => (value === null ? "" : value)), table.rowTotals[i]]);
  });
  lines.push([m.usage_column_total(), ...table.columnTotals, table.total]);
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
  const fmtShort = (v) => (metric === "cost" ? "$" + Number(v).toFixed(2) : formatTokensShort(v));
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
          data: table.rowTotals,
          backgroundColor: table.rows.map((_, i) => palette[i % palette.length]),
          borderWidth: 0,
          maxBarThickness: 48,
        },
      ];
  return {
    type: "bar",
    data: { labels: hasColumns ? columnLabels : rowLabels, datasets },
    options: {
      responsive: true,
      maintainAspectRatio: false,
      animation: { duration: 0 },
      scales: {
        x: {
          stacked: true,
          grid: { display: false },
          border: { display: false },
          ticks: { color: colors.text, font: chartTickFont() },
        },
        y: {
          stacked: true,
          beginAtZero: true,
          grid: { color: colors.grid },
          border: { display: false },
          ticks: { color: colors.text, font: chartTickFont(), callback: fmtShort },
        },
      },
      plugins: {
        legend: { display: hasColumns, labels: { color: colors.text, font: { size: 12 } } },
        tooltip: chartTooltip(colors, {
          label: (c) => c.dataset.label + ": " + formatPivotValue(c.parsed.y, metric),
        }),
      },
    },
  };
}
