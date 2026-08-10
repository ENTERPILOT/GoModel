// Shared runtime-config flags from GET /admin/runtime/config. Feature gates
// (sidebar visibility, page sections) read these flags; ensureLoaded() shares
// the in-flight request so parallel page fetches never duplicate the GET, and
// a failed load leaves the store unloaded so the next caller retries instead
// of trusting an empty config.

import { getJSON } from "$lib/api/client.js";

const CONFIG_KEYS = [
  "DEMO_MODE",
  "FAILOVER_ENABLED",
  "LOGGING_ENABLED",
  "LOGGING_RETENTION_DAYS",
  "USAGE_ENABLED",
  "BUDGETS_ENABLED",
  "RATE_LIMITS_ENABLED",
  "PER_CHILD_QUOTAS_ENABLED",
  "GUARDRAILS_ENABLED",
  "CACHE_ENABLED",
  "REDIS_URL",
  "SEMANTIC_CACHE_ENABLED",
  "USAGE_PRICING_RECALCULATION_ENABLED",
  "DASHBOARD_LIVE_LOGS_ENABLED",
  "MCP_ENABLED",
  "VIRTUAL_MODEL_STRATEGIES",
];

// Strategies every gateway supports; used when the backend predates the
// VIRTUAL_MODEL_STRATEGIES key.
const DEFAULT_VM_STRATEGIES = ["round_robin", "cost"];

class RuntimeConfigStore {
  config = $state({});
  loaded = $state(false);
  #inflight = null;

  flag(name) {
    const value = this.config && this.config[name];
    return String(value || "")
      .trim()
      .toLowerCase();
  }

  booleanFlag(name, defaultValue) {
    const value = this.flag(name);
    if (value === "") {
      return !!defaultValue;
    }
    return value === "on" || value === "true" || value === "1";
  }

  // virtualModelStrategies lists the load-balancing strategies this
  // deployment supports (comma-separated server-side). The backend is the
  // source of truth so extension-provided strategies (e.g. "adaptive") only
  // show up where they actually do something.
  virtualModelStrategies() {
    const list = this.flag("VIRTUAL_MODEL_STRATEGIES")
      .split(",")
      .map((s) => s.trim())
      .filter(Boolean);
    return list.length > 0 ? list : DEFAULT_VM_STRATEGIES;
  }

  // cacheVisible is a tri-source gate: an explicit CACHE_ENABLED wins;
  // otherwise Redis/semantic-cache presence decides.
  cacheVisible() {
    const explicit = this.flag("CACHE_ENABLED");
    if (explicit !== "") {
      return this.booleanFlag("CACHE_ENABLED", false);
    }
    const redis = this.flag("REDIS_URL");
    const semantic = this.flag("SEMANTIC_CACHE_ENABLED");
    if (redis === "" && semantic === "") {
      return true;
    }
    return (
      this.booleanFlag("REDIS_URL", false) ||
      this.booleanFlag("SEMANTIC_CACHE_ENABLED", false)
    );
  }

  auditVisible() {
    return this.booleanFlag("LOGGING_ENABLED", true);
  }

  usageVisible() {
    return this.booleanFlag("USAGE_ENABLED", true);
  }

  budgetsVisible() {
    return this.booleanFlag("BUDGETS_ENABLED", true);
  }

  rateLimitsVisible() {
    return this.booleanFlag("RATE_LIMITS_ENABLED", true);
  }

  quotaTemplatesVisible() {
    return this.booleanFlag("PER_CHILD_QUOTAS_ENABLED", false);
  }

  guardrailsVisible() {
    return this.booleanFlag("GUARDRAILS_ENABLED", true);
  }

  mcpVisible() {
    return this.booleanFlag("MCP_ENABLED", true);
  }

  liveLogsVisible() {
    return this.booleanFlag("DASHBOARD_LIVE_LOGS_ENABLED", true);
  }

  fetch() {
    if (this.#inflight) {
      return this.#inflight;
    }
    this.#inflight = this.#load().finally(() => {
      this.#inflight = null;
    });
    return this.#inflight;
  }

  // ensureLoaded resolves once the flags have loaded successfully at least
  // once, so gated callers never fall back to defaults by accident.
  async ensureLoaded() {
    if (this.#inflight) {
      await this.#inflight;
      return;
    }
    if (this.loaded) {
      return;
    }
    await this.fetch();
  }

  async #load() {
    const controller =
      typeof AbortController === "function" ? new AbortController() : null;
    const timeoutID = controller
      ? setTimeout(() => controller.abort(), 10000)
      : null;
    try {
      const result = await getJSON("/admin/runtime/config", {
        label: "dashboard config",
        signal: controller ? controller.signal : undefined,
      });
      if (result.stale) {
        return;
      }
      if (!result.ok) {
        this.config = {};
        this.loaded = false;
        return;
      }
      const payload = result.data;
      const next = {};
      for (const key of CONFIG_KEYS) {
        if (
          payload &&
          typeof payload === "object" &&
          !Array.isArray(payload) &&
          payload[key] !== undefined &&
          payload[key] !== null
        ) {
          next[key] = String(payload[key]).trim();
        }
      }
      this.config = next;
      this.loaded = true;
    } catch (e) {
      console.error("Failed to fetch dashboard config:", e);
      this.config = {};
      this.loaded = false;
    } finally {
      if (timeoutID !== null) {
        clearTimeout(timeoutID);
      }
    }
  }
}

export const runtimeConfig = new RuntimeConfigStore();
