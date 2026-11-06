<script>
  // First step of adding an MCP server: pick a well-known hosted server
  // (prefilled URL, then sign in or paste a token) or a custom server with
  // the full form. Each tile says how the server connects.
  import Modal from "$lib/components/atoms/Modal.svelte";
  import DialogCloseButton from "$lib/components/atoms/DialogCloseButton.svelte";
  import Icon from "$lib/components/atoms/Icon.svelte";
  import { mcpServers } from "./mcpServers.svelte.js";
  import {
    MCP_AUTH_OAUTH,
    MCP_AUTH_TOKEN,
    MCP_PRESET_CUSTOM,
    MCP_SERVER_PRESETS,
    mcpPresetHost,
  } from "./mcpPresets.js";
  import { Settings2 } from "lucide";
  import * as m from "$lib/paraglide/messages.js";

  function authLabel(preset) {
    if (preset.auth.includes(MCP_AUTH_OAUTH)) {
      return preset.auth.includes(MCP_AUTH_TOKEN)
        ? m.mcp_preset_auth_oauth_or_token()
        : m.mcp_preset_auth_oauth();
    }
    return m.mcp_preset_auth_token();
  }
</script>

<Modal open={mcpServers.presetPickerOpen} variant="editor" onclose={() => mcpServers.closePresetPicker()}>
  <div class="model-editor" role="dialog" aria-modal="true" aria-label={m.mcp_preset_title()}>
    <div class="editor-header">
      <div>
        <h3>{m.mcp_preset_title()}</h3>
        <p class="form-hint">{m.mcp_preset_help()}</p>
      </div>
      <DialogCloseButton label={m.mcp_close()} onclick={() => mcpServers.closePresetPicker()} />
    </div>

    <div class="mcp-preset-grid">
      {#each MCP_SERVER_PRESETS as preset (preset.id)}
        <button
          type="button"
          class="mcp-preset-tile"
          onclick={() => mcpServers.choosePreset(preset.id)}
        >
          <span class="mcp-preset-monogram" aria-hidden="true">{preset.name.slice(0, 1)}</span>
          <span class="mcp-preset-copy">
            <span class="mcp-preset-name">{preset.name}</span>
            <span class="mcp-preset-meta mono">{mcpPresetHost(preset)}</span>
            <span class="mcp-preset-meta">{authLabel(preset)}</span>
          </span>
        </button>
      {/each}
      <button
        type="button"
        class="mcp-preset-tile"
        onclick={() => mcpServers.choosePreset(MCP_PRESET_CUSTOM)}
      >
        <span class="mcp-preset-monogram" aria-hidden="true"><Icon icon={Settings2} /></span>
        <span class="mcp-preset-copy">
          <span class="mcp-preset-name">{m.mcp_preset_custom()}</span>
          <span class="mcp-preset-meta">{m.mcp_preset_custom_help()}</span>
        </span>
      </button>
    </div>
  </div>
</Modal>

<style>
  .mcp-preset-grid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(200px, 1fr));
    gap: 10px;
    margin-top: 16px;
  }
  .mcp-preset-tile {
    display: flex;
    align-items: flex-start;
    gap: 10px;
    padding: 12px;
    text-align: left;
    color: var(--text);
    background: var(--bg-surface);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    cursor: pointer;
  }
  .mcp-preset-tile:hover,
  .mcp-preset-tile:focus-visible {
    background: var(--bg-surface-hover);
    border-color: var(--accent);
  }
  .mcp-preset-monogram {
    flex: none;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 32px;
    height: 32px;
    border-radius: var(--radius);
    border: 1px solid var(--border);
    font-weight: 600;
  }
  .mcp-preset-monogram :global(svg) {
    width: 16px;
    height: 16px;
  }
  .mcp-preset-copy {
    display: flex;
    flex-direction: column;
    gap: 2px;
    min-width: 0;
  }
  .mcp-preset-name {
    font-weight: 600;
  }
  .mcp-preset-meta {
    font-size: 12px;
    color: var(--text-muted);
    overflow-wrap: anywhere;
  }
</style>
