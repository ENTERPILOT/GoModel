// Budgets page state (the budgets-page portion; budget settings live with
// the Settings page). Talks to the /admin/budgets endpoints.

import { errorMessage, getJSON, sendJSON } from "$lib/api/client.js";
import { flash } from "$lib/stores/flash.svelte.js";
import { runtimeConfig } from "$lib/stores/runtimeConfig.svelte.js";
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
    try {
      const result = await getJSON("/admin/budgets", { label: "budgets" });
      if (result.status === 503) {
        this.budgetsAvailable = false;
        this.budgets = [];
        return;
      }
      if (result.stale) {
        return;
      }
      this.budgetsAvailable = true;
      if (!result.ok) {
        this.error = "Unable to load budgets.";
        return;
      }
      this.budgets = normalizeBudgetListPayload(result.data);
    } catch (e) {
      console.error("Failed to fetch budgets:", e);
      this.budgets = [];
      this.error = "Unable to load budgets.";
    } finally {
      this.loading = false;
    }
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
        source: String(item.source || "manual"),
      };
    } else {
      this.form = defaultBudgetForm();
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
      this.form.scope === "label" ? String(value ?? "") : budgetInputUserPath(value);
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
    try {
      const result = await sendJSON(
        "/admin/budgets",
        "PUT",
        budgetPutBody(payload),
        { label: "budget" },
      );
      if (result.status === 503) {
        this.budgetsAvailable = false;
        this.formError = "Budget management is unavailable.";
        return;
      }
      if (result.stale) {
        return;
      }
      if (!result.ok) {
        this.formError = errorMessage(result, "Unable to save budget.");
        return;
      }
      this.closeForm();
      // Flash before the refetch so feedback is instant.
      flash.success("Budget saved.");
      void this.fetchBudgets();
    } catch (e) {
      console.error("Failed to save budget:", e);
      this.formError = "Unable to save budget.";
    } finally {
      this.formSubmitting = false;
    }
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
    if (!confirm('Reset budget "' + label + '"?')) {
      return;
    }
    this.resettingKey = key;
    try {
      const result = await sendJSON(
        "/admin/budgets/reset-one",
        "POST",
        budgetResetOneBody(item),
        { label: "budget reset" },
      );
      if (result.status === 503) {
        this.budgetsAvailable = false;
        flash.error("Budget management is unavailable.");
        return;
      }
      if (result.stale) {
        return;
      }
      if (!result.ok) {
        flash.error(errorMessage(result, "Unable to reset budget."));
        return;
      }
      flash.success("Budget reset.");
      void this.fetchBudgets();
    } catch (e) {
      console.error("Failed to reset budget:", e);
      flash.error("Unable to reset budget.");
    } finally {
      this.resettingKey = "";
    }
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
    if (!confirm('Delete budget "' + label + '"? This cannot be undone.')) {
      return;
    }
    this.deletingKey = key;
    try {
      const result = await sendJSON(
        "/admin/budgets",
        "DELETE",
        budgetDeleteBody(item),
        { label: "budget delete" },
      );
      if (result.status === 503) {
        this.budgetsAvailable = false;
        flash.error("Budget management is unavailable.");
        return;
      }
      if (result.stale) {
        return;
      }
      if (!result.ok) {
        flash.error(errorMessage(result, "Unable to delete budget."));
        return;
      }
      this.budgets = normalizeBudgetListPayload(result.data);
      flash.success("Budget deleted.");
    } catch (e) {
      console.error("Failed to delete budget:", e);
      flash.error("Unable to delete budget.");
    } finally {
      this.deletingKey = "";
    }
  }

  // Reset-all budgets: typed-confirmation dialog ("type reset"). The
  // trigger lives on the Settings page; the flow is exported here with the
  // rest of the budget logic so it can be reused from anywhere.
  openResetDialog() {
    confirmDialog.open({
      title: "Reset Budgets",
      titleId: "budgetResetDialogTitle",
      inputId: "budget-reset-confirmation",
      requiredText: "reset",
      confirmLabel: "Reset All Budgets",
      icon: "rotate-ccw",
      dialogClass: "budget-reset-dialog",
      onConfirm: () => this.resetAllBudgets(),
    });
  }

  async resetAllBudgets() {
    if (this.resetAllLoading) {
      return;
    }
    this.resetAllLoading = true;
    try {
      const result = await sendJSON(
        "/admin/budgets/reset",
        "POST",
        { confirmation: "reset" },
        { label: "budget reset" },
      );
      if (result.stale) {
        return;
      }
      if (!result.ok) {
        confirmDialog.error = "Unable to reset budgets.";
        return;
      }
      confirmDialog.close();
      flash.success("Budgets reset.");
      if (router.page === "budgets") {
        void this.fetchBudgets();
      }
    } catch (e) {
      console.error("Failed to reset budgets:", e);
      confirmDialog.error = "Unable to reset budgets.";
    } finally {
      this.resetAllLoading = false;
    }
  }
}

export const budgetsStore = new BudgetsStore();
