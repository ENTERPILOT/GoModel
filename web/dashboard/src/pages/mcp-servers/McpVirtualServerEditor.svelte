<script>
  // Virtual server editor modal (create + edit). The name becomes the
  // /mcp/{name} path and is immutable once the virtual server exists. Members
  // are picked from the MCP servers; one that no longer matches a server
  // stays listed so it can be removed.
  import FormField from "$lib/components/molecules/FormField.svelte";
  import EditorDialog from "$lib/components/organisms/EditorDialog.svelte";
  import { mcpServers } from "./mcpServers.svelte.js";
  import {
    MCP_TOOL_DISCOVERY_OFF,
    MCP_TOOL_DISCOVERY_SEARCH,
    mcpVirtualMemberOptions,
  } from "./mcp-servers.js";
  import * as m from "$lib/paraglide/messages.js";

  const members = $derived(mcpVirtualMemberOptions(mcpServers.servers, mcpServers.virtualForm.servers));
</script>

<EditorDialog
  open={mcpServers.virtualFormOpen}
  title={mcpServers.virtualFormMode === "edit" ? m.mcp_virtual_edit() : m.mcp_virtual_add()}
  ariaLabel={m.mcp_virtual_editor_label()}
  error={mcpServers.virtualFormError}
  submitting={mcpServers.virtualFormSubmitting}
  onclose={() => mcpServers.closeVirtualForm()}
  onsubmit={() => mcpServers.submitVirtualForm()}
>
  {#snippet headerHint()}
    <p class="form-hint">{m.mcp_virtual_editor_help()}</p>
  {/snippet}

  <FormField id="mcp-virtual-name" label={m.mcp_name()}>
    <input
      id="mcp-virtual-name"
      type="text"
      class="mono"
      placeholder="coding"
      bind:value={mcpServers.virtualForm.name}
      disabled={mcpServers.virtualFormMode === "edit"}
      data-modal-autofocus
    />
    <small class="form-hint">
      {mcpServers.virtualFormMode === "edit" ? m.mcp_virtual_name_edit_help() : m.mcp_virtual_name_help()}
    </small>
  </FormField>

  <FormField id="mcp-virtual-description" label={m.mcp_virtual_description()}>
    <input
      id="mcp-virtual-description"
      type="text"
      placeholder="Code review tools"
      bind:value={mcpServers.virtualForm.description}
    />
  </FormField>

  <fieldset class="form-field mcp-virtual-member-field">
    <legend class="form-field-label">{m.mcp_virtual_column_servers()}</legend>
    <div class="mcp-virtual-member-list">
      {#each members as member (member.slug)}
        <label class={["mcp-virtual-member", member.missing && "mcp-virtual-member-missing"]}>
          <input
            type="checkbox"
            checked={mcpServers.virtualForm.servers.includes(member.slug)}
            onchange={() => mcpServers.toggleVirtualMember(member.slug)}
          />
          <span>{member.name}</span>
          <span class="mono mcp-virtual-member-slug">{member.slug}</span>
          {#if member.missing}
            <span class="form-hint">{m.mcp_virtual_missing_member()}</span>
          {/if}
        </label>
      {/each}
    </div>
    <small class="form-hint">{m.mcp_virtual_servers_help()}</small>
  </fieldset>

  <FormField id="mcp-virtual-discovery" label={m.mcp_connect_mode_label()}>
    <select id="mcp-virtual-discovery" class="form-select" bind:value={mcpServers.virtualForm.tool_discovery}>
      <option value="">{m.mcp_virtual_discovery_default()}</option>
      <option value={MCP_TOOL_DISCOVERY_OFF}>{m.mcp_connect_mode_all()}</option>
      <option value={MCP_TOOL_DISCOVERY_SEARCH}>{m.mcp_connect_mode_search()}</option>
    </select>
    <small class="form-hint">{m.mcp_virtual_discovery_help()}</small>
  </FormField>
</EditorDialog>

<style>
  .mcp-virtual-member-field {
    margin: 0;
    padding: 0;
    border: 0;
  }

  .mcp-virtual-member-list {
    display: flex;
    flex-direction: column;
    gap: 6px;
    max-height: 260px;
    overflow-y: auto;
    padding: 8px 10px;
    border: 1px solid var(--border);
    border-radius: var(--radius);
    background: var(--bg);
  }

  .mcp-virtual-member {
    display: flex;
    align-items: center;
    gap: 8px;
    font-size: 13px;
  }

  .mcp-virtual-member-slug {
    color: var(--text-muted);
    font-size: 11px;
  }

  .mcp-virtual-member-missing span:not(.form-hint) {
    color: var(--warning);
    text-decoration: line-through;
  }
</style>
