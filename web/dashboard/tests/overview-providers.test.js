// Ported from the legacy providers.test.cjs (pure-logic cases only; the
// fetch/poll harness tests stayed with the Alpine module — polling and abort
// semantics live in overviewState.svelte.js now).
import test from "node:test";
import assert from "node:assert/strict";

import {
  emptyProviderStatus,
  loadProviderPreferences,
  saveDetailsPreference,
  saveCardOverrides,
  providerCardExpanded,
  providerStatusSummaryClass,
  providerStatusBadgeClass,
  providerStatusRatioText,
  providerStatusHasIssues,
  providerStatusSummaryText,
  providerStatusNeedsPolling,
  providerLastChecked,
  providerLastCheckedTime,
  providerLastCheckedTitle,
  providerTypeLabel,
  providerDocUrl,
  providerRetrySummary,
  providerCircuitBreakerSummary,
  providerModelsSummary,
  providerStatusPillTitle,
  providerRequestHealth,
  providerBreakerState,
  providerBreakerStateLabel,
  providerBreakerStateClass,
  providerRecentTrafficSummary,
  providerHealthModels,
  providerHealthModelStats,
  providerHealthModelTitle,
} from "../src/pages/overview/providersLogic.js";

function createStorageStub() {
  return {
    values: new Map(),
    getItem(key) {
      return this.values.has(key) ? this.values.get(key) : null;
    },
    setItem(key, value) {
      this.values.set(key, String(value));
    },
  };
}

test("provider status summary and badge helpers map health states to stable classes", () => {
  let summary = { total: 2, healthy: 1, degraded: 0, unhealthy: 1, overall_status: "degraded" };

  assert.equal(providerStatusSummaryClass(summary), "is-degraded");
  assert.equal(providerStatusBadgeClass("healthy"), "is-healthy");
  assert.equal(providerStatusBadgeClass("unhealthy"), "is-unhealthy");
  assert.equal(providerStatusRatioText(summary), "1/2");
  assert.equal(providerStatusHasIssues(summary), true);
  assert.equal(providerStatusSummaryText(summary), "1 needs attention");

  summary = { total: 2, healthy: 2, degraded: 0, unhealthy: 0, overall_status: "healthy" };
  assert.equal(providerStatusHasIssues(summary), false);
  assert.equal(providerStatusSummaryText(summary), "All healthy");

  summary = { total: 2, healthy: 0, degraded: 0, unhealthy: 2, overall_status: "unhealthy" };
  assert.equal(providerStatusSummaryText(summary), "None healthy");

  summary = { total: 3, healthy: 1, degraded: 2, unhealthy: 0, overall_status: "degraded" };
  assert.equal(providerStatusSummaryText(summary), "2 need attention");

  summary = { total: 0, healthy: 0, degraded: 0, unhealthy: 0, overall_status: "degraded" };
  assert.equal(providerStatusSummaryText(summary), "None configured");

  assert.equal(providerStatusSummaryClass(emptyProviderStatus().summary), "is-degraded");
});

test("provider helper methods format configured models and resilience summaries", () => {
  const provider = {
    config: {
      models: ["gpt-4o", "gpt-4.1"],
      resilience: {
        retry: {
          max_retries: 3,
          initial_backoff: "1s",
          max_backoff: "30s",
          backoff_factor: 2,
          jitter_factor: 0.1,
        },
        circuit_breaker: {
          failure_threshold: 5,
          success_threshold: 2,
          timeout: "30s",
        },
      },
    },
    runtime: {
      last_model_fetch_at: "2026-04-10T12:00:00Z",
      last_availability_check_at: "2026-04-10T11:55:00Z",
    },
  };

  assert.equal(providerModelsSummary(provider), "gpt-4o, gpt-4.1");
  assert.equal(
    providerRetrySummary(provider),
    "3 retries, 1s initial, 30s max, factor 2, jitter 0.1",
  );
  assert.equal(providerCircuitBreakerSummary(provider), "5 fail, 2 success, 30s timeout");
  assert.equal(providerLastChecked(provider), "2026-04-10T12:00:00Z");
  // The most recent of the two check timestamps wins, whichever side it is.
  assert.equal(
    providerLastChecked({
      runtime: {
        last_model_fetch_at: "2026-04-10T12:00:00Z",
        last_availability_check_at: "2026-04-10T12:30:00Z",
      },
    }),
    "2026-04-10T12:30:00Z",
  );
  assert.equal(
    providerLastChecked({
      runtime: { last_availability_check_at: "2026-04-10T11:55:00Z" },
    }),
    "2026-04-10T11:55:00Z",
  );
  assert.equal(providerTypeLabel({ name: "openai-primary", type: "openai" }), "openai");
  assert.equal(providerTypeLabel({ name: "openai", type: "openai" }), "");
  assert.equal(providerTypeLabel({ name: "azure-east", config: { type: "azure" } }), "azure");
  assert.equal(providerModelsSummary({ config: { models: [] } }), "Automatic");
  assert.equal(providerRetrySummary({}), "-");
  assert.equal(providerCircuitBreakerSummary({}), "-");
  assert.equal(
    providerCircuitBreakerSummary({
      config: { resilience: { circuit_breaker: { enabled: false, failure_threshold: 5 } } },
    }),
    "Disabled",
  );
});

test("providerDocUrl links provider types with docs and stays empty otherwise", () => {
  // Types with a dedicated docs page.
  assert.equal(
    providerDocUrl({ type: "anthropic" }),
    "https://gomodel.enterpilot.io/docs/providers/anthropic?utm_source=gomodel_dashboard",
  );
  assert.equal(
    providerDocUrl({ config: { type: "bedrock" } }),
    "https://gomodel.enterpilot.io/docs/providers/bedrock?utm_source=gomodel_dashboard",
  );
  assert.equal(
    providerDocUrl({ type: "bedrock-mantle" }),
    "https://gomodel.enterpilot.io/docs/providers/bedrock-mantle?utm_source=gomodel_dashboard",
  );
  assert.equal(
    providerDocUrl({ type: "cohere" }),
    "https://gomodel.enterpilot.io/docs/providers/cohere?utm_source=gomodel_dashboard",
  );
  // Type slug differs from the docs slug.
  assert.equal(
    providerDocUrl({ type: "opencode_go" }),
    "https://gomodel.enterpilot.io/docs/providers/opencode-go?utm_source=gomodel_dashboard",
  );
  // Resolves even when the (type) label is hidden because name === type.
  assert.equal(
    providerDocUrl({ name: "gemini", type: "GEMINI" }),
    "https://gomodel.enterpilot.io/docs/providers/gemini?utm_source=gomodel_dashboard",
  );
  assert.equal(
    providerDocUrl({ type: "llmd" }),
    "https://gomodel.enterpilot.io/docs/providers/llmd?utm_source=gomodel_dashboard",
  );
  assert.equal(
    providerDocUrl({ type: "sglang" }),
    "https://gomodel.enterpilot.io/docs/providers/sglang?utm_source=gomodel_dashboard",
  );
  // Types without a provider-specific doc → no link (no icon).
  assert.equal(providerDocUrl({ type: "openai" }), "");
  assert.equal(providerDocUrl({ type: "ollama" }), "");
  assert.equal(providerDocUrl({ name: "mystery" }), "");
  assert.equal(providerDocUrl(null), "");
});

test("provider detail preferences start collapsed, persist, and last-check text is time-only", () => {
  const storage = createStorageStub();

  let prefs = loadProviderPreferences(storage);
  assert.equal(prefs.detailsExpanded, false);
  assert.equal(storage.getItem("gomodel_provider_status_details_expanded"), "false");

  saveDetailsPreference(storage, true);
  prefs = loadProviderPreferences(storage);
  assert.equal(prefs.detailsExpanded, true);
  assert.equal(storage.getItem("gomodel_provider_status_details_expanded"), "true");

  const formatTimestamp = (value) =>
    value === "2026-04-10T12:00:00Z" ? "2026-04-10 14:00:00" : "-";
  const provider = {
    runtime: {
      last_model_fetch_at: "2026-04-10T12:00:00Z",
    },
  };

  assert.equal(providerLastCheckedTime(provider, formatTimestamp), "14:00:00");
  assert.equal(providerLastCheckedTitle(provider, formatTimestamp), "2026-04-10 14:00:00");
});

test("providerStatusNeedsPolling triggers only while a provider is starting", () => {
  assert.equal(
    providerStatusNeedsPolling([{ name: "openai", status_label: "Starting" }]),
    true,
  );
  assert.equal(
    providerStatusNeedsPolling([{ name: "openai", status_label: "Healthy" }]),
    false,
  );
  assert.equal(providerStatusNeedsPolling([]), false);
  assert.equal(providerStatusNeedsPolling(null), false);
});

test("request health helpers summarize windowed traffic and breaker state", () => {
  const provider = {
    request_health: {
      circuit_state: "half-open",
      window_seconds: 600,
      requests: 12,
      errors: 1,
      models: [
        {
          model: "qwen3.7-max",
          requests: 4,
          errors: 4,
          flagged: true,
          last_error: { status_code: 400, message: "Error from provider" },
        },
        { model: "gpt-5-nano", requests: 8, errors: 0, flagged: false },
      ],
    },
  };

  assert.equal(providerRequestHealth(provider), provider.request_health);
  assert.equal(providerBreakerState(provider), "half-open");
  assert.equal(providerBreakerStateLabel(provider), "Half-open");
  assert.equal(providerBreakerStateClass(provider), "is-degraded");
  assert.equal(providerRecentTrafficSummary(provider), "12 requests · 1 error (last 10 min)");
  assert.equal(providerHealthModels(provider).length, 2);
  assert.equal(providerHealthModelStats(provider.request_health.models[0]), "4/4 failed");
  assert.equal(
    providerHealthModelTitle(provider.request_health.models[0]),
    "HTTP 400: Error from provider",
  );
  assert.equal(providerHealthModelTitle(provider.request_health.models[1]), "");
});

test("request health helpers tolerate providers without request health", () => {
  const provider = { name: "openai" };

  assert.equal(providerRequestHealth(provider), null);
  assert.equal(providerBreakerState(provider), "");
  assert.equal(providerBreakerStateLabel(provider), "");
  assert.equal(providerRecentTrafficSummary(provider), "");
  assert.equal(providerHealthModels(provider).length, 0);
  assert.equal(providerHealthModelStats(null), "");
  assert.equal(providerHealthModelTitle(null), "");
});

test("breaker state classes map open and closed states to pill palettes", () => {
  const withState = (state) => ({ request_health: { circuit_state: state } });

  assert.equal(providerBreakerStateClass(withState("open")), "is-unhealthy");
  assert.equal(providerBreakerStateClass(withState("closed")), "is-healthy");
});

test("per-card override wins over the section-wide default and persists", () => {
  const storage = createStorageStub();
  let { detailsExpanded, cardOverrides } = loadProviderPreferences(storage);

  const ollama = { name: "ollama" };
  const openai = { name: "openai" };

  // Cards follow the collapsed section default until toggled individually.
  assert.equal(providerCardExpanded(cardOverrides, detailsExpanded, ollama), false);
  cardOverrides = { ...cardOverrides, ollama: true };
  saveCardOverrides(storage, cardOverrides);
  assert.equal(providerCardExpanded(cardOverrides, detailsExpanded, ollama), true);
  assert.equal(providerCardExpanded(cardOverrides, detailsExpanded, openai), false);

  // Overrides survive a reload.
  const reloaded = loadProviderPreferences(storage);
  assert.equal(providerCardExpanded(reloaded.cardOverrides, reloaded.detailsExpanded, ollama), true);
  assert.equal(providerCardExpanded(reloaded.cardOverrides, reloaded.detailsExpanded, openai), false);

  // The section-wide toggle acts as a master: dropped overrides make every
  // card follow it again.
  saveDetailsPreference(storage, true);
  saveCardOverrides(storage, {});
  const master = loadProviderPreferences(storage);
  assert.equal(providerCardExpanded(master.cardOverrides, master.detailsExpanded, ollama), true);
  assert.equal(storage.getItem("gomodel_provider_card_expanded_overrides"), "{}");
});

test("preference loading tolerates corrupt storage and nameless providers", () => {
  const storage = createStorageStub();
  storage.setItem("gomodel_provider_card_expanded_overrides", "not-json");
  const prefs = loadProviderPreferences(storage);

  assert.deepEqual(prefs.cardOverrides, {});
  assert.equal(providerCardExpanded(prefs.cardOverrides, prefs.detailsExpanded, { name: "ollama" }), false);
  assert.equal(providerCardExpanded(prefs.cardOverrides, prefs.detailsExpanded, null), false);
  assert.equal(providerCardExpanded(prefs.cardOverrides, prefs.detailsExpanded, {}), false);
});

test("status pill tooltip combines reason and last error", () => {
  assert.equal(providerStatusPillTitle(null), "");
  assert.equal(providerStatusPillTitle({}), "");
  assert.equal(
    providerStatusPillTitle({ status_reason: "configured and model discovery succeeded" }),
    "configured and model discovery succeeded",
  );
  assert.equal(
    providerStatusPillTitle({
      status_reason: "recent requests are failing for: gpt-4o",
      last_error: "gpt-4o: authentication_error",
    }),
    "recent requests are failing for: gpt-4o\n\nLast error: gpt-4o: authentication_error",
  );
});
