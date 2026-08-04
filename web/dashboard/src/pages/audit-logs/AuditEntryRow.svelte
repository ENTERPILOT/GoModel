<script>
  // One expandable audit-log entry: collapsed summary row plus, once opened,
  // the workflow chart, request/response tabs and metadata strip.
  //
  // The expansion is Svelte-controlled (the <details> stays open and the
  // summary click is intercepted): a native <details> toggle cannot animate
  // its close, and the CSS ::details-content transition needs
  // interpolate-size, which Firefox lacks.
  //
  // The panel fades and lifts rather than sliding its height open. A height
  // slide measures the target height once, when the transition starts — but
  // opening also kicks off the /admin/audit/detail fetch, so the real bodies
  // land mid-animation and the row slid to a stale height and then snapped to
  // the true one, reading as two animations. Opacity + transform is immune to
  // late-arriving content and stays off the layout path.
  import { fly } from "svelte/transition";
  import { motionDuration } from "$lib/utils/motion.js";
  import WorkflowChart from "$pages/workflows/WorkflowChart.svelte";
  import { workflowAuditChart } from "$pages/workflows/workflowChartLogic.js";
  import AuditEntryMetadata from "./AuditEntryMetadata.svelte";
  import AuditEntrySummary from "./AuditEntrySummary.svelte";
  import AuditPaneTabs from "./AuditPaneTabs.svelte";
  import { auditList } from "./auditList.svelte.js";
  import { auditWorkflows } from "./audit-workflows.svelte.js";
  import { conversationDrawer } from "./conversationDrawer.svelte.js";
  import { extractRequestPromptTextSegments } from "./conversation-helpers.js";
  import { auditPanes } from "./audit-logic.js";

  // `thread` is set on session-thread head rows (grouped mode): the summary
  // then renders the expander + count badge. { count, expanded, ontoggle }.
  let { entry, thread = null } = $props();

  const expanded = $derived(auditList.isAuditEntryExpanded(entry));
  const interactionsOpen = $derived(
    conversationDrawer.conversationOpen &&
      String(conversationDrawer.conversationAnchorID || "") ===
        String(entry.id || ""),
  );
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

  function toggleExpanded() {
    auditList.toggleAuditEntryExpanded(entry);
  }
</script>

<details
  class="audit-entry"
  class:audit-entry-interactions-open={interactionsOpen}
  open
>
  <AuditEntrySummary {entry} {thread} {expanded} onactivate={toggleExpanded} />
  {#if expanded}
    <div
      class="audit-entry-details"
      transition:fly={{ y: -6, duration: motionDuration(150) }}
    >
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
    transition:
      background 0.12s ease-out,
      border-color 0.12s ease-out;
  }

  .audit-entry-interactions-open {
    border-color: color-mix(
      in srgb,
      var(--prompt-cache-color) 38%,
      var(--border)
    );
    background: color-mix(
      in srgb,
      var(--prompt-cache-color) 7%,
      var(--bg)
    );
  }

  .audit-entry-details {
    border-top: 1px solid var(--border);
    padding: 12px;
    background: var(--bg-surface);
    overflow: hidden;
  }

  .audit-entry-interactions-open .audit-entry-details {
    border-top-color: color-mix(
      in srgb,
      var(--prompt-cache-color) 24%,
      var(--border)
    );
    background: color-mix(
      in srgb,
      var(--prompt-cache-color) 5%,
      var(--bg-surface)
    );
  }
</style>
