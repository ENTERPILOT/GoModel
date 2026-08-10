<script>
  // Rate limit create/edit modal (EditorDialog shell). Shared by the Rate
  // Limits page and the Models page inspector (mount it alongside
  // RateLimitInspector). Entirely driven by the rateLimits store.
  import EditorDialog from "$lib/components/organisms/EditorDialog.svelte";
  import FormField from "$lib/components/molecules/FormField.svelte";
  import { rateLimits } from "./rateLimits.svelte.js";
</script>

<!-- novalidate: validation lives in rateLimitFormPayload(), which surfaces
     the store's error copy; native constraint checks (e.g. a 0 typed into a
     min=1 number field) would block the submit before it runs. -->
<EditorDialog
  open={rateLimits.rateLimitFormOpen}
  title={rateLimits.rateLimitEditing ? "Edit Rate Limit" : "Create Rate Limit"}
  ariaLabel="Rate limit editor"
  error={rateLimits.rateLimitFormError}
  submitting={rateLimits.rateLimitFormSubmitting}
  submitLabel="Save Rate Limit"
  dialogClass="budget-editor"
  novalidate
  onclose={() => rateLimits.closeRateLimitForm()}
  onsubmit={() => rateLimits.submitRateLimitForm()}
>
  <div class="form-grid">
    <FormField id="rate-limit-scope" label="Scope">
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
    </FormField>
    <FormField
      id="rate-limit-subject"
      label={rateLimits.rateLimitSubjectFieldLabel()}
    >
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
    </FormField>
    <FormField id="rate-limit-period" label="Period">
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
    </FormField>
    {#if rateLimits.rateLimitForm.scope === "user_path" && rateLimits.quotaTemplatesEnabled()}
      <label class="per-child-option">
        <input
          type="checkbox"
          bind:checked={rateLimits.rateLimitForm.per_child}
        />
        <span>
          <strong>Apply per direct child</strong>
          <small
            >Each child below {rateLimits.rateLimitForm.subject || "/"} gets independent
            counters. Deeper descendants share their direct child's counters.</small
          >
        </span>
      </label>
    {/if}
    {#if rateLimits.rateLimitForm.period === "custom"}
      <FormField id="rate-limit-period-seconds" label="Period Seconds">
        <input
          id="rate-limit-period-seconds"
          class="form-input"
          type="number"
          min="1"
          step="1"
          bind:value={rateLimits.rateLimitForm.period_seconds}
        />
      </FormField>
    {/if}
    <FormField
      id="rate-limit-max-requests"
      label={rateLimits.rateLimitForm.period === "concurrent"
        ? "Max In-Flight Requests"
        : "Max Requests"}
    >
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
    </FormField>
    {#if rateLimits.rateLimitForm.period !== "concurrent"}
      <FormField id="rate-limit-max-tokens" label="Max Tokens">
        <input
          id="rate-limit-max-tokens"
          class="form-input"
          type="number"
          min="1"
          step="1"
          placeholder="50000"
          bind:value={rateLimits.rateLimitForm.max_tokens}
        />
      </FormField>
    {/if}
  </div>
  <p class="form-hint">
    A user path rule limits the whole subtree; provider and model rules cap all
    traffic routed there and make load balancing skip the target while it is
    saturated. One shared counter per rule. Leave a field empty to skip that
    limit. Token limits require usage tracking.
  </p>
  {#if rateLimits.rateLimitEditing}
    <p class="form-hint">
      Scope, subject, and period identify the rule: changing any of them moves
      the rule to a new key and restarts its live counters.
    </p>
  {/if}
</EditorDialog>

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
</style>
