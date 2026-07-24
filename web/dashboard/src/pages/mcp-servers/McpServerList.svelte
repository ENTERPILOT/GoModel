<script>
  // MCP server table: status pills, tool counts, and row actions
  // (edit / inspect catalog / reconnect / delete). Managed (config-declared)
  // rows are read-only: no edit or delete buttons.
  import TableActionButton from "$lib/components/atoms/TableActionButton.svelte";
  import Icon from "$lib/components/atoms/Icon.svelte";
  import { timezone } from "$lib/stores/timezone.svelte.js";
  import { formatNumber } from "$lib/utils/format.js";
  import { mcpServers } from "./mcpServers.svelte.js";
  import {
    mcpServerEndpointLabel,
    mcpServerSlug,
    mcpServerStatus,
    mcpServerStatusClass,
    mcpServerStatusTitle,
    mcpServerSubCountsLabel,
  } from "./mcp-servers.js";

  function statusTitle(server) {
    return mcpServerStatusTitle(server, (ts) => timezone.formatTimestamp(ts));
  }
</script>

<div class="table-wrapper mcp-server-table-wrapper">
  <table class="data-table">
    <thead>
      <tr>
        <th>Name</th>
        <th>Transport</th>
        <th>Endpoint</th>
        <th>Status</th>
        <th>Tools</th>
        <th>Enabled</th>
        <th class="col-actions">Actions</th>
      </tr>
    </thead>
    <tbody>
      {#each mcpServers.filtered as server (mcpServerSlug(server))}
        <tr>
          <td>
            <span class="font-size-md">{server.name}</span>
            {#if server.managed}
              <span
                class="alias-kind-badge"
                title="Declared in configuration (config.yaml mcp.servers); read-only in the dashboard"
                >Config</span
              >
            {/if}
            <div class="mcp-server-sub-counts mono">{mcpServerSlug(server)}</div>
          </td>
          <td><span class="budget-source mono">{server.transport || "http"}</span></td>
          <td class="mono font-size-md" title={mcpServerEndpointLabel(server)}
            >{mcpServerEndpointLabel(server)}</td
          >
          <td>
            <span
              class="audit-status-badge {mcpServerStatusClass(server)}"
              title={statusTitle(server)}>{mcpServerStatus(server)}</span
            >
            {#if mcpServerStatus(server) === "degraded" && server.last_error}
              <div class="mcp-server-sub-counts">{server.last_error}</div>
            {/if}
          </td>
          <td>
            <span>{formatNumber(server.tool_count || 0)}</span>
            <div class="mcp-server-sub-counts">{mcpServerSubCountsLabel(server)}</div>
          </td>
          <td>
            <span
              class="auth-key-status-badge {server.enabled
                ? 'auth-key-status-active'
                : 'auth-key-status-inactive'}"
              >{server.enabled ? "Enabled" : "Disabled"}</span
            >
          </td>
          <td class="col-actions">
            <div class="alias-actions-cell">
              {#if !server.managed}
                <TableActionButton
                  label={"Edit MCP server " + server.name}
                  class="table-icon-btn"
                  onclick={() => mcpServers.openEdit(server)}
                >
                  <Icon name="pencil" class="table-icon-svg" />
                </TableActionButton>
              {/if}
              <TableActionButton
                label={"Inspect catalog of MCP server " + server.name}
                class="table-icon-btn"
                onclick={() => mcpServers.openCatalog(server)}
              >
                <Icon name="list" class="form-action-icon" />
              </TableActionButton>
              <TableActionButton
                label={(mcpServers.reconnectingName === mcpServerSlug(server) ? "Reconnecting MCP server " : "Reconnect MCP server ") + server.name}
                class="table-icon-btn"
                onclick={() => mcpServers.reconnectServer(server)}
                disabled={mcpServers.reconnectingName === mcpServerSlug(server)}
              >
                <Icon name="refresh-cw" class="form-action-icon" />
              </TableActionButton>
              {#if !server.managed}
                <TableActionButton
                  label={(mcpServers.deletingName === mcpServerSlug(server) ? "Deleting MCP server " : "Delete MCP server ") + server.name}
                  class="table-action-btn-danger table-icon-btn"
                  onclick={() => mcpServers.deleteServer(server)}
                  disabled={mcpServers.deletingName === mcpServerSlug(server)}
                >
                  <Icon name="x" class="table-icon-svg" />
                </TableActionButton>
              {/if}
            </div>
          </td>
        </tr>
      {/each}
    </tbody>
  </table>
</div>

<style>
  /* Styles owned by this component (moved from dashboard.css). */
  /* MCP servers: secondary line under a table cell (prompt/resource counts,
     inline last_error for degraded servers). */
  .mcp-server-sub-counts {
    margin-top: 4px;
    color: var(--text-muted);
    font-size: 11px;
    max-width: 280px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  /* The server table has seven information-dense columns. Preserve readable
     cells on narrow screens and let the table scroll instead of clipping it. */
  .mcp-server-table-wrapper {
    overflow-x: auto;
  }

  .mcp-server-table-wrapper :global(.data-table) {
    min-width: 860px;
  }
</style>
