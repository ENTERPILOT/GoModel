<script>
  // Edit-labels modal for an existing API key (EditorDialog shell).
  import EditorDialog from "$lib/components/organisms/EditorDialog.svelte";
  import FormField from "$lib/components/molecules/FormField.svelte";
  import { authKeysStore as store } from "./authKeys.svelte.js";
</script>

<EditorDialog
  open={store.labelsEditor.open}
  title="Edit Labels"
  ariaLabel="API key labels editor"
  error={store.labelsEditor.error}
  submitting={store.labelsEditor.submitting}
  submitLabel="Save Labels"
  cancel={false}
  dialogClass="auth-key-editor"
  onclose={() => store.closeLabelsEditor()}
  onsubmit={() => store.submitLabelsEditor()}
>
  {#snippet headerHint()}
    <p class="form-hint">{store.labelsEditor.name}</p>
  {/snippet}

  <FormField id="auth-key-labels-edit" label="Labels (comma-separated)">
    <input
      id="auth-key-labels-edit"
      type="text"
      placeholder="ex. team-a, batch-jobs"
      autocomplete="off"
      data-modal-autofocus
      bind:value={store.labelsEditor.value}
    />
    <p class="form-hint">
      Applies to new requests made with this key. Leave empty to remove all labels.
    </p>
  </FormField>
</EditorDialog>
