<script>
  // Create/edit user modal: name, owning group, and description. The user
  // path is derived from the group chain plus the name.
  import EditorDialog from "$lib/components/organisms/EditorDialog.svelte";
  import FormField from "$lib/components/molecules/FormField.svelte";
  import { usersStore as store } from "./users.svelte.js";
  import { groupPathByName } from "./usersLogic.js";
  import * as m from "$lib/paraglide/messages.js";

  const editor = $derived(store.userEditor);
  const groupPaths = $derived(groupPathByName(store.groups));
  const derivedPath = $derived.by(() => {
    const name = String(editor.form.name || "").trim();
    if (!name) return "";
    const base = editor.form.group ? groupPaths.get(editor.form.group) || "" : "";
    return base + "/" + name;
  });
</script>

<EditorDialog
  open={editor.open}
  title={editor.mode === "edit" ? m.users_edit_title() : m.users_create()}
  error={editor.error}
  submitting={editor.submitting}
  submitLabel={m.users_save()}
  submittingLabel={m.users_saving()}
  onclose={() => store.closeUserEditor()}
  onsubmit={() => store.submitUserEditor()}
>
  <div class="user-editor-fields">
    <FormField id="user-editor-name" label={`${m.users_field_name()} (${m.users_required()})`}>
      <input
        id="user-editor-name"
        type="text"
        placeholder="ex. anna"
        autocomplete="off"
        data-modal-autofocus
        bind:value={editor.form.name}
      />
    </FormField>
    <FormField id="user-editor-group" label={m.users_field_group()}>
      <select id="user-editor-group" bind:value={editor.form.group}>
        <option value="">{m.users_group_root()}</option>
        {#each store.groups as group (group.name)}
          <option value={group.name}>{groupPaths.get(group.name) || group.name}</option>
        {/each}
      </select>
      {#if store.groups.length === 0}
        <p class="form-hint">{m.users_no_groups_hint()}</p>
      {/if}
    </FormField>
    {#if derivedPath}
      <p class="form-hint">
        {m.users_derived_path_hint()}
        <code>{derivedPath}</code>
      </p>
    {/if}
    <FormField id="user-editor-description" label={m.users_field_description()}>
      <textarea
        id="user-editor-description"
        rows="2"
        placeholder={m.users_description_placeholder()}
        bind:value={editor.form.description}
      ></textarea>
    </FormField>
  </div>
</EditorDialog>

<style>
  .user-editor-fields > :global(.form-field) {
    margin-bottom: 4px;
  }
</style>
