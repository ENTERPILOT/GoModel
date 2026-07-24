// Shared overview chart styling, so the line (Daily Token Usage), bar (Live
// Token Throughput), and audit charts read as one family.

import { formatTokensShort } from "../../lib/utils/format.js";

export function chartTickFont() {
  return { size: 11, family: "'SF Mono', Menlo, Consolas, monospace" };
}

export function chartTooltip(colors, callbacks) {
  return {
    backgroundColor: colors.tooltipBg,
    borderColor: colors.tooltipBorder,
    borderWidth: 1,
    titleColor: colors.tooltipText,
    bodyColor: colors.tooltipText,
    callbacks: callbacks,
  };
}

// Y-axis ticks that abbreviate token counts (e.g. 1.2K, 3.4M).
export function tokenAxisTicks(colors) {
  return {
    color: colors.text,
    font: chartTickFont(),
    callback: (v) => formatTokensShort(v),
  };
}

// Categorical palette for the audit latency chart.
export function barColors() {
  return [
    "#c2845a", "#7a9e7e", "#d4a574", "#b8a98e", "#8b9e6b",
    "#7d8a97", "#c47a5a", "#6b8e6b", "#a09486", "#9b7ea4",
    "#c49a6c",
  ];
}

// Resolve a CSS color expression to a canvas-safe rgb() string by letting the
// browser compute it on a throwaway element (handles var()/color-mix(), which
// canvas fillStyle may not accept directly).
export function resolveColor(expr) {
  if (typeof document === "undefined" || !document.body) {
    return expr;
  }
  const probe = document.createElement("span");
  probe.style.display = "none";
  probe.style.color = expr;
  document.body.appendChild(probe);
  const resolved = getComputedStyle(probe).color;
  document.body.removeChild(probe);
  return resolved || expr;
}
