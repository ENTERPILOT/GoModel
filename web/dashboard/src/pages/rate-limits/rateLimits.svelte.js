// Rate-limits singleton store. The Models page imports this store for its
// gauge buttons and the effective-limits inspector. Pure logic lives in
// ./rateLimitsLogic.js.

import { loadAdminList, sendAdminMutation } from "$lib/api/adminCrud.js";
import { flash } from "$lib/stores/flash.svelte.js";
import { runtimeConfig } from "$lib/stores/runtimeConfig.svelte.js";
import * as m from "$lib/paraglide/messages.js";
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

  quotaTemplatesEnabled() {
    return runtimeConfig.quotaTemplatesVisible();
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

  // groupedRateLimits partitions the filtered list into the three scope
  // buckets (user_path → provider → model) for the page's group headers.
  groupedRateLimits() {
    return logic.groupRateLimits(this.filteredRateLimits());
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
    const outcome = await loadAdminList("/admin/rate-limits", {
      label: "rate limits",
      errorFallback: m.rate_limits_load_failed(),
      normalize: logic.normalizeRateLimitListPayload,
    });
    this.rateLimitsLoading = false;
    if (outcome.status === "stale") {
      return;
    }
    if (outcome.status === "unavailable") {
      this.rateLimitsAvailable = false;
      this.rateLimits = [];
      return;
    }
    if (!outcome.result) {
      // Network failure: clear the rows, keep the availability flag as-is.
      this.rateLimits = [];
      this.rateLimitError = outcome.error;
      return;
    }
    this.rateLimitsAvailable = true;
    if (outcome.status === "error") {
      this.rateLimitError = outcome.error;
      return;
    }
    this.rateLimits = outcome.items;
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
        per_child: Boolean(item.per_child),
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
      const outcome = await sendAdminMutation(
        "/admin/rate-limits",
        "PUT",
        payload,
        {
          label: "save rate limit",
          errorFallback: m.rate_limits_save_failed(),
          // Rate-limit mutations never had a dedicated 503 branch; keep 503 an error.
          unavailableStatuses: [],
        },
      );
      if (outcome.status === "stale") {
        return;
      }
      if (outcome.status !== "ok") {
        this.rateLimitFormError = outcome.error;
        return;
      }
      this.rateLimits = logic.normalizeRateLimitListPayload(
        outcome.result.data,
      );
      // Identity change = move: the new rule exists, now drop
      // the one it replaces. The new rule is created first so a
      // failed delete can never lose the rule.
      if (moved && !(await this.deleteMovedRateLimitOriginal(original))) {
        return;
      }
      this.closeRateLimitForm();
      flash.success(
        moved
          ? m.rate_limits_moved()
          : m.rate_limits_saved(),
      );
    } finally {
      this.rateLimitFormSubmitting = false;
    }
  }

  async deleteMovedRateLimitOriginal(original) {
    const outcome = await sendAdminMutation(
      "/admin/rate-limits",
      "DELETE",
      {
        scope: original.scope,
        subject: original.subject,
        limit_key: { period_seconds: Number(original.period_seconds || 0) },
      },
      {
        label: "remove the moved rate limit",
        errorFallback: m.rate_limits_move_cleanup_failed(),
        unavailableStatuses: [],
      },
    );
    if (outcome.status === "stale") {
      return false;
    }
    if (outcome.status !== "ok") {
      this.rateLimitFormError = outcome.error;
      return false;
    }
    this.rateLimits = logic.normalizeRateLimitListPayload(outcome.result.data);
    return true;
  }

  async deleteRateLimit(item) {
    const key = logic.rateLimitKey(item);
    if (this.rateLimitDeletingKey === key) {
      return;
    }
    this.rateLimitDeletingKey = key;
    const outcome = await sendAdminMutation(
      "/admin/rate-limits",
      "DELETE",
      {
        scope: logic.rateLimitScope(item),
        subject: logic.rateLimitSubject(item),
        limit_key: { period_seconds: Number(item.period_seconds || 0) },
      },
      {
        label: "delete rate limit",
        errorFallback: m.rate_limits_delete_failed(),
        unavailableStatuses: [],
      },
    );
    this.rateLimitDeletingKey = "";
    if (outcome.status === "stale") {
      return;
    }
    if (outcome.status !== "ok") {
      flash.error(outcome.error);
      return;
    }
    this.rateLimits = logic.normalizeRateLimitListPayload(outcome.result.data);
    flash.success(m.rate_limits_deleted());
  }

  async resetRateLimit(item) {
    const key = logic.rateLimitKey(item);
    if (this.rateLimitResettingKey === key) {
      return;
    }
    this.rateLimitResettingKey = key;
    const outcome = await sendAdminMutation(
      "/admin/rate-limits/reset-one",
      "POST",
      {
        scope: logic.rateLimitScope(item),
        subject: logic.rateLimitSubject(item),
        period_seconds: Number(item.period_seconds || 0),
      },
      {
        label: "reset rate limit",
        errorFallback: m.rate_limits_reset_failed(),
        unavailableStatuses: [],
      },
    );
    this.rateLimitResettingKey = "";
    if (outcome.status === "stale") {
      return;
    }
    if (outcome.status !== "ok") {
      flash.error(outcome.error);
      return;
    }
    this.rateLimits = logic.normalizeRateLimitListPayload(outcome.result.data);
    flash.success(m.rate_limits_reset_success());
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
