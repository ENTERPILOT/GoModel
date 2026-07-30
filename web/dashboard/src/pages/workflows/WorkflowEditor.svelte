<script>
  // Workflow editor modal (EditorDialog shell): scope selection, feature
  // toggles, guardrail steps and the live preview card. Submitting POSTs an
  // immutable version that activates for the selected scope.
  import EditorDialog from "$lib/components/organisms/EditorDialog.svelte";
  import FormField from "$lib/components/molecules/FormField.svelte";
  import InlineHelpSection from "$lib/components/molecules/InlineHelpSection.svelte";
  import { router } from "$lib/stores/router.svelte.js";
  import { runtimeConfig } from "$lib/stores/runtimeConfig.svelte.js";
  import { workflowsStore as wf } from "./workflows.svelte.js";
  import WorkflowCard from "./WorkflowCard.svelte";
</script>

<EditorDialog
  open={wf.formOpen}
  ariaLabel="Workflow editor"
  error={wf.formError}
  submitting={wf.submitting}
  submitLabel={wf.submitLabel()}
  submittingLabel={wf.submittingLabel()}
  submitIcon={wf.submitMode() === "create" ? "plus" : "save"}
  dialogClass="workflow-editor"
  onclose={() => wf.closeForm()}
  onsubmit={() => wf.submitForm()}
>
  {#snippet header()}
    <InlineHelpSection
      copyId="workflow-help-copy"
      label="workflow help"
      text="Create immutable version. Submitting activates it for the selected scope."
    >
      {#snippet title()}
        <h3>{wf.submitMode() === "save" ? "Edit Workflow" : "Create Workflow"}</h3>
      {/snippet}
    </InlineHelpSection>
  {/snippet}

  <div class="form-grid">
    <FormField id="workflow-scope-provider" label="Provider Name">
      <select
        id="workflow-scope-provider"
        class="form-select workflow-input"
        bind:value={wf.form.scope_provider}
        onchange={(event) => wf.setProvider(event.currentTarget.value)}
        data-modal-autofocus
      >
        <option value="">All providers and models</option>
        {#each wf.providerOptions() as providerName (providerName)}
          <option value={providerName}>{providerName}</option>
        {/each}
      </select>
    </FormField>

    {#if wf.form.scope_provider}
      <FormField id="workflow-scope-model" label="Model">
        <select
          id="workflow-scope-model"
          class="form-select workflow-input"
          bind:value={wf.form.scope_model}
        >
          <option value="">All models for provider</option>
          {#each wf.modelOptions(wf.form.scope_provider) as modelID (wf.form.scope_provider + "-" + modelID)}
            <option value={modelID}>{modelID}</option>
          {/each}
        </select>
      </FormField>
    {/if}

    <FormField id="workflow-name" label="Name">
      <input
        id="workflow-name"
        type="text"
        class="workflow-input"
        placeholder="Optional. Defaults to scope label."
        bind:value={wf.form.name}
      />
    </FormField>

    <FormField id="workflow-user-path" label="User Path">
      <input
        id="workflow-user-path"
        type="text"
        class="workflow-input"
        placeholder="team/alpha or /team/alpha"
        bind:value={wf.form.scope_user_path}
      />
    </FormField>
  </div>

  <p class="form-hint">Leave provider name empty to target all providers and models. Select a provider name without a model to target all models for that configured provider.</p>
  <p class="form-hint">Add a path to scope the workflow to that subtree. Leading slashes are optional and will be normalized; you can enter "team/alpha" or "/team/alpha". Matching checks the deepest user path first, then falls back toward the root.</p>
  <p class="form-hint">If you leave the name empty, the workflow will display as the matched provider/model scope or “All models”.</p>

  <FormField id="workflow-description" label="Description">
    <textarea
      id="workflow-description"
      placeholder="Optional operator note for why this version exists."
      bind:value={wf.form.description}
    ></textarea>
  </FormField>

  <div class="workflow-feature-toggles">
    {#if runtimeConfig.cacheVisible()}
      <label class="workflow-feature-toggle">
        <input type="checkbox" bind:checked={wf.form.features.cache} />
        <span>Cache</span>
      </label>
    {/if}
    {#if runtimeConfig.auditVisible()}
      <label class="workflow-feature-toggle">
        <input type="checkbox" bind:checked={wf.form.features.audit} />
        <span>Audit Logs</span>
      </label>
    {/if}
    {#if runtimeConfig.usageVisible()}
      <label class="workflow-feature-toggle">
        <input type="checkbox" bind:checked={wf.form.features.usage} />
        <span>Usage</span>
      </label>
    {/if}
    {#if runtimeConfig.budgetsVisible()}
      <label class="workflow-feature-toggle">
        <input type="checkbox" bind:checked={wf.form.features.budget} />
        <span>Budget</span>
      </label>
    {/if}
    {#if runtimeConfig.guardrailsVisible()}
      <label class="workflow-feature-toggle">
        <input type="checkbox" bind:checked={wf.form.features.guardrails} />
        <span>Guardrails</span>
      </label>
    {/if}
    {#if wf.failoverVisible()}
      <label class="workflow-feature-toggle">
        <input type="checkbox" bind:checked={wf.form.features.failover} />
        <span>Failover</span>
      </label>
    {/if}
  </div>

  <div class="workflow-preview">
    <div class="workflow-section-head">
      <h4>Preview</h4>
      <span class="provider-badge">Live</span>
    </div>
    <WorkflowCard workflow={wf.preview()} preview />
  </div>

  {#if wf.form.features.guardrails && runtimeConfig.guardrailsVisible()}
    <div class="workflow-guardrail-editor">
      <div class="workflow-section-head">
        <div>
          <h4>Guardrail Steps</h4>
          <p class="form-hint">Guardrails in the same numeric step run together. Later steps wait for earlier ones to finish.</p>
        </div>
        <button type="button" class="table-action-btn" onclick={() => wf.addGuardrailStep()}>Add Step</button>
      </div>

      {#if wf.guardrailRefs.length === 0}
        <div class="alert alert-warning alert-inline-actions">
          <span>No named guardrails are currently registered on this deployment. You can still draft a workflow, but guardrail-backed creation may be rejected.</span>
          <button type="button" class="table-action-btn" onclick={() => router.navigate("guardrails")}>Open Guardrails</button>
        </div>
      {/if}

      {#if wf.form.guardrails.length > 0}
        <div class="workflow-guardrail-list-editor">
          {#each wf.form.guardrails as step, index (index)}
            <div class="workflow-guardrail-row">
              <div class="form-field workflow-guardrail-field">
                <label class="form-field-label" for={"workflow-guardrail-ref-" + index}>Guardrail reference</label>
                <input
                  type="text"
                  class="workflow-input"
                  id={"workflow-guardrail-ref-" + index}
                  list="workflow-guardrail-options"
                  placeholder="Guardrail ref"
                  bind:value={step.ref}
                  aria-label={"Guardrail reference " + (index + 1)}
                />
              </div>
              <div class="form-field workflow-guardrail-step-field">
                <label class="form-field-label" for={"workflow-guardrail-step-" + index}>Step</label>
                <input
                  type="number"
                  class="workflow-step-input"
                  id={"workflow-guardrail-step-" + index}
                  min="0"
                  step="1"
                  placeholder="Step"
                  bind:value={step.step}
                  aria-label={"Guardrail step " + (index + 1)}
                />
              </div>
              <button type="button" class="table-action-btn table-action-btn-danger" onclick={() => wf.removeGuardrailStep(index)}>Remove</button>
            </div>
          {/each}
        </div>
      {:else}
        <p class="form-hint">No guardrail steps configured yet.</p>
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

.workflow-input {
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
