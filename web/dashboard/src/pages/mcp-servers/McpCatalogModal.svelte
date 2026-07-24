<script>
  // Catalog inspector: read-only view of what one server currently exposes
  // (tools, prompts, resources, resource templates). Tools and prompts also
  // show their namespaced name on the aggregated /mcp endpoint.
  import LoadingState from "$lib/components/molecules/LoadingState.svelte";
  import DialogCloseButton from "$lib/components/atoms/DialogCloseButton.svelte";
  import Modal from "$lib/components/atoms/Modal.svelte";
  import { mcpServers } from "./mcpServers.svelte.js";
  import {
    mcpCatalogIsEmpty,
    mcpCatalogSections,
    mcpServerStatus,
    mcpServerStatusClass,
  } from "./mcp-servers.js";

  const sections = $derived(mcpCatalogSections(mcpServers.catalog));
</script>

<Modal open={mcpServers.catalogOpen} variant="editor" onclose={() => mcpServers.closeCatalog()}>
  <div class="model-editor" role="dialog" aria-modal="true" aria-label="MCP server catalog">
    <div class="editor-header">
      <div>
        <h3>Server Catalog</h3>
        <p class="form-hint mcp-catalog-subtitle">
          <code class="mono">{mcpServers.catalog.server}</code>
          <span class="audit-status-badge {mcpServerStatusClass(mcpServers.catalog)}"
            >{mcpServerStatus(mcpServers.catalog)}</span
          >
        </p>
      </div>
      <DialogCloseButton
        label="Close MCP server catalog"
        onclick={() => mcpServers.closeCatalog()}
      />
    </div>

    {#if mcpServers.catalogLoading}
      <LoadingState label="Loading catalog..." />
    {:else if mcpServers.catalogError}
      <p class="form-error" role="alert" aria-live="assertive">{mcpServers.catalogError}</p>
    {:else}
      {#if mcpServers.catalog.instructions}
        <p class="form-hint">{mcpServers.catalog.instructions}</p>
      {/if}

      {#each sections as section (section.key)}
        <section class="form-section mcp-catalog-section">
          <h4 class="form-field-label">{section.title}</h4>
          <ul class="mcp-catalog-list">
            {#each section.items as item (item.key)}
              <li class="mcp-catalog-item">
                <code class="mcp-catalog-item-name mono" title={item.aggregated || item.name}
                  >{item.name}</code
                >
                {#if item.aggregated}
                  <div
                    class="mcp-catalog-item-aggregated mono"
                    title={"Exposed on the aggregated /mcp endpoint as " + item.aggregated}
                  >
                    {item.aggregated}
                  </div>
                {/if}
                {#if item.description}
                  <p class="mcp-catalog-item-description">{item.description}</p>
                {/if}
              </li>
            {/each}
          </ul>
        </section>
      {/each}

      {#if mcpCatalogIsEmpty(mcpServers.catalog)}
        <p class="empty-state">No tools listed — the server may still be connecting or degraded.</p>
      {/if}
    {/if}

    <div class="form-actions">
      <button type="button" class="pagination-btn" onclick={() => mcpServers.closeCatalog()}>Close</button>
    </div>
  </div>
</Modal>
