<script>
  // Create-API-key modal (EditorDialog shell): form fields plus the one-time
  // issued-secret banner with clipboard copy. Once a key is issued the footer
  // submit turns into the "Done, I've stored it" dismissal.
  import CopyButton from "$lib/components/atoms/CopyButton.svelte";
  import EditorDialog from "$lib/components/organisms/EditorDialog.svelte";
  import FormField from "$lib/components/molecules/FormField.svelte";
  import InlineHelpSection from "$lib/components/molecules/InlineHelpSection.svelte";
  import SearchSelect from "$lib/components/molecules/SearchSelect.svelte";
  import { modelsStore } from "$lib/stores/models.svelte.js";
  import { authKeysStore as store } from "./authKeys.svelte.js";
  import { authKeySelectorOptions } from "./authKeysLogic.js";
  import { Check, Plus } from "lucide";
  import * as m from "$lib/paraglide/messages.js";

  const selectorOptions = $derived(authKeySelectorOptions(modelsStore.models));
</script>

<EditorDialog
  open={store.formOpen}
  title={m.api_keys_create()}
  ariaLabel={m.api_keys_editor()}
  error={store.issuedValue ? "" : store.error}
  submitting={store.formSubmitting}
  submitLabel={store.issuedValue ? m.api_keys_done() : m.api_keys_create()}
  submittingLabel={m.api_keys_creating()}
  submitIcon={store.issuedValue ? Check : Plus}
  cancel={false}
  dialogClass="auth-key-editor"
  onclose={() => store.closeForm()}
  onsubmit={() =>
    store.issuedValue ? store.dismissIssuedKey() : store.submitForm()}
>
  {#if store.issuedValue}
    <div class="auth-key-issued-banner">
      <p class="auth-key-issued-warning">
        {m.api_keys_store_warning()}
      </p>
      <div class="auth-key-issued-value-row">
        <code class="auth-key-issued-token">{store.issuedValue}</code>
        <CopyButton
          state={store.copyState}
          onclick={() => store.copyIssuedValue()}
        />
      </div>
      {#if store.copyState.error}
        <p class="form-error" role="alert" aria-live="assertive">
          {m.api_keys_copy_failed()}
        </p>
      {/if}
    </div>
  {:else}
    <div class="auth-key-form-fields">
      <div class="form-grid">
        <div class="form-field">
          <label class="form-field-label" for="auth-key-name">
            {m.api_keys_name()} <span class="form-hint">({m.api_keys_required()})</span>
          </label>
          <input
            id="auth-key-name"
            type="text"
            placeholder="e.g. ci-deploy"
            autocomplete="off"
            data-modal-autofocus
            bind:value={store.form.name}
          />
        </div>
        <div class="form-field">
          <label class="form-field-label" for="auth-key-expires">
            {m.api_keys_expires()} <span class="form-hint">({m.api_keys_expiry_help()})</span>
          </label>
          <input id="auth-key-expires" type="date" bind:value={store.form.expires_at} />
        </div>
      </div>
      <div class="form-field">
        <InlineHelpSection copyId="auth-key-user-path-help-copy" label={m.api_keys_user_path_help_label()}>
          {#snippet title()}
            <label class="form-field-label" for="auth-key-user-path">{m.api_keys_user_path()}</label>
          {/snippet}
          {#snippet help()}
            {m.api_keys_user_path_help()}
          {/snippet}
        </InlineHelpSection>
        <input
          id="auth-key-user-path"
          type="text"
          placeholder="ex. /department1/team-a"
          aria-describedby="auth-key-user-path-help-copy"
          bind:value={store.form.user_path}
        />
      </div>
      <div class="form-field">
        <InlineHelpSection copyId="auth-key-labels-help-copy" label={m.api_keys_labels_help_label()}>
          {#snippet title()}
            <label class="form-field-label" for="auth-key-labels">
              {m.api_keys_labels()}
            </label>
          {/snippet}
          {#snippet help()}
            {m.api_keys_labels_help()}
          {/snippet}
        </InlineHelpSection>
        <input
          id="auth-key-labels"
          type="text"
          placeholder="ex. team-a, batch-jobs"
          aria-describedby="auth-key-labels-help-copy"
          bind:value={store.form.labels}
        />
      </div>
      <div class="form-field">
        <InlineHelpSection copyId="auth-key-allowed-models-help-copy" label={m.api_keys_allowed_models_help_label()}>
          {#snippet title()}
            <label class="form-field-label" for="auth-key-allowed-models">
              {m.api_keys_allowed_models()}
            </label>
          {/snippet}
          {#snippet help()}
            {m.api_keys_allowed_models_help()}
          {/snippet}
        </InlineHelpSection>
        <div class="auth-key-allowed-models-select">
          <SearchSelect
            id="auth-key-allowed-models"
            options={selectorOptions}
            multiple
            bind:values={store.form.allowed_models}
            placeholder={m.model_selectors_placeholder()}
            searchPlaceholder={m.model_selectors_search()}
            ariaLabel={m.api_keys_allowed_models()}
            allowCustom
            mono
          />
        </div>
      </div>
      <div class="form-field">
        <InlineHelpSection copyId="auth-key-dashboard-access-help-copy" label={m.api_keys_dashboard_help_label()}>
          {#snippet title()}
            <label class="form-field-label" for="auth-key-dashboard-access">{m.api_keys_dashboard_access()}</label>
          {/snippet}
          {#snippet help()}
            {m.api_keys_dashboard_help()}
          {/snippet}
        </InlineHelpSection>
        <label class="auth-key-dashboard-toggle">
          <input
            id="auth-key-dashboard-access"
            type="checkbox"
            aria-describedby="auth-key-dashboard-access-help-copy"
            bind:checked={store.form.dashboard_access}
          />
          <span>{m.api_keys_dashboard_allow()}</span>
        </label>
      </div>
      <FormField id="auth-key-description" label={m.api_keys_description_optional()}>
        <textarea
          id="auth-key-description"
          rows="2"
          placeholder={m.api_keys_description_placeholder()}
          bind:value={store.form.description}
        ></textarea>
      </FormField>
    </div>
  {/if}
</EditorDialog>

<style>
  .auth-key-form-fields > :global(.form-field) {
    margin-bottom: 4px;
  }

  .auth-key-allowed-models-select :global(.search-select) {
    display: flex;
    width: 100%;
  }

  .auth-key-dashboard-toggle {
    display: inline-flex;
    align-items: center;
    gap: 8px;
    font-size: 13px;
    cursor: pointer;
  }

  .auth-key-issued-banner {
    background: color-mix(in srgb, var(--success) 8%, var(--bg-surface));
    border: 1px solid color-mix(in srgb, var(--success) 30%, var(--border));
    border-radius: var(--radius);
    padding: 16px;
    margin-bottom: 20px;
  }

  .auth-key-issued-warning {
    font-size: 13px;
    font-weight: 600;
    margin-bottom: 12px;
    color: color-mix(in srgb, var(--success) 80%, var(--text));
  }

  .auth-key-issued-value-row {
    display: flex;
    align-items: center;
    gap: 12px;
    margin-bottom: 12px;
    flex-wrap: wrap;
  }

  .auth-key-issued-token {
    flex: 1;
    min-width: 0;
    overflow-x: auto;
    padding: 8px 12px;
    background: var(--bg);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    font-size: 13px;
    word-break: break-all;
  }
</style>
