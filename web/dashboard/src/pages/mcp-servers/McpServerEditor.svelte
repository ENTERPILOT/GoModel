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
</script>

<EditorDialog
  open={mcpServers.formOpen}
  title={mcpServers.formMode === "edit" ? "Edit MCP Server" : "Add MCP Server"}
  ariaLabel="MCP server editor"
  error={mcpServers.error}
  submitting={mcpServers.formSubmitting}
  onclose={() => mcpServers.closeForm()}
  onsubmit={() => mcpServers.submitForm()}
>
  {#snippet headerHint()}
    <p class="form-hint">The display name can change. The slug is the stable client-facing identity.</p>
  {/snippet}

  <FormField id="mcp-server-name" label="Name">
    <input
      id="mcp-server-name"
      type="text"
      placeholder="Linear MCP"
      bind:value={mcpServers.form.name}
      oninput={() => mcpServers.syncSlugFromName()}
      data-modal-autofocus
    />
    <small class="form-hint">Human-readable and Unicode-friendly. You can change it later.</small>
  </FormField>

  <FormField id="mcp-server-slug" label="Slug">
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
      <small class="form-hint">Derived from the name. You may edit it before saving.</small>
    {:else}
      <small class="form-hint">Immutable because it is used in URLs, scope headers, and aggregated tool names.</small>
    {/if}
  </FormField>

  <FormField id="mcp-server-transport" label="Transport">
    <select id="mcp-server-transport" class="form-select" bind:value={mcpServers.form.transport}>
      <option value="http">Streamable HTTP</option>
      <option value="sse">SSE (legacy)</option>
    </select>
    <small class="form-hint">stdio servers are config-only: declare them in <code>config.yaml</code> under <code>mcp.servers</code>.</small>
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
    <span class="form-field-label">Headers</span>
    <div class="vm-target-list">
      {#each mcpServers.form.headers as header, index (index)}
        <div class="vm-target-row">
          <input
            type="text"
            class="mono vm-target-model"
            placeholder="Authorization"
            bind:value={header.name}
            aria-label="Header name"
          />
          <input
            type="text"
            class="mono vm-target-model"
            placeholder="Bearer secret-token"
            bind:value={header.value}
            aria-label="Header value"
          />
          <TableActionButton
            label="Remove header"
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
        <span>Add header</span>
      </button>
    </div>
    <small class="form-hint">Sent only to the configured server origin. Saved values are shown as <code>***</code>; leave <code>***</code> unchanged to keep the stored value.</small>
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
        <span class="mcp-server-advanced-title">Advanced settings</span>
        <span class="form-hint">Description, access rules, and timeout</span>
      </span>
    </summary>

    <div class="mcp-server-advanced-fields">
      <div class="form-field">
        <label class="form-field-label" for="mcp-server-description">Description</label>
        <input
          id="mcp-server-description"
          type="text"
          placeholder="Optional description"
          bind:value={mcpServers.form.description}
        />
      </div>

      <div class="form-field">
        <label class="form-field-label" for="mcp-server-allowed-tools">Allowed tools</label>
        <input
          id="mcp-server-allowed-tools"
          type="text"
          class="mono"
          placeholder="search_issues, get_file (comma-separated; empty allows all)"
          bind:value={mcpServers.form.allowed_tools}
        />
      </div>

      <div class="form-field">
        <label class="form-field-label" for="mcp-server-disallowed-tools">Disallowed tools</label>
        <input
          id="mcp-server-disallowed-tools"
          type="text"
          class="mono"
          placeholder="delete_repo (comma-separated)"
          bind:value={mcpServers.form.disallowed_tools}
        />
      </div>

      <div class="form-field">
        <label class="form-field-label" for="mcp-server-user-paths">User paths (empty means all)</label>
        <textarea
          id="mcp-server-user-paths"
          rows="4"
          class="mono"
          placeholder={"/\n/team/alpha"}
          bind:value={mcpServers.form.user_paths}
        ></textarea>
      </div>

      <div class="form-field">
        <label class="form-field-label" for="mcp-server-tool-timeout">Tool timeout (seconds)</label>
        <input
          id="mcp-server-tool-timeout"
          type="number"
          min="0"
          step="1"
          class="mono"
          placeholder="Default timeout when empty"
          bind:value={mcpServers.form.tool_timeout_seconds}
        />
      </div>
    </div>
  </details>
</EditorDialog>
