<script>
  // Edit-allowed-models modal for an existing API key (EditorDialog shell).
  import EditorDialog from "$lib/components/organisms/EditorDialog.svelte";
  import FormField from "$lib/components/molecules/FormField.svelte";
  import SearchSelect from "$lib/components/molecules/SearchSelect.svelte";
  import { modelsStore } from "$lib/stores/models.svelte.js";
  import { authKeysStore as store } from "./authKeys.svelte.js";
  import { authKeySelectorOptions } from "./authKeysLogic.js";
  import * as m from "$lib/paraglide/messages.js";

  const selectorOptions = $derived(authKeySelectorOptions(modelsStore.models));
</script>

<EditorDialog
  open={store.allowedModelsEditor.open}
  title={m.api_keys_edit_allowed_models()}
  ariaLabel={m.api_keys_allowed_models_editor()}
  error={store.allowedModelsEditor.error}
  submitting={store.allowedModelsEditor.submitting}
  submitLabel={m.api_keys_save_allowed_models()}
  cancel={false}
  dialogClass="auth-key-editor"
  onclose={() => store.closeAllowedModelsEditor()}
  onsubmit={() => store.submitAllowedModelsEditor()}
>
  {#snippet headerHint()}
    <p class="form-hint">{store.allowedModelsEditor.name}</p>
  {/snippet}

  <FormField id="auth-key-allowed-models-edit" label={m.api_keys_allowed_models_field()}>
    <div class="auth-key-allowed-models-select">
      <SearchSelect
        id="auth-key-allowed-models-edit"
        options={selectorOptions}
        multiple
        bind:values={store.allowedModelsEditor.value}
        placeholder={m.model_selectors_placeholder()}
        searchPlaceholder={m.model_selectors_search()}
        ariaLabel={m.api_keys_allowed_models_field()}
        allowCustom
        mono
      />
    </div>
    <p class="form-hint">
      {m.api_keys_allowed_models_edit_help()}
    </p>
  </FormField>
</EditorDialog>

<style>
  .auth-key-allowed-models-select :global(.search-select) {
    display: flex;
    width: 100%;
  }
</style>
