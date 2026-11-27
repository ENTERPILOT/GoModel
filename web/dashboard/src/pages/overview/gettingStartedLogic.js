// First-run checklist on the overview: the steps from an empty gateway to a
// first request, folded from data the page already loads (provider status
// and the activity calendar). Pure functions; GettingStarted.svelte owns the
// rendering and the dismissed flag.

const GETTING_STARTED_DISMISSED_STORAGE_KEY =
  "gomodel_getting_started_dismissed";

// A provider's models count once its inventory is current, the same rule the
// provider card uses for "Models Available".
function providerHasModels(provider) {
  const runtime = provider && provider.runtime;
  if (!runtime || runtime.inventory_stale) return false;
  return Number(runtime.discovered_model_count || 0) > 0;
}

function hasRequests(dailyUsage) {
  const days = Array.isArray(dailyUsage) ? dailyUsage : [];
  return days.some((day) => Number((day && day.requests) || 0) > 0);
}

// gettingStartedSteps returns the ordered checklist as { id, page, done }
// items; `page` is the route that completes the step. The request step needs
// usage tracking to be detectable, so a gateway with tracking off has none.
export function gettingStartedSteps({ providers, dailyUsage, usageTracked }) {
  const list = Array.isArray(providers) ? providers : [];
  const steps = [
    { id: "provider", page: "providers-config", done: list.length > 0 },
    { id: "models", page: "models", done: list.some(providerHasModels) },
  ];
  if (usageTracked) {
    steps.push({
      id: "request",
      page: "playground",
      done: hasRequests(dailyUsage),
    });
  }
  return steps;
}

export function gettingStartedProgress(steps) {
  const list = Array.isArray(steps) ? steps : [];
  return {
    done: list.filter((step) => step.done).length,
    total: list.length,
  };
}

// gettingStartedVisible: the checklist shows only while a step is open, so a
// gateway already in use never sees it. `loaded` must mean every input
// arrived successfully: a failed request reads as "nothing configured" and
// would otherwise put the checklist on a working gateway.
export function gettingStartedVisible({ loaded, dismissed, demo, steps }) {
  if (!loaded || dismissed || demo) return false;
  const progress = gettingStartedProgress(steps);
  return progress.done < progress.total;
}

export function loadGettingStartedDismissed(storage) {
  if (!storage) return false;
  try {
    return storage.getItem(GETTING_STARTED_DISMISSED_STORAGE_KEY) === "true";
  } catch {
    return false;
  }
}

export function saveGettingStartedDismissed(storage) {
  if (!storage) return;
  try {
    storage.setItem(GETTING_STARTED_DISMISSED_STORAGE_KEY, "true");
  } catch {
    // Ignore storage failures; the checklist stays hidden for this session.
  }
}
