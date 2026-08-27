<script>
  // Create/edit group modal. The group name is its identity, so it stays
  // read-only when editing (delete + recreate to rename).
  import EditorDialog from "$lib/components/organisms/EditorDialog.svelte";
  import FormField from "$lib/components/molecules/FormField.svelte";
  import { usersStore as store } from "./users.svelte.js";
  import * as m from "$lib/paraglide/messages.js";

  const editor = $derived(store.groupEditor);
</script>

<EditorDialog
  open={editor.open}
  title={editor.mode === "edit" ? m.groups_edit_title() : m.groups_create()}
  error={editor.error}
  submitting={editor.submitting}
  submitLabel={m.users_save()}
  submittingLabel={m.users_saving()}
  onclose={() => store.closeGroupEditor()}
  onsubmit={() => store.submitGroupEditor()}
>
  <div class="group-editor-fields">
    <FormField id="group-editor-name" label={m.groups_field_name()}>
      <input
        id="group-editor-name"
        type="text"
        placeholder="ex. beta-testers"
        autocomplete="off"
        data-modal-autofocus
        disabled={editor.mode === "edit"}
        bind:value={editor.form.name}
      />
      {#if editor.mode === "create"}
        <p class="form-hint">{m.groups_name_hint()}</p>
      {/if}
    </FormField>
    <FormField id="group-editor-description" label={m.users_field_description()}>
      <textarea
        id="group-editor-description"
        rows="2"
        placeholder={m.groups_description_placeholder()}
        bind:value={editor.form.description}
      ></textarea>
    </FormField>
  </div>
</EditorDialog>

<style>
  .group-editor-fields > :global(.form-field) {
    margin-bottom: 4px;
  }
</style>
