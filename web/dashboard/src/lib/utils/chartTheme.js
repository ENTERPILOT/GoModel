// Chart color helpers: charts pull their palette from CSS custom properties
// so they follow the active theme.

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
