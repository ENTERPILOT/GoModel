<script>
  // Budget editor modal (EditorDialog shell) + the "override existing budget"
  // confirmation dialog.
  import DialogCloseButton from "$lib/components/atoms/DialogCloseButton.svelte";
  import Modal from "$lib/components/atoms/Modal.svelte";
  import Icon from "$lib/components/atoms/Icon.svelte";
  import EditorDialog from "$lib/components/organisms/EditorDialog.svelte";
  import FormField from "$lib/components/molecules/FormField.svelte";
  import { access } from "$lib/stores/access.svelte.js";
  import { budgetsStore as store } from "./budgets.svelte.js";
  import {
    budgetOverrideDialogMessage,
    budgetPeriodOptions,
    budgetScopeOptions,
    budgetSubjectFieldLabel,
    budgetSubjectPlaceholder,
  } from "./budgets-helpers.js";
  import { Save } from "lucide";
  import * as m from "$lib/paraglide/messages.js";

  // Label budgets are gateway-wide, so a key scoped to a user path only
  // creates user-path budgets inside its subtree.
  const scopeOptions = $derived(
    access.scoped
      ? budgetScopeOptions().filter((option) => option.value === "user_path")
      : budgetScopeOptions(),
  );

  function onSubjectInput(event) {
    store.setFormSubject(event.target.value);
    // Keep the input strictly controlled.
    event.target.value = store.form.subject;
  }
</script>

<EditorDialog
  open={store.formOpen}
  title={store.editing ? m.budgets_edit_title() : m.budgets_create_title()}
  ariaLabel={m.budgets_editor_label()}
  error={store.formError}
  submitting={store.formSubmitting}
  submitLabel={m.budgets_save()}
  dialogClass="budget-editor"
  canClose={() => !store.overrideDialogOpen}
  onclose={() => store.closeForm()}
  onsubmit={() => store.submitForm()}
>
  <div class="form-grid">
    <FormField id="budget-scope" label={m.budgets_scope()}>
      <select
        id="budget-scope"
        class="form-select settings-select"
        bind:value={store.form.scope}
        disabled={store.editing}
        onchange={() => store.syncScope()}
      >
        {#each scopeOptions as option (option.value)}
          <option value={option.value}>{option.label}</option>
        {/each}
      </select>
    </FormField>
    <FormField id="budget-subject" label={budgetSubjectFieldLabel(store.form)}>
      <input
        id="budget-subject"
        class="form-input"
        type="text"
        placeholder={budgetSubjectPlaceholder(store.form)}
        value={store.form.subject}
        oninput={onSubjectInput}
        disabled={store.editing}
        data-modal-autofocus={!store.editing || undefined}
      />
    </FormField>
    <FormField id="budget-period" label={m.budgets_period()}>
      <select
        id="budget-period"
        class="form-select settings-select"
        bind:value={store.form.period}
        disabled={store.editing}
        onchange={() => store.syncPeriodSeconds()}
      >
        {#each budgetPeriodOptions() as option (option.value)}
          <option value={option.value}>{option.label}</option>
        {/each}
      </select>
    </FormField>
    {#if store.form.scope === "user_path" && store.quotaTemplatesEnabled()}
      <label class="per-child-option">
        <input type="checkbox" bind:checked={store.form.per_child} />
        <span>
          <strong>{m.budgets_per_child_option()}</strong>
          <small
            >{m.budgets_per_child_option_help({
              subject: store.form.subject || "/",
            })}</small
          >
        </span>
      </label>
    {/if}
    {#if store.form.period === "custom"}
      <FormField id="budget-period-seconds" label={m.budgets_period_seconds()}>
        <input
          id="budget-period-seconds"
          class="form-input"
          type="number"
          min="1"
          step="1"
          bind:value={store.form.period_seconds}
          disabled={store.editing}
        />
      </FormField>
    {/if}
    <FormField id="budget-amount" label={m.budgets_amount()}>
      <input
        id="budget-amount"
        class="form-input"
        type="number"
        min="0"
        step="0.0001"
        placeholder="10.00"
        bind:value={store.form.amount}
        data-modal-autofocus={store.editing || undefined}
      />
    </FormField>
  </div>
  {#if store.editing}
    <p class="form-hint">
      {m.budgets_edit_help()}
    </p>
  {/if}
</EditorDialog>

<Modal
  open={store.overrideDialogOpen}
  variant="auth"
  onclose={() => store.closeOverrideDialog()}
>
  <div
    class="auth-dialog budget-override-dialog"
    role="dialog"
    aria-modal="true"
    aria-labelledby="budgetOverrideDialogTitle"
    aria-describedby="budgetOverrideDialogDescription"
  >
    <div class="auth-dialog-header">
      <h2 id="budgetOverrideDialogTitle">{m.budgets_override_title()}</h2>
      <DialogCloseButton
        label={m.budgets_override_close()}
        onclick={() => store.closeOverrideDialog()}
        class="auth-dialog-close"
        iconClass=""
      />
    </div>
    <form
      class="auth-dialog-form"
      onsubmit={(event) => {
        event.preventDefault();
        store.confirmOverride();
      }}
    >
      <p id="budgetOverrideDialogDescription" class="auth-dialog-hint">
        {budgetOverrideDialogMessage(
          store.overridePendingPayload,
          store.overrideExistingBudget,
        )}
      </p>
      <div class="auth-dialog-actions">
        <button
          type="button"
          class="btn"
          data-modal-autofocus
          onclick={() => store.closeOverrideDialog()}>{m.common_action_cancel()}</button
        >
        <button
          type="submit"
          class="btn btn-danger btn-with-icon"
          disabled={store.formSubmitting}
        >
          <Icon icon={Save} class="form-action-icon" />
          <span>{store.formSubmitting ? m.budgets_saving() : m.budgets_override()}</span>
        </button>
      </div>
    </form>
  </div>
</Modal>

<style>
  .per-child-option {
    grid-column: 1 / -1;
    display: flex;
    gap: 0.65rem;
    align-items: flex-start;
    cursor: pointer;
  }

  .per-child-option input {
    margin-top: 0.2rem;
  }

  .per-child-option span {
    display: grid;
    gap: 0.2rem;
  }

  .per-child-option small {
    color: var(--text-muted);
    line-height: 1.4;
  }

  .budget-override-dialog {
    max-width: 460px;
  }
</style>
