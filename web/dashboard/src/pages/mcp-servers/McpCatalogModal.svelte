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
  import * as m from "$lib/paraglide/messages.js";

  const sections = $derived(mcpCatalogSections(mcpServers.catalog));
</script>

<Modal open={mcpServers.catalogOpen} variant="editor" onclose={() => mcpServers.closeCatalog()}>
  <div class="model-editor" role="dialog" aria-modal="true" aria-label={m.mcp_catalog_label()}>
    <div class="editor-header">
      <div>
        <h3>{m.mcp_catalog_title()}</h3>
        <p class="form-hint mcp-catalog-subtitle">
          <code class="mono">{mcpServers.catalog.server}</code>
          <span class="audit-status-badge {mcpServerStatusClass(mcpServers.catalog)}"
            >{mcpServerStatus(mcpServers.catalog)}</span
          >
        </p>
      </div>
      <DialogCloseButton
        label={m.mcp_catalog_close()}
        onclick={() => mcpServers.closeCatalog()}
      />
    </div>

    {#if mcpServers.catalogLoading}
      <LoadingState label={m.mcp_catalog_loading()} />
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
                    title={m.mcp_catalog_exposed({ name: item.aggregated })}
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
        <p class="empty-state">{m.mcp_catalog_empty()}</p>
      {/if}
    {/if}

    <div class="form-actions">
      <button type="button" class="btn" onclick={() => mcpServers.closeCatalog()}>{m.mcp_close()}</button>
    </div>
  </div>
</Modal>

<style>
  /* MCP catalog inspector: read-only lists of tools, prompts, and resources. */
  .mcp-catalog-section {
    margin-top: 18px;
  }

  .mcp-catalog-section :global(.form-field-label) {
    margin-bottom: 8px;
  }

  .mcp-catalog-subtitle {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 8px;
  }

  .mcp-catalog-list {
    list-style: none;
    margin: 0 0 8px;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 12px;
  }

  .mcp-catalog-item-name {
    font-size: 13px;
    overflow-wrap: anywhere;
  }

  .mcp-catalog-item-aggregated {
    margin-top: 2px;
    color: var(--text-muted);
    font-size: 11px;
    overflow-wrap: anywhere;
  }

  .mcp-catalog-item-description {
    margin: 2px 0 0;
    color: var(--text-muted);
    font-size: 13px;
    font-weight: 400;
  }
</style>
