<script>
  // MCP server editor modal (create + edit), built on the shared EditorDialog
  // shell. The slug is derived from the name until manually edited and becomes
  // immutable once the server exists. Saved header values arrive masked as
  // "***"; leaving them unchanged keeps the stored secret on save. Tool
  // exposure lives in McpToolPicker.
  //
  // Creating starts with the server choice, and the fields below follow it:
  // a preset shows its name and connection method with the rest under
  // Advanced, a custom server shows its URL and headers. A new server has no
  // tools listed yet, so the tool picker is only offered when editing.
  import TableActionButton from "$lib/components/atoms/TableActionButton.svelte";
  import Icon from "$lib/components/atoms/Icon.svelte";
  import EnabledToggle from "$lib/components/atoms/EnabledToggle.svelte";
  import FormField from "$lib/components/molecules/FormField.svelte";
  import SearchSelect from "$lib/components/molecules/SearchSelect.svelte";
  import EditorDialog from "$lib/components/organisms/EditorDialog.svelte";
  import McpToolPicker from "./McpToolPicker.svelte";
  import { mcpServers } from "./mcpServers.svelte.js";
  import {
    MCP_AUTH_NONE,
    MCP_AUTH_OAUTH,
    MCP_AUTH_TOKEN,
    MCP_PRESET_CUSTOM,
    MCP_SERVER_PRESETS,
    mcpPresetForURL,
    mcpPresetHost,
  } from "./mcpPresets.js";
  import { LogIn, Plus, Trash2 } from "lucide";
  import * as m from "$lib/paraglide/messages.js";

  const creating = $derived(mcpServers.formMode === "create");
  const kind = $derived(creating ? mcpServers.formKind : "");
  const preset = $derived(creating ? mcpServers.formPreset : null);
  const custom = $derived(kind === MCP_PRESET_CUSTOM);
  const signIn = $derived(Boolean(preset) && mcpServers.formAuth === MCP_AUTH_OAUTH);
  // A custom URL that is a known service's: offer that service's option.
  const urlPreset = $derived(custom ? mcpPresetForURL(mcpServers.form.url) : null);

  const serverOptions = [
    ...MCP_SERVER_PRESETS.map((entry) => ({
      value: entry.id,
      label: entry.name,
      description: mcpPresetHost(entry) + " · " + presetAuthLabel(entry),
    })),
    {
      value: MCP_PRESET_CUSTOM,
      label: m.mcp_preset_custom(),
      description: m.mcp_preset_custom_help(),
    },
  ];

  function presetAuthLabel(entry) {
    if (entry.auth.includes(MCP_AUTH_OAUTH)) {
      return entry.auth.includes(MCP_AUTH_TOKEN)
        ? m.mcp_preset_auth_oauth_or_token()
        : m.mcp_preset_auth_oauth();
    }
    return m.mcp_preset_auth_token();
  }

  function authOptionLabel(auth) {
    if (auth === MCP_AUTH_OAUTH) return m.mcp_preset_method_oauth({ name: preset.name });
    if (auth === MCP_AUTH_TOKEN) return m.mcp_preset_method_token();
    return m.mcp_preset_method_none();
  }
</script>

{#snippet slugField()}
  <FormField id="mcp-server-slug" label={m.mcp_slug()}>
    <input
      id="mcp-server-slug"
      type="text"
      class="mono"
      placeholder="linear-mcp"
      bind:value={mcpServers.form.slug}
      oninput={() => mcpServers.markSlugEdited()}
      disabled={!creating}
    />
    {#if creating}
      <small class="form-hint">{m.mcp_slug_create_help()}</small>
    {:else}
      <small class="form-hint">{m.mcp_slug_edit_help()}</small>
    {/if}
  </FormField>
{/snippet}

{#snippet transportField()}
  <FormField id="mcp-server-transport" label={m.mcp_transport()}>
    <select id="mcp-server-transport" class="form-select" bind:value={mcpServers.form.transport}>
      <option value="http">Streamable HTTP</option>
      <option value="sse">SSE (legacy)</option>
    </select>
    <small class="form-hint">{m.mcp_transport_help()}</small>
  </FormField>
{/snippet}

{#snippet urlField()}
  <FormField id="mcp-server-url" label="URL">
    <input
      id="mcp-server-url"
      type="text"
      class="mono"
      placeholder="https://mcp.example.com/mcp"
      bind:value={mcpServers.form.url}
      oninput={() => mcpServers.syncNameFromURL()}
    />
    {#if urlPreset}
      <small class="form-hint">
        {m.mcp_preset_url_match({ name: urlPreset.name })}
        <button
          type="button"
          class="mcp-inline-link"
          onclick={() => mcpServers.chooseServerKind(urlPreset.id)}
          >{m.mcp_preset_url_match_action({ name: urlPreset.name })}</button
        >
      </small>
    {/if}
  </FormField>
{/snippet}

{#snippet headersField()}
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

{#snippet enabledField()}
  <div class="vm-status-row">
    <div class="vm-status-toggle">
      <EnabledToggle
        enabled={mcpServers.form.enabled}
        label="MCP server"
        onclick={() => (mcpServers.form.enabled = !mcpServers.form.enabled)}
      />
    </div>
  </div>
{/snippet}

{#snippet connectWith()}
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
  title={creating ? m.mcp_add() : m.mcp_edit()}
  ariaLabel={m.mcp_editor_label()}
  error={mcpServers.error}
  submitting={mcpServers.formSubmitting}
  submitDisabled={creating && !kind}
  submitLabel={signIn ? m.mcp_preset_save_sign_in() : undefined}
  submittingLabel={signIn ? m.mcp_oauth_continuing() : undefined}
  submitIcon={signIn ? LogIn : undefined}
  onclose={() => mcpServers.closeForm()}
  onsubmit={() => mcpServers.submitForm()}
>
  {#snippet headerHint()}
    <p class="form-hint">{creating ? m.mcp_preset_help() : m.mcp_identity_help()}</p>
  {/snippet}

  {#if creating}
    <FormField id="mcp-server-kind" label={m.mcp_preset_server()}>
      <SearchSelect
        id="mcp-server-kind"
        options={serverOptions}
        value={kind}
        onchange={(value) => mcpServers.chooseServerKind(value)}
        placeholder={m.mcp_preset_server_placeholder()}
        searchPlaceholder={m.mcp_preset_server_search()}
        ariaLabel={m.mcp_preset_server()}
      />
    </FormField>
  {/if}

  {#if !creating || kind}
    <FormField id="mcp-server-name" label={m.mcp_name()}>
      <input
        id="mcp-server-name"
        type="text"
        placeholder="Linear MCP"
        bind:value={mcpServers.form.name}
        oninput={() => (creating ? mcpServers.markNameEdited() : mcpServers.syncSlugFromName())}
        data-modal-autofocus={creating ? undefined : true}
      />
      <small class="form-hint">{m.mcp_name_help()}</small>
    </FormField>

    {#if preset}
      {@render connectWith()}
    {:else if custom}
      {@render urlField()}
      {@render headersField()}
    {:else}
      {@render slugField()}
      {@render transportField()}
      {@render urlField()}
      {@render headersField()}
      {@render enabledField()}
      <McpToolPicker />
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
            >{preset
              ? m.mcp_preset_advanced_summary()
              : custom
                ? m.mcp_preset_custom_advanced_summary()
                : m.mcp_advanced_summary()}</span
          >
        </span>
      </summary>

      <div class="mcp-server-advanced-fields">
        {#if preset}
          {@render urlField()}
          {@render transportField()}
          {@render headersField()}
          {@render slugField()}
          {@render enabledField()}
        {:else if custom}
          {@render transportField()}
          {@render slugField()}
          {@render enabledField()}
        {/if}
        {@render accessFields()}
      </div>
    </details>
  {/if}
</EditorDialog>

<style>
  .mcp-inline-link {
    padding: 0;
    margin-left: 4px;
    border: 0;
    background: none;
    color: var(--accent);
    font: inherit;
    cursor: pointer;
  }
  .mcp-inline-link:hover {
    text-decoration: underline;
  }
  .mcp-preset-methods {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
    margin-bottom: 6px;
  }
</style>
