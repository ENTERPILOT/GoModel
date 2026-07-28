// State module for the Providers (provider credentials) page: networking +
// editor state on Svelte 5 runes.
//
// Endpoints:
//   GET    /admin/provider-credentials          — list rows
//   GET    /admin/provider-credentials/types    — types + their credential forms
//   PUT    /admin/provider-credentials          — upsert one row
//   DELETE /admin/provider-credentials/{name}   — delete one row

import { errorPayloadMessage } from "$lib/api/client.ts";
import { loadAdminList, sendAdminMutation } from "$lib/api/adminCrud.ts";
import { confirmDialog } from "$lib/stores/confirm.svelte.ts";
import { flash } from "$lib/stores/flash.svelte.ts";
import { modelsStore } from "$lib/stores/models.svelte.ts";
import {
  defaultProviderCredentialForm,
  filterProviderCredentials,
  providerCredentialFormFields,
  providerCredentialRowToForm,
  providerCredentialSchema,
  resetProviderCredentialFields,
  validateProviderCredentialForm,
  buildProviderCredentialPayload,
} from "./providersConfigLogic.js";

class ProvidersConfigState {
  rows = $state([]);
  available = $state(true);
  loading = $state(false);
  // Load failures, and the editor errors that belong to no single field;
  // anything blamed on one field goes to fieldErrors, and successful
  // mutations go through the flash store.
  error = $state("");
  filter = $state("");

  formOpen = $state(false);
  formSubmitting = $state(false);
  formMode = $state("create");
  advancedOpen = $state(false);
  form = $state(defaultProviderCredentialForm());
  // Per-field validation messages keyed by credential field name, from local
  // checks or from a rejected save's `param`. focusField names the input the
  // editor should move to; it is cleared once the editor has done so.
  fieldErrors = $state({});
  focusField = $state("");

  deletingName = $state("");
  deleteSubmitting = $state(false);

  // Credential schemas: one per constructible provider type, naming the
  // fields that type accepts.
  types = $state([]);
  typesLoaded = $state(false);

  #controller = null;

  get filteredRows() {
    return filterProviderCredentials(this.rows, this.filter);
  }

  // schema is the selected type's credential form, or null while the schemas
  // load (the editor then shows every field rather than none).
  get schema() {
    return providerCredentialSchema(this.types, this.form.type);
  }

  // formFields are the credential fields to render. Until a type is picked
  // there is nothing to show: which fields exist is the type's answer.
  get formFields() {
    if (!String(this.form.type || "").trim()) {
      return { primary: [], advanced: [] };
    }
    const schema = this.schema;
    return providerCredentialFormFields(schema, schema && schema.default_base_url);
  }

  // Schema load failures stay silent: the editor falls back to showing every
  // field, and the list request already reports availability problems.
  async fetchTypes() {
    const outcome = await loadAdminList("/admin/provider-credentials/types", {
      label: "provider credential types",
    });
    if (outcome.status !== "ok") {
      return;
    }
    this.types = outcome.items;
    this.typesLoaded = true;
  }

  async fetchPage() {
    if (this.#controller) this.#controller.abort();
    const controller = new AbortController();
    this.#controller = controller;
    this.loading = true;
    this.error = "";
    try {
      const outcome = await loadAdminList("/admin/provider-credentials", {
        label: "provider credentials",
        errorFallback: "Failed to load provider credentials.",
        unavailableStatuses: [503, 404],
        options: { signal: controller.signal },
      });
      if (outcome.status === "stale" || controller.signal.aborted) {
        return;
      }
      if (outcome.status === "unavailable") {
        this.available = false;
        this.rows = [];
        return;
      }
      if (outcome.status === "error") {
        // A gateway response proves the endpoint exists; a network failure
        // (result === null) leaves the availability flag as-is.
        if (outcome.result) {
          this.available = true;
        }
        this.rows = [];
        this.error = outcome.error;
        return;
      }
      this.available = true;
      this.rows = outcome.items;
      if (!this.typesLoaded) {
        await this.fetchTypes();
      }
    } finally {
      if (this.#controller === controller) {
        this.#controller = null;
        this.loading = false;
      }
    }
  }

  // #resetForm puts the editor in a known state: no leftover values, errors,
  // or open disclosure from the provider edited before this one.
  #resetForm(mode, form) {
    this.formMode = mode;
    this.form = form;
    this.advancedOpen = false;
    this.error = "";
    this.fieldErrors = {};
    this.focusField = "";
  }

  openCreate() {
    this.#resetForm("create", defaultProviderCredentialForm());
    this.formOpen = true;
    if (!this.typesLoaded) {
      this.fetchTypes();
    }
  }

  openEdit(row) {
    if (!row || row.managed) {
      return;
    }
    this.#resetForm("edit", providerCredentialRowToForm(row));
    this.formOpen = true;
    if (!this.typesLoaded) {
      this.fetchTypes();
    }
  }

  closeForm() {
    this.formOpen = false;
    this.#resetForm("create", defaultProviderCredentialForm());
  }

  // selectType reacts to a change of provider type: the form's shape changes
  // with it, so complaints and values belonging to the previous type go, and a
  // type that authenticates with a key opens with a row ready to paste into
  // rather than an "Add key" button the operator has to find first.
  //
  // Only creating clears: an existing provider's type is immutable, and there
  // the payload's echo of unrendered values is what keeps a stored setting the
  // form cannot show from being wiped.
  selectType() {
    this.fieldErrors = {};
    const fields = this.formFields;
    if (this.formMode === "create") {
      this.form = resetProviderCredentialFields(this.form, fields);
    }
    const apiKeys = fields.primary.find((field) => field.name === "api_keys");
    if (apiKeys && apiKeys.required && this.form.api_keys.length === 0) {
      this.form.api_keys = [{ value: "" }];
    }
  }

  // clearFieldError drops a field's message as soon as the operator edits it,
  // so a stale complaint never outlives the value it was about.
  clearFieldError(field) {
    if (this.fieldErrors[field] === undefined) {
      return;
    }
    const { [field]: _cleared, ...rest } = this.fieldErrors;
    this.fieldErrors = rest;
  }

  addApiKeyRow() {
    this.form.api_keys.push({ value: "" });
    this.clearFieldError("api_keys");
  }

  removeApiKeyRow(index) {
    this.form.api_keys.splice(index, 1);
    this.clearFieldError("api_keys");
  }

  // #reportSaveError routes a rejected save to the input that caused it. The
  // gateway names the offending credential field in `error.param`, using the
  // same names as the schema, so a server-side rule (which field combinations
  // actually authenticate) reads like a local validation error.
  #reportSaveError(data) {
    const message = errorPayloadMessage(data, "Failed to save provider credential.");
    const param = String(
      (data && data.error && typeof data.error === "object" && data.error.param) || "",
    ).trim();
    if (param && this.#isFormField(param)) {
      this.fieldErrors = { ...this.fieldErrors, [param]: message };
      this.error = "";
      this.#revealInvalidFields();
      return;
    }
    this.error = message;
  }

  #isFormField(name) {
    if (name === "name" || name === "type") {
      return true;
    }
    const { primary, advanced } = this.formFields;
    return [...primary, ...advanced].some((field) => field.name === name);
  }

  // #revealInvalidFields makes sure a complaint is visible: a field folded
  // into "Advanced settings" would otherwise be flagged behind a closed
  // disclosure.
  #revealInvalidFields() {
    const invalid = Object.keys(this.fieldErrors);
    if (invalid.length === 0) {
      return;
    }
    const { primary, advanced } = this.formFields;
    if (advanced.some((field) => invalid.includes(field.name))) {
      this.advancedOpen = true;
    }
    const order = ["type", "name", ...primary.map((f) => f.name), ...advanced.map((f) => f.name)];
    this.focusField = order.find((name) => invalid.includes(name)) || invalid[0];
  }

  // Hot-registering a provider changes the live model inventory, so refresh
  // the shared models store after every successful upsert/delete.
  #refreshInventory() {
    modelsStore.fetchModels();
    modelsStore.fetchCategories();
  }

  async submitForm() {
    const schema = this.schema;
    const fieldErrors = validateProviderCredentialForm(
      this.form,
      this.formMode,
      this.rows,
      schema,
    );
    if (Object.keys(fieldErrors).length > 0) {
      this.fieldErrors = fieldErrors;
      this.error = "";
      this.#revealInvalidFields();
      return;
    }

    const payload = buildProviderCredentialPayload(this.form, schema);
    this.error = "";
    this.fieldErrors = {};
    this.formSubmitting = true;
    try {
      const outcome = await sendAdminMutation("/admin/provider-credentials", "PUT", payload, {
        label: "save provider credential",
        errorFallback: "Failed to save provider credential.",
        unavailableMessage: "Provider credential management is unavailable.",
      });
      if (outcome.status === "stale") {
        return;
      }
      if (outcome.status === "unavailable") {
        this.available = false;
        this.error = outcome.error;
        return;
      }
      if (outcome.status === "error") {
        // Non-401 gateway rejections carry `error.param`, so route them to
        // the offending field; everything else lands in the form error slot.
        if (outcome.result && outcome.result.status !== 401) {
          this.#reportSaveError(outcome.result.data);
        } else {
          this.error = outcome.error;
        }
        return;
      }

      flash.success('Provider "' + payload.name + '" saved.');
      this.closeForm();
      this.#refreshInventory();
      void this.fetchPage();
    } finally {
      this.formSubmitting = false;
    }
  }

  async performDelete(name) {
    this.deleteSubmitting = true;
    this.deletingName = name;
    try {
      const outcome = await sendAdminMutation(
        "/admin/provider-credentials/" + encodeURIComponent(name),
        "DELETE",
        undefined,
        {
          label: "delete provider credential",
          errorFallback: "Failed to delete provider credential.",
          unavailableMessage: "Provider credential management is unavailable.",
        },
      );
      if (outcome.status === "stale") {
        return;
      }
      if (outcome.status === "unavailable") {
        this.available = false;
        confirmDialog.error = outcome.error;
        return;
      }
      if (outcome.status === "error") {
        confirmDialog.error = outcome.error;
        return;
      }

      flash.success('Provider "' + name + '" deleted.');
      confirmDialog.close();
      if (this.formOpen && this.form.name === name) {
        this.closeForm();
      }
      this.#refreshInventory();
      void this.fetchPage();
    } finally {
      this.deleteSubmitting = false;
      this.deletingName = "";
    }
  }

  // requestDelete drives the shared typed-confirmation dialog rather than
  // window.confirm: deleting a provider credential can silently break every
  // request routed to it, so the operator must type the provider name back.
  requestDelete(name) {
    const target = String(name || "").trim();
    if (!target || this.deleteSubmitting) {
      return;
    }
    const row = (this.rows || []).find(
      (item) => String((item && item.name) || "").trim() === target,
    );
    if (row && row.managed) {
      return;
    }
    confirmDialog.open({
      title: "Delete Provider",
      titleId: "providerCredentialDeleteDialogTitle",
      inputId: "provider-credential-delete-confirmation",
      message:
        'Type "' +
        target +
        '" to permanently delete this provider credential. Requests routed to it will fail until it is reconfigured.',
      requiredText: target,
      confirmLabel: "Delete Provider",
      icon: "trash-2",
      dialogClass: "budget-reset-dialog",
      onConfirm: () => this.performDelete(target),
    });
  }
}

export const providersConfig = new ProvidersConfigState();
