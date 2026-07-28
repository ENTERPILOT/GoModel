<script>
  // A single workflow card: head, description, pipeline chart, guardrails and
  // (list mode only) the deactivate/edit footer. `preview` renders the
  // footer-less live preview card used inside the editor.
  import Icon from "$lib/components/atoms/Icon.svelte";
  import TableActionButton from "$lib/components/atoms/TableActionButton.svelte";
  import { timezone } from "$lib/stores/timezone.svelte.ts";
  import { runtimeConfig } from "$lib/stores/runtimeConfig.svelte.ts";
  import { workflowsStore as wf } from "./workflows.svelte.js";
  import WorkflowChart from "./WorkflowChart.svelte";
  import {
    workflowScopeTypeLabel,
    workflowScopeLabel,
    workflowDisplayName,
    workflowFailoverLabel,
    workflowGuardrails,
    canDeactivateWorkflow,
    shortHash,
  } from "./workflowsLogic.js";
  import { workflowChart } from "./workflowChartLogic.js";

  let { workflow, preview = false } = $props();

  const caps = $derived(wf.featureCaps());
  const displayName = $derived(workflowDisplayName(workflow));
  const guardrails = $derived(workflowGuardrails(workflow, caps));
  const chart = $derived(workflowChart(workflow, caps));
  const guardrailKeyPrefix = $derived(
    preview ? "draft-workflow-preview-guardrail-" : workflow.id + "-guardrail-",
  );
</script>

<article class="workflow-card" class:workflow-preview-card={preview}>
  <div class="workflow-card-head">
    <div>
      <p class="form-kicker">{workflowScopeTypeLabel(workflow)}</p>
      <h3>{displayName}</h3>
    </div>
    <div class="workflow-card-badges">
      <span class="provider-badge">{workflowScopeLabel(workflow)}</span>
    </div>
  </div>

  {#if workflow.description}
    <p class="workflow-card-description">{workflow.description}</p>
  {/if}
  {#if wf.failoverVisible()}
    <p class="form-hint">Failover: {workflowFailoverLabel(workflow, caps)}</p>
  {/if}

  <WorkflowChart {chart} />

  {#if runtimeConfig.guardrailsVisible()}
    <div class="workflow-guardrails">
      <div class="workflow-section-head">
        <h4>Guardrails</h4>
        <span class="provider-badge">
          {guardrails.length ? guardrails.length + " steps" : "None"}
        </span>
      </div>
      {#if guardrails.length > 0}
        <div class="workflow-guardrail-list">
          {#each guardrails as step, stepIndex (guardrailKeyPrefix + stepIndex)}
            <div class="workflow-guardrail-item">
              <span class="mono font-size-md">{step.ref}</span>
              <span class="provider-badge">step {step.step}</span>
            </div>
          {/each}
        </div>
      {:else}
        <p class="form-hint">No guardrails configured for this workflow.</p>
      {/if}
    </div>
  {/if}

  {#if !preview}
    <div class="workflow-card-footer">
      <div class="alias-actions-cell">
        <button
          type="button"
          class="table-action-btn table-action-btn-danger"
          disabled={wf.deactivatingID === workflow.id || !canDeactivateWorkflow(workflow)}
          aria-label={"Deactivate workflow " + displayName}
          title={canDeactivateWorkflow(workflow)
            ? "Deactivate active workflow"
            : "The global workflow cannot be deactivated."}
          onclick={() => wf.deactivate(workflow)}
        >
          {wf.deactivatingID === workflow.id ? "Deactivating..." : "Deactivate"}
        </button>
        <TableActionButton
          label={"Edit workflow " + displayName}
          class="table-icon-btn"
          onclick={() => wf.openCreate(workflow)}
        >
          <Icon name="pencil" class="table-icon-svg" />
        </TableActionButton>
      </div>
      <div class="workflow-card-meta workflow-card-meta-footer">
        <span class="provider-badge mono">version: v{workflow.version}</span>
        <span class="provider-badge mono">
          created: {timezone.formatTimestamp(workflow.created_at)}
        </span>
        <span class="provider-badge mono">hash: {shortHash(workflow.workflow_hash)}</span>
      </div>
    </div>
  {/if}
</article>

<style>
  .workflow-card {
    display: flex;
    flex-direction: column;
    gap: 16px;
    background: var(--bg-surface);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    padding: 20px;
  }

  .workflow-card-head {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: 12px;
  }

  .workflow-card-footer {
    display: flex;
    flex-direction: column;
    align-items: stretch;
    gap: 10px;
  }

  .workflow-card-head :global(h3) {
    font-size: 18px;
    font-weight: 700;
  }

  .workflow-card-badges, .workflow-card-meta {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
    justify-content: flex-end;
  }

  .workflow-card-meta-footer {
    justify-content: flex-start;
  }

  .workflow-card-footer :global(.alias-actions-cell) {
    align-self: flex-end;
  }

  .workflow-card-description {
    color: var(--text-muted);
    font-size: 14px;
  }

  .workflow-guardrails {
    display: flex;
    flex-direction: column;
    gap: 12px;
  }

  .workflow-guardrail-list {
    display: flex;
    flex-direction: column;
    gap: 10px;
  }

  .workflow-guardrail-item {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    padding: 10px 12px;
    border: 1px solid var(--border);
    border-radius: 10px;
    background: var(--bg);
  }

  @media (max-width: 768px) {
    .workflow-card-head, .workflow-card-footer, .workflow-card-badges, .workflow-card-meta, .workflow-guardrail-item {
        flex-direction: column;
        align-items: flex-start;
      }
  }
</style>
