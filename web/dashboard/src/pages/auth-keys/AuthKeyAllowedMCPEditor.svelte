<script>
  // Edit-MCP-access modal for an existing API key (EditorDialog shell).
  import EditorDialog from "$lib/components/organisms/EditorDialog.svelte";
  import FormField from "$lib/components/molecules/FormField.svelte";
  import SearchSelect from "$lib/components/molecules/SearchSelect.svelte";
  import { authKeysStore as store } from "./authKeys.svelte.js";
  import { authKeyMCPSelectorOptions } from "./authKeysLogic.js";
  import * as m from "$lib/paraglide/messages.js";

  const selectorOptions = $derived(authKeyMCPSelectorOptions(store.mcpServerSlugs));
</script>

<EditorDialog
  open={store.allowedMCPEditor.open}
  title={m.api_keys_edit_allowed_mcp()}
  ariaLabel={m.api_keys_allowed_mcp_editor()}
  error={store.allowedMCPEditor.error}
  submitting={store.allowedMCPEditor.submitting}
  submitLabel={m.api_keys_save_allowed_mcp()}
  cancel={false}
  dialogClass="auth-key-editor"
  onclose={() => store.closeAllowedMCPEditor()}
  onsubmit={() => store.submitAllowedMCPEditor()}
>
  {#snippet headerHint()}
    <p class="form-hint">{store.allowedMCPEditor.name}</p>
  {/snippet}

  <FormField id="auth-key-allowed-mcp-edit" label={m.api_keys_allowed_mcp_field()}>
    <div class="auth-key-allowed-mcp-select">
      <SearchSelect
        id="auth-key-allowed-mcp-edit"
        options={selectorOptions}
        multiple
        bind:values={store.allowedMCPEditor.value}
        placeholder={m.api_keys_allowed_mcp_placeholder()}
        searchPlaceholder={m.model_selectors_search()}
        ariaLabel={m.api_keys_allowed_mcp_field()}
        allowCustom
        mono
      />
    </div>
    <p class="form-hint">
      {m.api_keys_allowed_mcp_edit_help()}
    </p>
  </FormField>
</EditorDialog>

<style>
  .auth-key-allowed-mcp-select :global(.search-select) {
    display: flex;
    width: 100%;
  }
</style>
