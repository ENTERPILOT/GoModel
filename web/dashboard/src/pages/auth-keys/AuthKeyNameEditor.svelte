<script>
  // Rename modal for an existing API key (EditorDialog shell). A name another
  // key already has is allowed; the dialog only warns about it.
  import EditorDialog from "$lib/components/organisms/EditorDialog.svelte";
  import FormField from "$lib/components/molecules/FormField.svelte";
  import { authKeysStore as store } from "./authKeys.svelte.js";
  import * as m from "$lib/paraglide/messages.js";
</script>

<EditorDialog
  open={store.nameEditor.open}
  title={m.api_keys_edit_name()}
  ariaLabel={m.api_keys_name_editor()}
  error={store.nameEditor.error}
  submitting={store.nameEditor.submitting}
  submitLabel={m.api_keys_save_name()}
  cancel={false}
  dialogClass="auth-key-editor"
  onclose={() => store.closeNameEditor()}
  onsubmit={() => store.submitNameEditor()}
>
  {#snippet headerHint()}
    <p class="form-hint">{store.nameEditor.name}</p>
  {/snippet}

  <FormField id="auth-key-name-edit" label={m.api_keys_name()}>
    <input
      id="auth-key-name-edit"
      type="text"
      autocomplete="off"
      data-modal-autofocus
      bind:value={store.nameEditor.value}
    />
    <p class="form-hint">
      {m.api_keys_name_edit_help()}
    </p>
  </FormField>
  {#if store.nameEditorWarning}
    <p class="alert alert-warning auth-key-name-warning" role="status" aria-live="polite">
      {store.nameEditorWarning}
    </p>
  {/if}
</EditorDialog>

<style>
  .auth-key-name-warning {
    margin: 0;
    font-size: 13px;
  }
</style>
