<script>
  // MCP server editor modal (create + edit), built on the shared EditorDialog
  // shell. The slug is derived from the name until manually edited and becomes
  // immutable once the server exists. Saved header values arrive masked as
  // "***"; leaving them unchanged keeps the stored secret on save.
  import TableActionButton from "$lib/components/atoms/TableActionButton.svelte";
  import Icon from "$lib/components/atoms/Icon.svelte";
  import EnabledToggle from "$lib/components/atoms/EnabledToggle.svelte";
  import FormField from "$lib/components/molecules/FormField.svelte";
  import EditorDialog from "$lib/components/organisms/EditorDialog.svelte";
  import { mcpServers } from "./mcpServers.svelte.js";
  import { Plus, Trash2 } from "lucide";
  import * as m from "$lib/paraglide/messages.js";
</script>

<EditorDialog
  open={mcpServers.formOpen}
  title={mcpServers.formMode === "edit" ? m.mcp_edit() : m.mcp_add()}
  ariaLabel={m.mcp_editor_label()}
  error={mcpServers.error}
  submitting={mcpServers.formSubmitting}
  onclose={() => mcpServers.closeForm()}
  onsubmit={() => mcpServers.submitForm()}
>
  {#snippet headerHint()}
    <p class="form-hint">{m.mcp_identity_help()}</p>
  {/snippet}

  <FormField id="mcp-server-name" label={m.mcp_name()}>
    <input
      id="mcp-server-name"
      type="text"
      placeholder="Linear MCP"
      bind:value={mcpServers.form.name}
      oninput={() => mcpServers.syncSlugFromName()}
      data-modal-autofocus
    />
    <small class="form-hint">{m.mcp_name_help()}</small>
  </FormField>

  <FormField id="mcp-server-slug" label={m.mcp_slug()}>
    <input
      id="mcp-server-slug"
      type="text"
      class="mono"
      placeholder="linear-mcp"
      bind:value={mcpServers.form.slug}
      oninput={() => mcpServers.markSlugEdited()}
      disabled={mcpServers.formMode === "edit"}
    />
    {#if mcpServers.formMode === "create"}
      <small class="form-hint">{m.mcp_slug_create_help()}</small>
    {:else}
      <small class="form-hint">{m.mcp_slug_edit_help()}</small>
    {/if}
  </FormField>

  <FormField id="mcp-server-transport" label={m.mcp_transport()}>
    <select id="mcp-server-transport" class="form-select" bind:value={mcpServers.form.transport}>
      <option value="http">Streamable HTTP</option>
      <option value="sse">SSE (legacy)</option>
    </select>
    <small class="form-hint">{m.mcp_stdio_help()}</small>
  </FormField>

  <FormField id="mcp-server-url" label="URL">
    <input
      id="mcp-server-url"
      type="text"
      class="mono"
      placeholder="https://mcp.example.com/mcp"
      bind:value={mcpServers.form.url}
    />
  </FormField>

  <div class="form-field">
    <span class="form-field-label">{m.mcp_headers()}</span>
    <div class="vm-target-list">
      {#each mcpServers.form.headers as header, index (index)}
        <div class="vm-target-row">
          <input
            type="text"
            class="mono vm-target-model"
            placeholder="Authorization"
            bind:value={header.name}
            aria-label={m.mcp_header_name()}
          />
          <input
            type="text"
            class="mono vm-target-model"
            placeholder="Bearer secret-token"
            bind:value={header.value}
            aria-label={m.mcp_header_value()}
          />
          <TableActionButton
            label={m.mcp_remove_header()}
            class="table-action-btn-danger table-icon-btn vm-target-remove"
            onclick={() => mcpServers.removeHeader(index)}
          >
            <Icon icon={Trash2} class="table-icon-svg" />
          </TableActionButton>
        </div>
      {/each}
    </div>
    <div class="failover-target-actions">
      <button
        type="button"
        class="btn btn-with-icon"
        onclick={() => mcpServers.addHeader()}
      >
        <Icon icon={Plus} class="form-action-icon" />
        <span>{m.mcp_add_header()}</span>
      </button>
    </div>
    <small class="form-hint">{m.mcp_headers_help()}</small>
  </div>

  <div class="vm-status-row">
    <div class="vm-status-toggle">
      <EnabledToggle
        enabled={mcpServers.form.enabled}
        label="MCP server"
        onclick={() => (mcpServers.form.enabled = !mcpServers.form.enabled)}
      />
    </div>
  </div>

  <details
    class="mcp-server-advanced"
    open={mcpServers.advancedOpen}
    ontoggle={(event) => (mcpServers.advancedOpen = event.currentTarget.open)}
  >
    <summary>
      <span class="mcp-server-advanced-summary-copy">
        <span class="mcp-server-advanced-title">{m.mcp_advanced()}</span>
        <span class="form-hint">{m.mcp_advanced_summary()}</span>
      </span>
    </summary>

    <div class="mcp-server-advanced-fields">
      <div class="form-field">
        <label class="form-field-label" for="mcp-server-description">{m.mcp_description()}</label>
        <input
          id="mcp-server-description"
          type="text"
          placeholder={m.mcp_description_placeholder()}
          bind:value={mcpServers.form.description}
        />
      </div>

      <div class="form-field">
        <label class="form-field-label" for="mcp-server-allowed-tools">{m.mcp_allowed_tools()}</label>
        <input
          id="mcp-server-allowed-tools"
          type="text"
          class="mono"
          placeholder={m.mcp_allowed_tools_placeholder()}
          bind:value={mcpServers.form.allowed_tools}
        />
      </div>

      <div class="form-field">
        <label class="form-field-label" for="mcp-server-disallowed-tools">{m.mcp_disallowed_tools()}</label>
        <input
          id="mcp-server-disallowed-tools"
          type="text"
          class="mono"
          placeholder={m.mcp_disallowed_tools_placeholder()}
          bind:value={mcpServers.form.disallowed_tools}
        />
      </div>

      <div class="form-field">
        <label class="form-field-label" for="mcp-server-user-paths">{m.mcp_user_paths()}</label>
        <textarea
          id="mcp-server-user-paths"
          rows="4"
          class="mono"
          placeholder={"/\n/team/alpha"}
          bind:value={mcpServers.form.user_paths}
        ></textarea>
      </div>

      <div class="form-field">
        <label class="form-field-label" for="mcp-server-tool-timeout">{m.mcp_timeout()}</label>
        <input
          id="mcp-server-tool-timeout"
          type="number"
          min="0"
          step="1"
          class="mono"
          placeholder={m.mcp_timeout_placeholder()}
          bind:value={mcpServers.form.tool_timeout_seconds}
        />
      </div>
    </div>
  </details>
</EditorDialog>
