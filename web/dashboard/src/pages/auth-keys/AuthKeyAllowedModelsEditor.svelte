<script>
  // Edit-allowed-models modal for an existing API key (EditorDialog shell).
  import EditorDialog from "$lib/components/organisms/EditorDialog.svelte";
  import FormField from "$lib/components/molecules/FormField.svelte";
  import { authKeysStore as store } from "./authKeys.svelte.js";
  import * as m from "$lib/paraglide/messages.js";
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
    <textarea
      id="auth-key-allowed-models-edit"
      rows="4"
      placeholder="ex. anthropic/*, openai/gpt-4o"
      autocomplete="off"
      data-modal-autofocus
      bind:value={store.allowedModelsEditor.value}
    ></textarea>
    <p class="form-hint">
      {m.api_keys_allowed_models_edit_help()}
    </p>
  </FormField>
</EditorDialog>
