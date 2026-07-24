<script>
  // Budget editor modal + the "override existing budget" confirmation dialog.
  import DialogCloseButton from "$lib/components/atoms/DialogCloseButton.svelte";
  import Modal from "$lib/components/atoms/Modal.svelte";
  import Icon from "$lib/components/atoms/Icon.svelte";
  import { auth } from "$lib/stores/auth.svelte.js";
  import { budgetsStore as store } from "./budgets.svelte.js";
  import {
    budgetOverrideDialogMessage,
    budgetPeriodOptions,
  } from "./budgets-helpers.js";

  // Escape handling: the editor only closes when neither the auth dialog
  // nor the override dialog sits on top of it.
  function onEditorClose() {
    if (!store.overrideDialogOpen && !auth.dialogOpen) {
      store.closeForm();
    }
  }

  function onUserPathInput(event) {
    store.setFormUserPath(event.target.value);
    // Keep the input strictly controlled.
    event.target.value = store.form.user_path;
  }
</script>

<Modal open={store.formOpen} variant="editor" onclose={onEditorClose}>
  <div
    class="model-editor budget-editor"
    role="dialog"
    aria-modal="true"
    aria-label="Budget editor"
  >
    <form
      class="form"
      onsubmit={(event) => {
        event.preventDefault();
        store.submitForm();
      }}
    >
      <div class="editor-header">
        <div>
          <h3>{store.editing ? "Edit Budget" : "Create Budget"}</h3>
        </div>
        <DialogCloseButton
          label="Close budget editor"
          onclick={() => store.closeForm()}
          iconClass=""
        />
      </div>

      <div class="form-grid">
        <div class="form-field">
          <label class="form-field-label" for="budget-user-path">User Path</label>
          <input
            id="budget-user-path"
            class="form-input"
            type="text"
            placeholder="/team/alpha"
            value={store.form.user_path}
            oninput={onUserPathInput}
            disabled={store.editing}
            data-modal-autofocus={!store.editing || undefined}
          />
        </div>
        <div class="form-field">
          <label class="form-field-label" for="budget-period">Period</label>
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
        </div>
        {#if store.form.period === "custom"}
          <div class="form-field">
            <label class="form-field-label" for="budget-period-seconds">
              Period Seconds
            </label>
            <input
              id="budget-period-seconds"
              class="form-input"
              type="number"
              min="1"
              step="1"
              bind:value={store.form.period_seconds}
              disabled={store.editing}
            />
          </div>
        {/if}
        <div class="form-field">
          <label class="form-field-label" for="budget-amount">Amount</label>
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
        </div>
      </div>
      {#if store.editing}
        <p class="form-hint">
          Editing a budget updates its limit only. Use Reset to start a new
          budget period.
        </p>
      {/if}

      {#if store.formError}
        <p class="form-error" role="alert" aria-live="assertive">
          {store.formError}
        </p>
      {/if}
      <div class="form-actions">
        <button
          type="button"
          class="pagination-btn"
          onclick={() => store.closeForm()}>Cancel</button
        >
        <button
          type="submit"
          class="pagination-btn pagination-btn-primary pagination-btn-with-icon"
          disabled={store.formSubmitting}
        >
          <Icon name="save" class="form-action-icon" />
          <span>{store.formSubmitting ? "Saving..." : "Save Budget"}</span>
        </button>
      </div>
    </form>
  </div>
</Modal>

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
          class="pagination-btn"
          data-modal-autofocus
          onclick={() => store.closeOverrideDialog()}>Cancel</button
        >
        <button
          type="submit"
          class="pagination-btn pagination-btn-danger pagination-btn-with-icon"
          disabled={store.formSubmitting}
        >
          <Icon name="save" class="form-action-icon" />
          <span>{store.formSubmitting ? "Saving..." : "Override Budget"}</span>
        </button>
      </div>
    </form>
  </div>
</Modal>

<style>
  /* Styles owned by this component (moved from dashboard.css). */
  .budget-override-dialog {
    max-width: 460px;
  }
</style>
