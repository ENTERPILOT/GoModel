<script>
  // One expandable audit-log entry: collapsed summary row plus, once opened,
  // the workflow chart, request/response tabs and metadata strip.
  import WorkflowChart from "$pages/workflows/WorkflowChart.svelte";
  import { workflowAuditChart } from "$pages/workflows/workflowChartLogic.js";
  import AuditEntryMetadata from "./AuditEntryMetadata.svelte";
  import AuditEntrySummary from "./AuditEntrySummary.svelte";
  import AuditPaneTabs from "./AuditPaneTabs.svelte";
  import { auditList } from "./auditList.svelte.js";
  import { auditWorkflows } from "./audit-workflows.svelte.js";
  import { liveLogs } from "./liveLogs.svelte.js";
  import { extractRequestPromptTextSegments } from "./conversation-helpers.js";
  import { auditPanes } from "./audit-logic.js";

  // `thread` is set on session-thread head rows (grouped mode): the summary
  // then renders the expander + count badge. { count, expanded, ontoggle }.
  let { entry, thread = null } = $props();

  const expanded = $derived(auditList.isAuditEntryExpanded(entry));
  const panes = $derived(
    expanded ? auditPanes(entry, extractRequestPromptTextSegments) : [],
  );

  // Workflow pipeline chart; the resolved version comes from the audit-side
  // version cache filled by prefetchAuditWorkflows.
  const workflowChart = $derived(
    expanded
      ? workflowAuditChart(
          entry,
          auditWorkflows.auditEntryWorkflow(entry),
          auditWorkflows.workflowFeatureCaps(),
        )
      : null,
  );

  function onToggle(event) {
    const detailsEl = event && event.currentTarget;
    if (!detailsEl || !detailsEl.open) return;
    auditList.markAuditEntryExpanded(entry);
    if (typeof liveLogs.fetchAuditEntryDetail === "function") {
      liveLogs.fetchAuditEntryDetail(entry);
    }
  }
</script>

<details class="audit-entry" ontoggle={onToggle}>
  <AuditEntrySummary {entry} {thread} />
  {#if expanded}
    <div class="audit-entry-details">
      {#if workflowChart}
        <WorkflowChart chart={workflowChart} />
      {/if}
      <AuditPaneTabs {entry} {panes} />
      <AuditEntryMetadata {entry} />
    </div>
  {/if}
</details>

<style>
  .audit-entry {
    border: 1px solid var(--border);
    border-radius: var(--radius);
    background: var(--bg);
    overflow: hidden;
  }

  .audit-entry-details {
    border-top: 1px solid var(--border);
    padding: 12px;
    background: var(--bg-surface);
    overflow: hidden;
  }
</style>
