<script>
  // Workflow editor modal (EditorDialog shell): scope selection, feature
  // toggles, guardrail steps and the live preview card. Submitting POSTs an
  // immutable version that activates for the selected scope.
  import SearchSelect from "$lib/components/molecules/SearchSelect.svelte";
  import EditorDialog from "$lib/components/organisms/EditorDialog.svelte";
  import FormField from "$lib/components/molecules/FormField.svelte";
  import InlineHelpSection from "$lib/components/molecules/InlineHelpSection.svelte";
  import { router } from "$lib/stores/router.svelte.js";
  import { runtimeConfig } from "$lib/stores/runtimeConfig.svelte.js";
  import { workflowsStore as wf } from "./workflows.svelte.js";
  import WorkflowCard from "./WorkflowCard.svelte";
  import { Plus, Save } from "lucide";
  import * as m from "$lib/paraglide/messages.js";
</script>

<EditorDialog
  open={wf.formOpen}
  ariaLabel={m.workflows_editor()}
  error={wf.formError}
  submitting={wf.submitting}
  submitLabel={wf.submitLabel()}
  submittingLabel={wf.submittingLabel()}
  submitIcon={wf.submitMode() === "create" ? Plus : Save}
  dialogClass="workflow-editor"
  onclose={() => wf.closeForm()}
  onsubmit={() => wf.submitForm()}
>
  {#snippet header()}
    <InlineHelpSection
      copyId="workflow-help-copy"
      label={m.workflows_help_label()}
      text={m.workflows_help()}
    >
      {#snippet title()}
        <h3>{wf.submitMode() === "save" ? m.workflows_edit() : m.workflows_create()}</h3>
      {/snippet}
    </InlineHelpSection>
  {/snippet}

  <div class="form-grid">
    <FormField id="workflow-scope-provider" label={m.workflows_provider_name()}>
      <select
        id="workflow-scope-provider"
        class="form-select workflow-input"
        bind:value={wf.form.scope_provider}
        onchange={(event) => wf.setProvider(event.currentTarget.value)}
        data-modal-autofocus
      >
        <option value="">{m.workflows_all_scope()}</option>
        {#each wf.providerOptions() as providerName (providerName)}
          <option value={providerName}>{providerName}</option>
        {/each}
      </select>
    </FormField>

    {#if wf.form.scope_provider}
      <FormField id="workflow-scope-model" label={m.workflows_model()}>
        <SearchSelect
          id="workflow-scope-model"
          class="workflow-input"
          options={[
            { value: "", label: m.workflows_all_provider_models() },
            ...wf.modelOptions(wf.form.scope_provider),
          ]}
          bind:value={wf.form.scope_model}
          placeholder={m.workflows_all_provider_models()}
          searchPlaceholder={m.workflows_model_search_placeholder()}
          ariaLabel={m.workflows_model()}
          mono
        />
      </FormField>
    {/if}

    <FormField id="workflow-name" label={m.workflows_name()}>
      <input
        id="workflow-name"
        type="text"
        class="workflow-input"
        placeholder={m.workflows_name_placeholder()}
        bind:value={wf.form.name}
      />
    </FormField>

    <FormField id="workflow-user-path" label={m.workflows_user_path()}>
      <input
        id="workflow-user-path"
        type="text"
        class="workflow-input"
        placeholder="team/alpha or /team/alpha"
        bind:value={wf.form.scope_user_path}
      />
    </FormField>
  </div>

  <p class="form-hint">{m.workflows_scope_help()}</p>
  <p class="form-hint">{m.workflows_path_help()}</p>
  <p class="form-hint">{m.workflows_name_help()}</p>

  <FormField id="workflow-description" label={m.workflows_description()}>
    <textarea
      id="workflow-description"
      placeholder={m.workflows_description_placeholder()}
      bind:value={wf.form.description}
    ></textarea>
  </FormField>

  <div class="workflow-feature-toggles">
    {#if runtimeConfig.cacheVisible()}
      <label class="workflow-feature-toggle">
        <input type="checkbox" bind:checked={wf.form.features.cache} />
        <span>{m.workflows_cache()}</span>
      </label>
    {/if}
    {#if runtimeConfig.auditVisible()}
      <label class="workflow-feature-toggle">
        <input type="checkbox" bind:checked={wf.form.features.audit} />
        <span>{m.workflows_audit()}</span>
      </label>
    {/if}
    {#if runtimeConfig.usageVisible()}
      <label class="workflow-feature-toggle">
        <input type="checkbox" bind:checked={wf.form.features.usage} />
        <span>{m.workflows_usage()}</span>
      </label>
    {/if}
    {#if runtimeConfig.budgetsVisible()}
      <label class="workflow-feature-toggle">
        <input type="checkbox" bind:checked={wf.form.features.budget} />
        <span>{m.workflows_budget()}</span>
      </label>
    {/if}
    {#if runtimeConfig.guardrailsVisible()}
      <label class="workflow-feature-toggle">
        <input type="checkbox" bind:checked={wf.form.features.guardrails} />
        <span>{m.workflows_guardrails()}</span>
      </label>
    {/if}
    {#if wf.failoverVisible()}
      <label class="workflow-feature-toggle">
        <input type="checkbox" bind:checked={wf.form.features.failover} />
        <span>{m.workflows_failover()}</span>
      </label>
    {/if}
  </div>

  <div class="workflow-preview">
    <div class="workflow-section-head">
      <h4>{m.workflows_preview()}</h4>
      <span class="provider-badge">{m.workflows_live()}</span>
    </div>
    <WorkflowCard workflow={wf.preview()} preview />
  </div>

  {#if wf.form.features.guardrails && runtimeConfig.guardrailsVisible()}
    <div class="workflow-guardrail-editor">
      <div class="workflow-section-head">
        <div>
          <h4>{m.workflows_guardrail_steps()}</h4>
          <p class="form-hint">{m.workflows_steps_help()}</p>
        </div>
        <button type="button" class="table-action-btn" onclick={() => wf.addGuardrailStep()}>{m.workflows_add_step()}</button>
      </div>

      {#if wf.guardrailRefs.length === 0}
        <div class="alert alert-warning alert-inline-actions">
          <span>{m.workflows_no_registered_guardrails()}</span>
          <button type="button" class="table-action-btn" onclick={() => router.navigate("guardrails")}>{m.workflows_open_guardrails()}</button>
        </div>
      {/if}

      {#if wf.form.guardrails.length > 0}
        <div class="workflow-guardrail-list-editor">
          {#each wf.form.guardrails as step, index (index)}
            <div class="workflow-guardrail-row">
              <div class="form-field workflow-guardrail-field">
                <label class="form-field-label" for={"workflow-guardrail-ref-" + index}>{m.workflows_guardrail_reference()}</label>
                <input
                  type="text"
                  class="workflow-input"
                  id={"workflow-guardrail-ref-" + index}
                  list="workflow-guardrail-options"
                  placeholder={m.workflows_guardrail_reference()}
                  bind:value={step.ref}
                  aria-label={`${m.workflows_guardrail_reference()} ${index + 1}`}
                />
              </div>
              <div class="form-field workflow-guardrail-step-field">
                <label class="form-field-label" for={"workflow-guardrail-step-" + index}>{m.workflows_step()}</label>
                <input
                  type="number"
                  class="workflow-step-input"
                  id={"workflow-guardrail-step-" + index}
                  min="0"
                  step="1"
                  placeholder={m.workflows_step()}
                  bind:value={step.step}
                  aria-label={`${m.workflows_guardrail_steps()} ${index + 1}`}
                />
              </div>
              <button type="button" class="table-action-btn table-action-btn-danger" onclick={() => wf.removeGuardrailStep(index)}>{m.workflows_remove()}</button>
            </div>
          {/each}
        </div>
      {:else}
        <p class="form-hint">{m.workflows_no_steps()}</p>
      {/if}
    </div>
  {/if}
</EditorDialog>

<style>
  /* EditorDialog renders the plain editor shell (no wide variant), so widen
     the dialog itself via the dialogClass hook. That div lives in
     EditorDialog's markup where this component's scope hash cannot match, so
     the rule is :global — compounded with .model-editor to keep outranking
     the shared base rule in forms.css. */
  :global(.model-editor.workflow-editor) {
    width: min(1080px, 100%);
    margin-bottom: 0;
  }
.alert-inline-actions {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  flex-wrap: wrap;
}

.workflow-guardrail-editor {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.workflow-guardrail-list-editor {
  display: flex;
  flex-direction: column;
  gap: 10px;
}

.workflow-guardrail-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding: 10px 12px;
  border: 1px solid var(--border);
  border-radius: 10px;
  background: var(--bg);
}

.workflow-guardrail-row {
  justify-content: stretch;
}

.workflow-guardrail-field {
  flex: 1 1 auto;
  min-width: 0;
}

.workflow-guardrail-step-field {
  flex: 0 0 120px;
}

.workflow-input,
:global(.search-select.workflow-input) {
  max-width: none;
  width: 100%;
}

.workflow-step-input {
  max-width: 120px;
}

@media (max-width: 768px) {
  .workflow-guardrail-row {
      flex-direction: column;
      align-items: flex-start;
    }

  .workflow-step-input {
      max-width: none;
      width: 100%;
    }

  .workflow-guardrail-field, .workflow-guardrail-step-field {
      flex-basis: auto;
      width: 100%;
    }
}
</style>
