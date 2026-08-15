<script>
  // Edit-labels modal for an existing API key (EditorDialog shell).
  import EditorDialog from "$lib/components/organisms/EditorDialog.svelte";
  import FormField from "$lib/components/molecules/FormField.svelte";
  import { authKeysStore as store } from "./authKeys.svelte.js";
  import * as m from "$lib/paraglide/messages.js";
</script>

<EditorDialog
  open={store.labelsEditor.open}
  title={m.api_keys_edit_labels()}
  ariaLabel={m.api_keys_labels_editor()}
  error={store.labelsEditor.error}
  submitting={store.labelsEditor.submitting}
  submitLabel={m.api_keys_save_labels()}
  cancel={false}
  dialogClass="auth-key-editor"
  onclose={() => store.closeLabelsEditor()}
  onsubmit={() => store.submitLabelsEditor()}
>
  {#snippet headerHint()}
    <p class="form-hint">{store.labelsEditor.name}</p>
  {/snippet}

  <FormField id="auth-key-labels-edit" label={m.api_keys_labels_field()}>
    <input
      id="auth-key-labels-edit"
      type="text"
      placeholder="ex. team-a, batch-jobs"
      autocomplete="off"
      data-modal-autofocus
      bind:value={store.labelsEditor.value}
    />
    <p class="form-hint">
      {m.api_keys_labels_edit_help()}
    </p>
  </FormField>
</EditorDialog>
