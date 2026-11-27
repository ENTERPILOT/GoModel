<script>
  // One expandable request-log entry: collapsed summary row plus, once opened,
  // the workflow chart, request/response tabs and metadata strip.
  //
  // The expansion is Svelte-controlled (the <details> stays open and the
  // summary click is intercepted): a native <details> toggle cannot animate
  // its close, and the CSS ::details-content transition needs
  // interpolate-size, which Firefox lacks.
  //
  // The panel fades and lifts rather than sliding its height open. A height
  // slide measures the target height once, when the transition starts — but
  // opening also kicks off the /admin/request-logs/detail fetch, so the real bodies
  // land mid-animation and the row slid to a stale height and then snapped to
  // the true one, reading as two animations. Opacity + transform is immune to
  // late-arriving content and stays off the layout path.
  import { fly } from "svelte/transition";
  import { motionDuration } from "$lib/utils/motion.js";
  import WorkflowChart from "$pages/workflows/WorkflowChart.svelte";
  import { workflowRequestLogChart } from "$pages/workflows/workflowChartLogic.js";
  import RequestEntryMetadata from "./RequestEntryMetadata.svelte";
  import RequestEntrySummary from "./RequestEntrySummary.svelte";
  import RequestLogPaneTabs from "./RequestLogPaneTabs.svelte";
  import { requestList } from "./requestList.svelte.js";
  import { requestWorkflows } from "./request-workflows.svelte.js";
  import { conversationDrawer } from "./conversationDrawer.svelte.js";
  import { usagePage } from "../usage/usage.svelte.js";
  import { extractRequestPromptTextSegments } from "./conversation-helpers.js";
  import { requestLogPanes } from "./request-log-logic.js";

  // `thread` is set on session-thread head rows (grouped mode): the summary
  // then renders the expander + count badge. { count, expanded, ontoggle }.
  let { entry, thread = null, hidePath = false } = $props();

  const expanded = $derived(requestList.isRequestEntryExpanded(entry));
  const interactionsOpen = $derived(
    conversationDrawer.conversationOpen &&
      String(conversationDrawer.conversationAnchorID || "") ===
        String(entry.id || ""),
  );
  const panes = $derived(
    expanded ? requestLogPanes(entry, extractRequestPromptTextSegments) : [],
  );

  // Workflow pipeline chart; the resolved version comes from the Requests-page
  // version cache filled by prefetchRequestWorkflows.
  const workflowChart = $derived(
    expanded
      ? workflowRequestLogChart(
          entry,
          requestWorkflows.requestEntryWorkflow(entry),
          requestWorkflows.workflowFeatureCaps(),
        )
      : null,
  );

  function toggleExpanded() {
    requestList.toggleRequestEntryExpanded(entry);
  }
</script>


<details
  class="request-log-entry"
  class:request-log-entry-interactions-open={interactionsOpen}
  open
>
  <RequestEntrySummary
    {entry}
    {thread}
    {expanded}
    {hidePath}
    onactivate={toggleExpanded}
    onusage={entry.session_id
      ? () => usagePage.filterBySession(entry.session_id, entry.user_path)
      : null}
  />
  {#if expanded}
    <div
      class="request-log-entry-details"
      transition:fly={{ y: -6, duration: motionDuration(150) }}
    >
      {#if workflowChart}
        <WorkflowChart chart={workflowChart} />
      {/if}
      <RequestLogPaneTabs {entry} {panes} />
      <RequestEntryMetadata {entry} />
    </div>
  {/if}

</details>

<style>
  .request-log-entry {
    border: 1px solid var(--border);
    border-radius: var(--radius-panel);
    background: var(--bg);
    overflow: hidden;
    transition:
      background 0.12s ease-out,

      border-color 0.12s ease-out;
  }

  .request-log-entry-interactions-open {
    border-width: 2px;
    border-color: color-mix(
      in srgb,
      var(--prompt-cache-color) 38%,
      var(--border)
    );
  }

  .request-log-entry-details {
    border-top: 1px solid var(--border);
    padding: 12px;
    background: var(--bg-surface);
    overflow: hidden;
  }

</style>
