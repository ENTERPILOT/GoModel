// Guardrails page state: definitions list + schema-driven editor form.
// Handles the /admin/guardrails endpoints, 503 "feature unavailable"
// responses, and the notice/error flow. The request guard ladder
// (stale → unavailable → error) lives in $lib/api/adminCrud.js.

import { loadAdminList, sendAdminMutation } from "$lib/api/adminCrud.js";
import { flash } from "$lib/stores/flash.svelte.js";
import {
  buildGuardrailPayload,
  defaultGuardrailConfig,
  defaultGuardrailForm,
  defaultGuardrailType,
  filterGuardrails,
  guardrailArrayFieldSelected,
  guardrailFieldValue,
  guardrailTypeFields,
  guardrailTypeLabel,
  normalizeGuardrailConfig,
  resolvedGuardrailType,
  setGuardrailFieldValue,
  toggleGuardrailArrayValue,
} from "./guardrails-logic.js";

class GuardrailsStore {
  guardrails = $state([]);
  types = $state([]);
  available = $state(true);
  loading = $state(false);
  typesLoading = $state(false);
  // Load and in-form errors only; mutation feedback goes through the
  // flash store.
  error = $state("");
  filter = $state("");
  formOpen = $state(false);
  formSubmitting = $state(false);
  deletingName = $state("");
  formMode = $state("create");
  formOriginalName = $state("");
  form = $state({
    name: "",
    type: "",
    description: "",
    user_path: "",
    config: {},
  });

  get filtered() {
    return filterGuardrails(this.guardrails, this.filter);
  }

  typeLabel(type) {
    return guardrailTypeLabel(this.types, type);
  }

  typeFields(type) {
    return guardrailTypeFields(this.types, type);
  }

  fieldValue(field) {
    return guardrailFieldValue(this.form && this.form.config, field);
  }

  setFieldValue(field, value) {
    this.form = {
      ...this.form,
      config: setGuardrailFieldValue(this.form.config, field, value),
    };
  }

  arrayFieldSelected(field, optionValue) {
    return guardrailArrayFieldSelected(
      this.form && this.form.config,
      field,
      optionValue,
    );
  }

  toggleArrayFieldValue(field, optionValue, checked) {
    this.form = {
      ...this.form,
      config: toggleGuardrailArrayValue(
        this.form.config,
        field,
        optionValue,
        checked,
      ),
    };
  }

  openCreate() {
    this.formMode = "create";
    this.formOriginalName = "";
    this.error = "";
    this.form = defaultGuardrailForm(this.types, defaultGuardrailType(this.types));
    this.formOpen = true;
  }

  openEdit(guardrail) {
    const resolvedType = resolvedGuardrailType(
      this.types,
      guardrail && guardrail.type,
    );
    this.formMode = "edit";
    this.formOriginalName = String((guardrail && guardrail.name) || "").trim();
    this.error = "";
    this.form = {
      name: this.formOriginalName,
      type: resolvedType,
      description: String((guardrail && guardrail.description) || "").trim(),
      user_path: String((guardrail && guardrail.user_path) || "").trim(),
      config: normalizeGuardrailConfig(
        this.types,
        guardrail && guardrail.config,
        resolvedType,
      ),
    };
    this.formOpen = true;
  }

  closeForm() {
    this.formOpen = false;
    this.formMode = "create";
    this.formOriginalName = "";
    this.error = "";
    this.form = defaultGuardrailForm(this.types, defaultGuardrailType(this.types));
  }

  // changeType resolves the selected type and resets the config to that
  // type's defaults.
  changeType(type) {
    const resolvedType = resolvedGuardrailType(this.types, type);
    this.form = {
      ...this.form,
      type: resolvedType,
      config: defaultGuardrailConfig(this.types, resolvedType),
    };
  }

  async fetchTypes() {
    this.typesLoading = true;
    try {
      const outcome = await loadAdminList("/admin/guardrails/types", {
        label: "guardrail types",
      });
      if (outcome.status === "stale") return;
      if (outcome.status === "unavailable") {
        this.available = false;
        this.types = [];
        return;
      }
      // Only a real gateway response proves the feature is back — a thrown
      // request (offline, DNS) must not undo an earlier 503 unavailable.
      if (outcome.result) {
        this.available = true;
      }
      this.types = outcome.items;
      if (outcome.status === "error") {
        this.error = outcome.error;
        return;
      }
      const resolvedType = resolvedGuardrailType(this.types, this.form.type);
      this.form = {
        ...this.form,
        type: resolvedType,
        config: normalizeGuardrailConfig(
          this.types,
          this.form.config,
          resolvedType,
        ),
      };
    } finally {
      this.typesLoading = false;
    }
  }

  async fetchGuardrails() {
    this.loading = true;
    this.error = "";
    try {
      const outcome = await loadAdminList("/admin/guardrails", {
        label: "guardrails",
      });
      if (outcome.status === "stale") return;
      if (outcome.status === "unavailable") {
        this.available = false;
        this.guardrails = [];
        return;
      }
      // Same offline guard as fetchTypes: only trust an answered request.
      if (outcome.result) {
        this.available = true;
      }
      this.guardrails = outcome.items;
      this.error = outcome.error;
    } finally {
      this.loading = false;
    }
  }

  async fetchPage() {
    await Promise.all([this.fetchTypes(), this.fetchGuardrails()]);
  }

  async submitForm() {
    const name = String(this.form.name || "").trim();
    const type = String(this.form.type || "").trim();
    if (!name) {
      this.error = "Name is required.";
      return;
    }
    if (!type) {
      this.error = "Type is required.";
      return;
    }

    this.error = "";
    this.formSubmitting = true;

    const payload = buildGuardrailPayload(this.form);

    try {
      const outcome = await sendAdminMutation("/admin/guardrails", "PUT", payload, {
        label: "save guardrail",
        errorFallback: "Failed to save guardrail.",
        unavailableMessage: "Guardrails feature is unavailable.",
      });
      if (outcome.status === "stale") return;
      if (outcome.status === "unavailable") {
        this.available = false;
        this.error = outcome.error;
        return;
      }
      if (outcome.status === "error") {
        this.error = outcome.error;
        return;
      }

      flash.success('Guardrail "' + name + '" saved.');
      this.closeForm();
      void this.fetchGuardrails();
    } finally {
      this.formSubmitting = false;
    }
  }

  async deleteGuardrail(guardrail) {
    const name = String((guardrail && guardrail.name) || "").trim();
    if (!name || this.deletingName) {
      return;
    }
    if (
      !window.confirm(
        'Delete guardrail "' +
          name +
          '"? Workflows that still reference it must be updated first.',
      )
    ) {
      return;
    }

    this.deletingName = name;

    try {
      const outcome = await sendAdminMutation(
        "/admin/guardrails",
        "DELETE",
        { name },
        {
          label: "delete guardrail",
          errorFallback: "Failed to delete guardrail.",
          unavailableMessage: "Guardrails feature is unavailable.",
        },
      );
      if (outcome.status === "stale") return;
      if (outcome.status === "unavailable") {
        this.available = false;
        flash.error(outcome.error);
        return;
      }
      if (outcome.status === "error") {
        flash.error(outcome.error);
        return;
      }

      flash.success('Guardrail "' + name + '" deleted.');
      if (this.formOpen && this.formOriginalName === name) {
        this.closeForm();
      }
      void this.fetchGuardrails();
    } finally {
      this.deletingName = "";
    }
  }
}

export const guardrailsStore = new GuardrailsStore();
