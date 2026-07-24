<script>
  // Workflow pipeline visualization.
  // Renders a chart contract object built by workflowChartLogic.js.
  import { createCopyState } from "$lib/utils/clipboard.svelte.js";

  let { chart = {} } = $props();

  const copyState = createCopyState({ logPrefix: "Failed to copy workflow ID:" });

  // Reset the copied/error feedback whenever the chart points at another
  // workflow id.
  $effect(() => {
    void chart.workflowID;
    copyState.reset();
  });

  function copyTitle() {
    if (copyState.error) return "Unable to copy workflow ID";
    if (copyState.copied) return "Workflow ID copied";
    return "Copy workflow ID";
  }

  function copyAriaLabel() {
    if (!chart.workflowID) return "Copy workflow ID";
    if (copyState.error) return "Unable to copy workflow ID " + chart.workflowID;
    if (copyState.copied) return "Workflow ID copied " + chart.workflowID;
    return "Copy workflow ID " + chart.workflowID;
  }

  async function copyWorkflowID(event) {
    event.preventDefault();
    if (!chart.workflowID) {
      return;
    }
    await copyState.copy(chart.workflowID);
  }
</script>

<div class="workflow-pipeline" class:workflow-pipeline-has-meta={chart.workflowID}>
  {#if chart.workflowID}
    <button
      type="button"
      class="workflow-pipeline-meta workflow-pipeline-meta-copy mono"
      class:workflow-pipeline-meta-copied={copyState.copied}
      class:workflow-pipeline-meta-error={copyState.error}
      title={copyTitle()}
      aria-label={copyAriaLabel()}
      onclick={copyWorkflowID}
    >
      <span class="workflow-pipeline-meta-label">id:</span>
      <span class="workflow-pipeline-meta-placeholder">...</span>
      <span class="workflow-pipeline-meta-value">{chart.workflowID}</span>
      <span class="workflow-pipeline-meta-icon" aria-hidden="true">
        <svg viewBox="0 0 24 24"><rect x="9" y="9" width="13" height="13" rx="2"/><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/></svg>
      </span>
    </button>
  {/if}
  <div class="workflow-pipeline-row">
    <div class="workflow-node workflow-node-endpoint">
      <div class="workflow-node-icon workflow-node-icon-endpoint">
        <svg viewBox="0 0 24 24"><circle cx="12" cy="7" r="4"/><path d="M4 21v-1a8 8 0 0 1 16 0v1"/></svg>
      </div>
      <span class="workflow-node-label">Client</span>
    </div>

    <div class="workflow-conn"></div>
    <div class="workflow-node workflow-node-feature workflow-node-auth {chart.authNodeClass || ''}">
      <div class="workflow-node-icon">
        <svg viewBox="0 0 24 24"><ellipse cx="12" cy="5" rx="9" ry="3"/><path d="M21 12c0 1.66-4 3-9 3s-9-1.34-9-3"/><path d="M3 5v14c0 1.66 4 3 9 3s9-1.34 9-3V5"/></svg>
      </div>
      <span class="workflow-node-label">Auth</span>
      {#if chart.authNodeSublabel}
        <span class="workflow-node-sub">{chart.authNodeSublabel}</span>
      {/if}
    </div>

    {#if chart.showCache}
      <div class="workflow-conn {chart.cacheConnClass || ''}"></div>
      <div class="workflow-node workflow-node-feature workflow-node-cache {chart.cacheNodeClass || ''}">
        <div class="workflow-node-icon">
          <svg viewBox="0 0 24 24"><ellipse cx="12" cy="5" rx="9" ry="3"/><path d="M21 12c0 1.66-4 3-9 3s-9-1.34-9-3"/><path d="M3 5v14c0 1.66 4 3 9 3s9-1.34 9-3V5"/></svg>
        </div>
        <span class="workflow-node-label">Cache</span>
        {#if chart.cacheStatusLabel}
          <span class="workflow-node-badge">{chart.cacheStatusLabel}</span>
        {/if}
      </div>
    {/if}

    {#if chart.showBudget}
      <div class="workflow-conn"></div>
      <div class="workflow-node workflow-node-feature workflow-node-budget {chart.budgetNodeClass || ''}">
        <div class="workflow-node-icon">
          <svg viewBox="0 0 24 24"><path d="M19 7V4a1 1 0 0 0-1-1H5a2 2 0 0 0 0 4h15a1 1 0 0 1 1 1v4h-3a2 2 0 0 0 0 4h3a1 1 0 0 0 1-1v-2a1 1 0 0 0-1-1"/><path d="M3 5v14a2 2 0 0 0 2 2h15a1 1 0 0 0 1-1v-4"/></svg>
        </div>
        <span class="workflow-node-label">Budget</span>
        {#if chart.budgetStatusLabel}
          <span class="workflow-node-badge">{chart.budgetStatusLabel}</span>
        {/if}
      </div>
    {/if}

    {#if chart.showGuardrails}
      <div class="workflow-conn"></div>
      <div class="workflow-node workflow-node-feature workflow-node-guardrails">
        <div class="workflow-node-icon">
          <svg viewBox="0 0 24 24"><path d="M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10z"/></svg>
        </div>
        <span class="workflow-node-label">Guardrails</span>
        {#if chart.guardrailLabel}
          <span class="workflow-node-sub">{chart.guardrailLabel}</span>
        {/if}
      </div>
    {/if}

    <div class="workflow-conn {chart.aiConnClass || ''}"></div>

    <div class="workflow-node workflow-node-ai {chart.aiNodeClass || ''}">
      <span class="workflow-node-label">{chart.aiLabel}</span>
      {#if chart.aiSublabel}
        <span class="workflow-node-sub">{chart.aiSublabel}</span>
      {/if}
    </div>

    {#if chart.showFailover}
      <div class="workflow-conn {chart.failoverConnClass || ''}"></div>
      <div class="workflow-node workflow-node-feature workflow-node-failover {chart.failoverNodeClass || ''}">
        <div class="workflow-node-icon">
          <svg viewBox="0 0 24 24"><path d="M17 3h4v4"/><path d="M7 21H3v-4"/><path d="M21 3l-7 7"/><path d="M3 21l7-7"/></svg>
        </div>
        <span class="workflow-node-label">Failover</span>
        {#if chart.failoverStatusLabel}
          <span class="workflow-node-badge">{chart.failoverStatusLabel}</span>
        {/if}
        {#if chart.failoverTargetLabel}
          <span class="workflow-node-sub">{chart.failoverTargetLabel}</span>
        {/if}
      </div>
    {/if}

    <div class="workflow-conn {chart.responseConnClass || ''}"></div>
    <div class="workflow-node workflow-node-endpoint {chart.responseNodeClass || ''}">
      <div class="workflow-node-icon workflow-node-icon-endpoint">
        <svg viewBox="0 0 24 24"><path d="M22 11.08V12a10 10 0 1 1-5.93-9.14"/><polyline points="22 4 12 14.01 9 11.01"/></svg>
      </div>
      <span class="workflow-node-label">Response</span>
      {#if chart.responseNodeSublabel}
        <span class="workflow-node-sub">{chart.responseNodeSublabel}</span>
      {/if}
    </div>
  </div>

  {#if chart.showAsync}
    <div class="workflow-async-section">
      <div class="workflow-async-row">
        {#if chart.showUsage}
          <div class="workflow-node workflow-node-feature workflow-node-async workflow-node-async-usage {chart.usageNodeClass || ''}">
            <div class="workflow-node-icon">
              <svg viewBox="0 0 24 24"><line x1="18" y1="20" x2="18" y2="10"/><line x1="12" y1="20" x2="12" y2="4"/><line x1="6" y1="20" x2="6" y2="14"/></svg>
            </div>
            <span class="workflow-node-label">Usage</span>
          </div>
        {/if}
        {#if chart.showUsage && chart.showAudit}
          <div class="workflow-conn workflow-conn-async"></div>
        {/if}
        {#if chart.showAudit}
          <div class="workflow-node workflow-node-feature workflow-node-async workflow-node-async-audit {chart.auditNodeClass || ''}">
            <div class="workflow-node-icon">
              <svg viewBox="0 0 24 24"><path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z"/><polyline points="14 2 14 8 20 8"/><line x1="16" y1="13" x2="8" y2="13"/><line x1="16" y1="17" x2="8" y2="17"/></svg>
          </div>
            <span class="workflow-node-label">Audit Log</span>
          </div>
        {/if}
      </div>
      <div class="workflow-async-turn"></div>
      <span class="workflow-async-label">Async</span>
    </div>
  {/if}
</div>
