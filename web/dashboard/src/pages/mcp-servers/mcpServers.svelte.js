// MCP Servers page state: fetch/save/delete/reconnect/catalog flows on top
// of the shared admin API client. Pure helpers live in ./mcp-servers.js so
// tests can exercise them without Svelte.

import { errorPayloadMessage, getJSON } from "$lib/api/client.js";
import { loadAdminList, sendAdminMutation } from "$lib/api/adminCrud.js";
import { flash } from "$lib/stores/flash.svelte.js";
import { runtimeConfig } from "$lib/stores/runtimeConfig.svelte.js";
import * as m from "$lib/paraglide/messages.js";
import {
  buildMcpServerPayload,
  defaultMcpCatalog,
  defaultMcpServerForm,
  deriveMcpServerSlug,
  filterMcpServers,
  mcpDiscoveredTools,
  mcpServerFormFromServer,
  mcpPollShouldRetry,
  mcpServerSlug,
  mcpServersNeedPolling,
  mcpServerStatus,
  normalizeMcpCatalog,
  setMcpToolsExposed,
  switchMcpToolMode,
  MCP_SERVERS_POLL_MS,
  MCP_TOOL_MODE_ALLOW,
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

  // Editor tool picker: every tool the server reports, exposed or not. A new
  // server, or one that never listed, has none yet.
  editorTools = $state({ loading: false, error: "", tools: [] });
  toolQuery = $state("");
  toolDraft = $state("");

  deletingName = $state("");
  reconnectingName = $state("");

  catalogOpen = $state(false);
  catalogLoading = $state(false);
  catalogError = $state("");
  catalog = $state(defaultMcpCatalog());

  filtered = $derived(filterMcpServers(this.servers, this.filter));

  #pollTimer = null;
  // stopPolling starts a new generation. A list request already in flight
  // then belongs to the previous one: clearing the timer cannot cancel it, so
  // the generation is what keeps its response from scheduling a fresh timer
  // after the page is gone.
  #pollGeneration = 0;
  #pollFailures = 0;
  // Row actions and the poll timer can both have a list request in flight.
  // loadAdminList only reports "stale" when the API key changed, so the
  // newest request wins here: an older response must not restore the list a
  // delete just removed, or replace the timer the newer one scheduled.
  #listSeq = 0;

  // --- server list -------------------------------------------------------

  // background=true is the poll loop re-fetching: it leaves the loading flag
  // alone so the settled list never flickers back to the spinner.
  async fetchServers({ background = false } = {}) {
    // Both captured before the first await, so a cleanup or a newer request
    // during any suspension of this one — not just the list call — retires it.
    const generation = this.#pollGeneration;
    const seq = ++this.#listSeq;
    // Wait for the shared runtime-config request before deciding whether the
    // MCP admin API is available.
    await runtimeConfig.ensureLoaded();
    if (generation !== this.#pollGeneration) {
      return;
    }
    if (!runtimeConfig.mcpVisible()) {
      this.stopPolling();
      this.available = false;
      this.servers = [];
      this.error = "";
      this.loading = false;
      return;
    }

    this.#clearPoll();
    if (!background) {
      this.loading = true;
      this.#pollFailures = 0;
    }
    this.error = "";
    try {
      const outcome = await loadAdminList("/admin/mcp-servers", {
        label: "mcp servers",
        errorFallback: m.mcp_load_failed(),
        unavailableStatuses: [503, 404],
      });
      if (outcome.status === "stale" || seq !== this.#listSeq) {
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
        // A failed background poll keeps the list it already has: a blip
        // while waiting for a dial must not blank the table or replace it
        // with an error the operator never asked for. It retries, since the
        // pending row is exactly what the loop is waiting on, until the
        // failure budget runs out.
        if (background) {
          this.#pollFailures += 1;
          if (mcpPollShouldRetry(this.#pollFailures)) {
            this.#schedulePoll(generation);
          }
          return;
        }
        this.servers = [];
        this.error = outcome.error;
        return;
      }
      this.available = true;
      this.servers = outcome.items;
      this.#pollFailures = 0;
      this.#schedulePoll(generation);
    } finally {
      if (!background && seq === this.#listSeq) {
        this.loading = false;
      }
    }
  }

  // --- connect poll ------------------------------------------------------

  #schedulePoll(generation) {
    // The generation check comes first: a response from a stopped loop must
    // not clear a timer a newer load already scheduled.
    if (generation !== this.#pollGeneration) {
      return;
    }
    this.#clearPoll();
    if (!mcpServersNeedPolling(this.servers)) {
      return;
    }
    this.#pollTimer = setTimeout(() => {
      this.#pollTimer = null;
      void this.fetchServers({ background: true });
    }, MCP_SERVERS_POLL_MS);
  }

  #clearPoll() {
    if (this.#pollTimer) {
      clearTimeout(this.#pollTimer);
      this.#pollTimer = null;
    }
  }

  // stopPolling is called when the page is left, so neither a timer nor an
  // in-flight request outlives it.
  stopPolling() {
    this.#pollGeneration += 1;
    this.#pollFailures = 0;
    this.#clearPoll();
  }

  // --- editor form -------------------------------------------------------

  openCreate() {
    this.formMode = "create";
    this.slugEdited = false;
    this.advancedOpen = false;
    this.error = "";
    this.form = defaultMcpServerForm();
    this.#resetEditorTools();
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
    this.#resetEditorTools();
    this.formOpen = true;
    void this.#loadEditorTools(server);
  }

  closeForm() {
    this.formOpen = false;
    this.formMode = "create";
    this.slugEdited = false;
    this.advancedOpen = false;
    this.error = "";
    this.form = defaultMcpServerForm();
    this.#resetEditorTools();
  }

  #resetEditorTools() {
    this.editorTools = { loading: false, error: "", tools: [] };
    this.toolQuery = "";
    this.toolDraft = "";
  }

  async #loadEditorTools(server) {
    const slug = mcpServerSlug(server);
    this.editorTools = { ...this.editorTools, loading: true, error: "" };
    const loaded = await this.#fetchCatalog(server);
    // The editor may have closed or moved to another server meanwhile.
    if (!this.formOpen || this.form.slug !== slug) {
      return;
    }
    if (loaded.stale) {
      this.editorTools = { ...this.editorTools, loading: false };
      return;
    }
    this.editorTools = {
      loading: false,
      error: loaded.error || "",
      tools: loaded.error ? [] : mcpDiscoveredTools(loaded.catalog),
    };
  }

  setToolsExposed(names, exposed) {
    this.form.tool_names = setMcpToolsExposed(this.form, names, exposed);
  }

  switchToolMode(mode) {
    const next = switchMcpToolMode(this.form, mode, this.editorTools.tools);
    this.form.tool_mode = next.tool_mode;
    this.form.tool_names = next.tool_names;
  }

  addToolDraft() {
    const name = this.toolDraft.trim();
    if (!name) {
      return;
    }
    // Adding a name lists it in the current mode: excluded or allowed.
    this.setToolsExposed([name], this.form.tool_mode === MCP_TOOL_MODE_ALLOW);
    this.toolDraft = "";
  }

  removeToolName(name) {
    this.form.tool_names = (this.form.tool_names || []).filter((item) => item !== name);
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
          errorFallback: m.mcp_save_failed(),
          unavailableMessage: m.mcp_unavailable(),
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

      flash.success(m.mcp_saved({ name: built.payload.name }));
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
        m.mcp_delete_confirm({ name }),
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
          errorFallback: m.mcp_delete_failed(),
          unavailableMessage: m.mcp_unavailable(),
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

      flash.success(m.mcp_deleted({ name }));
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
          errorFallback: m.mcp_reconnect_failed(),
          unavailableMessage: m.mcp_unavailable(),
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
        flash.success(m.mcp_reconnected({ name }));
      } else if (status === "disabled") {
        flash.success(m.mcp_reconnect_disabled({ name }));
      } else {
        flash.error(m.mcp_reconnect_status({ name, status }));
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

    const loaded = await this.#fetchCatalog(server);
    this.catalogLoading = false;
    if (loaded.stale) {
      return;
    }
    if (loaded.error) {
      this.catalogError = loaded.error;
      return;
    }
    this.catalog = loaded.catalog;
  }

  // chooseToolsFromCatalog jumps from the read-only inspector to the editor's
  // tool picker for the same server.
  chooseToolsFromCatalog() {
    const server = (this.servers || []).find(
      (item) => mcpServerSlug(item) === this.catalog.server,
    );
    if (!server || server.managed) {
      return;
    }
    this.closeCatalog();
    this.openEdit(server);
  }

  // #fetchCatalog resolves to { catalog }, { error }, or { stale }.
  async #fetchCatalog(server) {
    const name = String((server && server.name) || "").trim();
    const slug = mcpServerSlug(server);
    try {
      const result = await getJSON(
        "/admin/mcp-servers/" + encodeURIComponent(slug) + "/catalog",
        { label: "mcp server catalog" },
      );
      if (result.stale) {
        return { stale: true };
      }
      if (result.status === 503) {
        this.available = false;
        return { error: m.mcp_unavailable() };
      }
      if (result.status === 404) {
        return { error: m.mcp_not_found({ name }) };
      }
      if (!result.ok) {
        return {
          error:
            result.status === 401
              ? m.common_authentication_required()
              : errorPayloadMessage(result.data, m.mcp_catalog_load_failed()),
        };
      }
      return { catalog: normalizeMcpCatalog(slug, result.data) };
    } catch (e) {
      console.error("Failed to load MCP server catalog:", e);
      return { error: m.mcp_catalog_load_failed() };
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
