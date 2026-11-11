<script>
  // All-tools view: every tool exposed across all servers in one searchable
  // list, named as clients call it ({slug}_{name}). It can be narrowed to one
  // virtual server's members and can include the tools filters hide. The
  // download actions fetch the server-side export of the same endpoint.
  import DialogCloseButton from "$lib/components/atoms/DialogCloseButton.svelte";
  import HighlightText from "$lib/components/atoms/HighlightText.svelte";
  import Icon from "$lib/components/atoms/Icon.svelte";
  import Modal from "$lib/components/atoms/Modal.svelte";
  import FilterInput from "$lib/components/molecules/FilterInput.svelte";
  import LoadingState from "$lib/components/molecules/LoadingState.svelte";
  import { apiFetch } from "$lib/api/client.js";
  import { errorPayloadMessage } from "$lib/api/errors.js";
  import { flash } from "$lib/stores/flash.svelte.js";
  import { formatNumber } from "$lib/utils/format.js";
  import { Download } from "lucide";
  import { mcpServers } from "./mcpServers.svelte.js";
  import {
    mcpServedVirtualServers,
    mcpServerStatusLabel,
    mcpToolCatalogExport,
    mcpToolCatalogIssues,
    mcpToolCatalogMembers,
    mcpToolCatalogRows,
    mcpToolCatalogSummary,
  } from "./mcp-servers.js";
  import * as m from "$lib/paraglide/messages.js";

  const virtualServers = $derived(mcpServedVirtualServers(mcpServers.virtualServers));
  const members = $derived(
    mcpToolCatalogMembers(virtualServers, mcpServers.toolCatalogScope),
  );
  const rows = $derived(
    mcpToolCatalogRows(mcpServers.toolCatalogEntries, {
      members,
      showExcluded: mcpServers.toolCatalogShowExcluded,
      query: mcpServers.toolCatalogQuery,
    }),
  );
  const summary = $derived(mcpToolCatalogSummary(mcpServers.toolCatalogEntries, members));
  const issues = $derived(mcpToolCatalogIssues(mcpServers.toolCatalogEntries, members));

  let exporting = $state(false);

  // The file is the full inventory of the endpoint in view: it ignores the
  // search and always includes the tools filters hide, marked as not exposed.
  async function exportCatalog(format) {
    const target = mcpToolCatalogExport(format, mcpServers.toolCatalogScope);
    exporting = true;
    try {
      const res = await apiFetch(target.path);
      if (!res.ok) {
        let data = null;
        try {
          data = await res.json();
        } catch {
          data = null;
        }
        flash.error(errorPayloadMessage(data, m.mcp_all_tools_export_failed()));
        return;
      }
      const url = URL.createObjectURL(await res.blob());
      const link = document.createElement("a");
      link.href = url;
      link.download = target.filename;
      link.click();
      URL.revokeObjectURL(url);
    } catch (e) {
      console.error("Failed to export MCP tool catalog:", e);
      flash.error(m.mcp_all_tools_export_failed());
    } finally {
      exporting = false;
    }
  }
</script>

<Modal
  open={mcpServers.toolCatalogOpen}
  variant="editor"
  onclose={() => mcpServers.closeToolCatalog()}
>
  <div class="model-editor" role="dialog" aria-modal="true" aria-label={m.mcp_all_tools_label()}>
    <div class="editor-header">
      <div>
        <h3>{m.mcp_all_tools_title()}</h3>
        <p class="form-hint">{m.mcp_all_tools_hint()}</p>
      </div>
      <DialogCloseButton
        label={m.mcp_all_tools_close()}
        onclick={() => mcpServers.closeToolCatalog()}
      />
    </div>

    {#if mcpServers.toolCatalogLoading}
      <LoadingState label={m.mcp_all_tools_loading()} />
    {:else}
      <div class="mcp-all-tools-toolbar">
        <FilterInput
          bind:value={mcpServers.toolCatalogQuery}
          placeholder={m.mcp_all_tools_filter()}
          label={m.mcp_all_tools_filter()}
          onclear={() => (mcpServers.toolCatalogQuery = "")}
        />
        {#if virtualServers.length > 0}
          <select
            class="form-select mcp-all-tools-scope"
            aria-label={m.mcp_all_tools_scope_label()}
            bind:value={mcpServers.toolCatalogScope}
          >
            <option value="">{m.mcp_connect_target_all()}</option>
            {#each virtualServers as entry (entry.name)}
              <option value={entry.name}>{m.mcp_connect_target_virtual({ name: entry.name })}</option>
            {/each}
          </select>
        {/if}
        <label class="mcp-all-tools-excluded-toggle">
          <input type="checkbox" bind:checked={mcpServers.toolCatalogShowExcluded} />
          <span>{m.mcp_all_tools_show_excluded()}</span>
        </label>
      </div>

      <p class="form-hint mcp-all-tools-summary">
        {m.mcp_all_tools_summary({
          tools: formatNumber(summary.tools),
          servers: formatNumber(summary.servers),
        })}
      </p>

      {#if issues.length > 0}
        <div class="alert alert-warning mcp-all-tools-issues">
          <p>{m.mcp_all_tools_issues()}</p>
          <ul>
            {#each issues as issue (issue.server)}
              <li>
                <code class="mono">{issue.server}</code>
                <span class="audit-status-badge {issue.statusClass}"
                  >{mcpServerStatusLabel(issue)}</span
                >
                {#if issue.error}
                  <span class="mcp-all-tools-issue-error">{issue.error}</span>
                {/if}
              </li>
            {/each}
          </ul>
        </div>
      {/if}

      {#if rows.length > 0}
        <ul class="mcp-all-tools-list">
          {#each rows as row (row.key)}
            <li
              class="mcp-all-tools-item"
              class:mcp-all-tools-item-excluded={row.excluded || row.shadowedBy}
            >
              <code class="mcp-all-tools-name mono"
                ><HighlightText text={row.aggregated || row.name} needle={mcpServers.toolCatalogQuery} /></code
              >
              {#if row.excluded}
                <span class="mcp-all-tools-badge" title={m.mcp_catalog_excluded_tools_hint()}
                  >{m.mcp_all_tools_hidden()}</span
                >
              {/if}
              {#if row.shadowedBy}
                <span
                  class="mcp-all-tools-badge"
                  title={m.mcp_all_tools_shadowed_title({ server: row.shadowedBy })}
                  >{m.mcp_all_tools_shadowed({ server: row.shadowedBy })}</span
                >
              {/if}
              {#if row.readOnly}
                <span class="mcp-all-tools-badge" title={m.mcp_tools_hint_title()}
                  >{m.mcp_tools_read_only()}</span
                >
              {/if}
              {#if row.destructive}
                <span
                  class="mcp-all-tools-badge mcp-all-tools-badge-danger"
                  title={m.mcp_tools_hint_title()}>{m.mcp_tools_destructive()}</span
                >
              {/if}
              <div class="mcp-all-tools-server mono">
                {m.mcp_all_tools_server({ server: row.server, name: row.name })}
              </div>
              {#if row.description}
                <p class="mcp-all-tools-description">
                  <HighlightText text={row.description} needle={mcpServers.toolCatalogQuery} />
                </p>
              {/if}
            </li>
          {/each}
        </ul>
      {:else if mcpServers.toolCatalogQuery}
        <p class="empty-state">{m.mcp_all_tools_no_match()}</p>
      {:else if issues.length === 0}
        <p class="empty-state">{m.mcp_all_tools_empty()}</p>
      {/if}
    {/if}

    <div class="form-actions">
      <button
        type="button"
        class="btn btn-with-icon"
        title={m.mcp_all_tools_export_title()}
        disabled={exporting}
        onclick={() => exportCatalog("csv")}
      >
        <Icon icon={Download} class="form-action-icon" />
        <span>{m.mcp_all_tools_export_csv()}</span>
      </button>
      <button
        type="button"
        class="btn btn-with-icon"
        title={m.mcp_all_tools_export_title()}
        disabled={exporting}
        onclick={() => exportCatalog("json")}
      >
        <Icon icon={Download} class="form-action-icon" />
        <span>{m.mcp_all_tools_export_json()}</span>
      </button>
      <button type="button" class="btn" onclick={() => mcpServers.closeToolCatalog()}
        >{m.mcp_close()}</button
      >
    </div>
  </div>
</Modal>

<style>
  .mcp-all-tools-toolbar {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 8px 12px;
    margin-top: 12px;
  }

  .mcp-all-tools-toolbar :global(.filter-input-wrap) {
    flex: 1 1 220px;
  }

  .mcp-all-tools-scope {
    width: auto;
    max-width: 100%;
  }

  .mcp-all-tools-excluded-toggle {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    font-size: 13px;
  }

  .mcp-all-tools-summary {
    margin: 10px 0 0;
  }

  .mcp-all-tools-issues {
    margin-top: 12px;

    p {
      margin: 0 0 6px;
    }

    ul {
      margin: 0;
      padding-left: 18px;
    }

    li + li {
      margin-top: 4px;
    }
  }

  .mcp-all-tools-issue-error {
    font-size: 12px;
    overflow-wrap: anywhere;
  }

  .mcp-all-tools-list {
    list-style: none;
    margin: 16px 0 8px;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 12px;
  }

  .mcp-all-tools-name {
    font-size: 13px;
    overflow-wrap: anywhere;
  }

  .mcp-all-tools-item-excluded .mcp-all-tools-name {
    color: var(--text-muted);
    text-decoration: line-through;
  }

  .mcp-all-tools-badge {
    display: inline-block;
    margin-left: 6px;
    padding: 0 6px;
    border: 1px solid var(--border);
    border-radius: 999px;
    color: var(--text-muted);
    font-size: 11px;
    line-height: 18px;
    vertical-align: middle;
  }

  .mcp-all-tools-badge-danger {
    border-color: color-mix(in srgb, var(--danger) 35%, var(--border));
    color: var(--danger);
  }

  .mcp-all-tools-server {
    margin-top: 2px;
    color: var(--text-muted);
    font-size: 11px;
    overflow-wrap: anywhere;
  }

  .mcp-all-tools-description {
    margin: 2px 0 0;
    color: var(--text-muted);
    font-size: 13px;
    font-weight: 400;
  }
</style>
