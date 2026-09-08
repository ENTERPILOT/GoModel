<script>
  // Workflow editor modal (EditorDialog shell): scope selection, feature
  // toggles, per-phase guardrail steps and the live preview card. Submitting
  // POSTs an immutable version that activates for the selected scope.
  import EnabledToggle from "$lib/components/atoms/EnabledToggle.svelte";
  import Icon from "$lib/components/atoms/Icon.svelte";
  import SearchSelect from "$lib/components/molecules/SearchSelect.svelte";
  import EditorDialog from "$lib/components/organisms/EditorDialog.svelte";
  import FormField from "$lib/components/molecules/FormField.svelte";
  import InlineHelpSection from "$lib/components/molecules/InlineHelpSection.svelte";
  import { router } from "$lib/stores/router.svelte.js";
  import { runtimeConfig } from "$lib/stores/runtimeConfig.svelte.js";
  import { workflowsStore as wf } from "./workflows.svelte.js";
  import WorkflowCard from "./WorkflowCard.svelte";
  import { WORKFLOW_PHASES, phaseLabel } from "$lib/utils/pluginPhases.js";
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
    <div class="form-field">
      <InlineHelpSection
        copyId="workflow-scope-help-copy"
        label={m.workflows_scope_help_label()}
        text={m.workflows_scope_help()}
      >
        {#snippet title()}
          <label class="form-field-label" for="workflow-scope-provider">{m.workflows_provider_name()}</label>
        {/snippet}
      </InlineHelpSection>
      <select
        id="workflow-scope-provider"
        class="form-select workflow-input"
        aria-describedby="workflow-scope-help-copy"
        bind:value={wf.form.scope_provider}
        onchange={(event) => wf.setProvider(event.currentTarget.value)}
        data-modal-autofocus
      >
        <option value="">{m.workflows_all_scope()}</option>
        {#each wf.providerOptions() as providerName (providerName)}
          <option value={providerName}>{providerName}</option>
        {/each}
      </select>
    </div>

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

    <div class="form-field">
      <InlineHelpSection
        copyId="workflow-name-help-copy"
        label={m.workflows_name_help_label()}
        text={m.workflows_name_help()}
      >
        {#snippet title()}
          <label class="form-field-label" for="workflow-name">{m.workflows_name()}</label>
        {/snippet}
      </InlineHelpSection>
      <input
        id="workflow-name"
        type="text"
        class="workflow-input"
        placeholder={m.workflows_name_placeholder()}
        aria-describedby="workflow-name-help-copy"
        bind:value={wf.form.name}
      />
    </div>

    <div class="form-field">
      <InlineHelpSection
        copyId="workflow-user-path-help-copy"
        label={m.workflows_path_help_label()}
        text={m.workflows_path_help()}
      >
        {#snippet title()}
          <label class="form-field-label" for="workflow-user-path">{m.workflows_user_path()}</label>
        {/snippet}
      </InlineHelpSection>
      <input
        id="workflow-user-path"
        type="text"
        class="workflow-input"
        placeholder="team/alpha or /team/alpha"
        aria-describedby="workflow-user-path-help-copy"
        bind:value={wf.form.scope_user_path}
      />
    </div>
  </div>

  <FormField id="workflow-description" label={m.workflows_description()}>
    <textarea
      id="workflow-description"
      placeholder={m.workflows_description_placeholder()}
      bind:value={wf.form.description}
    ></textarea>
  </FormField>

  <div class="workflow-feature-toggles" role="group" aria-label={m.workflows_features()}>
    {#if runtimeConfig.cacheVisible()}
      <EnabledToggle
        enabled={wf.form.features.cache}
        label={m.workflows_cache()}
        text={m.workflows_cache()}
        onclick={() => (wf.form.features.cache = !wf.form.features.cache)}
      />
    {/if}
    {#if runtimeConfig.auditVisible()}
      <EnabledToggle
        enabled={wf.form.features.audit}
        label={m.workflows_audit()}
        text={m.workflows_audit()}
        onclick={() => (wf.form.features.audit = !wf.form.features.audit)}
      />
    {/if}
    {#if runtimeConfig.usageVisible()}
      <EnabledToggle
        enabled={wf.form.features.usage}
        label={m.workflows_usage()}
        text={m.workflows_usage()}
        onclick={() => (wf.form.features.usage = !wf.form.features.usage)}
      />
    {/if}
    {#if runtimeConfig.budgetsVisible()}
      <EnabledToggle
        enabled={wf.form.features.budget}
        label={m.workflows_budget()}
        text={m.workflows_budget()}
        onclick={() => (wf.form.features.budget = !wf.form.features.budget)}
      />
    {/if}
    {#if runtimeConfig.guardrailsVisible()}
      <EnabledToggle
        enabled={wf.form.features.guardrails}
        label={m.workflows_guardrails()}
        text={m.workflows_guardrails()}
        onclick={() => (wf.form.features.guardrails = !wf.form.features.guardrails)}
      />
    {/if}
    {#if wf.failoverVisible()}
      <EnabledToggle
        enabled={wf.form.features.failover}
        label={m.workflows_failover()}
        text={m.workflows_failover()}
        onclick={() => (wf.form.features.failover = !wf.form.features.failover)}
      />
    {/if}
  </div>

  <div class="workflow-preview">
    <div class="workflow-section-head">
      <h4>{m.workflows_preview()}</h4>
    </div>
    <WorkflowCard workflow={wf.preview()} preview />
  </div>

  {#if wf.form.features.guardrails && runtimeConfig.guardrailsVisible()}
    <div class="workflow-guardrail-editor">
      <InlineHelpSection
        copyId="workflow-steps-help-copy"
        label={m.workflows_steps_help_label()}
      >
        {#snippet title()}
          <h4>{m.workflows_guardrail_steps()}</h4>
        {/snippet}
        {#snippet help()}
          {m.workflows_phase_help()} {m.workflows_steps_help()}
        {/snippet}
      </InlineHelpSection>

      {#if wf.guardrailRefs.length === 0}
        <div class="alert alert-warning alert-inline-actions">
          <span>{m.workflows_no_registered_guardrails()}</span>
          <button type="button" class="table-action-btn" onclick={() => router.navigate("guardrails")}>{m.workflows_open_guardrails()}</button>
        </div>
      {/if}

      {#each WORKFLOW_PHASES as phase (phase)}
        {@const phaseName = phaseLabel(phase)}
        {@const phaseRows = wf.form.guardrails.filter((step) => step.phase === phase)}
        <section class="workflow-guardrail-phase" aria-label={m.workflows_guardrail_flow_title({ phase: phaseName })}>
          <div class="workflow-section-head workflow-guardrail-phase-head">
            <h5>{m.workflows_guardrail_flow_title({ phase: phaseName })}</h5>
            <button
              type="button"
              class="table-action-btn workflow-add-btn"
              aria-label={m.workflows_add_phase_guardrail({ phase: phaseName })}
              onclick={() => wf.addGuardrailStep(phase)}
            >
              <Icon icon={Plus} class="table-icon-svg" aria-hidden="true" />
              <span>{m.workflows_add_guardrail()}</span>
            </button>
          </div>

          {#if phaseRows.length > 0}
            <div class="workflow-guardrail-list-editor">
              {#each wf.form.guardrails as step, index (index)}
                {#if step.phase === phase}
                  {@const refOptions = wf.refOptions(phase, step.ref)}
                  <div class="workflow-guardrail-row">
                    <div class="form-field workflow-guardrail-field">
                      <label class="form-field-label" for={"workflow-guardrail-ref-" + index}>{m.workflows_guardrail_reference()}</label>
                      <select
                        class="form-select workflow-input mono"
                        id={"workflow-guardrail-ref-" + index}
                        bind:value={step.ref}
                        aria-label={`${phaseName} ${m.workflows_guardrail_reference()} ${index + 1}`}
                      >
                        <option value="">{m.workflows_select_guardrail()}</option>
                        {#each refOptions as option (option.value)}
                          <option value={option.value}>{option.label}</option>
                        {/each}
                      </select>
                      {#if refOptions.length === 0 && wf.guardrailRefs.length > 0}
                        <small class="form-hint">{m.workflows_no_phase_guardrails({ phase: phaseName })}</small>
                      {/if}
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
                        aria-label={`${phaseName} ${m.workflows_step()} ${index + 1}`}
                      />
                    </div>
                    <button type="button" class="table-action-btn table-action-btn-danger" onclick={() => wf.removeGuardrailStep(index)}>{m.workflows_remove()}</button>
                  </div>
                {/if}
              {/each}
            </div>
          {:else}
            <p class="form-hint">{m.workflows_no_phase_steps({ phase: phaseName })}</p>
          {/if}
        </section>
      {/each}
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
    gap: 16px;
  }

  .workflow-guardrail-editor :global(h4) {
    font-size: 14px;
    font-weight: 700;
  }

  .workflow-guardrail-phase {
    display: flex;
    flex-direction: column;
    gap: 10px;
  }

  .workflow-guardrail-phase-head {
    align-items: center;
  }

  .workflow-guardrail-phase-head h5 {
    font-size: 13px;
    font-weight: 600;
    color: var(--text-muted);
    text-transform: uppercase;
    letter-spacing: 0.04em;
  }

  .workflow-add-btn {
    flex: 0 0 auto;
    white-space: nowrap;
  }

  .workflow-guardrail-list-editor {
    display: flex;
    flex-direction: column;
    gap: 10px;
  }

  .workflow-guardrail-row {
    display: flex;
    align-items: center;
    gap: 12px;
    padding: 10px 12px;
    border: 1px solid var(--border);
    border-radius: 10px;
    background: var(--bg);
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

    .workflow-guardrail-phase-head {
      flex-direction: row;
      align-items: center;
    }

    .workflow-step-input {
      max-width: none;
      width: 100%;
    }

    .workflow-guardrail-field,
    .workflow-guardrail-step-field {
      flex-basis: auto;
      width: 100%;
    }
  }
</style>
