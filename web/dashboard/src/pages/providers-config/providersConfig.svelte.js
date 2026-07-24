// State module for the Providers (provider credentials) page: networking +
// editor state on Svelte 5 runes.
//
// Endpoints:
//   GET    /admin/provider-credentials          — list rows
//   GET    /admin/provider-credentials/types    — provider type catalog
//   PUT    /admin/provider-credentials          — upsert one row
//   DELETE /admin/provider-credentials/{name}   — delete one row

import { getJSON, sendJSON, isAbortError } from "$lib/api/client.js";
import { confirmDialog } from "$lib/stores/confirm.svelte.js";
import { flash } from "$lib/stores/flash.svelte.js";
import { modelsStore } from "$lib/stores/models.svelte.js";
import {
  defaultProviderCredentialForm,
  filterProviderCredentials,
  providerCredentialRowToForm,
  validateProviderCredentialForm,
  buildProviderCredentialPayload,
  providerCredentialErrorMessage,
} from "./providersConfigLogic.js";

class ProvidersConfigState {
  rows = $state([]);
  available = $state(true);
  loading = $state(false);
  // Load and in-form errors only; mutation feedback goes through the
  // flash store.
  error = $state("");
  filter = $state("");

  formOpen = $state(false);
  formSubmitting = $state(false);
  formMode = $state("create");
  advancedOpen = $state(false);
  form = $state(defaultProviderCredentialForm());

  deletingName = $state("");
  deleteSubmitting = $state(false);

  types = $state([]);
  typesLoaded = $state(false);

  #controller = null;

  get filteredRows() {
    return filterProviderCredentials(this.rows, this.filter);
  }

  async fetchTypes() {
    try {
      const result = await getJSON("/admin/provider-credentials/types", {
        label: "provider credential types",
      });
      if (result.stale) return;
      if (result.status === 503 || result.status === 404) return;
      if (!result.ok) return;
      this.types = Array.isArray(result.data) ? result.data : [];
      this.typesLoaded = true;
    } catch (e) {
      console.error("Failed to fetch provider credential types:", e);
    }
  }

  async fetchPage() {
    if (this.#controller) this.#controller.abort();
    const controller = new AbortController();
    this.#controller = controller;
    this.loading = true;
    this.error = "";
    try {
      const result = await getJSON("/admin/provider-credentials", {
        label: "provider credentials",
        signal: controller.signal,
      });
      if (result.stale || controller.signal.aborted) return;
      if (result.status === 503 || result.status === 404) {
        this.available = false;
        this.rows = [];
        return;
      }
      this.available = true;
      if (!result.ok) {
        this.rows = [];
        if (result.status !== 401) {
          this.error = providerCredentialErrorMessage(
            result.data,
            "Failed to load provider credentials.",
          );
        }
        return;
      }
      this.rows = Array.isArray(result.data) ? result.data : [];
      if (!this.typesLoaded) {
        await this.fetchTypes();
      }
    } catch (e) {
      if (isAbortError(e)) return;
      console.error("Failed to fetch provider credentials:", e);
      this.rows = [];
      this.error = "Unable to load provider credentials.";
    } finally {
      if (this.#controller === controller) {
        this.#controller = null;
        this.loading = false;
      }
    }
  }

  openCreate() {
    this.formMode = "create";
    this.advancedOpen = false;
    this.error = "";
    this.form = defaultProviderCredentialForm();
    this.formOpen = true;
    if (!this.typesLoaded) {
      this.fetchTypes();
    }
  }

  openEdit(row) {
    if (!row || row.managed) {
      return;
    }
    this.formMode = "edit";
    this.advancedOpen = false;
    this.error = "";
    this.form = providerCredentialRowToForm(row);
    this.formOpen = true;
    if (!this.typesLoaded) {
      this.fetchTypes();
    }
  }

  closeForm() {
    this.formOpen = false;
    this.formMode = "create";
    this.advancedOpen = false;
    this.error = "";
    this.form = defaultProviderCredentialForm();
  }

  addApiKeyRow() {
    this.form.api_keys.push({ value: "" });
  }

  removeApiKeyRow(index) {
    this.form.api_keys.splice(index, 1);
  }

  // Hot-registering a provider changes the live model inventory, so refresh
  // the shared models store after every successful upsert/delete.
  #refreshInventory() {
    modelsStore.fetchModels();
    modelsStore.fetchCategories();
  }

  async submitForm() {
    const validationError = validateProviderCredentialForm(
      this.form,
      this.formMode,
      this.rows,
    );
    if (validationError) {
      this.error = validationError;
      return;
    }

    const payload = buildProviderCredentialPayload(this.form);
    this.error = "";
    this.formSubmitting = true;
    try {
      const result = await sendJSON("/admin/provider-credentials", "PUT", payload, {
        label: "save provider credential",
      });
      if (result.stale) return;
      if (result.status === 503) {
        this.available = false;
        this.error = "Provider credential management is unavailable.";
        return;
      }
      if (!result.ok) {
        if (result.status === 401) {
          this.error = "Authentication required.";
          return;
        }
        this.error = providerCredentialErrorMessage(
          result.data,
          "Failed to save provider credential.",
        );
        return;
      }

      flash.success('Provider "' + payload.name + '" saved.');
      this.closeForm();
      this.#refreshInventory();
      void this.fetchPage();
    } catch (e) {
      console.error("Failed to save provider credential:", e);
      this.error = "Failed to save provider credential.";
    } finally {
      this.formSubmitting = false;
    }
  }

  async performDelete(name) {
    this.deleteSubmitting = true;
    this.deletingName = name;
    try {
      const result = await sendJSON(
        "/admin/provider-credentials/" + encodeURIComponent(name),
        "DELETE",
        undefined,
        { label: "delete provider credential" },
      );
      if (result.stale) return;
      if (result.status === 503) {
        this.available = false;
        confirmDialog.error = "Provider credential management is unavailable.";
        return;
      }
      if (!result.ok) {
        confirmDialog.error =
          result.status === 401
            ? "Authentication required."
            : providerCredentialErrorMessage(
                result.data,
                "Failed to delete provider credential.",
              );
        return;
      }

      flash.success('Provider "' + name + '" deleted.');
      confirmDialog.close();
      if (this.formOpen && this.form.name === name) {
        this.closeForm();
      }
      this.#refreshInventory();
      void this.fetchPage();
    } catch (e) {
      console.error("Failed to delete provider credential:", e);
      confirmDialog.error = "Failed to delete provider credential.";
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
