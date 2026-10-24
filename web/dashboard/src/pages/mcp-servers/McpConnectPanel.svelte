<script>
  // "Connect a client" disclosure on the MCP Servers page: the aggregated
  // /mcp endpoint or a virtual server's /mcp/{name}, the endpoint's default
  // tool discovery mode, and a copyable client config. The pickers only change
  // the snippet; it adds the X-MCP-Tool-Discovery header when the chosen mode
  // differs from the endpoint's default.
  import CopyButton from "$lib/components/atoms/CopyButton.svelte";
  import SegmentedControl from "$lib/components/atoms/SegmentedControl.svelte";
  import { basePath } from "$lib/api/paths.js";
  import { runtimeConfig } from "$lib/stores/runtimeConfig.svelte.js";
  import { createCopyState } from "$lib/utils/clipboard.svelte.js";
  import {
    MCP_API_KEY_PLACEHOLDER,
    MCP_TOOL_DISCOVERY_HEADER,
    MCP_TOOL_DISCOVERY_OFF,
    MCP_TOOL_DISCOVERY_SEARCH,
    mcpClientConfig,
    mcpEndpointIsInsecure,
    mcpGatewayEndpoint,
    mcpServedVirtualServers,
    mcpVirtualServerEndpoint,
    normalizeMcpToolDiscovery,
  } from "./mcp-servers.js";
  import { mcpServers } from "./mcpServers.svelte.js";
  import * as m from "$lib/paraglide/messages.js";

  const gatewayEndpoint = mcpGatewayEndpoint(
    typeof window === "undefined" ? "" : window.location.origin,
    basePath(),
  );
  const gatewayMode = $derived(runtimeConfig.mcpToolDiscovery());
  const virtualServers = $derived(mcpServedVirtualServers(mcpServers.virtualServers));
  // "" is the aggregated /mcp endpoint; otherwise a virtual server name.
  let target = $state("");
  const virtual = $derived(virtualServers.find((entry) => entry.name === target));
  const endpoint = $derived(
    virtual ? mcpVirtualServerEndpoint(gatewayEndpoint, virtual.name) : gatewayEndpoint,
  );
  const insecure = $derived(mcpEndpointIsInsecure(endpoint));
  const defaultMode = $derived(
    virtual ? normalizeMcpToolDiscovery(virtual.tool_discovery) : gatewayMode,
  );
  // null follows the endpoint's default until the user picks a mode.
  let picked = $state(null);
  const mode = $derived(picked ?? defaultMode);
  const snippet = $derived(mcpClientConfig(endpoint, mode, defaultMode, virtual?.name));

  const modeOptions = [
    { value: MCP_TOOL_DISCOVERY_OFF, label: m.mcp_connect_mode_all() },
    { value: MCP_TOOL_DISCOVERY_SEARCH, label: m.mcp_connect_mode_search() },
  ];
  const endpointCopy = createCopyState({ logPrefix: "Failed to copy MCP endpoint:" });
  const snippetCopy = createCopyState({ logPrefix: "Failed to copy MCP client config:" });
</script>

<details class="mcp-server-advanced mcp-connect">
  <summary>
    <span class="mcp-server-advanced-summary-copy">
      <span class="mcp-server-advanced-title">{m.mcp_connect_title()}</span>
      <span class="form-hint">
        {gatewayMode === MCP_TOOL_DISCOVERY_SEARCH
          ? m.mcp_connect_summary_search()
          : m.mcp_connect_summary_off()}
      </span>
    </span>
  </summary>
  <div class="mcp-server-advanced-fields">
    {#if virtualServers.length > 0}
      <div class="mcp-connect-row">
        <label class="mcp-connect-label" for="mcp-connect-target">{m.mcp_connect_target_label()}</label>
        <select
          id="mcp-connect-target"
          class="form-select mcp-connect-target"
          bind:value={target}
          onchange={() => (picked = null)}
        >
          <option value="">{m.mcp_connect_target_all()}</option>
          {#each virtualServers as entry (entry.name)}
            <option value={entry.name}>{m.mcp_connect_target_virtual({ name: entry.name })}</option>
          {/each}
        </select>
      </div>
    {/if}
    <div class="mcp-connect-row">
      <span class="mcp-connect-label">{m.mcp_connect_endpoint()}</span>
      <code class="mcp-connect-endpoint mono">{endpoint}</code>
      <CopyButton
        state={endpointCopy}
        label={m.mcp_connect_copy()}
        copiedLabel={m.common_copied()}
        errorLabel={m.common_copy_failed()}
        onclick={() => endpointCopy.copy(endpoint)}
      />
    </div>
    {#if insecure}
      <p class="alert alert-warning mcp-connect-warning">{m.mcp_connect_insecure_warning()}</p>
    {/if}

    <div class="mcp-connect-row">
      <span class="mcp-connect-label">{m.mcp_connect_mode_label()}</span>
      <SegmentedControl
        options={modeOptions}
        value={mode}
        ariaLabel={m.mcp_connect_mode_label()}
        onchange={(value) => (picked = value)}
      />
    </div>
    <p class="form-hint">
      {mode === MCP_TOOL_DISCOVERY_SEARCH ? m.mcp_connect_mode_help_search() : m.mcp_connect_mode_help_all()}
      {virtual
        ? m.mcp_connect_virtual_default_note({
            name: virtual.name,
            mode: defaultMode,
            header: MCP_TOOL_DISCOVERY_HEADER,
          })
        : m.mcp_connect_default_note({
            mode: defaultMode,
            header: MCP_TOOL_DISCOVERY_HEADER,
          })}
    </p>

    <div class="mcp-connect-snippet">
      <div class="mcp-connect-row">
        <span class="mcp-connect-label">{m.mcp_connect_config_label()}</span>
        <CopyButton
          state={snippetCopy}
          label={m.mcp_connect_copy()}
          copiedLabel={m.common_copied()}
          errorLabel={m.common_copy_failed()}
          onclick={() => snippetCopy.copy(snippet)}
        />
      </div>
      <pre class="mcp-connect-code mono">{snippet}</pre>
      <p class="form-hint">{m.mcp_connect_key_hint({ placeholder: MCP_API_KEY_PLACEHOLDER })}</p>
    </div>
  </div>
</details>

<style>
  .mcp-connect {
    margin-bottom: var(--space-stack);
  }

  .mcp-connect-row {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 10px;
  }

  .mcp-connect-label {
    min-width: 140px;
    color: var(--text-muted);
    font-size: 12px;
    font-weight: 600;
  }

  .mcp-connect-target {
    width: auto;
    min-width: 220px;
  }

  .mcp-connect-endpoint {
    min-width: 0;
    overflow-wrap: anywhere;
    font-size: 13px;
  }

  /* The panel's flex gap spaces rows; drop the alert's own margin. */
  .mcp-connect-warning {
    margin: 0;
  }

  .mcp-connect-snippet {
    display: flex;
    flex-direction: column;
    gap: 8px;
  }

  .mcp-connect-code {
    margin: 0;
    padding: 12px;
    border: 1px solid var(--border);
    border-radius: var(--radius);
    background: var(--bg);
    color: var(--text);
    font-size: 12px;
    line-height: 1.55;
    white-space: pre-wrap;
    overflow-wrap: anywhere;
  }
</style>
