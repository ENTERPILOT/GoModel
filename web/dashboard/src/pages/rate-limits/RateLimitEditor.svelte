<script>
  // Rate limit create/edit modal (EditorDialog shell). Shared by the Rate
  // Limits page and the Models page inspector (mount it alongside
  // RateLimitInspector). Entirely driven by the rateLimits store.
  import EditorDialog from "$lib/components/organisms/EditorDialog.svelte";
  import FormField from "$lib/components/molecules/FormField.svelte";
  import { rateLimits } from "./rateLimits.svelte.js";
  import * as m from "$lib/paraglide/messages.js";
</script>

<!-- novalidate: validation lives in rateLimitFormPayload(), which surfaces
     the store's error copy; native constraint checks (e.g. a 0 typed into a
     min=1 number field) would block the submit before it runs. -->
<EditorDialog
  open={rateLimits.rateLimitFormOpen}
  title={rateLimits.rateLimitEditing ? m.rate_limits_edit_title() : m.rate_limits_create()}
  ariaLabel={m.rate_limits_editor()}
  error={rateLimits.rateLimitFormError}
  submitting={rateLimits.rateLimitFormSubmitting}
  submitLabel={m.rate_limits_save()}
  dialogClass="budget-editor"
  novalidate
  onclose={() => rateLimits.closeRateLimitForm()}
  onsubmit={() => rateLimits.submitRateLimitForm()}
>
  <div class="form-grid">
    <FormField id="rate-limit-scope" label={m.rate_limits_scope()}>
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
    <FormField id="rate-limit-period" label={m.rate_limits_period()}>
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
    {#if rateLimits.rateLimitForm.period === "custom"}
      <FormField id="rate-limit-period-seconds" label={m.rate_limits_period_seconds()}>
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
        ? m.rate_limits_max_in_flight()
        : m.rate_limits_max_requests()}
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
      <FormField id="rate-limit-max-tokens" label={m.rate_limits_max_tokens()}>
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
    {m.rate_limits_editor_help()}
  </p>
  {#if rateLimits.rateLimitEditing}
    <p class="form-hint">
      {m.rate_limits_edit_help()}
    </p>
  {/if}
</EditorDialog>
