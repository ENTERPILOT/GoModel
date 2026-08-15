<script>
  // MCP Servers page: list, filter, create/edit, reconnect, delete, and the
  // per-server catalog inspector. The server list is fetched when this page
  // is active (the overview card does its own count fetch) and again
  // whenever the API key changes.
  import LoadingState from "$lib/components/molecules/LoadingState.svelte";
  import Icon from "$lib/components/atoms/Icon.svelte";
  import FilterInput from "$lib/components/molecules/FilterInput.svelte";
  import InlineHelpSection from "$lib/components/molecules/InlineHelpSection.svelte";
  import { auth } from "$lib/stores/auth.svelte.js";
  import { router } from "$lib/stores/router.svelte.js";
  import McpCatalogModal from "./McpCatalogModal.svelte";
  import McpServerEditor from "./McpServerEditor.svelte";
  import McpServerList from "./McpServerList.svelte";
  import { mcpServers } from "./mcpServers.svelte.js";
  import { Plus } from "lucide";
  import * as m from "$lib/paraglide/messages.js";

  const PAGE = "mcp-servers";
  const HELP_TEXT = m.mcp_help();

  // Re-fetch when the page becomes active or the API key changes.
  $effect(() => {
    void auth.refreshTick;
    if (router.page === PAGE) mcpServers.fetchServers();
  });
</script>

<div>
  <div class="page-header">
    <div>
      <InlineHelpSection copyId="mcp-servers-help-copy" label={m.mcp_help_label()} text={HELP_TEXT}>
        {#snippet title()}<h2>{m.mcp_title()}</h2>{/snippet}
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
          <Icon icon={Plus} class="form-action-icon" />
          <span>{m.mcp_add()}</span>
        </button>
      {/if}
    </div>
  </div>

  {#if !mcpServers.available && !auth.authError}
    <div class="alert alert-warning">{m.mcp_unavailable()}</div>
  {/if}
  {#if mcpServers.error && !auth.authError && !mcpServers.formOpen}
    <p class="form-error" role="alert" aria-live="assertive">{mcpServers.error}</p>
  {/if}
  {#if mcpServers.loading && !auth.authError}
    <LoadingState label={m.mcp_loading()} />
  {/if}

  {#if (mcpServers.servers.length > 0 || mcpServers.filter) && mcpServers.available && !auth.authError}
    <div class="table-toolbar">
      <div class="table-toolbar-main">
        <FilterInput
          id="mcp-server-filter"
          placeholder={m.mcp_filter_placeholder()}
          label={m.mcp_filter_label()}
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
      {m.mcp_empty()}
    </p>
  {/if}
  {#if mcpServers.servers.length > 0 && mcpServers.filtered.length === 0 && mcpServers.filter && !mcpServers.loading && !auth.authError && mcpServers.available}
    <p class="empty-state">{m.mcp_no_match()}</p>
  {/if}
</div>
