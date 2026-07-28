// Chart color helpers: charts pull their palette from CSS custom properties
// so they follow the active theme, plus the shared Chart.js style fragments
// that keep every chart on the dashboard reading as one family.

export function cssVar(name) {
  return getComputedStyle(document.documentElement)
    .getPropertyValue(name)
    .trim();
}

export function chartColors() {
  return {
    grid: cssVar("--chart-grid"),
    text: cssVar("--chart-text"),
    dayMarker: cssVar("--chart-day-marker"),
    tooltipBg: cssVar("--chart-tooltip-bg"),
    tooltipBorder: cssVar("--chart-tooltip-border"),
    tooltipText: cssVar("--chart-tooltip-text"),
  };
}

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

// Resolve a CSS color expression to a canvas-safe rgb() string by letting the
// browser compute it on a throwaway element (handles var()/color-mix(), which
// canvas fillStyle may not accept directly). Returns the input unchanged
// without a DOM, so config builders stay testable under node:test.
export function resolveCssColor(expr) {
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

// Categorical palette for series and label chips.
const PALETTE = [
  "#c2845a",
  "#7a9e7e",
  "#d4a574",
  "#b8a98e",
  "#8b9e6b",
  "#7d8a97",
  "#c47a5a",
  "#6b8e6b",
  "#a09486",
  "#9b7ea4",
  "#c49a6c",
];

export function barColors() {
  return [...PALETTE];
}

// Deterministic label -> palette color (djb2), so a label keeps one color
// across the usage charts and every chip on the dashboard.
export function labelColor(label) {
  let hash = 5381;
  const text = String(label || "");
  for (let i = 0; i < text.length; i++) {
    hash = ((hash << 5) + hash + text.charCodeAt(i)) | 0;
  }
  return PALETTE[Math.abs(hash) % PALETTE.length];
}

// Inline style string for a usage-label chip (drives --label-color).
export function labelChipStyle(label) {
  return "--label-color: " + labelColor(label);
}
