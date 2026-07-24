// Provider status card helpers.
// Pure functions (state lives in overviewState.svelte.js) so status folding
// and preference persistence are testable with node.

const PROVIDER_STATUS_DETAILS_STORAGE_KEY =
  "gomodel_provider_status_details_expanded";
const PROVIDER_CARD_OVERRIDES_STORAGE_KEY =
  "gomodel_provider_card_expanded_overrides";
export const PROVIDER_STATUS_POLL_MS = 3000;

// Provider types that have a dedicated docs page at
// gomodel.enterpilot.io/docs/providers/. Keyed by provider type; the value is
// the docs slug (identical to the type except opencode_go → opencode-go).
// Types absent here get no help icon — that is the "doc exists" gate.
const PROVIDER_DOCS_BASE_URL = "https://gomodel.enterpilot.io/docs/providers/";
const PROVIDER_DOC_SLUGS = {
  anthropic: "anthropic",
  azure: "azure",
  bailian: "bailian",
  bedrock: "bedrock",
  "bedrock-mantle": "bedrock-mantle",
  deepseek: "deepseek",
  gemini: "gemini",
  opencode_go: "opencode-go",
  oracle: "oracle",
  vertex: "vertex",
  vllm: "vllm",
  xiaomi: "xiaomi",
};

export function emptyProviderStatus() {
  return {
    summary: {
      total: 0,
      healthy: 0,
      degraded: 0,
      unhealthy: 0,
      overall_status: "degraded",
    },
    providers: [],
  };
}

// --- Preferences (details toggle + per-card overrides, localStorage) ---

export function loadProviderPreferences(storage) {
  const prefs = { detailsExpanded: false, cardOverrides: {} };
  try {
    if (storage) {
      const stored = storage.getItem(PROVIDER_STATUS_DETAILS_STORAGE_KEY);
      if (stored === "true" || stored === "false") {
        prefs.detailsExpanded = stored === "true";
      } else {
        storage.setItem(PROVIDER_STATUS_DETAILS_STORAGE_KEY, "false");
      }
      const overrides = JSON.parse(
        storage.getItem(PROVIDER_CARD_OVERRIDES_STORAGE_KEY) || "{}",
      );
      if (
        overrides &&
        typeof overrides === "object" &&
        !Array.isArray(overrides)
      ) {
        prefs.cardOverrides = overrides;
      }
    }
  } catch {
    // Ignore storage failures; details still start collapsed.
  }
  return prefs;
}

export function saveDetailsPreference(storage, expanded) {
  if (!storage) return;
  try {
    storage.setItem(
      PROVIDER_STATUS_DETAILS_STORAGE_KEY,
      expanded ? "true" : "false",
    );
  } catch {
    // Ignore storage failures and keep the in-memory preference active.
  }
}

export function saveCardOverrides(storage, overrides) {
  if (!storage) return;
  try {
    storage.setItem(
      PROVIDER_CARD_OVERRIDES_STORAGE_KEY,
      JSON.stringify(overrides),
    );
  } catch {
    // Ignore storage failures and keep the in-memory state active.
  }
}

// Cards without an override entry follow the section-wide default.
export function providerCardExpanded(overrides, detailsExpanded, provider) {
  const name = provider && provider.name ? String(provider.name) : "";
  if (name && Object.prototype.hasOwnProperty.call(overrides, name)) {
    return overrides[name] === true;
  }
  return detailsExpanded;
}

// --- Summary + card display helpers ---

export function providerStatusSummaryClass(summary) {
  const status = String((summary && summary.overall_status) || "degraded").trim();
  return "is-" + (status || "degraded");
}

export function providerStatusBadgeClass(status) {
  const normalized = String(status || "degraded").trim() || "degraded";
  return "is-" + normalized;
}

export function providerStatusRatioText(summary) {
  const s = summary || {};
  return String(s.healthy || 0) + "/" + String(s.total || 0);
}

export function providerStatusHasIssues(summary) {
  const s = summary || {};
  const total = Number(s.total || 0);
  const healthy = Number(s.healthy || 0);
  return total > 0 && healthy < total;
}

export function providerStatusSummaryText(summary) {
  const s = summary || {};
  const total = Number(s.total || 0);
  const healthy = Number(s.healthy || 0);
  if (total === 0) return "None configured";
  if (healthy === total) return "All healthy";
  if (healthy === 0) return "None healthy";
  const attention = total - healthy;
  return (
    String(attention) + (attention === 1 ? " needs" : " need") + " attention"
  );
}

export function providerStatusNeedsPolling(providers) {
  const list = Array.isArray(providers) ? providers : [];
  return list.some((provider) => {
    const label = String((provider && provider.status_label) || "")
      .trim()
      .toLowerCase();
    return label === "starting";
  });
}

export function providerLastChecked(provider) {
  if (!provider || !provider.runtime) return "";
  const fetchAt = provider.runtime.last_model_fetch_at || "";
  const availabilityAt = provider.runtime.last_availability_check_at || "";
  if (!fetchAt) return availabilityAt;
  if (!availabilityAt) return fetchAt;
  // Whichever check ran most recently; an unparsable timestamp falls back to
  // the model fetch side.
  return Date.parse(availabilityAt) > Date.parse(fetchAt)
    ? availabilityAt
    : fetchAt;
}

export function providerLastCheckedTime(provider, formatTimestamp) {
  const timestamp = providerLastChecked(provider);
  if (!timestamp || typeof formatTimestamp !== "function") {
    return "-";
  }
  const formatted = formatTimestamp(timestamp);
  if (!formatted || formatted === "-") {
    return "-";
  }
  const parts = String(formatted).split(" ");
  return parts.length > 1 ? parts.slice(1).join(" ") : formatted;
}

export function providerLastCheckedTitle(provider, formatTimestamp) {
  const timestamp = providerLastChecked(provider);
  if (!timestamp) {
    return "";
  }
  if (typeof formatTimestamp === "function") {
    return formatTimestamp(timestamp);
  }
  return String(timestamp);
}

export function providerTypeLabel(provider) {
  if (!provider) {
    return "";
  }
  const name = String(provider.name || "").trim();
  const type = String(
    provider.type || (provider.config && provider.config.type) || "",
  ).trim();
  if (!type || type === name) {
    return "";
  }
  return type;
}

// Docs URL for a provider's type, or '' when no provider-specific page exists
// (the help icon is shown only when this is non-empty). Uses the raw type so
// it still resolves when the (type) label is hidden because the provider name
// equals its type.
export function providerDocUrl(provider) {
  const type = String(
    (provider && (provider.type || (provider.config && provider.config.type))) ||
      "",
  )
    .trim()
    .toLowerCase();
  const slug = type ? PROVIDER_DOC_SLUGS[type] : "";
  return slug
    ? PROVIDER_DOCS_BASE_URL + slug + "?utm_source=gomodel_dashboard"
    : "";
}

export function providerRetrySummary(provider) {
  const retry =
    provider && provider.config && provider.config.resilience
      ? provider.config.resilience.retry
      : null;
  if (!retry) return "-";
  return (
    String(retry.max_retries) +
    " retries, " +
    retry.initial_backoff +
    " initial, " +
    retry.max_backoff +
    " max, factor " +
    retry.backoff_factor +
    ", jitter " +
    retry.jitter_factor
  );
}

export function providerCircuitBreakerSummary(provider) {
  const breaker =
    provider && provider.config && provider.config.resilience
      ? provider.config.resilience.circuit_breaker
      : null;
  if (!breaker) return "-";
  return (
    String(breaker.failure_threshold) +
    " fail, " +
    String(breaker.success_threshold) +
    " success, " +
    breaker.timeout +
    " timeout"
  );
}

export function providerModelsSummary(provider) {
  const models =
    provider && provider.config && Array.isArray(provider.config.models)
      ? provider.config.models.filter(Boolean)
      : [];
  if (models.length === 0) return "Automatic";
  return models.join(", ");
}

// Tooltip for the status pill: the classification reason plus the last error,
// so the collapsed card already explains its state.
export function providerStatusPillTitle(provider) {
  if (!provider) return "";
  const parts = [];
  if (provider.status_reason) {
    parts.push(String(provider.status_reason));
  }
  if (provider.last_error) {
    parts.push("Last error: " + String(provider.last_error));
  }
  return parts.join("\n\n");
}

// --- Request health (10-minute windowed traffic + breaker state) ---

export function providerRequestHealth(provider) {
  const requestHealth = provider && provider.request_health;
  return requestHealth && typeof requestHealth === "object"
    ? requestHealth
    : null;
}

export function providerBreakerState(provider) {
  const requestHealth = providerRequestHealth(provider);
  return requestHealth ? String(requestHealth.circuit_state || "").trim() : "";
}

export function providerBreakerStateLabel(provider) {
  const state = providerBreakerState(provider);
  if (!state) return "";
  return state.charAt(0).toUpperCase() + state.slice(1);
}

export function providerBreakerStateClass(provider) {
  const state = providerBreakerState(provider);
  if (state === "open") return "is-unhealthy";
  if (state === "half-open") return "is-degraded";
  return "is-healthy";
}

export function providerRecentTrafficSummary(provider) {
  const requestHealth = providerRequestHealth(provider);
  if (!requestHealth) return "";
  const requests = Number(requestHealth.requests || 0);
  const errors = Number(requestHealth.errors || 0);
  const minutes = Math.round(Number(requestHealth.window_seconds || 0) / 60);
  const windowText = minutes > 0 ? "last " + minutes + " min" : "recent";
  return (
    String(requests) +
    " request" +
    (requests === 1 ? "" : "s") +
    " · " +
    String(errors) +
    " error" +
    (errors === 1 ? "" : "s") +
    " (" +
    windowText +
    ")"
  );
}

export function providerHealthModels(provider) {
  const requestHealth = providerRequestHealth(provider);
  return requestHealth && Array.isArray(requestHealth.models)
    ? requestHealth.models
    : [];
}

export function providerHealthModelStats(model) {
  if (!model) return "";
  return (
    String(Number(model.errors || 0)) +
    "/" +
    String(Number(model.requests || 0)) +
    " failed"
  );
}

// Tooltip with the model's most recent failure; empty when the model has no
// windowed errors.
export function providerHealthModelTitle(model) {
  const lastError = model && model.last_error;
  if (!lastError || !lastError.message) return "";
  const prefix = lastError.status_code
    ? "HTTP " + String(lastError.status_code) + ": "
    : "";
  return prefix + lastError.message;
}
