<script>
  // Create/edit user modal: user path, display name, description, and group
  // membership checkboxes.
  import EditorDialog from "$lib/components/organisms/EditorDialog.svelte";
  import FormField from "$lib/components/molecules/FormField.svelte";
  import InlineHelpSection from "$lib/components/molecules/InlineHelpSection.svelte";
  import { usersStore as store } from "./users.svelte.js";
  import * as m from "$lib/paraglide/messages.js";

  const editor = $derived(store.userEditor);
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
    <div class="form-field">
      <InlineHelpSection copyId="user-path-help-copy" label={m.users_path_help_label()}>
        {#snippet title()}
          <label class="form-field-label" for="user-editor-path">
            {m.users_field_path()} <span class="form-hint">({m.users_required()})</span>
          </label>
        {/snippet}
        {#snippet help()}
          {m.users_path_help()}
        {/snippet}
      </InlineHelpSection>
      <input
        id="user-editor-path"
        type="text"
        placeholder="ex. /engineering/team-a"
        autocomplete="off"
        data-modal-autofocus
        aria-describedby="user-path-help-copy"
        bind:value={editor.form.user_path}
      />
    </div>
    <FormField id="user-editor-name" label={m.users_field_name()}>
      <input
        id="user-editor-name"
        type="text"
        placeholder="ex. Team A"
        autocomplete="off"
        bind:value={editor.form.name}
      />
    </FormField>
    <div class="form-field">
      <span class="form-field-label">{m.users_field_groups()}</span>
      {#if store.groups.length === 0}
        <p class="form-hint">{m.users_no_groups_hint()}</p>
      {:else}
        <div class="user-editor-groups">
          {#each store.groups as group (group.name)}
            <label class="user-editor-group-option">
              <input
                type="checkbox"
                checked={editor.form.groups.includes(group.name)}
                onchange={() => store.toggleUserFormGroup(group.name)}
              />
              <span>{group.name}</span>
            </label>
          {/each}
        </div>
      {/if}
    </div>
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

  .user-editor-groups {
    display: flex;
    flex-wrap: wrap;
    gap: 8px 16px;
    padding: 4px 0;
  }

  .user-editor-group-option {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    font-size: 13px;
    cursor: pointer;
  }
</style>
