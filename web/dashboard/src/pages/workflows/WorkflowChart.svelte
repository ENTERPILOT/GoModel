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

<style>
  /* Styles owned by this component (moved from dashboard.css). */
  /* ═══════════════════════════════════════════════════════════════
     Workflow Pipeline Visualization
     ═══════════════════════════════════════════════════════════════ */
  .workflow-pipeline {
    position: relative;
    display: flex;
    flex-direction: column;
    gap: 0;
    padding: 18px 20px 20px;
    margin-bottom: 12px;
    border-radius: var(--radius);
    border: 1px solid var(--border);
    background: var(--bg);
  }

  .workflow-pipeline-has-meta {
    padding-top: 42px;
  }

  .workflow-pipeline-meta {
    position: absolute;
    top: 12px;
    right: 14px;
    display: inline-flex;
    align-items: center;
    gap: 0;
    min-width: 0;
    max-width: calc(100% - 28px);
    padding: 2px 10px;
    border-radius: 12px;
    border: 1px solid var(--border);
    background: color-mix(in srgb, var(--bg-surface) 86%, transparent);
    color: var(--text-muted);
    font-size: 12px;
    font-weight: 500;
    line-height: 1.2;
    white-space: nowrap;
  }

  .workflow-pipeline-meta-copy {
    appearance: none;
    cursor: pointer;
    text-align: left;
    overflow: hidden;
    transition:
      background-color 0.15s,
      border-color 0.15s,
      color 0.15s,
      box-shadow 0.15s;
  }

  .workflow-pipeline-meta-copy:hover, .workflow-pipeline-meta-copy:focus-visible {
    border-color: color-mix(in srgb, var(--accent) 40%, var(--border));
    background: color-mix(in srgb, var(--accent) 8%, var(--bg-surface));
    color: color-mix(in srgb, var(--accent) 74%, var(--text));
  }

  .workflow-pipeline-meta-copy:focus-visible {
    outline: none;
    box-shadow: 0 0 0 2px color-mix(in srgb, var(--accent) 18%, transparent);
  }

  .workflow-pipeline-meta-label {
    flex: 0 0 auto;
    font-weight: 700;
  }

  .workflow-pipeline-meta-placeholder {
    flex: 0 0 auto;
    max-width: 3ch;
    margin-left: 4px;
    overflow: hidden;
    opacity: 1;
    transition:
      max-width 0.18s ease,
      margin-left 0.18s ease,
      opacity 0.15s ease;
  }

  .workflow-pipeline-meta-value {
    flex: 0 1 auto;
    max-width: 0;
    margin-left: 0;
    overflow: hidden;
    opacity: 0;
    text-overflow: clip;
    transition:
      max-width 0.22s ease,
      margin-left 0.18s ease,
      opacity 0.15s ease;
  }

  .workflow-pipeline-meta-copy:hover .workflow-pipeline-meta-placeholder, .workflow-pipeline-meta-copy:focus-visible .workflow-pipeline-meta-placeholder, .workflow-pipeline-meta-copied .workflow-pipeline-meta-placeholder, .workflow-pipeline-meta-error .workflow-pipeline-meta-placeholder {
    max-width: 0;
    margin-left: 0;
    opacity: 0;
  }

  .workflow-pipeline-meta-copy:hover .workflow-pipeline-meta-value, .workflow-pipeline-meta-copy:focus-visible .workflow-pipeline-meta-value, .workflow-pipeline-meta-copied .workflow-pipeline-meta-value, .workflow-pipeline-meta-error .workflow-pipeline-meta-value {
    max-width: 42ch;
    margin-left: 4px;
    opacity: 1;
  }

  .workflow-pipeline-meta-icon {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    flex: 0 0 auto;
    width: 0;
    height: 14px;
    margin-left: 0;
    overflow: hidden;
    opacity: 0;
    line-height: 0;
    transform: translateX(4px) translateY(1px) scale(0.84);
    transition:
      width 0.18s ease,
      margin-left 0.18s ease,
      opacity 0.15s ease,
      transform 0.18s ease;
  }

  .workflow-pipeline-meta-icon :global(svg) {
    width: 14px;
    height: 14px;
    stroke: currentcolor;
    fill: none;
    stroke-width: 2;
    stroke-linecap: round;
    stroke-linejoin: round;
  }

  .workflow-pipeline-meta-copied, .workflow-pipeline-meta-copied:hover, .workflow-pipeline-meta-copied:focus-visible {
    background: color-mix(in srgb, var(--success) 12%, var(--bg));
    border-color: color-mix(in srgb, var(--success) 40%, var(--border));
    color: var(--success);
  }

  .workflow-pipeline-meta-copied .workflow-pipeline-meta-icon {
    width: 14px;
    margin-left: 6px;
    opacity: 1;
    transform: translateY(1px);
  }

  .workflow-pipeline-meta-error, .workflow-pipeline-meta-error:hover, .workflow-pipeline-meta-error:focus-visible {
    background: color-mix(in srgb, var(--danger) 10%, var(--bg));
    border-color: color-mix(in srgb, var(--danger) 34%, var(--border));
    color: var(--danger);
  }

  /* ─── Main pipeline row ─── */
  .workflow-pipeline-row {
    display: flex;
    align-items: center;
    width: 100%;
    min-width: 0;
    overflow-x: auto;
  }

  .workflow-node-icon {
    display: flex;
    align-items: center;
    justify-content: center;
    width: 28px;
    height: 28px;
    border-radius: var(--radius);
    background: var(--bg);
    color: var(--text-muted);
  }

  .workflow-node-icon :global(svg) {
    width: 15px;
    height: 15px;
    stroke: currentcolor;
    fill: none;
    stroke-width: 2;
    stroke-linecap: round;
    stroke-linejoin: round;
  }

  .workflow-node-label {
    font-size: 11px;
    font-weight: 700;
    letter-spacing: 0.03em;
    color: var(--text);
    white-space: nowrap;
    line-height: 1.2;
  }

  .workflow-node-sub {
    font-size: 10px;
    font-weight: 500;
    color: var(--text-muted);
    white-space: nowrap;
    max-width: 120px;
    overflow: hidden;
    text-overflow: ellipsis;
    line-height: 1.2;
    font-family: var(--font-mono, ui-monospace, monospace);
  }

  .workflow-node-badge {
    display: inline-flex;
    align-items: center;
    padding: 2px 7px;
    border-radius: var(--radius);
    font-size: 9px;
    font-weight: 800;
    letter-spacing: 0.06em;
    text-transform: uppercase;
    white-space: nowrap;
    border: 1px solid var(--border);
    background: var(--bg);
    color: var(--text-muted);
    line-height: 1.5;
  }

  /* ─── Endpoint nodes (Client / Response) ─── */
  .workflow-node-endpoint {
    flex-direction: row;
    padding: 10px 14px;
    border-radius: var(--radius);
    min-width: auto;
    gap: 7px;
    border-color: var(--border);
    background: var(--bg-surface);
  }

  .workflow-node-icon-endpoint {
    width: auto;
    height: auto;
    justify-content: flex-start;
    padding: 0;
    background: transparent;
    border-radius: var(--radius);
    color: var(--text-muted);
  }

  .workflow-node-icon-endpoint :global(svg) {
    width: 14px;
    height: 14px;
  }

  .workflow-node-endpoint .workflow-node-label {
    font-size: 11px;
    font-weight: 600;
    color: var(--text-muted);
  }

  /* ─── Shared node variants ─── */
  .workflow-node-feature {
    border-color: color-mix(in srgb, var(--accent) 46%, var(--border));
    background: color-mix(in srgb, var(--accent) 8%, var(--bg-surface));
  }

  .workflow-node-feature .workflow-node-icon {
    background: color-mix(in srgb, var(--accent) 16%, var(--bg));
    color: var(--accent);
  }

  .workflow-node-feature .workflow-node-label {
    color: var(--accent);
  }

  .workflow-node-feature .workflow-node-sub {
    color: color-mix(in srgb, var(--accent) 70%, var(--text-muted));
  }

  /* ─── Auth node ─── */
  /* ─── AI node ─── */
  .workflow-node-ai {
    min-width: 96px;
    padding: 12px 16px;
    border-radius: var(--radius);
    gap: 6px;
  }

  /* ─── Async section ─── */
  /*
   * Async nodes fire after the response is returned to the client.
   * They drop below the main row via an L-turn from Response, then
   * flow right-to-left: Audit Log on the right, Usage on the left.
   *
   * [Client] ──→ [Cache?] ──── [AI] ──────── [Response]
   *                                               │
   *                                               │ (dashed drop)
   *                                               │
   *                         [Usage] ← ─ ─ [Audit Log]
   */
  /* Container — full-width branch lane below the main row */
  .workflow-async-section {
    width: 100%;
    display: flex;
    align-items: center;
    justify-content: flex-end;
    gap: 0;
    min-width: 0;
    margin-top: 10px;
  }

  /* L-turn connector: centered horizontal leg plus vertical rise back to Response */
  .workflow-async-turn {
    flex: 0 0 60px;
    position: relative; /* for arrowhead + vertical rise */
    height: 2px;
    background: repeating-linear-gradient(
      to left,
      color-mix(in srgb, var(--text-muted) 45%, var(--border)) 0,
      color-mix(in srgb, var(--text-muted) 45%, var(--border)) 5px,
      transparent 5px,
      transparent 9px
    );
  }

  /* Left-pointing arrowhead at the end of the horizontal L-turn line */
  .workflow-async-turn::before {
    content: "";
    position: absolute;
    left: -7px;
    top: 50%;
    transform: translateY(-50%);
    width: 7px;
    height: 9px;
    background: color-mix(in srgb, var(--text-muted) 40%, var(--border));
    clip-path: polygon(100% 0, 0 50%, 100% 100%);
  }

  /* Vertical dashed rise that connects the inline turn back up to Response */
  .workflow-async-turn::after {
    content: "";
    position: absolute;
    right: 0;
    bottom: 1px;
    height: 16px;
    border-right: 2px dashed
      color-mix(in srgb, var(--text-muted) 40%, var(--border));
  }

  /* RTL async row — Audit Log right, Usage left */
  .workflow-async-row {
    display: flex;
    align-items: center;
    min-width: 0;
    margin-right: 7px;
  }

  /* Dashed left-pointing connector between async nodes */
  .workflow-conn-async {
    flex: 0 0 24px;
    background: repeating-linear-gradient(
      to left,
      color-mix(in srgb, var(--text-muted) 45%, var(--border)) 0,
      color-mix(in srgb, var(--text-muted) 45%, var(--border)) 5px,
      transparent 5px,
      transparent 9px
    );
    width: 24px;
  }

  /* Left-pointing arrowhead (overrides workflow-conn::after right-pointing default) */
  .workflow-conn-async::after {
    background: color-mix(in srgb, var(--text-muted) 45%, var(--border));
    left: -1px;
    right: auto;
    clip-path: polygon(100% 0, 0 50%, 100% 100%);
  }

  /* Async nodes — horizontal inline pills */
  .workflow-node-async {
    flex-direction: row;
    padding: 7px 12px;
    border-radius: var(--radius);
    border-style: dashed;
    min-width: auto;
    gap: 7px;
  }

  .workflow-node-async .workflow-node-icon {
    width: 12px;
    height: 12px;
    border-radius: var(--radius);
  }

  .workflow-node-async .workflow-node-icon :global(svg) {
    width: 12px;
    height: 12px;
  }

  .workflow-node-async .workflow-node-label {
    font-size: 10px;
    font-weight: 700;
  }

  /* "ASYNC" label inline on the right of the branch */
  .workflow-async-label {
    display: inline-flex;
    align-items: center;
    margin-left: 8px;
    font-size: 9px;
    font-weight: 800;
    letter-spacing: 0.1em;
    text-transform: uppercase;
    color: var(--text-muted);
    opacity: 0.55;
    white-space: nowrap;
    flex-shrink: 0;
  }

  /* Status variants of the node internals. The state classes
     (workflow-node-success/current/...) are computed in
     workflowChartLogic.js; these rules must live AFTER the base
     icon/label/sub/badge rules above so they win the cascade —
     as global rules they would lose ties to the scoped bases. */
  .workflow-node-success .workflow-node-icon {
    background: color-mix(in srgb, var(--success) 18%, var(--bg));
    color: var(--success);
  }

  .workflow-node-success .workflow-node-icon-endpoint {
    color: var(--success);
  }

  .workflow-node-success .workflow-node-label {
    color: color-mix(in srgb, var(--success) 85%, var(--text));
  }

  .workflow-node-success .workflow-node-sub {
    color: color-mix(in srgb, var(--success) 74%, var(--text-muted));
  }

  .workflow-node-success .workflow-node-badge {
    background: color-mix(in srgb, var(--success) 14%, var(--bg));
    border-color: color-mix(in srgb, var(--success) 38%, var(--border));
    color: var(--success);
  }

  .workflow-node-current .workflow-node-icon {
    background: color-mix(in srgb, var(--info) 16%, var(--bg));
    color: var(--info);
  }

  .workflow-node-current .workflow-node-icon-endpoint {
    color: var(--info);
  }

  .workflow-node-current .workflow-node-label {
    color: color-mix(in srgb, var(--info) 85%, var(--text));
  }

  .workflow-node-current .workflow-node-sub {
    color: color-mix(in srgb, var(--info) 72%, var(--text-muted));
  }

  .workflow-node-current .workflow-node-badge {
    background: color-mix(in srgb, var(--info) 13%, var(--bg));
    border-color: color-mix(in srgb, var(--info) 36%, var(--border));
    color: var(--info);
  }

  .workflow-node-warning .workflow-node-icon {
    background: color-mix(in srgb, var(--warning) 14%, var(--bg));
    color: var(--warning);
  }

  .workflow-node-warning .workflow-node-icon-endpoint {
    color: var(--warning);
  }

  .workflow-node-warning .workflow-node-label {
    color: color-mix(in srgb, var(--warning) 85%, var(--text));
  }

  .workflow-node-warning .workflow-node-sub {
    color: color-mix(in srgb, var(--warning) 72%, var(--text-muted));
  }

  .workflow-node-warning .workflow-node-badge {
    background: color-mix(in srgb, var(--warning) 14%, var(--bg));
    border-color: color-mix(in srgb, var(--warning) 38%, var(--border));
    color: var(--warning);
  }

  .workflow-node-error .workflow-node-icon {
    background: color-mix(in srgb, var(--danger) 14%, var(--bg));
    color: var(--danger);
  }

  .workflow-node-error .workflow-node-icon-endpoint {
    color: var(--danger);
  }

  .workflow-node-error .workflow-node-label {
    color: color-mix(in srgb, var(--danger) 85%, var(--text));
  }

  .workflow-node-error .workflow-node-sub {
    color: color-mix(in srgb, var(--danger) 72%, var(--text-muted));
  }

  .workflow-node-neutral .workflow-node-icon {
    background: color-mix(in srgb, var(--text-muted) 12%, var(--bg));
    color: var(--text-muted);
  }

  .workflow-node-neutral .workflow-node-icon-endpoint {
    color: var(--text-muted);
  }

  .workflow-node-neutral .workflow-node-label {
    color: var(--text-muted);
  }

  .workflow-node-neutral .workflow-node-sub {
    color: color-mix(in srgb, var(--text-muted) 84%, var(--border));
  }

  .workflow-node-neutral .workflow-node-badge {
    background: color-mix(in srgb, var(--text-muted) 10%, var(--bg));
    border-color: color-mix(in srgb, var(--text-muted) 28%, var(--border));
    color: var(--text-muted);
  }

  .workflow-node-cache-miss .workflow-node-badge {
    color: var(--text-muted);
    border-color: var(--border);
  }

  /* Node/connector state tints (classes computed in workflowChartLogic.js).
     Must live after the structural rules (feature/endpoint/async set their
     own border/background) so the state coloring wins the cascade. */
  .workflow-conn-hit {
    background: color-mix(in srgb, var(--success) 58%, var(--border));
  }

  .workflow-conn-hit::after {
    background: color-mix(in srgb, var(--success) 58%, var(--border));
  }

  .workflow-conn-dim {
    background: color-mix(in srgb, var(--border) 75%, transparent);
  }

  .workflow-conn-dim::after {
    background: color-mix(in srgb, var(--border) 75%, transparent);
  }

  .workflow-node-success {
    border-color: color-mix(in srgb, var(--success) 52%, var(--border));
    background: color-mix(in srgb, var(--success) 9%, var(--bg-surface));
  }

  .workflow-node-current {
    border-color: color-mix(in srgb, var(--info) 56%, var(--border));
    background: color-mix(in srgb, var(--info) 10%, var(--bg-surface));
  }

  .workflow-node-warning {
    border-color: color-mix(in srgb, var(--warning) 52%, var(--border));
    background: color-mix(in srgb, var(--warning) 9%, var(--bg-surface));
  }

  .workflow-node-error {
    border-color: color-mix(in srgb, var(--danger) 52%, var(--border));
    background: color-mix(in srgb, var(--danger) 9%, var(--bg-surface));
  }

  .workflow-node-neutral {
    border-color: color-mix(in srgb, var(--text-muted) 40%, var(--border));
    background: color-mix(in srgb, var(--text-muted) 8%, var(--bg-surface));
  }

  .workflow-node-skipped {
    position: relative;
    opacity: 0.28;
  }
</style>
