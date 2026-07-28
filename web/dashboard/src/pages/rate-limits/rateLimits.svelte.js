// Rate-limits singleton store. The Models page imports this store for its
// gauge buttons and the effective-limits inspector. Pure logic lives in
// ./rateLimitsLogic.js.

import { errorMessage, getJSON, sendJSON } from "$lib/api/client.ts";
import { flash } from "$lib/stores/flash.svelte.ts";
import { runtimeConfig } from "$lib/stores/runtimeConfig.svelte.ts";
import * as logic from "./rateLimitsLogic.js";

class RateLimitsStore {
  rateLimits = $state([]);
  rateLimitsAvailable = $state(true);
  rateLimitsLoading = $state(false);
  rateLimitFetchPromise = null;
  rateLimitFilter = $state("");
  // Load failures only; mutation feedback goes through the flash store.
  rateLimitError = $state("");
  rateLimitFormOpen = $state(false);
  rateLimitFormSubmitting = $state(false);
  rateLimitFormError = $state("");
  rateLimitEditing = $state(false);
  rateLimitEditingOriginal = null;
  rateLimitFormReturnToInspector = false;
  rateLimitResettingKey = $state("");
  rateLimitDeletingKey = $state("");
  rateLimitInspectorOpen = $state(false);
  rateLimitInspector = $state({ kind: "", provider: "", model: "", title: "" });
  rateLimitForm = $state(logic.defaultRateLimitForm());

  rateLimitsEnabled() {
    return runtimeConfig.rateLimitsVisible();
  }

  defaultRateLimitForm() {
    return logic.defaultRateLimitForm();
  }

  rateLimitScopeMeta(scope) {
    return logic.rateLimitScopeMeta(scope);
  }

  rateLimitScopeOptions() {
    return logic.rateLimitScopeOptions();
  }

  rateLimitScope(item) {
    return logic.rateLimitScope(item);
  }

  rateLimitSubject(item) {
    return logic.rateLimitSubject(item);
  }

  rateLimitScopeLabel(item) {
    return logic.rateLimitScopeLabel(item);
  }

  rateLimitSubjectFieldLabel() {
    return logic.rateLimitSubjectFieldLabel(this.rateLimitForm);
  }

  rateLimitSubjectPlaceholder() {
    return logic.rateLimitSubjectPlaceholder(this.rateLimitForm);
  }

  // Changing scope resets the subject: a user path never carries
  // over to a provider or model rule.
  syncRateLimitScope() {
    logic.syncRateLimitScope(this.rateLimitForm);
  }

  rateLimitPeriodOptions() {
    return logic.rateLimitPeriodOptions();
  }

  rateLimitPeriodSeconds(period) {
    return logic.rateLimitPeriodSeconds(period);
  }

  rateLimitPeriodFromSeconds(seconds) {
    return logic.rateLimitPeriodFromSeconds(seconds);
  }

  syncRateLimitPeriodSeconds() {
    logic.syncRateLimitPeriodSeconds(this.rateLimitForm);
  }

  rateLimitKey(item) {
    return logic.rateLimitKey(item);
  }

  rateLimitIsConcurrent(item) {
    return logic.rateLimitIsConcurrent(item);
  }

  rateLimitPeriodLabel(item) {
    return logic.rateLimitPeriodLabel(item);
  }

  rateLimitSourceLabel(item) {
    return logic.rateLimitSourceLabel(item);
  }

  rateLimitIsReadOnly(item) {
    return logic.rateLimitIsReadOnly(item);
  }

  formatRateLimitNumber(value) {
    return logic.formatRateLimitNumber(value);
  }

  rateLimitUsagePercent(used, limit) {
    return logic.rateLimitUsagePercent(used, limit);
  }

  filteredRateLimits() {
    return logic.filteredRateLimits(this.rateLimits, this.rateLimitFilter);
  }

  normalizeRateLimitListPayload(payload) {
    return logic.normalizeRateLimitListPayload(payload);
  }

  async fetchRateLimitsPage() {
    // Wait for the runtime flags before touching a possibly disabled endpoint.
    await runtimeConfig.ensureLoaded();
    if (!this.rateLimitsEnabled()) {
      this.rateLimits = [];
      this.rateLimitsAvailable = false;
      this.rateLimitError = "";
      return;
    }
    if (this.rateLimitFetchPromise) {
      return this.rateLimitFetchPromise;
    }
    this.rateLimitFetchPromise = this.fetchRateLimits().finally(() => {
      this.rateLimitFetchPromise = null;
    });
    return this.rateLimitFetchPromise;
  }

  async fetchRateLimits() {
    this.rateLimitsLoading = true;
    this.rateLimitError = "";
    try {
      const result = await getJSON("/admin/rate-limits", {
        label: "rate limits",
      });
      if (result.status === 503) {
        this.rateLimitsAvailable = false;
        this.rateLimits = [];
        return;
      }
      if (result.stale) {
        return;
      }
      this.rateLimitsAvailable = true;
      if (!result.ok) {
        this.rateLimitError = "Unable to load rate limits.";
        return;
      }
      this.rateLimits = logic.normalizeRateLimitListPayload(result.data);
    } catch (e) {
      console.error("Failed to fetch rate limits:", e);
      this.rateLimits = [];
      this.rateLimitError = "Unable to load rate limits.";
    } finally {
      this.rateLimitsLoading = false;
    }
  }

  openRateLimitForm(item) {
    this.rateLimitEditing = !!item;
    this.rateLimitFormError = "";
    if (item) {
      const periodSeconds = Number(item.period_seconds || 0);
      this.rateLimitEditingOriginal = {
        scope: logic.rateLimitScope(item),
        subject: logic.rateLimitSubject(item),
        period_seconds: periodSeconds,
      };
      this.rateLimitForm = {
        scope: logic.rateLimitScope(item),
        subject: logic.rateLimitSubject(item),
        period: logic.rateLimitPeriodFromSeconds(periodSeconds),
        period_seconds: periodSeconds,
        max_requests:
          item.max_requests === null || item.max_requests === undefined
            ? ""
            : String(item.max_requests),
        max_tokens:
          item.max_tokens === null || item.max_tokens === undefined
            ? ""
            : String(item.max_tokens),
        source: String(item.source || "manual"),
      };
    } else {
      this.rateLimitEditingOriginal = null;
      this.rateLimitForm = logic.defaultRateLimitForm();
    }
    this.rateLimitFormOpen = true;
  }

  closeRateLimitForm() {
    this.rateLimitFormOpen = false;
    this.rateLimitFormSubmitting = false;
    this.rateLimitFormError = "";
    this.rateLimitEditing = false;
    this.rateLimitEditingOriginal = null;
    this.rateLimitForm = logic.defaultRateLimitForm();
    if (this.rateLimitFormReturnToInspector) {
      this.rateLimitFormReturnToInspector = false;
      this.rateLimitInspectorOpen = true;
    }
  }

  rateLimitNormalizedIdentity(scope, subject, periodSeconds) {
    return logic.rateLimitNormalizedIdentity(scope, subject, periodSeconds);
  }

  rateLimitIdentityMoved(payload) {
    return logic.rateLimitIdentityMoved(this.rateLimitEditingOriginal, payload);
  }

  setRateLimitFormSubject(value) {
    this.rateLimitForm.subject = String(value || "");
  }

  rateLimitFormPayload() {
    return logic.rateLimitFormPayload(this.rateLimitForm);
  }

  async submitRateLimitForm() {
    if (this.rateLimitFormSubmitting) {
      return;
    }
    const { payload, error } = this.rateLimitFormPayload();
    if (error) {
      this.rateLimitFormError = error;
      return;
    }
    const moved = this.rateLimitIdentityMoved(payload);
    const original = this.rateLimitEditingOriginal;
    this.rateLimitFormSubmitting = true;
    this.rateLimitFormError = "";
    try {
      const result = await sendJSON("/admin/rate-limits", "PUT", payload, {
        label: "rate limit save",
      });
      if (result.stale) {
        return;
      }
      if (!result.ok) {
        this.rateLimitFormError = errorMessage(
          result,
          "Unable to save rate limit.",
        );
        return;
      }
      this.rateLimits = logic.normalizeRateLimitListPayload(result.data);
      // Identity change = move: the new rule exists, now drop
      // the one it replaces. The new rule is created first so a
      // failed delete can never lose the rule.
      if (moved && !(await this.deleteMovedRateLimitOriginal(original))) {
        return;
      }
      this.closeRateLimitForm();
      flash.success(
        moved
          ? "Rate limit moved; live counters restarted."
          : "Rate limit saved.",
      );
    } catch (e) {
      console.error("Failed to save rate limit:", e);
      this.rateLimitFormError = "Unable to save rate limit.";
    } finally {
      this.rateLimitFormSubmitting = false;
    }
  }

  async deleteMovedRateLimitOriginal(original) {
    try {
      const result = await sendJSON(
        "/admin/rate-limits",
        "DELETE",
        {
          scope: original.scope,
          subject: original.subject,
          limit_key: { period_seconds: Number(original.period_seconds || 0) },
        },
        { label: "rate limit move" },
      );
      if (!result.ok) {
        this.rateLimitFormError = errorMessage(
          result,
          "The new rule was saved, but the previous one could not be removed. Delete it manually.",
        );
        return false;
      }
      this.rateLimits = logic.normalizeRateLimitListPayload(result.data);
      return true;
    } catch (e) {
      console.error("Failed to remove the moved rate limit:", e);
      this.rateLimitFormError =
        "The new rule was saved, but the previous one could not be removed. Delete it manually.";
      return false;
    }
  }

  async deleteRateLimit(item) {
    const key = logic.rateLimitKey(item);
    if (this.rateLimitDeletingKey === key) {
      return;
    }
    this.rateLimitDeletingKey = key;
    try {
      const result = await sendJSON(
        "/admin/rate-limits",
        "DELETE",
        {
          scope: logic.rateLimitScope(item),
          subject: logic.rateLimitSubject(item),
          limit_key: { period_seconds: Number(item.period_seconds || 0) },
        },
        { label: "rate limit delete" },
      );
      if (result.stale) {
        return;
      }
      if (!result.ok) {
        flash.error(errorMessage(result, "Unable to delete rate limit."));
        return;
      }
      this.rateLimits = logic.normalizeRateLimitListPayload(result.data);
      flash.success("Rate limit deleted.");
    } catch (e) {
      console.error("Failed to delete rate limit:", e);
      flash.error("Unable to delete rate limit.");
    } finally {
      this.rateLimitDeletingKey = "";
    }
  }

  async resetRateLimit(item) {
    const key = logic.rateLimitKey(item);
    if (this.rateLimitResettingKey === key) {
      return;
    }
    this.rateLimitResettingKey = key;
    try {
      const result = await sendJSON(
        "/admin/rate-limits/reset-one",
        "POST",
        {
          scope: logic.rateLimitScope(item),
          subject: logic.rateLimitSubject(item),
          period_seconds: Number(item.period_seconds || 0),
        },
        { label: "rate limit reset" },
      );
      if (result.stale) {
        return;
      }
      if (!result.ok) {
        flash.error(errorMessage(result, "Unable to reset rate limit."));
        return;
      }
      this.rateLimits = logic.normalizeRateLimitListPayload(result.data);
      flash.success("Rate limit counters reset.");
    } catch (e) {
      console.error("Failed to reset rate limit:", e);
      flash.error("Unable to reset rate limit.");
    } finally {
      this.rateLimitResettingKey = "";
    }
  }

  // --- Effective-limits inspector (Models page) ---

  rateLimitInspectorModelID(row) {
    return String((row && row.model && row.model.id) || "").trim();
  }

  openRateLimitInspectorForModel(row) {
    const model = this.rateLimitInspectorModelID(row);
    const provider = String((row && row.provider_name) || "")
      .trim()
      .toLowerCase();
    this.rateLimitInspector = {
      kind: "model",
      provider: provider,
      model: model,
      title: String((row && row.display_name) || model),
    };
    this.showRateLimitInspector();
  }

  openRateLimitInspectorForProvider(group) {
    const provider = String((group && group.provider_name) || "")
      .trim()
      .toLowerCase();
    this.rateLimitInspector = {
      kind: "provider",
      provider: provider,
      model: "",
      title: String((group && group.display_name) || provider),
    };
    this.showRateLimitInspector();
  }

  showRateLimitInspector() {
    this.rateLimitInspectorOpen = true;
    this.fetchRateLimitsPage();
  }

  closeRateLimitInspector() {
    this.rateLimitInspectorOpen = false;
  }

  rateLimitRuleMatchesModel(rule, provider, model) {
    return logic.rateLimitRuleMatchesModel(rule, provider, model);
  }

  rateLimitRuleMatchesProvider(rule, provider) {
    return logic.rateLimitRuleMatchesProvider(rule, provider);
  }

  rateLimitInspectorQualifiedModel() {
    return logic.rateLimitInspectorQualifiedModel(this.rateLimitInspector);
  }

  rateLimitInspectorSections() {
    return logic.rateLimitInspectorSections(
      this.rateLimitInspector,
      this.rateLimits,
    );
  }

  rateLimitPressurePercent(item) {
    return logic.rateLimitPressurePercent(item);
  }

  rateLimitPressureStyle(item) {
    return logic.rateLimitPressureStyle(item);
  }

  rateLimitPressureClass(item) {
    return logic.rateLimitPressureClass(item);
  }

  // Gauge indicator states on the Models page: fully painted when the subject
  // has its own rules, half painted when only provider or global rules
  // throttle it, plain otherwise. The class bindings run several times per
  // row on every render, so results are memoized until the rules list is
  // replaced (fetches always assign a new array).
  rateLimitGaugeCache = { rules: null, states: {} };

  rateLimitGaugeMemo(key, compute) {
    if (this.rateLimitGaugeCache.rules !== this.rateLimits) {
      this.rateLimitGaugeCache = { rules: this.rateLimits, states: {} };
    }
    const states = this.rateLimitGaugeCache.states;
    if (!(key in states)) {
      states[key] = compute();
    }
    return states[key];
  }

  rateLimitGaugeClassForModel(row) {
    const model = this.rateLimitInspectorModelID(row);
    const provider = String((row && row.provider_name) || "")
      .trim()
      .toLowerCase();
    // Read the reactive list outside the memo so bindings re-run on refetch.
    const rules = this.rateLimits;
    return this.rateLimitGaugeMemo("model:" + provider + "/" + model, () =>
      logic.rateLimitGaugeClassForModel(rules, provider, model),
    );
  }

  rateLimitGaugeClassForProvider(group) {
    const provider = String((group && group.provider_name) || "")
      .trim()
      .toLowerCase();
    const rules = this.rateLimits;
    return this.rateLimitGaugeMemo("provider:" + provider, () =>
      logic.rateLimitGaugeClassForProvider(rules, provider),
    );
  }

  hasGlobalRateLimits() {
    const rules = this.rateLimits;
    return this.rateLimitGaugeMemo("global", () =>
      logic.hasGlobalRateLimits(rules),
    );
  }

  rateLimitGaugeTitle(subject, gaugeClass) {
    return logic.rateLimitGaugeTitle(subject, gaugeClass);
  }

  rateLimitInspectorSummary(item) {
    return logic.rateLimitInspectorSummary(item);
  }

  openRateLimitFormFromInspector(scope, subject, item) {
    this.rateLimitInspectorOpen = false;
    this.rateLimitFormReturnToInspector = true;
    this.openRateLimitForm(item || undefined);
    if (!item) {
      this.rateLimitForm.scope = scope;
      this.rateLimitForm.subject = subject;
    }
  }
}

export const rateLimits = new RateLimitsStore();
