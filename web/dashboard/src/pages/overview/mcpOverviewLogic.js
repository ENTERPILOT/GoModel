// MCP servers overview-card helpers (only the small slice the overview card
// needs). The card is hidden entirely when the feature is unavailable or no
// servers are configured.

export function mcpServerStatus(server) {
  return String((server && server.status) || "").trim() || "connecting";
}

function mcpOverviewTotal(servers) {
  return (servers || []).length;
}

function mcpOverviewConnectedCount(servers) {
  return (servers || []).filter((server) => mcpServerStatus(server) === "connected")
    .length;
}

function mcpOverviewDegradedCount(servers) {
  return (servers || []).filter(
    (server) =>
      server && server.enabled !== false && mcpServerStatus(server) === "degraded",
  ).length;
}

export function mcpOverviewVisible(available, servers) {
  return !!available && mcpOverviewTotal(servers) > 0;
}

export function mcpOverviewRatioText(servers) {
  return (
    String(mcpOverviewConnectedCount(servers)) +
    "/" +
    String(mcpOverviewTotal(servers))
  );
}

// Mirrors providerStatusSummaryClass: the card gets a warning accent while
// any enabled server is degraded.
export function mcpOverviewSummaryClass(servers) {
  return mcpOverviewDegradedCount(servers) > 0 ? "is-degraded" : "is-healthy";
}

export function mcpOverviewSummaryText(servers) {
  const degraded = mcpOverviewDegradedCount(servers);
  if (degraded > 0) {
    return (
      String(degraded) +
      " server" +
      (degraded === 1 ? "" : "s") +
      " need" +
      (degraded === 1 ? "s" : "") +
      " attention"
    );
  }
  const total = mcpOverviewTotal(servers);
  const connected = mcpOverviewConnectedCount(servers);
  if (total > 0 && connected === total) {
    return "All MCP servers connected";
  }
  return (
    String(connected) +
    " of " +
    String(total) +
    " server" +
    (total === 1 ? "" : "s") +
    " connected"
  );
}
