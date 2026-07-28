<script>
  // Workflow editor modal: scope selection, feature toggles, guardrail steps
  // and the live preview card. Submitting POSTs an immutable version that
  // activates for the selected scope.
  import DialogCloseButton from "$lib/components/atoms/DialogCloseButton.svelte";
  import Modal from "$lib/components/atoms/Modal.svelte";
  import InlineHelpSection from "$lib/components/molecules/InlineHelpSection.svelte";
  import Icon from "$lib/components/atoms/Icon.svelte";
  import { auth } from "$lib/stores/auth.svelte.ts";
  import { router } from "$lib/stores/router.svelte.ts";
  import { runtimeConfig } from "$lib/stores/runtimeConfig.svelte.ts";
  import { workflowsStore as wf } from "./workflows.svelte.js";
  import WorkflowCard from "./WorkflowCard.svelte";


  function close() {
    // Escape must not close the editor under the auth dialog.
    if (auth.dialogOpen) return;
    wf.closeForm();
  }

  function onSubmit(event) {
    event.preventDefault();
    wf.submitForm();
  }
</script>

<Modal open={wf.formOpen} variant="editor" onclose={close}>
  <div
    class="model-editor workflow-editor"
    role="dialog"
    aria-modal="true"
    aria-label="Workflow editor"
  >
    <form class="form" onsubmit={onSubmit}>
      <div class="editor-header">
        <div>
          <InlineHelpSection
            copyId="workflow-help-copy"
            label="workflow help"
            text="Create immutable version. Submitting activates it for the selected scope."
          >
            {#snippet title()}
              <h3>{wf.submitMode() === "save" ? "Edit Workflow" : "Create Workflow"}</h3>
            {/snippet}
          </InlineHelpSection>
        </div>
        <DialogCloseButton label="Close workflow editor" onclick={close} />
      </div>

      {#if wf.formError}
        <p class="form-error" role="alert" aria-live="assertive">{wf.formError}</p>
      {/if}

      <div class="form-grid">
        <div class="form-field">
          <label class="form-field-label" for="workflow-scope-provider">Provider Name</label>
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
        </div>

        {#if wf.form.scope_provider}
          <div class="form-field">
            <label class="form-field-label" for="workflow-scope-model">Model</label>
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
          </div>
        {/if}

        <div class="form-field">
          <label class="form-field-label" for="workflow-name">Name</label>
          <input
            id="workflow-name"
            type="text"
            class="workflow-input"
            placeholder="Optional. Defaults to scope label."
            bind:value={wf.form.name}
          />
        </div>

        <div class="form-field">
          <label class="form-field-label" for="workflow-user-path">User Path</label>
          <input
            id="workflow-user-path"
            type="text"
            class="workflow-input"
            placeholder="team/alpha or /team/alpha"
            bind:value={wf.form.scope_user_path}
          />
        </div>
      </div>

      <p class="form-hint">Leave provider name empty to target all providers and models. Select a provider name without a model to target all models for that configured provider.</p>
      <p class="form-hint">Add a path to scope the workflow to that subtree. Leading slashes are optional and will be normalized; you can enter "team/alpha" or "/team/alpha". Matching checks the deepest user path first, then falls back toward the root.</p>
      <p class="form-hint">If you leave the name empty, the workflow will display as the matched provider/model scope or “All models”.</p>

      <div class="form-field">
        <label class="form-field-label" for="workflow-description">Description</label>
        <textarea
          id="workflow-description"
          placeholder="Optional operator note for why this version exists."
          bind:value={wf.form.description}
        ></textarea>
      </div>

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
                      step="10"
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

      <div class="form-actions">
        <button type="button" class="btn" onclick={close}>Cancel</button>
        <button
          type="submit"
          class="btn btn-primary btn-with-icon workflow-submit-btn"
          disabled={wf.submitting}
        >
          {#if wf.submitMode() === "create"}
            <Icon name="plus" class="form-action-icon" aria-hidden="true" />
          {:else}
            <Icon name="save" class="form-action-icon" aria-hidden="true" />
          {/if}
          <span>{wf.submitting ? wf.submittingLabel() : wf.submitLabel()}</span>
        </button>
      </div>
    </form>
  </div>
</Modal>

<style>
  /* The Modal atom renders the plain editor shell (no wide variant), so
     widen the editor section itself to min(1080px, 100%). */
  .workflow-editor {
    width: min(1080px, 100%);
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

.workflow-editor {
  margin-bottom: 0;
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
