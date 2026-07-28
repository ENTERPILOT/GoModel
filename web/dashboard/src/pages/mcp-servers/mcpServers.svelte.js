// MCP Servers page state: fetch/save/delete/reconnect/catalog flows on top
// of the shared admin API client. Pure helpers live in ./mcp-servers.js so
// tests can exercise them without Svelte.

import { errorPayloadMessage, getJSON } from "$lib/api/client.js";
import { loadAdminList, sendAdminMutation } from "$lib/api/adminCrud.js";
import { flash } from "$lib/stores/flash.svelte.js";
import { runtimeConfig } from "$lib/stores/runtimeConfig.svelte.js";
import {
  buildMcpServerPayload,
  defaultMcpCatalog,
  defaultMcpServerForm,
  deriveMcpServerSlug,
  filterMcpServers,
  mcpServerFormFromServer,
  mcpServerSlug,
  mcpServerStatus,
  normalizeMcpCatalog,
} from "./mcp-servers.js";

class McpServersState {
  servers = $state([]);
  available = $state(true);
  loading = $state(false);
  // Load and in-form errors only; row-action feedback goes through the
  // flash store.
  error = $state("");
  filter = $state("");

  formOpen = $state(false);
  formSubmitting = $state(false);
  formMode = $state("create");
  slugEdited = $state(false);
  advancedOpen = $state(false);
  form = $state(defaultMcpServerForm());

  deletingName = $state("");
  reconnectingName = $state("");

  catalogOpen = $state(false);
  catalogLoading = $state(false);
  catalogError = $state("");
  catalog = $state(defaultMcpCatalog());

  filtered = $derived(filterMcpServers(this.servers, this.filter));

  // --- server list -------------------------------------------------------

  async fetchServers() {
    // Wait for the shared runtime-config request before deciding whether the
    // MCP admin API is available.
    await runtimeConfig.ensureLoaded();
    if (!runtimeConfig.mcpVisible()) {
      this.available = false;
      this.servers = [];
      this.error = "";
      this.loading = false;
      return;
    }

    this.loading = true;
    this.error = "";
    try {
      const outcome = await loadAdminList("/admin/mcp-servers", {
        label: "mcp servers",
        errorFallback: "Failed to load MCP servers.",
        unavailableStatuses: [503, 404],
      });
      if (outcome.status === "stale") {
        return;
      }
      if (outcome.status === "unavailable") {
        this.available = false;
        this.servers = [];
        return;
      }
      if (outcome.status === "error") {
        // A gateway response proves the endpoint exists; a network failure
        // (result === null) leaves the availability flag as-is.
        if (outcome.result) {
          this.available = true;
        }
        this.servers = [];
        this.error = outcome.error;
        return;
      }
      this.available = true;
      this.servers = outcome.items;
    } finally {
      this.loading = false;
    }
  }

  // --- editor form -------------------------------------------------------

  openCreate() {
    this.formMode = "create";
    this.slugEdited = false;
    this.advancedOpen = false;
    this.error = "";
    this.form = defaultMcpServerForm();
    this.formOpen = true;
  }

  openEdit(server) {
    if (!server || server.managed) {
      return;
    }
    this.formMode = "edit";
    this.slugEdited = true;
    this.advancedOpen = false;
    this.error = "";
    this.form = mcpServerFormFromServer(server);
    this.formOpen = true;
  }

  closeForm() {
    this.formOpen = false;
    this.formMode = "create";
    this.slugEdited = false;
    this.advancedOpen = false;
    this.error = "";
    this.form = defaultMcpServerForm();
  }

  syncSlugFromName() {
    if (this.formMode === "create" && !this.slugEdited) {
      this.form.slug = deriveMcpServerSlug(this.form.name);
    }
  }

  markSlugEdited() {
    if (this.formMode === "create") {
      this.slugEdited = true;
    }
  }

  addHeader() {
    this.form.headers.push({ name: "", value: "" });
  }

  removeHeader(index) {
    this.form.headers.splice(index, 1);
  }

  async submitForm() {
    const built = buildMcpServerPayload(this.form, this.formMode, this.servers);
    if (built.error) {
      this.error = built.error;
      return;
    }

    this.error = "";
    this.formSubmitting = true;

    try {
      const outcome = await sendAdminMutation(
        "/admin/mcp-servers",
        "PUT",
        built.payload,
        {
          label: "save mcp server",
          errorFallback: "Failed to save MCP server.",
          unavailableMessage: "MCP server management is unavailable.",
        },
      );
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
        return;
      }

      flash.success('MCP server "' + built.payload.name + '" saved.');
      this.closeForm();
      void this.fetchServers();
    } finally {
      this.formSubmitting = false;
    }
  }

  // --- row actions -------------------------------------------------------

  async deleteServer(server) {
    const name = String((server && server.name) || "").trim();
    const slug = mcpServerSlug(server);
    if (!slug || this.deletingName || (server && server.managed)) {
      return;
    }
    if (
      !confirm(
        'Delete MCP server "' +
          name +
          '"? Clients lose access to its tools immediately.',
      )
    ) {
      return;
    }

    this.deletingName = slug;

    try {
      const outcome = await sendAdminMutation(
        "/admin/mcp-servers/" + encodeURIComponent(slug),
        "DELETE",
        undefined,
        {
          label: "delete mcp server",
          errorFallback: "Failed to delete MCP server.",
          unavailableMessage: "MCP server management is unavailable.",
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
        flash.error(outcome.error);
        return;
      }

      flash.success('MCP server "' + name + '" deleted.');
      if (this.formOpen && this.form.slug === slug) {
        this.closeForm();
      }
      void this.fetchServers();
    } finally {
      this.deletingName = "";
    }
  }

  async reconnectServer(server) {
    const name = String((server && server.name) || "").trim();
    const slug = mcpServerSlug(server);
    if (!slug || this.reconnectingName) {
      return;
    }

    this.reconnectingName = slug;

    try {
      const outcome = await sendAdminMutation(
        "/admin/mcp-servers/" + encodeURIComponent(slug) + "/reconnect",
        "POST",
        undefined,
        {
          label: "reconnect mcp server",
          errorFallback: "Failed to reconnect MCP server.",
          unavailableMessage: "MCP server management is unavailable.",
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
        flash.error(outcome.error);
        return;
      }

      const refreshed = outcome.result.data;
      const status = mcpServerStatus(refreshed);
      if (status === "connected") {
        flash.success('MCP server "' + name + '" reconnected.');
      } else if (status === "disabled") {
        flash.success(
          'MCP server "' + name + '" is disabled; no connection was attempted.',
        );
      } else {
        flash.error(
          'Reconnect attempted, but MCP server "' +
            name +
            '" is still ' +
            status +
            ".",
        );
      }
      if (refreshed && refreshed.name) {
        this.servers = (this.servers || []).map((item) =>
          mcpServerSlug(item) === mcpServerSlug(refreshed) ? refreshed : item,
        );
      } else {
        void this.fetchServers();
      }
    } finally {
      this.reconnectingName = "";
    }
  }

  // --- catalog inspector -------------------------------------------------
  // Hand-rolled on purpose: a single-resource GET whose 404 means "this
  // server does not exist" (with its own message), not "feature disabled",
  // so it does not map onto the shared list/mutation ladder.

  async openCatalog(server) {
    const name = String((server && server.name) || "").trim();
    const slug = mcpServerSlug(server);
    if (!slug) {
      return;
    }

    this.catalogOpen = true;
    this.catalogLoading = true;
    this.catalogError = "";
    this.catalog = {
      ...defaultMcpCatalog(),
      server: slug,
      status: mcpServerStatus(server),
    };

    try {
      const result = await getJSON(
        "/admin/mcp-servers/" + encodeURIComponent(slug) + "/catalog",
        { label: "mcp server catalog" },
      );
      if (result.stale) {
        return;
      }
      if (result.status === 503) {
        this.available = false;
        this.catalogError = "MCP server management is unavailable.";
        return;
      }
      if (result.status === 404) {
        this.catalogError = 'MCP server "' + name + '" was not found.';
        return;
      }
      if (!result.ok) {
        this.catalogError =
          result.status === 401
            ? "Authentication required."
            : errorPayloadMessage(
                result.data,
                "Failed to load MCP server catalog.",
              );
        return;
      }
      this.catalog = normalizeMcpCatalog(slug, result.data);
    } catch (e) {
      console.error("Failed to load MCP server catalog:", e);
      this.catalogError = "Failed to load MCP server catalog.";
    } finally {
      this.catalogLoading = false;
    }
  }

  closeCatalog() {
    this.catalogOpen = false;
    this.catalogLoading = false;
    this.catalogError = "";
    this.catalog = defaultMcpCatalog();
  }
}

export const mcpServers = new McpServersState();
