// Shared runtime-config flags from GET /admin/runtime/config. Feature gates
// (sidebar visibility, page sections) read these flags; ensureLoaded() shares
// the in-flight request so parallel page fetches never duplicate the GET, and
// a failed load leaves the store unloaded so the next caller retries instead
// of trusting an empty config.

import { getJSON } from "$lib/api/client.ts";

const CONFIG_KEYS = [
  "DEMO_MODE",
  "FAILOVER_ENABLED",
  "LOGGING_ENABLED",
  "LOGGING_RETENTION_DAYS",
  "USAGE_ENABLED",
  "BUDGETS_ENABLED",
  "RATE_LIMITS_ENABLED",
  "GUARDRAILS_ENABLED",
  "CACHE_ENABLED",
  "REDIS_URL",
  "SEMANTIC_CACHE_ENABLED",
  "USAGE_PRICING_RECALCULATION_ENABLED",
  "DASHBOARD_LIVE_LOGS_ENABLED",
  "MCP_ENABLED",
];

class RuntimeConfigStore {
  config = $state<Record<string, string>>({});
  loaded = $state(false);
  #inflight: Promise<void> | null = null;

  flag(name: string): string {
    const value = this.config && this.config[name];
    return String(value || "")
      .trim()
      .toLowerCase();
  }

  booleanFlag(name: string, defaultValue?: boolean): boolean {
    const value = this.flag(name);
    if (value === "") {
      return !!defaultValue;
    }
    return value === "on" || value === "true" || value === "1";
  }

  // cacheVisible is a tri-source gate: an explicit CACHE_ENABLED wins;
  // otherwise Redis/semantic-cache presence decides.
  cacheVisible(): boolean {
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

  auditVisible(): boolean {
    return this.booleanFlag("LOGGING_ENABLED", true);
  }

  usageVisible(): boolean {
    return this.booleanFlag("USAGE_ENABLED", true);
  }

  budgetsVisible(): boolean {
    return this.booleanFlag("BUDGETS_ENABLED", true);
  }

  rateLimitsVisible(): boolean {
    return this.booleanFlag("RATE_LIMITS_ENABLED", true);
  }

  guardrailsVisible(): boolean {
    return this.booleanFlag("GUARDRAILS_ENABLED", true);
  }

  mcpVisible(): boolean {
    return this.booleanFlag("MCP_ENABLED", true);
  }

  liveLogsVisible(): boolean {
    return this.booleanFlag("DASHBOARD_LIVE_LOGS_ENABLED", true);
  }

  fetch(): Promise<void> {
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
  async ensureLoaded(): Promise<void> {
    if (this.#inflight) {
      await this.#inflight;
      return;
    }
    if (this.loaded) {
      return;
    }
    await this.fetch();
  }

  async #load(): Promise<void> {
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
      const next: Record<string, string> = {};
      if (payload && typeof payload === "object" && !Array.isArray(payload)) {
        const record = payload as Record<string, unknown>;
        for (const key of CONFIG_KEYS) {
          if (record[key] !== undefined && record[key] !== null) {
            next[key] = String(record[key]).trim();
          }
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
