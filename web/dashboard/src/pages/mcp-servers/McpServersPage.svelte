<script>
  // MCP Servers page: list, filter, create/edit, reconnect, delete, and the
  // per-server catalog inspector. The server list is fetched when this page
  // is active (the overview card does its own count fetch) and again
  // whenever the API key changes.
  import LoadingState from "$lib/components/molecules/LoadingState.svelte";
  import Icon from "$lib/components/atoms/Icon.svelte";
  import FilterInput from "$lib/components/molecules/FilterInput.svelte";
  import InlineHelpSection from "$lib/components/molecules/InlineHelpSection.svelte";
  import { auth } from "$lib/stores/auth.svelte.ts";
  import { router } from "$lib/stores/router.svelte.ts";
  import McpCatalogModal from "./McpCatalogModal.svelte";
  import McpServerEditor from "./McpServerEditor.svelte";
  import McpServerList from "./McpServerList.svelte";
  import { mcpServers } from "./mcpServers.svelte.js";

  const PAGE = "mcp-servers";
  const HELP_TEXT =
    "Upstream Model Context Protocol servers whose tools, prompts, and resources the gateway exposes to clients. Servers added here connect over HTTP or SSE; stdio servers and rows marked Config are declared in config.yaml under mcp.servers and are read-only in the dashboard. Saved header values are masked in API and dashboard responses.";

  // Re-fetch when the page becomes active or the API key changes.
  $effect(() => {
    void auth.refreshTick;
    if (router.page === PAGE) mcpServers.fetchServers();
  });
</script>

<div>
  <div class="page-header">
    <div>
      <InlineHelpSection copyId="mcp-servers-help-copy" label="MCP servers help" text={HELP_TEXT}>
        {#snippet title()}<h2>MCP Servers</h2>{/snippet}
      </InlineHelpSection>
    </div>
    <div class="page-header-controls">
      {#if mcpServers.available && !auth.authError}
        <button
          type="button"
          class="btn btn-primary btn-with-icon"
          disabled={mcpServers.formSubmitting}
          onclick={() => mcpServers.openCreate()}
        >
          <Icon name="plus" class="form-action-icon" />
          <span>Add MCP Server</span>
        </button>
      {/if}
    </div>
  </div>

  {#if !mcpServers.available && !auth.authError}
    <div class="alert alert-warning">MCP server management is unavailable.</div>
  {/if}
  {#if mcpServers.error && !auth.authError && !mcpServers.formOpen}
    <p class="form-error" role="alert" aria-live="assertive">{mcpServers.error}</p>
  {/if}
  {#if mcpServers.loading && !auth.authError}
    <LoadingState label="Loading MCP servers..." />
  {/if}

  {#if (mcpServers.servers.length > 0 || mcpServers.filter) && mcpServers.available && !auth.authError}
    <div class="table-toolbar">
      <div class="table-toolbar-main">
        <FilterInput
          id="mcp-server-filter"
          placeholder="Filter by name, slug, URL, transport, or status..."
          label="Filter MCP servers by name, slug, URL, transport, or status"
          bind:value={mcpServers.filter}
        />
      </div>
    </div>
  {/if}

  <McpServerEditor />
  <McpCatalogModal />

  {#if mcpServers.filtered.length > 0 && mcpServers.available && !auth.authError}
    <McpServerList />
  {/if}

  {#if mcpServers.servers.length === 0 && !mcpServers.filter && !mcpServers.loading && !auth.authError && !mcpServers.error && mcpServers.available}
    <p class="empty-state">
      No MCP servers yet. Add one here, or declare servers in <code>config.yaml</code> under
      <code>mcp.servers</code>.
    </p>
  {/if}
  {#if mcpServers.servers.length > 0 && mcpServers.filtered.length === 0 && mcpServers.filter && !mcpServers.loading && !auth.authError && mcpServers.available}
    <p class="empty-state">No MCP servers match your filter.</p>
  {/if}
</div>
