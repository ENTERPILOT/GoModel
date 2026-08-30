// State module for the API Keys page: fetch/create/labels/deactivate flows
// on top of the shared admin API client (401s and stale-auth are handled by
// the client).

import { loadAdminList, sendAdminMutation } from "$lib/api/adminCrud.js";
import { flash } from "$lib/stores/flash.svelte.js";
import { router } from "$lib/stores/router.svelte.js";
import * as m from "$lib/paraglide/messages.js";
import { createCopyState } from "$lib/utils/clipboard.svelte.js";
import { displayModelSelector } from "$lib/utils/modelSelectors.js";
import {
  buildCreateAuthKeyPayload,
  countInactiveAuthKeys,
  defaultAuthKeyForm,
  filterAuthKeys,
  parseAuthKeyAllowedModels,
  parseAuthKeyLabels,
  sortAuthKeys,
} from "./authKeysLogic.js";

function emptyLabelsEditor() {
  return { open: false, id: "", name: "", value: "", submitting: false, error: "" };
}

function emptyAllowedModelsEditor() {
  return { open: false, id: "", name: "", value: [], submitting: false, error: "" };
}

class AuthKeysStore {
  keys = $state([]);
  available = $state(true);
  loading = $state(false);
  // Load and in-form errors only; row-action feedback goes through the
  // flash store.
  error = $state("");

  // Toolbar: inactive keys (deactivated or expired) are hidden by default.
  filter = $state("");
  showInactive = $state(false);
  // userPathFilter narrows the table to one user path and its subtree; it is
  // set when arriving from the Users page and cleared from the toolbar chip.
  userPathFilter = $state("");

  visibleKeys = $derived(
    sortAuthKeys(
      filterAuthKeys(this.keys, {
        query: this.filter,
        showInactive: this.showInactive,
        userPath: this.userPathFilter,
      }),
    ),
  );
  inactiveCount = $derived(countInactiveAuthKeys(this.keys));

  formOpen = $state(false);
  formSubmitting = $state(false);
  issuedValue = $state("");
  deactivatingID = $state("");
  dashboardAccessID = $state("");
  form = $state(defaultAuthKeyForm());
  labelsEditor = $state(emptyLabelsEditor());
  allowedModelsEditor = $state(emptyAllowedModelsEditor());

  copyState = createCopyState({ logPrefix: "Failed to copy auth key:" });

  async fetchKeys() {
    this.loading = true;
    this.error = "";
    try {
      const outcome = await loadAdminList("/admin/auth-keys", {
        label: "auth keys",
        errorFallback: m.api_keys_load_failed(),
      });
      if (outcome.status === "stale") {
        return;
      }
      if (outcome.status === "unavailable") {
        this.available = false;
        this.keys = [];
        return;
      }
      if (outcome.status === "error") {
        // Any load failure keeps the last-known rows next to the inline
        // error; only an answered request proves availability.
        if (outcome.result) {
          this.available = true;
        }
        this.error = outcome.error;
        return;
      }
      this.available = true;
      this.keys = outcome.items;
    } finally {
      this.loading = false;
    }
  }

  // Cross-page entry points used by the Users page: land on API Keys either
  // filtered to one user path (its subtree, since the filter is a substring
  // match) or with the create form open and that path prefilled.
  showForUserPath(userPath) {
    this.userPathFilter = String(userPath || "").trim();
    this.filter = "";
    this.showInactive = false;
    router.navigate("auth-keys");
  }

  clearUserPathFilter() {
    this.userPathFilter = "";
  }

  openFormForUserPath(userPath) {
    router.navigate("auth-keys");
    this.issuedValue = "";
    this.formOpen = false;
    this.openForm();
    this.form.user_path = String(userPath || "").trim();
  }

  openForm() {
    if (this.formSubmitting || this.formOpen) {
      return;
    }
    this.formOpen = true;
    this.error = "";
    if (!this.issuedValue) {
      this.copyState.reset();
      this.form = defaultAuthKeyForm();
    }
  }

  closeForm() {
    if (!this.formOpen) {
      return;
    }
    this.formOpen = false;
    this.error = "";
    this.copyState.reset();
    if (!this.formSubmitting && !this.issuedValue) {
      this.form = defaultAuthKeyForm();
    }
  }

  copyIssuedValue() {
    return this.copyState.copy(this.issuedValue);
  }

  dismissIssuedKey() {
    this.issuedValue = "";
    this.copyState.reset();
    this.form = defaultAuthKeyForm();
  }

  async submitForm() {
    const built = buildCreateAuthKeyPayload(this.form);
    if (built.error) {
      this.error = built.error;
      return;
    }

    this.error = "";
    this.formSubmitting = true;
    try {
      const outcome = await sendAdminMutation("/admin/auth-keys", "POST", built.payload, {
        label: "create API key",
        errorFallback: m.api_keys_create_failed(),
        unavailableMessage: m.api_keys_feature_unavailable(),
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
        this.error = outcome.error;
        if (outcome.result && outcome.result.status !== 401) {
          console.error("Failed to create API key:", outcome.result.status, this.error);
        }
        return;
      }
      const issued = outcome.result.data || {};
      this.issuedValue = issued.value || "";
      // Reopen the editor if issuance finished after a manual close so the
      // one-time secret is always shown.
      this.formOpen = true;
      this.copyState.reset();
      this.form = defaultAuthKeyForm();
      void this.fetchKeys();
    } finally {
      this.formSubmitting = false;
    }
  }

  openLabelsEditor(key) {
    if (!key || this.labelsEditor.submitting) {
      return;
    }
    this.labelsEditor = {
      open: true,
      id: key.id,
      name: key.name || "",
      value: (key.labels || []).join(", "),
      submitting: false,
      error: "",
    };
  }

  closeLabelsEditor() {
    if (!this.labelsEditor.open || this.labelsEditor.submitting) {
      return;
    }
    this.labelsEditor = emptyLabelsEditor();
  }

  async submitLabelsEditor() {
    const editor = this.labelsEditor;
    if (!editor.open || editor.submitting || !editor.id) {
      return;
    }
    editor.submitting = true;
    editor.error = "";
    const payload = { labels: parseAuthKeyLabels(editor.value) };

    try {
      const outcome = await sendAdminMutation(
        "/admin/auth-keys/" + encodeURIComponent(editor.id) + "/labels",
        "PUT",
        payload,
        {
          label: "update API key labels",
          errorFallback: m.api_keys_labels_update_failed(),
          unavailableMessage: m.api_keys_feature_unavailable(),
        },
      );
      if (outcome.status === "stale") {
        return;
      }
      if (outcome.status === "unavailable") {
        this.available = false;
        editor.error = outcome.error;
        return;
      }
      if (outcome.status === "error") {
        editor.error = outcome.error;
        if (outcome.result && outcome.result.status !== 401) {
          console.error("Failed to update auth key labels:", outcome.result.status, editor.error);
        }
        return;
      }
      flash.success(m.api_keys_labels_updated({ name: editor.name }));
      editor.submitting = false;
      this.closeLabelsEditor();
      void this.fetchKeys();
    } finally {
      editor.submitting = false;
    }
  }

  openAllowedModelsEditor(key) {
    if (!key || this.allowedModelsEditor.submitting) {
      return;
    }
    this.allowedModelsEditor = {
      open: true,
      id: key.id,
      name: key.name || "",
      value: (key.allowed_models || []).map(displayModelSelector),
      submitting: false,
      error: "",
    };
  }

  closeAllowedModelsEditor() {
    if (!this.allowedModelsEditor.open || this.allowedModelsEditor.submitting) {
      return;
    }
    this.allowedModelsEditor = emptyAllowedModelsEditor();
  }

  async submitAllowedModelsEditor() {
    const editor = this.allowedModelsEditor;
    if (!editor.open || editor.submitting || !editor.id) {
      return;
    }
    editor.submitting = true;
    editor.error = "";
    const payload = { allowed_models: parseAuthKeyAllowedModels(editor.value) };

    try {
      const outcome = await sendAdminMutation(
        "/admin/auth-keys/" + encodeURIComponent(editor.id) + "/allowed-models",
        "PUT",
        payload,
        {
          label: "update API key allowed models",
          errorFallback: m.api_keys_allowed_models_update_failed(),
          unavailableMessage: m.api_keys_feature_unavailable(),
        },
      );
      if (outcome.status === "stale") {
        return;
      }
      if (outcome.status === "unavailable") {
        this.available = false;
        editor.error = outcome.error;
        return;
      }
      if (outcome.status === "error") {
        editor.error = outcome.error;
        if (outcome.result && outcome.result.status !== 401) {
          console.error("Failed to update auth key allowed models:", outcome.result.status, editor.error);
        }
        return;
      }
      flash.success(m.api_keys_allowed_models_updated({ name: editor.name }));
      editor.submitting = false;
      this.closeAllowedModelsEditor();
      void this.fetchKeys();
    } finally {
      editor.submitting = false;
    }
  }

  async toggleDashboardAccess(key) {
    if (!key || !key.active || this.dashboardAccessID) {
      return;
    }
    const grant = !key.dashboard_access;

    this.dashboardAccessID = key.id;
    try {
      const outcome = await sendAdminMutation(
        "/admin/auth-keys/" + encodeURIComponent(key.id) + "/dashboard-access",
        "PUT",
        { dashboard_access: grant },
        {
          label: "update API key dashboard access",
          errorFallback: m.api_keys_access_update_failed(),
          unavailableMessage: m.api_keys_feature_unavailable(),
        },
      );
      if (outcome.status === "stale") {
        return;
      }
      if (outcome.status === "unavailable") {
        this.available = false;
        flash.error(outcome.error);
        return;
      }
      if (outcome.status === "error") {
        if (outcome.result && outcome.result.status !== 401) {
          console.error(
            "Failed to update auth key dashboard access:",
            outcome.result.status,
            outcome.error,
          );
        }
        flash.error(outcome.error);
        return;
      }
      flash.success(grant
        ? m.api_keys_access_granted({ name: key.name })
        : m.api_keys_access_revoked({ name: key.name }));
      void this.fetchKeys();
    } finally {
      this.dashboardAccessID = "";
    }
  }

  async deactivateKey(key) {
    if (!key || !key.active) {
      return;
    }
    if (!window.confirm(m.api_keys_deactivate_confirm({ name: key.name }))) {
      return;
    }

    this.deactivatingID = key.id;
    try {
      const outcome = await sendAdminMutation(
        "/admin/auth-keys/" + encodeURIComponent(key.id) + "/deactivate",
        "POST",
        undefined,
        {
          label: "deactivate API key",
          errorFallback: m.api_keys_deactivate_failed(),
          unavailableMessage: m.api_keys_feature_unavailable(),
        },
      );
      if (outcome.status === "stale") {
        return;
      }
      if (outcome.status === "unavailable") {
        this.available = false;
        flash.error(outcome.error);
        return;
      }
      if (outcome.status === "error") {
        if (outcome.result && outcome.result.status !== 401) {
          console.error("Failed to deactivate auth key:", outcome.result.status, outcome.error);
        }
        flash.error(outcome.error);
        return;
      }
      flash.success(m.api_keys_deactivate_success({ name: key.name }));
      void this.fetchKeys();
    } finally {
      this.deactivatingID = "";
    }
  }
}

export const authKeysStore = new AuthKeysStore();
