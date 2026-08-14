// Model pricing overrides state for the Models page.

import { errorMessage, getJSON, sendJSON } from "$lib/api/client.js";
import { flash } from "$lib/stores/flash.svelte.js";
import * as m from "$lib/paraglide/messages.js";
import {
  GLOBAL_PRICING_SELECTOR,
  PRICE_FIELDS,
  availablePricingFieldOptions,
  buildPricingOverridePayload,
  clonePricing,
  effectivePreviewRows,
  findModelPricingOverrideView,
  modelPricingExactSelector,
  modelPricingModelWideSelector,
  modelRowPricingState,
  pricingFieldLabel,
  pricingRowsFromOverride,
  providerPricingOverrideSelector,
  selectedPricingFields,
} from "./pricingOverridesLogic.js";

class PricingOverridesStore {
  modelPricingOverridesAvailable = $state(true);
  modelPricingOverrideViews = $state([]);
  // Load and in-form errors only; mutation feedback goes through the
  // flash store.
  modelPricingOverrideError = $state("");
  modelPricingOverrideFormOpen = $state(false);
  modelPricingOverrideSubmitting = $state(false);
  modelPricingOverrideFormHasExistingOverride = $state(false);
  modelPricingOverrideFormDisplayName = $state("");
  modelPricingOverrideFormScope = $state("");
  modelPricingOverrideFormScopeOptions = $state([]);
  modelPricingOverrideFormRow = $state(null);
  modelPricingOverrideFormBasePricing = $state(null);
  modelPricingOverrideFormBasePricingSources = $state(null);
  modelPricingOverrideFormPreservedTiers = $state([]);
  modelPricingOverrideRows = $state([]);
  modelPricingOverrideForm = $state({ selector: "" });
  _modelPricingOverrideRowID = 0;

  pricingFieldOptions() {
    return PRICE_FIELDS;
  }

  pricingFieldLabel(field) {
    return pricingFieldLabel(field);
  }

  async fetchModelPricingOverrides() {
    this.modelPricingOverrideError = "";
    try {
      const result = await getJSON("/admin/model-pricing-overrides", {
        label: "model pricing overrides",
      });
      if (result.status === 503) {
        this.modelPricingOverridesAvailable = false;
        this.modelPricingOverrideViews = [];
        return;
      }
      if (result.stale) {
        return;
      }
      this.modelPricingOverridesAvailable = true;
      if (!result.ok) {
        this.modelPricingOverrideViews = [];
        return;
      }
      this.modelPricingOverrideViews = Array.isArray(result.data) ? result.data : [];
    } catch (e) {
      console.error("Failed to fetch model pricing overrides:", e);
      this.modelPricingOverrideViews = [];
      this.modelPricingOverrideError = m.models_pricing_load_failed();
    }
  }

  findModelPricingOverrideView(selector) {
    return findModelPricingOverrideView(this.modelPricingOverrideViews, selector);
  }

  hasGlobalPricingOverride() {
    return Boolean(this.findModelPricingOverrideView(GLOBAL_PRICING_SELECTOR));
  }

  hasProviderPricingOverride(group) {
    return Boolean(
      this.findModelPricingOverrideView(providerPricingOverrideSelector(group && group.provider_name)),
    );
  }

  hasModelPricingOverride(row) {
    return Boolean(this.findModelPricingOverrideView(modelPricingExactSelector(row)));
  }

  modelPricingButtonClass(hasOverride) {
    return hasOverride ? "table-action-btn-active" : "";
  }

  modelPricingButtonLabel(subject, hasOverride) {
    const value = String(subject || m.models_global_pricing());
    return hasOverride
      ? m.models_edit_action_override({ subject: value })
      : m.models_edit_action({ subject: value });
  }

  modelRowPricing(row) {
    return modelRowPricingState(row, this.modelPricingOverrideViews).pricing;
  }

  // ---- Editor ----

  openGlobalPricingOverrideEdit() {
    this.openModelPricingOverrideForm({
      displayName: m.models_pricing_global_scope(),
      selector: GLOBAL_PRICING_SELECTOR,
      scope: "global",
      scopeOptions: [
        { value: "global", label: m.models_pricing_global_scope(), selector: GLOBAL_PRICING_SELECTOR },
      ],
      row: null,
    });
  }

  openProviderPricingOverrideEdit(group) {
    const selector = providerPricingOverrideSelector(group && group.provider_name);
    if (!selector) {
      return;
    }
    this.openModelPricingOverrideForm({
      displayName: m.models_pricing_provider_scope({ provider: group.display_name || group.provider_name || selector }),
      selector,
      scope: "provider",
      scopeOptions: [{ value: "provider", label: m.models_pricing_provider(), selector }],
      row: null,
    });
  }

  openModelPricingOverrideEdit(row) {
    if (!row || row.is_alias) {
      return;
    }
    const exact = modelPricingExactSelector(row);
    const modelWide = modelPricingModelWideSelector(row);
    const scopeOptions = [{ value: "exact", label: m.models_pricing_exact_scope(), selector: exact }];
    if (modelWide && modelWide !== exact) {
      scopeOptions.push({ value: "model", label: m.models_pricing_model_scope(), selector: modelWide });
    }
    this.openModelPricingOverrideForm({
      displayName: row.display_name || exact,
      selector: exact,
      scope: "exact",
      scopeOptions,
      row,
    });
  }

  openModelPricingOverrideForm(options) {
    const opts = options || {};
    this.modelPricingOverrideFormOpen = true;
    this.modelPricingOverrideError = "";
    this.modelPricingOverrideFormDisplayName = opts.displayName || opts.selector || m.models_pricing_fallback_title();
    this.modelPricingOverrideFormScope = opts.scope || "";
    this.modelPricingOverrideFormScopeOptions = Array.isArray(opts.scopeOptions)
      ? opts.scopeOptions
      : [];
    this.modelPricingOverrideFormRow = opts.row || null;
    this.modelPricingOverrideForm = { selector: opts.selector || "" };
    this.loadModelPricingOverrideFormSelector(opts.selector || "");
  }

  loadModelPricingOverrideFormSelector(selector) {
    selector = String(selector || "").trim();
    const override = this.findModelPricingOverrideView(selector);
    this.modelPricingOverrideFormHasExistingOverride = Boolean(override);
    this.modelPricingOverrideRows = pricingRowsFromOverride(override, () =>
      this.nextModelPricingOverrideRowID(),
    );
    this.modelPricingOverrideFormPreservedTiers =
      override && override.pricing && Array.isArray(override.pricing.tiers)
        ? clonePricing(override.pricing.tiers)
        : [];
    if (
      this.modelPricingOverrideRows.length === 0 &&
      this.modelPricingOverrideFormPreservedTiers.length === 0
    ) {
      this.addModelPricingOverrideRow();
    }
    const row = this.modelPricingOverrideFormRow;
    const state = row
      ? modelRowPricingState(row, this.modelPricingOverrideViews, selector)
      : { pricing: {}, sources: {} };
    this.modelPricingOverrideFormBasePricing = state.pricing;
    this.modelPricingOverrideFormBasePricingSources = state.sources;
  }

  setModelPricingOverrideScope(scope) {
    this.modelPricingOverrideFormScope = scope;
    const option = this.modelPricingOverrideFormScopeOptions.find((item) => item.value === scope);
    if (!option) {
      return;
    }
    this.modelPricingOverrideForm.selector = option.selector;
    this.loadModelPricingOverrideFormSelector(option.selector);
  }

  nextModelPricingOverrideRowID() {
    this._modelPricingOverrideRowID = (this._modelPricingOverrideRowID || 0) + 1;
    return "pricing-row-" + this._modelPricingOverrideRowID;
  }

  availablePricingFieldOptions(row) {
    return availablePricingFieldOptions(this.modelPricingOverrideRows, row);
  }

  addModelPricingOverrideRow() {
    const selected = selectedPricingFields(this.modelPricingOverrideRows);
    const option = PRICE_FIELDS.find((item) => !selected.has(item.value)) || PRICE_FIELDS[0];
    if (!option) {
      return;
    }
    this.modelPricingOverrideRows.push({
      id: this.nextModelPricingOverrideRowID(),
      field: option.value,
      value: "",
    });
  }

  removeModelPricingOverrideRow(row) {
    this.modelPricingOverrideRows = this.modelPricingOverrideRows.filter(
      (item) => item.id !== row.id,
    );
    if (
      this.modelPricingOverrideRows.length === 0 &&
      this.modelPricingOverrideFormPreservedTiers.length === 0
    ) {
      this.addModelPricingOverrideRow();
    }
  }

  modelPricingOverridePayload() {
    return buildPricingOverridePayload(
      this.modelPricingOverrideRows,
      this.modelPricingOverrideFormPreservedTiers,
    );
  }

  modelPricingOverrideDraftPricing() {
    const payload = this.modelPricingOverridePayload();
    return payload && payload.pricing ? payload.pricing : {};
  }

  modelPricingEffectivePreviewRows() {
    return effectivePreviewRows(
      this.modelPricingOverrideFormBasePricing,
      this.modelPricingOverrideFormBasePricingSources,
      this.modelPricingOverrideDraftPricing(),
    );
  }

  closeModelPricingOverrideForm() {
    this.modelPricingOverrideFormOpen = false;
    this.modelPricingOverrideSubmitting = false;
    this.modelPricingOverrideError = "";
    this.modelPricingOverrideFormHasExistingOverride = false;
    this.modelPricingOverrideFormDisplayName = "";
    this.modelPricingOverrideFormScope = "";
    this.modelPricingOverrideFormScopeOptions = [];
    this.modelPricingOverrideFormRow = null;
    this.modelPricingOverrideFormBasePricing = null;
    this.modelPricingOverrideFormBasePricingSources = null;
    this.modelPricingOverrideFormPreservedTiers = [];
    this.modelPricingOverrideRows = [];
    this.modelPricingOverrideForm = { selector: "" };
  }

  async submitModelPricingOverrideForm() {
    const selector = String(this.modelPricingOverrideForm.selector || "").trim();
    if (!selector) {
      this.modelPricingOverrideError = m.models_pricing_selector_required();
      return;
    }
    const payload = this.modelPricingOverridePayload();
    if (payload.error) {
      this.modelPricingOverrideError = payload.error;
      return;
    }
    const requestPayload = { selector, ...payload };

    this.modelPricingOverrideSubmitting = true;
    this.modelPricingOverrideError = "";
    try {
      const result = await sendJSON("/admin/model-pricing-overrides", "PUT", requestPayload, {
        label: "model pricing override",
      });
      if (result.status === 503) {
        this.modelPricingOverridesAvailable = false;
        this.modelPricingOverrideError = m.models_pricing_feature_unavailable();
        return;
      }
      if (result.stale) {
        return;
      }
      if (!result.ok) {
        this.modelPricingOverrideError =
          result.status === 401
            ? m.common_authentication_required()
            : errorMessage(result, m.models_pricing_save_failed());
        return;
      }
      this.modelPricingOverridesAvailable = true;
      this.closeModelPricingOverrideForm();
      flash.success(m.models_pricing_saved());
      void this.fetchModelPricingOverrides();
    } catch (e) {
      console.error("Failed to save model pricing override:", e);
      this.modelPricingOverrideError = m.models_pricing_save_failed();
    } finally {
      this.modelPricingOverrideSubmitting = false;
    }
  }

  async deleteModelPricingOverride() {
    const selector = String(this.modelPricingOverrideForm.selector || "").trim();
    if (!selector || !this.modelPricingOverrideFormHasExistingOverride) {
      return;
    }
    if (!window.confirm(m.models_pricing_remove_confirm({ selector }))) {
      return;
    }

    this.modelPricingOverrideSubmitting = true;
    this.modelPricingOverrideError = "";
    try {
      const result = await sendJSON("/admin/model-pricing-overrides", "DELETE", { selector }, {
        label: "model pricing override",
      });
      if (result.status === 503) {
        this.modelPricingOverridesAvailable = false;
        this.modelPricingOverrideError = m.models_pricing_feature_unavailable();
        return;
      }
      if (result.status !== 404) {
        if (result.stale) {
          return;
        }
        if (!result.ok) {
          this.modelPricingOverrideError =
            result.status === 401
              ? m.common_authentication_required()
              : errorMessage(result, m.models_pricing_remove_failed());
          return;
        }
      }
      this.modelPricingOverridesAvailable = true;
      this.closeModelPricingOverrideForm();
      flash.success(m.models_pricing_removed());
      void this.fetchModelPricingOverrides();
    } catch (e) {
      console.error("Failed to delete model pricing override:", e);
      this.modelPricingOverrideError = m.models_pricing_remove_failed();
    } finally {
      this.modelPricingOverrideSubmitting = false;
    }
  }
}

export const pricingOverrides = new PricingOverridesStore();
