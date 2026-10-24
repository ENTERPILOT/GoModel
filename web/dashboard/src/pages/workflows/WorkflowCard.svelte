<script>
  import * as m from "$lib/paraglide/messages.js";
  // A single workflow card: head, description, pipeline chart (whose
  // Guardrails nodes expand the step flow) and (list mode only) the
  // deactivate/edit footer. `preview` renders the
  // footer-less live preview card used inside the editor.
  import Icon from "$lib/components/atoms/Icon.svelte";
  import TableActionButton from "$lib/components/atoms/TableActionButton.svelte";
  import { timezone } from "$lib/stores/timezone.svelte.js";
  import { workflowsStore as wf } from "./workflows.svelte.js";
  import WorkflowChart from "./WorkflowChart.svelte";
  import {
    workflowScopeTypeLabel,
    workflowScopeLabel,
    workflowScopeBadgeVisible,
    workflowDisplayName,
    canDeactivateWorkflow,
    shortHash,
  } from "./workflowsLogic.js";
  import { workflowChart } from "./workflowChartLogic.js";
  import { Pencil } from "lucide";

  let { workflow, preview = false } = $props();

  const caps = $derived(wf.featureCaps());
  const displayName = $derived(workflowDisplayName(workflow));
  const chart = $derived(workflowChart(workflow, caps, wf.guardrailRefs));
</script>

<article class="workflow-card" class:workflow-preview-card={preview}>
  <div class="workflow-card-head">
    <div class="workflow-card-title">
      <h3>{displayName}</h3>
      <span class="workflow-card-scope-type">{workflowScopeTypeLabel(workflow)}</span>
    </div>
    {#if workflowScopeBadgeVisible(workflow)}
      <div class="workflow-card-badges">
        <span class="provider-badge">{workflowScopeLabel(workflow)}</span>
      </div>
    {/if}
  </div>

  {#if workflow.description}
    <p class="workflow-card-description">{workflow.description}</p>
  {/if}

  <WorkflowChart {chart} />


  {#if !preview}
    <div class="workflow-card-footer">
      <div class="alias-actions-cell">
        <button
          type="button"
          class="table-action-btn table-action-btn-danger"
          disabled={wf.deactivatingID === workflow.id || !canDeactivateWorkflow(workflow)}
          aria-label={m.workflows_deactivate_action({ name: displayName })}
          title={canDeactivateWorkflow(workflow)
            ? m.workflows_deactivate_active()
            : m.workflows_global_no_deactivate()}
          onclick={() => wf.deactivate(workflow)}
        >
          {wf.deactivatingID === workflow.id ? m.workflows_deactivating() : m.workflows_deactivate()}
        </button>
        <TableActionButton
          label={m.workflows_edit_action({ name: displayName })}
          class="table-icon-btn"
          onclick={() => wf.openCreate(workflow)}
        >
          <Icon icon={Pencil} class="table-icon-svg" />
        </TableActionButton>
      </div>
      <div class="workflow-card-meta workflow-card-meta-footer">
        <span class="provider-badge mono">{m.workflows_version({ version: workflow.version })}</span>
        <span class="provider-badge mono">
          {m.workflows_created_at({ date: timezone.formatTimestamp(workflow.created_at) })}
        </span>
        <span class="provider-badge mono">{m.workflows_hash({ hash: shortHash(workflow.workflow_hash) })}</span>
      </div>
    </div>
  {/if}
</article>

<style>
  .workflow-card {
    display: flex;
    flex-direction: column;
    gap: 16px;
    min-width: 0;
    max-width: 100%;
    background: var(--bg-surface);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    padding: var(--space-card);
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

  /* Name first, scope type after it on the same baseline in a lighter,
     smaller face; the pair wraps only when the name is long. */
  .workflow-card-title {
    display: flex;
    flex-wrap: wrap;
    align-items: baseline;
    gap: 4px 10px;
    min-width: 0;
  }

  .workflow-card-head :global(h3) {
    font-size: 18px;
    font-weight: 700;
    margin: 0;
  }

  .workflow-card-scope-type {
    color: var(--text-muted);
    font-size: 13px;
    font-weight: 500;
    text-transform: uppercase;
    letter-spacing: 0.04em;
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


  @media (max-width: 768px) {
    .workflow-card-head, .workflow-card-footer, .workflow-card-badges, .workflow-card-meta {
        flex-direction: column;
        align-items: flex-start;
      }
  }
</style>
