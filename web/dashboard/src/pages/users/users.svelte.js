// State module for the Users page: users + groups CRUD, the supporting
// API-key and access-policy inventories, and the grant-access editor flow on
// top of the shared admin API client.

import { loadAdminList, sendAdminMutation } from "$lib/api/adminCrud.js";
import { flash } from "$lib/stores/flash.svelte.js";
import * as m from "$lib/paraglide/messages.js";
import {
  buildAccessRows,
  buildAccessSavePlan,
  buildGroupPayload,
  buildRegistryTree,
  buildUserPayload,
  defaultGroupForm,
  defaultUserForm,
  filterTreeRows,
  groupMemberCounts,
  keyCountsByPath,
  userFormFromUser,
} from "./usersLogic.js";

function emptyAccessEditor() {
  return { open: false, subject: null, rows: [], loading: false, submitting: false, error: "", filter: "" };
}

class UsersStore {
  users = $state([]);
  groups = $state([]);
  authKeys = $state([]);
  policyViews = $state([]);
  available = $state(true);
  loading = $state(false);
  error = $state("");
  filter = $state("");

  treeRows = $derived(filterTreeRows(buildRegistryTree(this.groups, this.users), this.filter));
  keyCounts = $derived(keyCountsByPath(this.authKeys));
  memberCounts = $derived(groupMemberCounts(this.users));

  userEditor = $state({ open: false, mode: "create", submitting: false, error: "", form: defaultUserForm() });
  groupEditor = $state({ open: false, mode: "create", submitting: false, error: "", form: defaultGroupForm() });
  accessEditor = $state(emptyAccessEditor());
  // Plain counter (not $state): identifies the in-flight openAccessEditor
  // load so a close-and-reopen invalidates the stale one. The subject object
  // itself can't be compared — $state proxies it, so `subject !== subject`.
  accessRequest = 0;
  deletingID = $state("");
  deletingGroup = $state("");

  async fetchAll() {
    this.loading = true;
    this.error = "";
    try {
      const [users, groups] = await Promise.all([
        loadAdminList("/admin/users", { label: "users", errorFallback: m.users_load_failed() }),
        loadAdminList("/admin/user-groups", { label: "user groups", errorFallback: m.users_load_failed() }),
        this.fetchSupportingData(),
      ]);
      if (users.status === "stale" || groups.status === "stale") {
        return;
      }
      if (users.status === "unavailable" || groups.status === "unavailable") {
        this.available = false;
        this.users = [];
        this.groups = [];
        return;
      }
      if (users.status === "error" || groups.status === "error") {
        if (users.result || groups.result) {
          this.available = true;
        }
        this.error = users.error || groups.error;
        return;
      }
      this.available = true;
      this.users = users.items;
      this.groups = groups.items;
    } finally {
      this.loading = false;
    }
  }

  // API keys and access policies enrich the page (key counts, access
  // summaries); their failures stay silent so the page still works when only
  // the users API is available.
  async fetchSupportingData() {
    const [keys, views] = await Promise.all([
      loadAdminList("/admin/auth-keys", { label: "auth keys" }),
      loadAdminList("/admin/virtual-models", { label: "virtual models" }),
    ]);
    if (keys.status === "ok") {
      this.authKeys = keys.items;
    }
    if (views.status === "ok") {
      this.policyViews = views.items;
    }
  }

  // ---- user editor ----

  openUserEditor(user = null) {
    if (this.userEditor.submitting) return;
    this.userEditor = {
      open: true,
      mode: user ? "edit" : "create",
      submitting: false,
      error: "",
      form: user ? userFormFromUser(user) : defaultUserForm(),
    };
  }

  // openUserEditorForGroup pre-selects the owning group when adding a user
  // from a group row.
  openUserEditorForGroup(name) {
    this.openUserEditor();
    this.userEditor.form.group = name;
  }

  closeUserEditor() {
    if (this.userEditor.submitting) return;
    this.userEditor = { open: false, mode: "create", submitting: false, error: "", form: defaultUserForm() };
  }

  async submitUserEditor() {
    const editor = this.userEditor;
    if (!editor.open || editor.submitting) return;
    const built = buildUserPayload(editor.form);
    if (built.error) {
      editor.error = built.error === "name_invalid" ? m.users_name_invalid() : m.users_name_required();
      return;
    }
    editor.submitting = true;
    editor.error = "";
    try {
      const outcome = await sendAdminMutation("/admin/users", "PUT", built.payload, {
        label: "save user",
        errorFallback: m.users_save_failed(),
        unavailableMessage: m.users_unavailable(),
      });
      if (outcome.status === "stale") return;
      if (outcome.status !== "ok") {
        if (outcome.status === "unavailable") this.available = false;
        editor.error = outcome.error;
        return;
      }
      flash.success(m.users_saved({ name: built.payload.name }));
      editor.submitting = false;
      this.closeUserEditor();
      void this.fetchAll();
    } finally {
      editor.submitting = false;
    }
  }

  async deleteUser(user) {
    if (!user || this.deletingID) return;
    if (!window.confirm(m.users_delete_confirm({ name: user.name || user.user_path }))) return;
    this.deletingID = user.id;
    try {
      const outcome = await sendAdminMutation("/admin/users", "DELETE", { id: user.id }, {
        label: "delete user",
        errorFallback: m.users_delete_failed(),
        unavailableMessage: m.users_unavailable(),
      });
      if (outcome.status === "stale") return;
      if (outcome.status !== "ok") {
        if (outcome.status === "unavailable") this.available = false;
        flash.error(outcome.error);
        return;
      }
      flash.success(m.users_deleted({ name: user.name || user.user_path }));
      void this.fetchAll();
    } finally {
      this.deletingID = "";
    }
  }

  // ---- group editor ----

  openGroupEditor(group = null) {
    if (this.groupEditor.submitting) return;
    this.groupEditor = {
      open: true,
      mode: group ? "edit" : "create",
      submitting: false,
      error: "",
      form: group
        ? { name: group.name, original: group.name, description: group.description || "", parent: group.parent || "" }
        : defaultGroupForm(),
    };
  }

  closeGroupEditor() {
    if (this.groupEditor.submitting) return;
    this.groupEditor = { open: false, mode: "create", submitting: false, error: "", form: defaultGroupForm() };
  }

  async submitGroupEditor() {
    const editor = this.groupEditor;
    if (!editor.open || editor.submitting) return;
    const built = buildGroupPayload(editor.form);
    if (built.error) {
      editor.error =
        built.error === "name_invalid"
          ? m.groups_name_invalid()
          : built.error === "parent_invalid"
            ? m.groups_parent_invalid()
            : m.groups_name_required();
      return;
    }
    editor.submitting = true;
    editor.error = "";
    try {
      const outcome = await sendAdminMutation("/admin/user-groups", "PUT", built.payload, {
        label: "save group",
        errorFallback: m.groups_save_failed(),
        unavailableMessage: m.users_unavailable(),
      });
      if (outcome.status === "stale") return;
      if (outcome.status !== "ok") {
        if (outcome.status === "unavailable") this.available = false;
        editor.error = outcome.error;
        return;
      }
      flash.success(m.groups_saved({ name: built.payload.name }));
      editor.submitting = false;
      this.closeGroupEditor();
      void this.fetchAll();
    } finally {
      editor.submitting = false;
    }
  }

  async deleteGroup(group) {
    if (!group || this.deletingGroup) return;
    if (!window.confirm(m.groups_delete_confirm({ name: group.name }))) return;
    this.deletingGroup = group.name;
    try {
      const outcome = await sendAdminMutation("/admin/user-groups", "DELETE", { name: group.name }, {
        label: "delete group",
        errorFallback: m.groups_delete_failed(),
        unavailableMessage: m.users_unavailable(),
      });
      if (outcome.status === "stale") return;
      if (outcome.status !== "ok") {
        if (outcome.status === "unavailable") this.available = false;
        flash.error(outcome.error);
        return;
      }
      flash.success(m.groups_deleted({ name: group.name }));
      void this.fetchAll();
    } finally {
      this.deletingGroup = "";
    }
  }

  // ---- grant model access ----

  async openAccessEditor(subject) {
    if (this.accessEditor.submitting) return;
    const request = ++this.accessRequest;
    this.accessEditor = { ...emptyAccessEditor(), open: true, subject, loading: true };
    // Refresh the policy views so the editor diffs against current state.
    const [views, models] = await Promise.all([
      loadAdminList("/admin/virtual-models", { label: "virtual models" }),
      loadAdminList("/admin/models", { label: "models" }),
    ]);
    if (!this.accessEditor.open || request !== this.accessRequest) return;
    if (views.status === "ok") {
      this.policyViews = views.items;
    }
    if (views.status !== "ok" || models.status !== "ok") {
      // Without both inventories the diff base is unknown; leave the editor
      // rowless (nothing to submit) instead of building rows against stale
      // policy state.
      this.accessEditor.loading = false;
      this.accessEditor.error = m.users_access_load_failed();
      return;
    }
    this.accessEditor.rows = buildAccessRows({
      models: models.items,
      views: this.policyViews,
      subject,
      groups: this.groups,
    });
    this.accessEditor.loading = false;
  }

  closeAccessEditor() {
    if (this.accessEditor.submitting) return;
    this.accessEditor = emptyAccessEditor();
  }

  accessPlan() {
    const editor = this.accessEditor;
    if (!editor.open || !editor.subject) return [];
    return buildAccessSavePlan(editor.rows, editor.subject);
  }

  async submitAccessEditor() {
    const editor = this.accessEditor;
    if (!editor.open || editor.submitting || !editor.subject) return;
    const plan = this.accessPlan();
    if (plan.length === 0) {
      this.closeAccessEditor();
      return;
    }
    editor.submitting = true;
    editor.error = "";
    try {
      for (const step of plan) {
        const outcome = await sendAdminMutation("/admin/virtual-models", step.method, step.payload, {
          label: "update model access",
          errorFallback: m.users_access_save_failed(),
          unavailableMessage: m.users_unavailable(),
        });
        if (outcome.status === "stale") return;
        if (outcome.status !== "ok") {
          editor.error = outcome.error;
          return;
        }
      }
      flash.success(m.users_access_saved());
      editor.submitting = false;
      this.closeAccessEditor();
      void this.fetchAll();
    } finally {
      editor.submitting = false;
    }
  }
}

export const usersStore = new UsersStore();
