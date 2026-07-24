// State module for the API Keys page: fetch/create/labels/deactivate flows
// on top of the shared admin API client (401s and stale-auth are handled by
// the client).

import { getJSON, sendJSON } from "$lib/api/client.js";
import { createCopyState } from "$lib/utils/clipboard.svelte.js";
import {
  authKeyErrorMessage,
  buildCreateAuthKeyPayload,
  defaultAuthKeyForm,
  parseAuthKeyLabels,
} from "./authKeysLogic.js";

function emptyLabelsEditor() {
  return { open: false, id: "", name: "", value: "", submitting: false, error: "" };
}

class AuthKeysStore {
  keys = $state([]);
  available = $state(true);
  loading = $state(false);
  error = $state("");
  notice = $state("");

  formOpen = $state(false);
  formSubmitting = $state(false);
  issuedValue = $state("");
  deactivatingID = $state("");
  form = $state(defaultAuthKeyForm());
  labelsEditor = $state(emptyLabelsEditor());

  copyState = createCopyState({ logPrefix: "Failed to copy auth key:" });

  async fetchKeys() {
    this.loading = true;
    this.error = "";
    try {
      const result = await getJSON("/admin/auth-keys", { label: "auth keys" });
      if (result.status === 503) {
        this.available = false;
        this.keys = [];
        return;
      }
      if (result.stale) {
        return;
      }
      this.available = true;
      if (!result.ok) {
        if (result.status !== 401) {
          this.error = authKeyErrorMessage(result.data, "Unable to load API keys.");
        }
        return;
      }
      this.keys = Array.isArray(result.data) ? result.data : [];
    } catch (e) {
      console.error("Failed to fetch auth keys:", e);
      this.keys = [];
      this.error = "Unable to load API keys.";
    } finally {
      this.loading = false;
    }
  }

  openForm() {
    if (this.formSubmitting || this.formOpen) {
      return;
    }
    this.formOpen = true;
    this.error = "";
    this.notice = "";
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
    this.notice = "";
    this.formSubmitting = true;
    try {
      const result = await sendJSON("/admin/auth-keys", "POST", built.payload, {
        label: "create API key",
      });
      if (result.status === 503) {
        this.available = false;
        this.error = "Auth keys feature is unavailable.";
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
        this.error = authKeyErrorMessage(result.data, "Failed to create API key.");
        console.error("Failed to create API key:", result.status, this.error);
        return;
      }
      const issued = result.data || {};
      this.issuedValue = issued.value || "";
      // Reopen the editor if issuance finished after a manual close so the
      // one-time secret is always shown.
      this.formOpen = true;
      this.copyState.reset();
      this.form = defaultAuthKeyForm();
      await this.fetchKeys();
    } catch (e) {
      console.error("Failed to issue auth key:", e);
      this.error = "Failed to create API key.";
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
    this.notice = "";
    const payload = { labels: parseAuthKeyLabels(editor.value) };

    try {
      const result = await sendJSON(
        "/admin/auth-keys/" + encodeURIComponent(editor.id) + "/labels",
        "PUT",
        payload,
        { label: "update API key labels" },
      );
      if (result.status === 503) {
        this.available = false;
        editor.error = "Auth keys feature is unavailable.";
        return;
      }
      if (result.stale) {
        return;
      }
      if (!result.ok) {
        if (result.status === 401) {
          editor.error = "Authentication required.";
          return;
        }
        editor.error = authKeyErrorMessage(result.data, "Failed to update labels.");
        console.error("Failed to update auth key labels:", result.status, editor.error);
        return;
      }
      await this.fetchKeys();
      this.notice = 'Labels updated for key "' + editor.name + '".';
      editor.submitting = false;
      this.closeLabelsEditor();
    } catch (e) {
      console.error("Failed to update auth key labels:", e);
      editor.error = "Failed to update labels.";
    } finally {
      editor.submitting = false;
    }
  }

  async deactivateKey(key) {
    if (!key || !key.active) {
      return;
    }
    if (!window.confirm('Deactivate key "' + key.name + '"? This cannot be undone.')) {
      return;
    }

    this.deactivatingID = key.id;
    this.error = "";
    this.notice = "";
    try {
      const result = await sendJSON(
        "/admin/auth-keys/" + encodeURIComponent(key.id) + "/deactivate",
        "POST",
        undefined,
        { label: "deactivate API key" },
      );
      if (result.status === 503) {
        this.available = false;
        this.error = "Auth keys feature is unavailable.";
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
        this.error = authKeyErrorMessage(result.data, "Failed to deactivate key.");
        console.error("Failed to deactivate auth key:", result.status, this.error);
        return;
      }
      await this.fetchKeys();
      this.notice = 'Key "' + key.name + '" deactivated.';
    } catch (e) {
      console.error("Failed to deactivate auth key:", e);
      this.error = "Failed to deactivate key.";
    } finally {
      this.deactivatingID = "";
    }
  }
}

export const authKeysStore = new AuthKeysStore();
