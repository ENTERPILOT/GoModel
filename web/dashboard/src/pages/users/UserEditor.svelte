<script>
  // Create/edit modal for one user-path policy (EditorDialog shell). The
  // allowlist is free text; the quick-add picker (the shared SearchSelect)
  // appends a provider wildcard or a concrete model from the inventory. Its
  // value is deliberately not bound so it reads "Quick add…" after each pick.
  import EditorDialog from "$lib/components/organisms/EditorDialog.svelte";
  import FormField from "$lib/components/molecules/FormField.svelte";
  import InlineHelpSection from "$lib/components/molecules/InlineHelpSection.svelte";
  import SearchSelect from "$lib/components/molecules/SearchSelect.svelte";
  import { modelsStore } from "$lib/stores/models.svelte.js";
  import { usersStore as store } from "./users.svelte.js";
  import { userSelectorOptions } from "./usersLogic.js";
  import * as m from "$lib/paraglide/messages.js";

  const selectorOptions = $derived(userSelectorOptions(modelsStore.models));
</script>

<EditorDialog
  open={store.formOpen}
  title={store.editingPath ? m.users_edit() : m.users_create()}
  ariaLabel={m.users_editor()}
  error={store.error}
  submitting={store.formSubmitting}
  submitLabel={m.users_save()}
  submittingLabel={m.users_saving()}
  dialogClass="user-editor"
  onclose={() => store.closeForm()}
  onsubmit={() => store.submitForm()}
>
  <div class="form-field">
    <InlineHelpSection copyId="user-path-help-copy" label={m.users_path_help_label()}>
      {#snippet title()}
        <label class="form-field-label" for="user-path">{m.users_path()}</label>
      {/snippet}
      {#snippet help()}
        {m.users_path_help()}
      {/snippet}
    </InlineHelpSection>
    <input
      id="user-path"
      type="text"
      placeholder="ex. /acme/engineering"
      autocomplete="off"
      aria-describedby="user-path-help-copy"
      disabled={Boolean(store.editingPath)}
      data-modal-autofocus={store.editingPath ? undefined : true}
      bind:value={store.form.user_path}
    />
  </div>

  <div class="form-field">
    <InlineHelpSection copyId="user-allowed-models-help-copy" label={m.users_allowed_models_help_label()}>
      {#snippet title()}
        <label class="form-field-label" for="user-allowed-models">{m.users_allowed_models()}</label>
      {/snippet}
      {#snippet help()}
        {m.users_allowed_models_help()}
      {/snippet}
    </InlineHelpSection>
    <textarea
      id="user-allowed-models"
      rows="5"
      placeholder={"anthropic/*\nopenai/gpt-4o"}
      aria-describedby="user-allowed-models-help-copy"
      data-modal-autofocus={store.editingPath ? true : undefined}
      bind:value={store.form.allowed_models}
    ></textarea>
    {#if selectorOptions.length > 0}
      <div class="user-quick-add">
        <SearchSelect
          id="user-allowed-models-quick-add"
          options={selectorOptions}
          value=""
          onchange={(value) => store.appendSelector(value)}
          placeholder={m.users_quick_add()}
          searchPlaceholder={m.users_quick_add_search()}
          ariaLabel={m.users_quick_add()}
          allowCustom
          mono
        />
      </div>
    {/if}
  </div>

  <FormField id="user-description" label={m.users_description_optional()}>
    <textarea
      id="user-description"
      rows="2"
      placeholder={m.users_description_placeholder()}
      bind:value={store.form.description}
    ></textarea>
  </FormField>
</EditorDialog>

<style>
  .user-quick-add {
    margin-top: 8px;
  }
</style>
