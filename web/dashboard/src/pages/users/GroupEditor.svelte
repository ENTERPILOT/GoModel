<script>
  // Create/edit group modal. The group name is its identity, so it stays
  // read-only when editing (delete + recreate to rename). Changing the
  // parent moves the whole subtree: every member's derived path follows.
  import EditorDialog from "$lib/components/organisms/EditorDialog.svelte";
  import FormField from "$lib/components/molecules/FormField.svelte";
  import { usersStore as store } from "./users.svelte.js";
  import { groupPathByName, parentGroupOptions } from "./usersLogic.js";
  import * as m from "$lib/paraglide/messages.js";

  const editor = $derived(store.groupEditor);
  const groupPaths = $derived(groupPathByName(store.groups));
  // A group cannot be moved under itself or one of its descendants.
  const parentOptions = $derived(parentGroupOptions(store.groups, editor.form.original));
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
        placeholder="ex. engineering"
        autocomplete="off"
        data-modal-autofocus
        disabled={editor.mode === "edit"}
        bind:value={editor.form.name}
      />
      {#if editor.mode === "create"}
        <p class="form-hint">{m.groups_name_hint()}</p>
      {/if}
    </FormField>
    <FormField id="group-editor-parent" label={m.groups_field_parent()}>
      <select id="group-editor-parent" bind:value={editor.form.parent}>
        <option value="">{m.users_group_root()}</option>
        {#each parentOptions as group (group.name)}
          <option value={group.name}>{groupPaths.get(group.name) || group.name}</option>
        {/each}
      </select>
      {#if editor.mode === "edit"}
        <p class="form-hint">{m.groups_parent_move_hint()}</p>
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
