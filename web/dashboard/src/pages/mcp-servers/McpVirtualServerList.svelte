<script>
  // Virtual servers table: config-declared, read-only subsets of the MCP
  // servers, each served at /mcp/{name}. Flags members that match no server
  // and virtual servers a same-named server keeps from being served.
  import { basePath } from "$lib/api/paths.js";
  import { mcpServers } from "./mcpServers.svelte.js";
  import {
    MCP_TOOL_DISCOVERY_SEARCH,
    mcpGatewayEndpoint,
    mcpVirtualServerEndpoint,
  } from "./mcp-servers.js";
  import * as m from "$lib/paraglide/messages.js";

  const gatewayEndpoint = mcpGatewayEndpoint(
    typeof window === "undefined" ? "" : window.location.origin,
    basePath(),
  );

  function isMissing(virtual, member) {
    return (virtual.missing_servers || []).includes(member);
  }
</script>

<section class="mcp-virtual-servers" aria-labelledby="mcp-virtual-servers-title">
  <h3 id="mcp-virtual-servers-title" class="mcp-virtual-title">{m.mcp_virtual_title()}</h3>
  <p class="form-hint">{m.mcp_virtual_hint()}</p>
  <div class="table-wrapper">
    <table class="data-table">
      <thead>
        <tr>
          <th>{m.mcp_name()}</th>
          <th>{m.mcp_column_endpoint()}</th>
          <th>{m.mcp_virtual_column_servers()}</th>
          <th>{m.mcp_connect_mode_label()}</th>
        </tr>
      </thead>
      <tbody>
        {#each mcpServers.virtualServers as virtual (virtual.name)}
          <tr>
            <td>
              <span class="font-size-md">{virtual.name}</span>
              <span class="alias-kind-badge" title={m.mcp_virtual_managed()}>{m.common_config()}</span>
              {#if virtual.description}
                <div class="mcp-virtual-sub">{virtual.description}</div>
              {/if}
            </td>
            <td class="font-size-md">
              {#if virtual.conflict}
                <span class="audit-status-badge status-warning">{m.mcp_virtual_not_served()}</span>
                <div class="mcp-virtual-sub">{virtual.conflict}</div>
              {:else}
                <span class="mono">{mcpVirtualServerEndpoint(gatewayEndpoint, virtual.name)}</span>
              {/if}
            </td>
            <td>
              <div class="mcp-virtual-members">
                {#each virtual.servers || [] as member (member)}
                  <span
                    class={["budget-source mono", isMissing(virtual, member) && "mcp-virtual-missing"]}
                    title={isMissing(virtual, member) ? m.mcp_virtual_missing_member() : undefined}
                    >{member}</span
                  >
                {/each}
              </div>
            </td>
            <td>
              {virtual.tool_discovery === MCP_TOOL_DISCOVERY_SEARCH
                ? m.mcp_connect_mode_search()
                : m.mcp_connect_mode_all()}
            </td>
          </tr>
        {/each}
      </tbody>
    </table>
  </div>
</section>

<style>
  .mcp-virtual-servers {
    margin-top: var(--space-stack);
  }

  .mcp-virtual-title {
    margin: 0 0 4px;
    font-size: 15px;
  }

  .mcp-virtual-servers .form-hint {
    margin: 0 0 10px;
  }

  .mcp-virtual-sub {
    margin-top: 4px;
    max-width: 320px;
    color: var(--text-muted);
    font-size: 11px;
  }

  .mcp-virtual-members {
    display: flex;
    flex-wrap: wrap;
    gap: 4px;
  }

  .mcp-virtual-missing {
    border-style: dashed;
    color: var(--warning);
    text-decoration: line-through;
  }
</style>
