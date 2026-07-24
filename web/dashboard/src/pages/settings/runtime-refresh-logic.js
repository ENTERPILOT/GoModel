// Pure runtime-refresh report logic.

function runtimeRefreshStatus(report) {
  return String((report && report.status) || "ok").toLowerCase();
}

export function runtimeRefreshSummary(report) {
  if (!report || typeof report !== "object") {
    return "Runtime refresh completed.";
  }
  const modelCount = Number(report.model_count || 0);
  const providerCount = Number(report.provider_count || 0);
  const status = runtimeRefreshStatus(report);
  const prefix =
    status === "ok"
      ? "Runtime refreshed."
      : status === "partial"
        ? "Runtime refresh completed with warnings."
        : "Runtime refresh failed.";
  return (
    prefix +
    " " +
    modelCount +
    " model" +
    (modelCount === 1 ? "" : "s") +
    " across " +
    providerCount +
    " provider" +
    (providerCount === 1 ? "" : "s") +
    "."
  );
}

export function runtimeRefreshSucceeded(report) {
  return Boolean(report) && runtimeRefreshStatus(report) === "ok";
}

export function runtimeRefreshWarnings(report) {
  return Boolean(report) && runtimeRefreshStatus(report) !== "ok";
}

export function runtimeRefreshSteps(report) {
  const steps = report && report.steps;
  return Array.isArray(steps) ? steps : [];
}

export function runtimeRefreshStepLabel(step) {
  const name = String((step && step.name) || "").replace(/_/g, " ");
  const status = String((step && step.status) || "").trim();
  const detail = String((step && (step.error || step.message)) || "").trim();
  if (!name) return detail || status || "";
  if (!detail) return name + ": " + status;
  return name + ": " + status + " - " + detail;
}
