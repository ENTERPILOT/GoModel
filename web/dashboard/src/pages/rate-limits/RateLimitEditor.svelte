<script>
  // Rate limit create/edit modal. Shared by the Rate Limits page and the
  // Models page inspector (mount it alongside RateLimitInspector).
  // Entirely driven by the rateLimits store.
  import DialogCloseButton from "$lib/components/atoms/DialogCloseButton.svelte";
  import Modal from "$lib/components/atoms/Modal.svelte";
  import Icon from "$lib/components/atoms/Icon.svelte";
  import { auth } from "$lib/stores/auth.svelte.ts";
  import { rateLimits } from "./rateLimits.svelte.js";

  function onclose() {
    // Never close the editor underneath the auth dialog.
    if (auth.dialogOpen) return;
    rateLimits.closeRateLimitForm();
  }
</script>

<Modal open={rateLimits.rateLimitFormOpen} variant="editor" {onclose}>
  <div
    class="model-editor budget-editor"
    role="dialog"
    aria-modal="true"
    aria-labelledby="rate-limit-editor-title"
  >
    <!-- novalidate: the concurrent period keeps 0 in the hidden period-seconds
         input (min=1), and native validation silently blocks submit on hidden
         invalid controls. Validation lives in rateLimitFormPayload(). -->
    <form
      class="form"
      novalidate
      onsubmit={(event) => {
        event.preventDefault();
        rateLimits.submitRateLimitForm();
      }}
    >
      <div class="editor-header">
        <div>
          <h3 id="rate-limit-editor-title">
            {rateLimits.rateLimitEditing ? "Edit Rate Limit" : "Create Rate Limit"}
          </h3>
        </div>
        <DialogCloseButton
          label="Close rate limit editor"
          onclick={() => rateLimits.closeRateLimitForm()}
        />
      </div>

      <div class="form-grid">
        <div class="form-field">
          <label class="form-field-label" for="rate-limit-scope">Scope</label>
          <select
            id="rate-limit-scope"
            class="form-select settings-select"
            bind:value={rateLimits.rateLimitForm.scope}
            onchange={() => rateLimits.syncRateLimitScope()}
          >
            {#each rateLimits.rateLimitScopeOptions() as option (option.value)}
              <option value={option.value}>{option.label}</option>
            {/each}
          </select>
        </div>
        <div class="form-field">
          <label class="form-field-label" for="rate-limit-subject">
            {rateLimits.rateLimitSubjectFieldLabel()}
          </label>
          <input
            id="rate-limit-subject"
            class="form-input"
            type="text"
            placeholder={rateLimits.rateLimitSubjectPlaceholder()}
            data-modal-autofocus={rateLimits.rateLimitEditing ? undefined : true}
            value={rateLimits.rateLimitForm.subject}
            oninput={(event) =>
              rateLimits.setRateLimitFormSubject(event.currentTarget.value)}
          />
        </div>
        <div class="form-field">
          <label class="form-field-label" for="rate-limit-period">Period</label>
          <select
            id="rate-limit-period"
            class="form-select settings-select"
            bind:value={rateLimits.rateLimitForm.period}
            onchange={() => rateLimits.syncRateLimitPeriodSeconds()}
          >
            {#each rateLimits.rateLimitPeriodOptions() as option (option.value)}
              <option value={option.value}>{option.label}</option>
            {/each}
          </select>
        </div>
        {#if rateLimits.rateLimitForm.period === "custom"}
          <div class="form-field">
            <label class="form-field-label" for="rate-limit-period-seconds">
              Period Seconds
            </label>
            <input
              id="rate-limit-period-seconds"
              class="form-input"
              type="number"
              min="1"
              step="1"
              bind:value={rateLimits.rateLimitForm.period_seconds}
            />
          </div>
        {/if}
        <div class="form-field">
          <label class="form-field-label" for="rate-limit-max-requests">
            {rateLimits.rateLimitForm.period === "concurrent"
              ? "Max In-Flight Requests"
              : "Max Requests"}
          </label>
          <input
            id="rate-limit-max-requests"
            class="form-input"
            type="number"
            min="1"
            step="1"
            placeholder="100"
            data-modal-autofocus={rateLimits.rateLimitEditing ? true : undefined}
            bind:value={rateLimits.rateLimitForm.max_requests}
          />
        </div>
        {#if rateLimits.rateLimitForm.period !== "concurrent"}
          <div class="form-field">
            <label class="form-field-label" for="rate-limit-max-tokens">
              Max Tokens
            </label>
            <input
              id="rate-limit-max-tokens"
              class="form-input"
              type="number"
              min="1"
              step="1"
              placeholder="50000"
              bind:value={rateLimits.rateLimitForm.max_tokens}
            />
          </div>
        {/if}
      </div>
      <p class="form-hint">
        A user path rule limits the whole subtree; provider and model rules cap
        all traffic routed there and make load balancing skip the target while
        it is saturated. One shared counter per rule. Leave a field empty to
        skip that limit. Token limits require usage tracking.
      </p>
      {#if rateLimits.rateLimitEditing}
        <p class="form-hint">
          Scope, subject, and period identify the rule: changing any of them
          moves the rule to a new key and restarts its live counters.
        </p>
      {/if}

      {#if rateLimits.rateLimitFormError}
        <p class="form-error" role="alert" aria-live="assertive">
          {rateLimits.rateLimitFormError}
        </p>
      {/if}
      <div class="form-actions">
        <button
          type="button"
          class="btn"
          onclick={() => rateLimits.closeRateLimitForm()}
        >
          Cancel
        </button>
        <button
          type="submit"
          class="btn btn-primary btn-with-icon"
          disabled={rateLimits.rateLimitFormSubmitting}
        >
          <Icon name="save" class="form-action-icon" />
          <span>
            {rateLimits.rateLimitFormSubmitting ? "Saving..." : "Save Rate Limit"}
          </span>
        </button>
      </div>
    </form>
  </div>
</Modal>
