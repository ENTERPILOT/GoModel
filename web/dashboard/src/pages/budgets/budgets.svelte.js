// Budgets page state (the budgets-page portion; budget settings live with
// the Settings page). Talks to the /admin/budgets endpoints.

import { loadAdminList, sendAdminMutation } from "$lib/api/adminCrud.js";
import { flash } from "$lib/stores/flash.svelte.js";
import { runtimeConfig } from "$lib/stores/runtimeConfig.svelte.js";
import { access } from "$lib/stores/access.svelte.js";
import { router } from "$lib/stores/router.svelte.js";
import { confirmDialog } from "$lib/stores/confirm.svelte.js";
import {
  budgetDeleteBody,
  budgetInputUserPath,
  budgetScope,
  budgetSubject,
  syncBudgetScope,
  budgetKey,
  budgetPeriodFromSeconds,
  budgetPeriodLabel,
  budgetPeriodSeconds,
  budgetPutBody,
  budgetResetOneBody,
  buildBudgetFormPayload,
  defaultBudgetForm,
  filterAndSortBudgets,
  findExistingBudget,
  normalizeBudgetListPayload,
} from "./budgets-helpers.js";
import { RotateCcw } from "lucide";
import * as m from "$lib/paraglide/messages.js";

class BudgetsStore {
  budgets = $state([]);
  budgetsAvailable = $state(true);
  loading = $state(false);
  filter = $state("");
  sortBy = $state("subject");
  // Load failures only; mutation feedback goes through the flash store.
  error = $state("");

  formOpen = $state(false);
  formSubmitting = $state(false);
  formError = $state("");
  editing = $state(false);
  form = $state(defaultBudgetForm());

  overrideDialogOpen = $state(false);
  overridePendingPayload = $state(null);
  overrideExistingBudget = $state(null);

  resettingKey = $state("");
  deletingKey = $state("");
  resetAllLoading = $state(false);

  #fetchPromise = null;

  managementEnabled() {
    return runtimeConfig.budgetsVisible();
  }

  quotaTemplatesEnabled() {
    return runtimeConfig.quotaTemplatesVisible();
  }

  filteredBudgets() {
    return filterAndSortBudgets(this.budgets, this.filter, this.sortBy);
  }

  // fetchBudgetsPage waits for the runtime flags before touching a disabled
  // endpoint and dedupes concurrent page-activation fetches.
  async fetchBudgetsPage() {
    await runtimeConfig.ensureLoaded();
    if (!this.managementEnabled()) {
      this.budgets = [];
      this.budgetsAvailable = false;
      this.error = "";
      return;
    }
    if (this.#fetchPromise) {
      return this.#fetchPromise;
    }
    this.#fetchPromise = this.fetchBudgets().finally(() => {
      this.#fetchPromise = null;
    });
    return this.#fetchPromise;
  }

  async fetchBudgets() {
    this.loading = true;
    this.error = "";
    const outcome = await loadAdminList("/admin/budgets", {
      label: "budgets",
      errorFallback: m.budgets_load_failed(),
      normalize: normalizeBudgetListPayload,
    });
    this.loading = false;
    if (outcome.status === "stale") {
      return;
    }
    if (outcome.status === "unavailable") {
      this.budgetsAvailable = false;
      this.budgets = [];
      return;
    }
    if (!outcome.result) {
      // Network failure: clear the rows, keep the availability flag as-is.
      this.budgets = [];
      this.error = outcome.error;
      return;
    }
    this.budgetsAvailable = true;
    if (outcome.status === "error") {
      this.error = outcome.error;
      return;
    }
    this.budgets = outcome.items;
  }

  openForm(item) {
    this.editing = !!item;
    this.formError = "";
    if (item) {
      const periodSeconds = Number(item.period_seconds || 0);
      this.form = {
        scope: budgetScope(item),
        subject: budgetSubject(item),
        period: budgetPeriodFromSeconds(periodSeconds),
        period_seconds: periodSeconds,
        amount: String(item.amount || ""),
        per_child: Boolean(item.per_child),
        source: String(item.source || "manual"),
      };
    } else {
      this.form = defaultBudgetForm();
      // A scoped key can only budget its own subtree: start there.
      this.form.subject = access.defaultPath(this.form.subject);
    }
    this.formOpen = true;
  }

  // syncPeriodSeconds mirrors the standard-period seconds into the form when
  // a preset period is picked (custom keeps the user-entered value).
  syncPeriodSeconds() {
    const seconds = budgetPeriodSeconds(String(this.form.period || "").trim());
    if (seconds > 0) {
      this.form.period_seconds = seconds;
    }
  }

  // A user-path subject stays controlled with a leading slash; a label is
  // taken verbatim, since labels are matched exactly.
  setFormSubject(value) {
    this.form.subject =
      this.form.scope === "label"
        ? String(value ?? "")
        : budgetInputUserPath(value);
  }

  // syncScope clears the subject when the scope changes, so a user path is
  // never submitted as a label.
  syncScope() {
    syncBudgetScope(this.form);
  }

  closeForm() {
    this.closeOverrideDialog();
    this.formOpen = false;
    this.formSubmitting = false;
    this.formError = "";
    this.editing = false;
    this.form = defaultBudgetForm();
  }

  async submitForm() {
    if (this.formSubmitting) {
      return;
    }
    const { payload, error } = buildBudgetFormPayload(this.form);
    if (!payload) {
      this.formError = error;
      return;
    }
    if (!this.editing) {
      const existing = findExistingBudget(this.budgets, payload);
      if (existing) {
        this.openOverrideDialog(existing, payload);
        return;
      }
    }
    await this.saveBudgetPayload(payload);
  }

  async saveBudgetPayload(payload) {
    if (this.formSubmitting || !payload) {
      return;
    }
    this.formSubmitting = true;
    this.formError = "";
    const outcome = await sendAdminMutation(
      "/admin/budgets",
      "PUT",
      budgetPutBody(payload),
      {
        label: "save budget",
        errorFallback: m.budgets_save_failed(),
        unavailableMessage: m.budgets_unavailable(),
      },
    );
    this.formSubmitting = false;
    if (outcome.status === "stale") {
      return;
    }
    if (outcome.status === "unavailable") {
      this.budgetsAvailable = false;
      this.formError = outcome.error;
      return;
    }
    if (outcome.status === "error") {
      this.formError = outcome.error;
      return;
    }
    this.closeForm();
    // Flash before the refetch so feedback is instant.
    flash.success(m.budgets_saved());
    void this.fetchBudgets();
  }

  openOverrideDialog(existing, payload) {
    this.overrideExistingBudget = existing || null;
    this.overridePendingPayload = payload || null;
    this.overrideDialogOpen = true;
  }

  closeOverrideDialog() {
    this.overrideDialogOpen = false;
    this.overridePendingPayload = null;
    this.overrideExistingBudget = null;
  }

  async confirmOverride() {
    if (!this.overridePendingPayload) {
      this.closeOverrideDialog();
      return;
    }
    const payload = this.overridePendingPayload;
    this.closeOverrideDialog();
    await this.saveBudgetPayload(payload);
  }

  async resetBudget(item) {
    if (!item) {
      return;
    }
    const key = budgetKey(item);
    if (this.resettingKey === key) {
      return;
    }
    const label = budgetSubject(item) + " " + budgetPeriodLabel(item);
    if (!confirm(m.budgets_reset_confirm({ label }))) {
      return;
    }
    this.resettingKey = key;
    const outcome = await sendAdminMutation(
      "/admin/budgets/reset-one",
      "POST",
      budgetResetOneBody(item),
      {
        label: "reset budget",
        errorFallback: m.budgets_reset_failed(),
        unavailableMessage: m.budgets_unavailable(),
      },
    );
    this.resettingKey = "";
    if (outcome.status === "stale") {
      return;
    }
    if (outcome.status === "unavailable") {
      this.budgetsAvailable = false;
      flash.error(outcome.error);
      return;
    }
    if (outcome.status === "error") {
      flash.error(outcome.error);
      return;
    }
    flash.success(m.budgets_reset_done());
    void this.fetchBudgets();
  }

  async deleteBudget(item) {
    if (!item) {
      return;
    }
    const key = budgetKey(item);
    if (this.deletingKey === key) {
      return;
    }
    const label = budgetSubject(item) + " " + budgetPeriodLabel(item);
    if (!confirm(m.budgets_delete_confirm({ label }))) {
      return;
    }
    this.deletingKey = key;
    const outcome = await sendAdminMutation(
      "/admin/budgets",
      "DELETE",
      budgetDeleteBody(item),
      {
        label: "delete budget",
        errorFallback: m.budgets_delete_failed(),
        unavailableMessage: m.budgets_unavailable(),
      },
    );
    this.deletingKey = "";
    if (outcome.status === "stale") {
      return;
    }
    if (outcome.status === "unavailable") {
      this.budgetsAvailable = false;
      flash.error(outcome.error);
      return;
    }
    if (outcome.status === "error") {
      flash.error(outcome.error);
      return;
    }
    this.budgets = normalizeBudgetListPayload(outcome.result.data);
    flash.success(m.budgets_deleted());
  }

  // Reset-all budgets: typed-confirmation dialog ("type reset"). The
  // trigger lives on the Settings page; the flow is exported here with the
  // rest of the budget logic so it can be reused from anywhere.
  openResetDialog() {
    confirmDialog.open({
      title: m.budgets_reset_all_title(),
      titleId: "budgetResetDialogTitle",
      inputId: "budget-reset-confirmation",
      requiredText: "reset",
      confirmLabel: m.budgets_reset_all(),
      icon: RotateCcw,
      dialogClass: "budget-reset-dialog",
      onConfirm: () => this.resetAllBudgets(),
    });
  }

  async resetAllBudgets() {
    if (this.resetAllLoading) {
      return;
    }
    this.resetAllLoading = true;
    const outcome = await sendAdminMutation(
      "/admin/budgets/reset",
      "POST",
      { confirmation: "reset" },
      {
        label: "reset budgets",
        errorFallback: m.budgets_reset_all_failed(),
        // Reset-all never had a dedicated 503 branch; keep 503 an error.
        unavailableStatuses: [],
      },
    );
    this.resetAllLoading = false;
    if (outcome.status === "stale") {
      return;
    }
    if (outcome.status !== "ok") {
      confirmDialog.error = outcome.error;
      return;
    }
    confirmDialog.close();
    flash.success(m.budgets_reset_all_done());
    if (router.page === "budgets") {
      void this.fetchBudgets();
    }
  }
}

export const budgetsStore = new BudgetsStore();
