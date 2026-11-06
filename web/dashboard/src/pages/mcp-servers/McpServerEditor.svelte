<script>
  // MCP server editor modal (create + edit), built on the shared EditorDialog
  // shell. The slug is derived from the name until manually edited and becomes
  // immutable once the server exists. Saved header values arrive masked as
  // "***"; leaving them unchanged keeps the stored secret on save. Tool
  // exposure lives in McpToolPicker. A server added from a preset shows only
  // its name and connection method; the connection, tool and access fields
  // move under Advanced.
  import TableActionButton from "$lib/components/atoms/TableActionButton.svelte";
  import Icon from "$lib/components/atoms/Icon.svelte";
  import EnabledToggle from "$lib/components/atoms/EnabledToggle.svelte";
  import FormField from "$lib/components/molecules/FormField.svelte";
  import EditorDialog from "$lib/components/organisms/EditorDialog.svelte";
  import McpToolPicker from "./McpToolPicker.svelte";
  import { mcpServers } from "./mcpServers.svelte.js";
  import {
    MCP_AUTH_NONE,
    MCP_AUTH_OAUTH,
    MCP_AUTH_TOKEN,
  } from "./mcpPresets.js";
  import { LogIn, Plus, Trash2 } from "lucide";
  import * as m from "$lib/paraglide/messages.js";

  const preset = $derived(mcpServers.formPreset);
  const signIn = $derived(Boolean(preset) && mcpServers.formAuth === MCP_AUTH_OAUTH);

  function authOptionLabel(auth) {
    if (auth === MCP_AUTH_OAUTH) return m.mcp_preset_method_oauth({ name: preset.name });
    if (auth === MCP_AUTH_TOKEN) return m.mcp_preset_method_token();
    return m.mcp_preset_method_none();
  }
</script>

{#snippet connectionFields()}
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
    <small class="form-hint">{m.mcp_transport_help()}</small>
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
{/snippet}

{#snippet toolFields()}
  <div class="vm-status-row">
    <div class="vm-status-toggle">
      <EnabledToggle
        enabled={mcpServers.form.enabled}
        label="MCP server"
        onclick={() => (mcpServers.form.enabled = !mcpServers.form.enabled)}
      />
    </div>
  </div>

  <McpToolPicker />
{/snippet}

{#snippet accessFields()}
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
    <label class="form-field-label" for="mcp-server-disallowed-user-paths"
      >{m.mcp_disallowed_user_paths()}</label
    >
    <textarea
      id="mcp-server-disallowed-user-paths"
      rows="3"
      class="mono"
      placeholder={"/contractors"}
      bind:value={mcpServers.form.disallowed_user_paths}
    ></textarea>
    <small class="form-hint">{m.mcp_disallowed_user_paths_help()}</small>
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
{/snippet}

<EditorDialog
  open={mcpServers.formOpen}
  title={mcpServers.formMode === "edit"
    ? m.mcp_edit()
    : preset
      ? m.mcp_preset_editor_title({ name: preset.name })
      : m.mcp_add()}
  ariaLabel={m.mcp_editor_label()}
  error={mcpServers.error}
  submitting={mcpServers.formSubmitting}
  onclose={() => mcpServers.closeForm()}
  onsubmit={() => mcpServers.submitForm()}
  submitLabel={signIn ? m.mcp_preset_save_sign_in() : undefined}
  submittingLabel={signIn ? m.mcp_oauth_continuing() : undefined}
  submitIcon={signIn ? LogIn : undefined}
>
  {#snippet headerHint()}
    <p class="form-hint">
      {preset ? m.mcp_preset_editor_help({ name: preset.name }) : m.mcp_identity_help()}
      {#if mcpServers.formMode === "create"}
        <button type="button" class="mcp-preset-change" onclick={() => mcpServers.changePreset()}
          >{m.mcp_preset_change()}</button
        >
      {/if}
    </p>
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

  {#if preset}
    <div class="form-field">
      <span class="form-field-label">{m.mcp_preset_method()}</span>
      {#if preset.auth.length > 1}
        <div class="mcp-preset-methods" role="group" aria-label={m.mcp_preset_method()}>
          {#each preset.auth as auth (auth)}
            <button
              type="button"
              class="btn"
              class:btn-primary={mcpServers.formAuth === auth}
              aria-pressed={mcpServers.formAuth === auth}
              onclick={() => (mcpServers.formAuth = auth)}>{authOptionLabel(auth)}</button
            >
          {/each}
        </div>
      {/if}
      {#if mcpServers.formAuth === MCP_AUTH_OAUTH}
        <small class="form-hint">
          {#if preset.oauthClient === "manual"}
            {m.mcp_preset_oauth_manual_help({ name: preset.name })}
            <a href={preset.clientURL} target="_blank" rel="noopener noreferrer"
              >{m.mcp_preset_oauth_manual_link({ name: preset.name })}</a
            >
          {:else}
            {m.mcp_preset_oauth_help({ name: preset.name })}
          {/if}
        </small>
      {:else if mcpServers.formAuth === MCP_AUTH_TOKEN}
        <input
          id="mcp-server-token"
          type="password"
          class="mono"
          autocomplete="off"
          aria-label={m.mcp_preset_token()}
          placeholder={m.mcp_preset_token()}
          bind:value={mcpServers.formToken}
        />
        <small class="form-hint">
          {m.mcp_preset_token_help()}
          {#if preset.tokenURL}
            <a href={preset.tokenURL} target="_blank" rel="noopener noreferrer"
              >{m.mcp_preset_token_link({ name: preset.name })}</a
            >
          {/if}
        </small>
      {:else if mcpServers.formAuth === MCP_AUTH_NONE}
        <small class="form-hint">{m.mcp_preset_none_help()}</small>
      {/if}
    </div>
  {:else}
    {@render connectionFields()}
    {@render toolFields()}
  {/if}

  <details
    class="mcp-server-advanced"
    open={mcpServers.advancedOpen}
    ontoggle={(event) => (mcpServers.advancedOpen = event.currentTarget.open)}
  >
    <summary>
      <span class="mcp-server-advanced-summary-copy">
        <span class="mcp-server-advanced-title">{m.mcp_advanced()}</span>
        <span class="form-hint"
          >{preset ? m.mcp_preset_advanced_summary() : m.mcp_advanced_summary()}</span
        >
      </span>
    </summary>

    <div class="mcp-server-advanced-fields">
      {#if preset}
        {@render connectionFields()}
        {@render toolFields()}
      {/if}
      {@render accessFields()}
    </div>
  </details>
</EditorDialog>

<style>
  .mcp-preset-change {
    padding: 0;
    margin-left: 4px;
    border: 0;
    background: none;
    color: var(--accent);
    font: inherit;
    cursor: pointer;
  }
  .mcp-preset-change:hover {
    text-decoration: underline;
  }
  .mcp-preset-methods {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
    margin-bottom: 6px;
  }
</style>
