// Guardrails page state: definitions list + schema-driven editor form.
// Handles the /admin/guardrails endpoints, 503 "feature unavailable"
// responses, and the notice/error flow.

import { getJSON, sendJSON } from "$lib/api/client.js";
import { flash } from "$lib/stores/flash.svelte.js";
import {
  buildGuardrailPayload,
  defaultGuardrailConfig,
  defaultGuardrailForm,
  defaultGuardrailType,
  filterGuardrails,
  guardrailArrayFieldSelected,
  guardrailErrorMessage,
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
      const result = await getJSON("/admin/guardrails/types", {
        label: "guardrail types",
      });
      if (result.status === 503) {
        this.available = false;
        this.types = [];
        return;
      }
      if (result.stale) {
        return;
      }
      this.available = true;
      if (!result.ok) {
        this.types = [];
        return;
      }
      this.types = Array.isArray(result.data) ? result.data : [];
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
    } catch (e) {
      console.error("Failed to fetch guardrail types:", e);
      this.types = [];
      this.error = "Unable to load guardrail types.";
    } finally {
      this.typesLoading = false;
    }
  }

  async fetchGuardrails() {
    this.loading = true;
    this.error = "";
    try {
      const result = await getJSON("/admin/guardrails", {
        label: "guardrails",
      });
      if (result.status === 503) {
        this.available = false;
        this.guardrails = [];
        return;
      }
      if (result.stale) {
        return;
      }
      this.available = true;
      if (!result.ok) {
        this.guardrails = [];
        return;
      }
      this.guardrails = Array.isArray(result.data) ? result.data : [];
    } catch (e) {
      console.error("Failed to fetch guardrails:", e);
      this.guardrails = [];
      this.error = "Unable to load guardrails.";
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
      const result = await sendJSON("/admin/guardrails", "PUT", payload, {
        label: "save guardrail",
      });
      if (result.status === 503) {
        this.available = false;
        this.error = "Guardrails feature is unavailable.";
        return;
      }
      if (result.stale) {
        return;
      }
      if (!result.ok) {
        if (result.status === 401) {
          this.error = "Authentication required.";
          return;
        }
        this.error = guardrailErrorMessage(
          result.data,
          "Failed to save guardrail.",
        );
        console.error("Failed to save guardrail:", result.status, this.error);
        return;
      }

      flash.success('Guardrail "' + name + '" saved.');
      this.closeForm();
      await this.fetchGuardrails();
    } catch (e) {
      console.error("Failed to save guardrail:", e);
      this.error = "Failed to save guardrail.";
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
      const result = await sendJSON(
        "/admin/guardrails",
        "DELETE",
        { name },
        { label: "delete guardrail" },
      );
      if (result.status === 503) {
        this.available = false;
        flash.error("Guardrails feature is unavailable.");
        return;
      }
      if (result.stale) {
        return;
      }
      if (!result.ok) {
        if (result.status === 401) {
          flash.error("Authentication required.");
          return;
        }
        const message = guardrailErrorMessage(
          result.data,
          "Failed to delete guardrail.",
        );
        console.error("Failed to delete guardrail:", result.status, message);
        flash.error(message);
        return;
      }

      flash.success('Guardrail "' + name + '" deleted.');
      if (this.formOpen && this.formOriginalName === name) {
        this.closeForm();
      }
      await this.fetchGuardrails();
    } catch (e) {
      console.error("Failed to delete guardrail:", e);
      flash.error("Failed to delete guardrail.");
    } finally {
      this.deletingName = "";
    }
  }
}

export const guardrailsStore = new GuardrailsStore();
