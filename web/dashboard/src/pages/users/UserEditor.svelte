<script>
  // Create/edit modal for one user-path policy (EditorDialog shell). The
  // allowlist is a multi-select over the shared model inventory (provider
  // wildcards first, then concrete models); custom selectors can be typed
  // into its search box, comma-separated. The request policy (default model
  // and ceilings) is inherited by every path below the node.
  import EditorDialog from "$lib/components/organisms/EditorDialog.svelte";
  import FormField from "$lib/components/molecules/FormField.svelte";
  import InlineHelpSection from "$lib/components/molecules/InlineHelpSection.svelte";
  import SearchSelect from "$lib/components/molecules/SearchSelect.svelte";
  import { modelsStore } from "$lib/stores/models.svelte.js";
  import { usersStore as store } from "./users.svelte.js";
  import { parseModelSelectors } from "$lib/utils/modelSelectors.js";
  import { parentEffectiveModels, previewEffectiveModels, userSelectorOptions } from "./usersLogic.js";
  import { REASONING_EFFORT_LEVELS } from "$pages/models/vmForm.js";
  import * as m from "$lib/paraglide/messages.js";

  const selectorOptions = $derived(userSelectorOptions(modelsStore.models));

  // Warn while typing when the list would leave no model available under the
  // parents' effective access; unknown parents (no catalog) stay silent.
  const typedSelectors = $derived(parseModelSelectors(store.form.allowed_models));
  const parentModels = $derived(parentEffectiveModels(store.nodes, store.form.user_path));
  const noModelsRemain = $derived(
    typedSelectors.length > 0 &&
      parentModels !== null &&
      previewEffectiveModels(parentModels, typedSelectors).length === 0,
  );
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
    <div class="user-allowed-models-select">
      <SearchSelect
        id="user-allowed-models"
        options={selectorOptions}
        multiple
        bind:values={store.form.allowed_models}
        placeholder={m.model_selectors_placeholder()}
        searchPlaceholder={m.model_selectors_search()}
        ariaLabel={m.users_allowed_models()}
        allowCustom
        mono
      />
    </div>
    {#if noModelsRemain}
      <p class="form-error user-no-models-warning" role="alert">{m.users_no_models_warning()}</p>
    {/if}
  </div>

  <FormField id="user-default-model" label={m.users_default_model()}>
    <input
      id="user-default-model"
      type="text"
      class="user-mono-input"
      placeholder={m.users_default_model_placeholder()}
      autocomplete="off"
      bind:value={store.form.default_model}
    />
    <span class="form-hint">{m.users_default_model_help()}</span>
  </FormField>

  <FormField id="user-max-reasoning-effort" label={m.users_max_reasoning_effort()}>
    <select
      id="user-max-reasoning-effort"
      class="form-select"
      bind:value={store.form.max_reasoning_effort}
    >
      <option value="">{m.users_no_ceiling()}</option>
      {#each REASONING_EFFORT_LEVELS as level (level)}
        <option value={level}>{level}</option>
      {/each}
    </select>
    <span class="form-hint">{m.users_max_reasoning_effort_help()}</span>
  </FormField>

  <FormField id="user-max-output-tokens" label={m.users_max_output_tokens()}>
    <input
      id="user-max-output-tokens"
      type="number"
      min="1"
      step="1"
      inputmode="numeric"
      placeholder={m.users_no_ceiling()}
      bind:value={store.form.max_output_tokens}
    />
    <span class="form-hint">{m.users_max_output_tokens_help()}</span>
  </FormField>

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
  .user-allowed-models-select :global(.search-select) {
    display: flex;
    width: 100%;
  }

  .user-mono-input {
    font-family: var(--font-mono, monospace);
  }

  .user-no-models-warning {
    margin-top: 6px;
  }
</style>
