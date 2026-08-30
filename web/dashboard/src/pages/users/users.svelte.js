// State module for the Users page: the user-path tree with per-node model
// allowlists (GET/PUT/DELETE /admin/users), on top of the shared admin API
// client.

import { loadAdminList, sendAdminMutation } from "$lib/api/adminCrud.js";
import { flash } from "$lib/stores/flash.svelte.js";
import * as m from "$lib/paraglide/messages.js";
import { writeTextToClipboard } from "$lib/utils/clipboard.svelte.js";
import { displayModelSelector } from "$lib/utils/modelSelectors.js";
import {
  buildUpsertUserPayload,
  defaultUserForm,
  filterUserNodes,
  sortUserNodes,
} from "./usersLogic.js";

class UsersStore {
  nodes = $state([]);
  available = $state(true);
  loading = $state(false);
  error = $state("");
  filter = $state("");

  visibleNodes = $derived(sortUserNodes(filterUserNodes(this.nodes, this.filter)));

  formOpen = $state(false);
  formSubmitting = $state(false);
  // editingPath is the path being edited; "" while creating a new node.
  editingPath = $state("");
  form = $state(defaultUserForm());
  deletingPath = $state("");

  // copiedPath names the row whose copy is confirmed. Every copy carries its
  // own sequence number and only the latest one may touch the flag — an
  // earlier copy settling later (in either outcome) is ignored.
  copiedPath = $state("");
  #copySequence = 0;
  #copyResetTimer = null;

  async copyPath(node) {
    if (!node || !node.user_path) {
      return;
    }
    const sequence = ++this.#copySequence;
    this.copiedPath = "";
    let ok = true;
    try {
      await writeTextToClipboard(node.user_path);
    } catch (error) {
      ok = false;
      console.error("Failed to copy user path:", error);
    }
    if (sequence !== this.#copySequence) {
      return;
    }
    clearTimeout(this.#copyResetTimer);
    this.copiedPath = ok ? node.user_path : "";
    if (ok) {
      this.#copyResetTimer = setTimeout(() => {
        if (sequence === this.#copySequence) {
          this.copiedPath = "";
        }
      }, 2000);
    }
  }

  applyList(outcome) {
    if (outcome.status === "stale") {
      return false;
    }
    if (outcome.status === "unavailable") {
      this.available = false;
      this.nodes = [];
      return false;
    }
    if (outcome.status === "error") {
      if (outcome.result) {
        this.available = true;
      }
      this.error = outcome.error;
      return false;
    }
    this.available = true;
    this.nodes = outcome.items;
    return true;
  }

  async fetchUsers() {
    this.loading = true;
    this.error = "";
    try {
      const outcome = await loadAdminList("/admin/users", {
        label: "users",
        errorFallback: m.users_load_failed(),
        normalize: (data) => (data && Array.isArray(data.users) ? data.users : []),
      });
      this.applyList(outcome);
    } finally {
      this.loading = false;
    }
  }

  openForm(node = null) {
    if (this.formSubmitting || this.formOpen) {
      return;
    }
    this.error = "";
    this.editingPath = node ? node.user_path : "";
    this.form = node
      ? {
          user_path: node.user_path,
          allowed_models: (node.allowed_models || []).map(displayModelSelector),
          description: node.description || "",
        }
      : defaultUserForm();
    this.formOpen = true;
  }

  closeForm() {
    if (!this.formOpen || this.formSubmitting) {
      return;
    }
    this.formOpen = false;
    this.error = "";
    this.editingPath = "";
    this.form = defaultUserForm();
  }

  async submitForm() {
    const built = buildUpsertUserPayload(this.form);
    if (built.error) {
      this.error = built.error;
      return;
    }
    this.error = "";
    this.formSubmitting = true;
    try {
      const outcome = await sendAdminMutation("/admin/users", "PUT", built.payload, {
        label: "save user",
        errorFallback: m.users_save_failed(),
        unavailableMessage: m.users_feature_unavailable(),
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
          console.error("Failed to save user:", outcome.result.status, this.error);
        }
        return;
      }
      flash.success(m.users_saved({ path: built.payload.user_path }));
      const data = outcome.result.data;
      if (data && Array.isArray(data.users)) {
        this.nodes = data.users;
      }
      this.formSubmitting = false;
      this.closeForm();
    } finally {
      this.formSubmitting = false;
    }
  }

  async deleteUser(node) {
    if (!node || !node.configured || node.managed || this.deletingPath) {
      return;
    }
    if (!window.confirm(m.users_delete_confirm({ path: node.user_path }))) {
      return;
    }
    this.deletingPath = node.user_path;
    try {
      const outcome = await sendAdminMutation(
        "/admin/users?user_path=" + encodeURIComponent(node.user_path),
        "DELETE",
        undefined,
        {
          label: "delete user",
          errorFallback: m.users_delete_failed(),
          unavailableMessage: m.users_feature_unavailable(),
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
          console.error("Failed to delete user:", outcome.result.status, outcome.error);
        }
        flash.error(outcome.error);
        return;
      }
      flash.success(m.users_deleted({ path: node.user_path }));
      const data = outcome.result.data;
      if (data && Array.isArray(data.users)) {
        this.nodes = data.users;
      } else {
        void this.fetchUsers();
      }
    } finally {
      this.deletingPath = "";
    }
  }
}

export const usersStore = new UsersStore();
