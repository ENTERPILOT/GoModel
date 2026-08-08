<script>
  // Budget editor modal (EditorDialog shell) + the "override existing budget"
  // confirmation dialog.
  import DialogCloseButton from "$lib/components/atoms/DialogCloseButton.svelte";
  import Modal from "$lib/components/atoms/Modal.svelte";
  import Icon from "$lib/components/atoms/Icon.svelte";
  import EditorDialog from "$lib/components/organisms/EditorDialog.svelte";
  import FormField from "$lib/components/molecules/FormField.svelte";
  import { budgetsStore as store } from "./budgets.svelte.js";
  import {
    budgetOverrideDialogMessage,
    budgetPeriodOptions,
    budgetScopeOptions,
    budgetSubjectFieldLabel,
    budgetSubjectPlaceholder,
  } from "./budgets-helpers.js";
  import { Save } from "lucide";

  function onSubjectInput(event) {
    store.setFormSubject(event.target.value);
    // Keep the input strictly controlled.
    event.target.value = store.form.subject;
  }
</script>

<EditorDialog
  open={store.formOpen}
  title={store.editing ? "Edit Budget" : "Create Budget"}
  ariaLabel="Budget editor"
  error={store.formError}
  submitting={store.formSubmitting}
  submitLabel="Save Budget"
  dialogClass="budget-editor"
  canClose={() => !store.overrideDialogOpen}
  onclose={() => store.closeForm()}
  onsubmit={() => store.submitForm()}
>
  <div class="form-grid">
    <FormField id="budget-scope" label="Scope">
      <select
        id="budget-scope"
        class="form-select settings-select"
        bind:value={store.form.scope}
        disabled={store.editing}
        onchange={() => store.syncScope()}
      >
        {#each budgetScopeOptions() as option (option.value)}
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
    <FormField id="budget-period" label="Period">
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
    {#if store.form.scope === "user_path"}
      <label class="per-child-option">
        <input type="checkbox" bind:checked={store.form.per_child} />
        <span>
          <strong>Apply per direct child</strong>
          <small
            >Each child below {store.form.subject || "/"} gets an independent budget.
            Deeper descendants share their direct child's budget.</small
          >
        </span>
      </label>
    {/if}
    {#if store.form.period === "custom"}
      <FormField id="budget-period-seconds" label="Period Seconds">
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
    <FormField id="budget-amount" label="Amount">
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
      Editing a budget updates its limit only. Use Reset to start a new budget
      period.
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
      <h2 id="budgetOverrideDialogTitle">Override Existing Budget</h2>
      <DialogCloseButton
        label="Close budget override dialog"
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
          onclick={() => store.closeOverrideDialog()}>Cancel</button
        >
        <button
          type="submit"
          class="btn btn-danger btn-with-icon"
          disabled={store.formSubmitting}
        >
          <Icon icon={Save} class="form-action-icon" />
          <span>{store.formSubmitting ? "Saving..." : "Override Budget"}</span>
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
