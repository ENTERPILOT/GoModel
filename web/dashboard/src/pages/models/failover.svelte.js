// Failover rules store (editor modal + generated-drafts modal).

import { getJSON, sendJSON } from "$lib/api/client.js";
import { flash } from "$lib/stores/flash.svelte.js";
import { runtimeConfig } from "$lib/stores/runtimeConfig.svelte.js";
import { confirmDialog } from "$lib/stores/confirm.svelte.js";
import * as m from "$lib/paraglide/messages.js";
import {
  qualifiedModelName,
  failoverPrimaryModel,
  failoverTargets,
  normalizeFailoverRules,
  failoverTargetLabel,
  failoverRuleStatus,
  findFailoverMapping,
  hasActiveFailoverMapping,
  failoverButtonClass,
  failoverButtonLabel,
  failoverFormTargets,
  splitFailoverTargets,
  failoverRulePayload,
  failoverDraftKey,
  buildDraftSelections,
  failoverDraftSelected,
  selectedFailoverDrafts,
  allFailoverDraftsSelected,
  failoverDraftSearchText,
  filterFailoverDrafts,
  failoverDraftPayload,
} from "./failover-logic.js";
import { Trash2 } from "lucide";

function emptyFailoverForm() {
  return {
    source: "",
    target_model: "",
    targets: [],
    enabled: true,
  };
}

class FailoverStore {
  failoverAvailable = $state(true);
  failoverRules = $state([]);
  failoverLoading = $state(false);
  failoverSaving = $state(false);
  failoverGenerating = $state(false);
  // Load, in-form, and in-drafts-modal errors; success feedback goes
  // through the flash store.
  failoverError = $state("");
  failoverGeneratedRules = $state([]);
  failoverDraftsOpen = $state(false);
  failoverDraftSelections = $state({});
  failoverDraftFilter = $state("");
  failoverDraftSaving = $state(false);
  failoverFormOpen = $state(false);
  failoverFormMode = $state("create");
  failoverFormManaged = $state(false);
  failoverForm = $state(emptyFailoverForm());

  failoverEnabled() {
    return runtimeConfig.booleanFlag("FAILOVER_ENABLED", true);
  }

  async fetchFailoverRules() {
    if (!this.failoverEnabled()) {
      this.failoverAvailable = false;
      this.failoverRules = [];
      this.failoverGeneratedRules = [];
      this.failoverDraftSelections = {};
      this.failoverDraftFilter = "";
      this.failoverDraftsOpen = false;
      this.failoverError = "";
      this.failoverLoading = false;
      return;
    }
    this.failoverLoading = true;
    this.failoverError = "";
    try {
      const result = await getJSON("/admin/failover", {
        label: "failover mappings",
      });
      if (result.status === 503) {
        this.failoverAvailable = false;
        this.failoverRules = [];
        return;
      }
      if (result.stale) {
        return;
      }
      this.failoverAvailable = true;
      if (!result.ok) {
        this.failoverRules = [];
        return;
      }
      this.failoverRules = normalizeFailoverRules(result.data);
    } catch (e) {
      console.error("Failed to fetch failover mappings:", e);
      this.failoverRules = [];
      this.failoverError = m.models_failover_load_failed();
    } finally {
      this.failoverLoading = false;
    }
  }

  resetFailoverForm() {
    this.failoverFormMode = "create";
    this.failoverFormManaged = false;
    this.failoverForm = emptyFailoverForm();
  }

  openFailoverCreate() {
    this.resetFailoverForm();
    this.failoverFormOpen = true;
    this.focusFailoverEditor();
  }

  openFailoverEdit(rule) {
    if (!rule) return;
    this.resetFailoverForm();
    this.failoverFormMode = "edit";
    this.failoverFormOpen = true;
    this.failoverFormManaged = Boolean(rule.managed);
    const source = this.failoverPrimaryModel(rule);
    const targets = this.failoverTargets(rule);
    this.failoverForm = {
      source,
      target_model: targets[0] || "",
      targets: targets.slice(1).map((model) => ({ model })),
      enabled: rule.enabled !== false,
    };
    this.focusFailoverEditor();
  }

  openFailoverForModel(row) {
    if (!row || row.is_alias) return;
    const source = this.qualifiedModelName(row);
    const existing = this.failoverRules.find(
      (rule) => this.failoverPrimaryModel(rule) === source,
    );
    if (existing) {
      this.openFailoverEdit(existing);
      return;
    }
    this.resetFailoverForm();
    this.failoverFormMode = "create";
    this.failoverFormOpen = true;
    this.failoverForm.source = source;
    this.focusFailoverEditor();
  }

  closeFailoverForm() {
    this.failoverFormOpen = false;
  }

  closeFailoverDraftsModal() {
    if (this.failoverDraftSaving) return;
    this.failoverDraftsOpen = false;
  }

  failoverFormTargets() {
    return failoverFormTargets(this.failoverForm);
  }

  setFailoverFormTargets(targets) {
    const next = splitFailoverTargets(targets);
    this.failoverForm.target_model = next.target_model;
    this.failoverForm.targets = next.targets;
  }

  addFailoverTarget() {
    if (!Array.isArray(this.failoverForm.targets)) {
      this.failoverForm.targets = [];
    }
    this.failoverForm.targets.push({ model: "" });
    this.focusFailoverEditor();
  }

  removeFailoverTarget(index) {
    if (!Array.isArray(this.failoverForm.targets)) {
      this.failoverForm.targets = [];
      return;
    }
    this.failoverForm.targets.splice(index, 1);
  }

  removePrimaryFailoverTarget() {
    const rows = Array.isArray(this.failoverForm.targets)
      ? this.failoverForm.targets
      : [];
    if (rows.length > 0) {
      const next = rows.shift();
      this.failoverForm.target_model = next && next.model ? next.model : "";
      this.failoverForm.targets = rows;
      return;
    }
    this.failoverForm.target_model = "";
  }

  failoverRulePayload() {
    return failoverRulePayload(this.failoverForm);
  }

  async submitFailoverForm() {
    if (this.failoverSaving || this.failoverGenerating || this.failoverFormManaged)
      return;
    const payload = this.failoverRulePayload();
    if (!payload.primary_model) {
      this.failoverError = m.models_failover_primary_required();
      return;
    }
    if (payload.enabled && payload.fallback_models.length === 0) {
      this.failoverError = m.models_failover_target_required();
      return;
    }
    this.failoverSaving = true;
    this.failoverError = "";
    try {
      const result = await sendJSON("/admin/failover", "PUT", payload, {
        label: "failover mapping",
      });
      if (result.stale) {
        return;
      }
      if (!result.ok) {
        this.failoverError = m.models_failover_save_failed();
        return;
      }
      flash.success(m.models_failover_mapping_saved());
      this.closeFailoverForm();
      void this.fetchFailoverRules();
    } catch (e) {
      console.error("Failed to save failover mapping:", e);
      this.failoverError = m.models_failover_save_failed();
    } finally {
      this.failoverSaving = false;
    }
  }

  async deleteFailoverRule(rule) {
    const source = String(
      (rule && this.failoverPrimaryModel(rule)) || this.failoverForm.source || "",
    ).trim();
    if (!source || this.failoverSaving || this.failoverGenerating) return;
    if (!confirm(m.models_failover_remove_confirm({ source }))) return;
    this.failoverSaving = true;
    this.failoverError = "";
    try {
      const result = await sendJSON(
        "/admin/failover",
        "DELETE",
        { primary_model: source },
        { label: "failover mapping" },
      );
      if (result.stale) {
        return;
      }
      if (!result.ok) {
        this.failoverError = m.models_failover_remove_failed();
        return;
      }
      flash.success(m.models_failover_removed());
      this.closeFailoverForm();
      void this.fetchFailoverRules();
    } catch (e) {
      console.error("Failed to remove failover mapping:", e);
      this.failoverError = m.models_failover_remove_failed();
    } finally {
      this.failoverSaving = false;
    }
  }

  async generateFailoverForForm() {
    if (this.failoverGenerating || this.failoverSaving || this.failoverFormManaged)
      return;
    const source = String(this.failoverForm.source || "").trim();
    if (!source) {
      this.failoverError = m.models_failover_primary_required();
      return;
    }
    this.failoverGenerating = true;
    this.failoverError = "";
    try {
      const result = await sendJSON(
        "/admin/failover/generate",
        "POST",
        { primary_model: source },
        { label: "failover generation" },
      );
      if (result.stale) {
        return;
      }
      if (!result.ok) {
        this.failoverError = m.models_failover_generate_failed();
        return;
      }
      const suggestions = normalizeFailoverRules(result.data);
      const suggestion =
        suggestions.find(
          (rule) => this.failoverPrimaryModel(rule) === source,
        ) ||
        suggestions[0] ||
        null;
      const targets = this.failoverTargets(suggestion);
      if (targets.length === 0) {
        this.failoverError =
          m.models_failover_no_suggestions();
        return;
      }
      this.setFailoverFormTargets(targets);
      flash.success(m.models_failover_generated({ count: targets.length }));
      this.focusFailoverEditor();
    } catch (e) {
      console.error("Failed to generate failover mapping:", e);
      this.failoverError = m.models_failover_generate_failed();
    } finally {
      this.failoverGenerating = false;
    }
  }

  openFailoverResetDialog() {
    confirmDialog.open({
      title: m.models_failover_reset_title(),
      titleId: "failoverResetDialogTitle",
      inputId: "failover-reset-confirmation",
      message: m.models_failover_reset_message(),
      requiredText: "remove",
      confirmLabel: m.models_failover_reset_confirm(),
      icon: Trash2,
      dialogClass: "budget-reset-dialog",
      onConfirm: async () => {
        await this.resetFailoverRules();
        // Surface the failure inside the dialog, which stays open until
        // the removal succeeds.
        if (this.failoverError) {
          confirmDialog.error = this.failoverError;
        }
      },
    });
  }

  async resetFailoverRules() {
    if (this.failoverSaving) return;
    this.failoverSaving = true;
    this.failoverError = "";
    try {
      const result = await sendJSON("/admin/failover/reset", "POST", undefined, {
        label: "failover removal",
      });
      if (result.stale) {
        return;
      }
      if (!result.ok) {
        this.failoverError = m.models_failover_remove_all_failed();
        return;
      }
      this.failoverRules = normalizeFailoverRules(result.data);
      this.failoverGeneratedRules = [];
      this.failoverDraftSelections = {};
      this.failoverDraftFilter = "";
      this.failoverDraftsOpen = false;
      flash.success(m.models_failover_remove_all_success());
      confirmDialog.close();
    } catch (e) {
      console.error("Failed to remove failover mappings:", e);
      this.failoverError = m.models_failover_remove_all_failed();
    } finally {
      this.failoverSaving = false;
    }
  }

  async generateFailoverRules() {
    if (this.failoverGenerating || this.failoverDraftSaving) return;
    this.failoverGenerating = true;
    this.failoverError = "";
    this.failoverGeneratedRules = [];
    this.failoverDraftSelections = {};
    this.failoverDraftFilter = "";
    this.failoverDraftsOpen = true;
    try {
      const result = await sendJSON("/admin/failover/generate", "POST", undefined, {
        label: "failover generation",
      });
      if (result.stale) {
        return;
      }
      if (!result.ok) {
        this.failoverError = m.models_failover_generate_all_failed();
        return;
      }
      this.failoverGeneratedRules = normalizeFailoverRules(result.data);
      this.selectAllFailoverDrafts(this.failoverGeneratedRules);
      // No message here: the drafts modal itself shows the generated list
      // (or its empty state).
    } catch (e) {
      console.error("Failed to generate failover mappings:", e);
      this.failoverError = m.models_failover_generate_all_failed();
    } finally {
      this.failoverGenerating = false;
    }
  }

  failoverDraftKey(rule) {
    return failoverDraftKey(rule);
  }

  selectAllFailoverDrafts(rules) {
    this.failoverDraftSelections = buildDraftSelections(rules);
  }

  failoverDraftSelected(rule) {
    return failoverDraftSelected(this.failoverDraftSelections, rule);
  }

  setFailoverDraftSelected(rule, selected) {
    const key = this.failoverDraftKey(rule);
    if (!key) return;
    this.failoverDraftSelections = {
      ...this.failoverDraftSelections,
      [key]: Boolean(selected),
    };
  }

  selectedFailoverDrafts() {
    return selectedFailoverDrafts(
      this.failoverGeneratedRules,
      this.failoverDraftSelections,
    );
  }

  selectedFailoverDraftCount() {
    return this.selectedFailoverDrafts().length;
  }

  failoverDraftCountLabel() {
    return m.models_failover_selected_count({
      selected: this.selectedFailoverDraftCount(),
      total: this.failoverGeneratedRules.length,
    });
  }

  allFailoverDraftsSelected() {
    return allFailoverDraftsSelected(
      this.failoverGeneratedRules,
      this.failoverDraftSelections,
    );
  }

  toggleAllFailoverDrafts() {
    if (
      this.failoverDraftSaving ||
      this.failoverGenerating ||
      this.failoverGeneratedRules.length === 0
    )
      return;
    if (this.allFailoverDraftsSelected()) {
      this.failoverDraftSelections = {};
      return;
    }
    this.selectAllFailoverDrafts(this.failoverGeneratedRules);
  }

  failoverDraftSearchText(rule) {
    return failoverDraftSearchText(rule);
  }

  filteredFailoverDrafts() {
    return filterFailoverDrafts(this.failoverGeneratedRules, this.failoverDraftFilter);
  }

  failoverDraftPayload(rule) {
    return failoverDraftPayload(rule);
  }

  async saveSelectedFailoverDrafts() {
    if (this.failoverDraftSaving || this.failoverGenerating) return;
    const drafts = this.selectedFailoverDrafts();
    if (drafts.length === 0) {
      this.failoverError = m.models_failover_select_required();
      return;
    }
    this.failoverDraftSaving = true;
    this.failoverError = "";
    try {
      for (const draft of drafts) {
        const payload = this.failoverDraftPayload(draft);
        if (!payload.primary_model || payload.fallback_models.length === 0) {
          this.failoverError = m.models_failover_draft_invalid();
          return;
        }
        const result = await sendJSON("/admin/failover", "PUT", payload, {
          label: "failover mapping",
        });
        if (result.stale) {
          return;
        }
        if (!result.ok) {
          this.failoverError = m.models_failover_save_failed();
          return;
        }
      }
      flash.success(m.models_failover_saved({ count: drafts.length }));
      this.failoverDraftsOpen = false;
      this.failoverGeneratedRules = [];
      this.failoverDraftSelections = {};
      this.failoverDraftFilter = "";
      void this.fetchFailoverRules();
    } catch (e) {
      console.error("Failed to save generated failover mappings:", e);
      this.failoverError = m.models_failover_save_all_failed();
    } finally {
      this.failoverDraftSaving = false;
    }
  }

  // Refocus the editor's first focusable field.
  // The Modal atom autofocuses [data-modal-autofocus] on open; this handles
  // the post-open cases (add target, generate).
  focusFailoverEditor() {
    setTimeout(() => {
      const editor = document.querySelector("[data-failover-editor]");
      const field =
        editor && editor.querySelector
          ? editor.querySelector(
              "[data-modal-autofocus], input:not([disabled]), textarea:not([disabled]), button:not([disabled])",
            )
          : null;
      if (field && typeof field.focus === "function") {
        field.focus({ preventScroll: true });
      }
    }, 0);
  }

  failoverTargetLabel(rule) {
    return failoverTargetLabel(rule);
  }

  failoverPrimaryModel(rule) {
    return failoverPrimaryModel(rule);
  }

  failoverTargets(rule) {
    return failoverTargets(rule);
  }

  findFailoverMapping(source) {
    return findFailoverMapping(this.failoverRules, source);
  }

  hasActiveFailoverMapping(row) {
    return hasActiveFailoverMapping(this.failoverRules, row);
  }

  failoverButtonClass(row) {
    return failoverButtonClass(this.failoverRules, row);
  }

  failoverButtonLabel(row) {
    return failoverButtonLabel(this.failoverRules, row);
  }

  normalizeFailoverRules(payload) {
    return normalizeFailoverRules(payload);
  }

  failoverRuleStatus(rule) {
    return failoverRuleStatus(rule);
  }

  qualifiedModelName(model) {
    return qualifiedModelName(model);
  }
}

export const failover = new FailoverStore();
