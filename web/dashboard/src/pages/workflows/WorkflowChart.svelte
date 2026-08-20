<script>
  import * as m from "$lib/paraglide/messages.js";
  // Workflow pipeline visualization.
  // Renders a chart contract object built by workflowChartLogic.js.
  import Icon from "$lib/components/atoms/Icon.svelte";
  import WorkflowIdBadge from "./WorkflowIdBadge.svelte";
  import {
    ChartColumnIncreasing,
    CircleCheckBig,
    Database,
    FileText,
    Maximize2,
    Shield,
    User,
    Wallet,
  } from "lucide";

  let { chart = {} } = $props();
</script>

<!-- One pipeline node. `icon` is a lucide icon (omitted for the AI node);
     `variant` carries the structural class, `state` the computed status
     class from workflowChartLogic.js. -->
{#snippet node({ icon, label, variant = "workflow-node-feature", state, sub, badge })}
  <div class={["workflow-node", variant, state]}>
    {#if icon}
      <div
        class="workflow-node-icon"
        class:workflow-node-icon-endpoint={variant === "workflow-node-endpoint"}
      >
        <Icon {icon} />
      </div>
    {/if}
    <span class="workflow-node-label">{label}</span>
    {#if badge}
      <span class="workflow-node-badge">{badge}</span>
    {/if}
    {#if sub}
      <span class="workflow-node-sub">{sub}</span>
    {/if}
  </div>
{/snippet}

<div class="workflow-pipeline">
  {#if chart.workflowID}
    <WorkflowIdBadge workflowID={chart.workflowID} />
  {/if}
  <div class="workflow-pipeline-row">
    {@render node({ icon: User, label: m.workflows_client(), variant: "workflow-node-endpoint" })}

    <div class="workflow-conn"></div>
    {@render node({
      icon: Database,
      label: m.workflows_auth(),
      state: chart.authNodeClass,
      sub: chart.authNodeSublabel,
    })}

    {#if chart.showCache}
      <div class={["workflow-conn", chart.cacheConnClass]}></div>
      {@render node({
        icon: Database,
        label: m.workflows_cache(),
        state: chart.cacheNodeClass,
        badge: chart.cacheStatusLabel,
      })}
    {/if}

    {#if chart.showBudget}
      <div class="workflow-conn"></div>
      {@render node({
        icon: Wallet,
        label: m.workflows_budget(),
        state: chart.budgetNodeClass,
        badge: chart.budgetStatusLabel,
      })}
    {/if}

    {#if chart.showGuardrails}
      <div class="workflow-conn"></div>
      {@render node({
        icon: Shield,
        label: m.workflows_guardrails(),
        sub: chart.guardrailLabel,
      })}
    {/if}

    <div class={["workflow-conn", chart.aiConnClass]}></div>
    {@render node({
      label: chart.aiLabel,
      variant: "workflow-node-ai",
      state: chart.aiNodeClass,
      sub: chart.aiSublabel,
    })}

    {#if chart.showFailover}
      <div class={["workflow-conn", chart.failoverConnClass]}></div>
      {@render node({
        icon: Maximize2,
        label: m.workflows_failover(),
        state: chart.failoverNodeClass,
        badge: chart.failoverStatusLabel,
        sub: chart.failoverTargetLabel,
      })}
    {/if}

    <div class={["workflow-conn", chart.responseConnClass]}></div>
    {@render node({
      icon: CircleCheckBig,
      label: m.workflows_response(),
      variant: "workflow-node-endpoint",
      state: chart.responseNodeClass,
      sub: chart.responseNodeSublabel,
    })}
  </div>

  {#if chart.showAsync}
    <div class="workflow-pipeline-row workflow-async-section">
      {#if chart.showUsage}
        {@render node({
          icon: ChartColumnIncreasing,
          label: m.workflows_usage(),
          variant: "workflow-node-feature workflow-node-async",
          state: chart.usageNodeClass,
        })}
      {/if}
      {#if chart.showUsage && chart.showAudit}
        <div class="workflow-conn workflow-conn-async"></div>
      {/if}
      {#if chart.showAudit}
        {@render node({
          icon: FileText,
          label: m.workflows_audit_log(),
          variant: "workflow-node-feature workflow-node-async",
          state: chart.auditNodeClass,
        })}
      {/if}
      <div class="workflow-async-turn"></div>
      <span class="workflow-async-label">{m.workflows_async()}</span>
    </div>
  {/if}
</div>

<style>
  /* ═══════════════════════════════════════════════════════════════
     Workflow Pipeline Visualization
     ═══════════════════════════════════════════════════════════════ */
  .workflow-pipeline {
    position: relative;
    display: flex;
    flex-direction: column;
    gap: 0;
    padding: 18px 20px 4px;
    margin-bottom: 12px;
    border-radius: var(--radius);
    border: 1px solid var(--border);
    background: var(--bg);
  }

  /* ─── Main pipeline row ─── */
  .workflow-pipeline-row {
    display: flex;
    align-items: center;
    width: 100%;
    min-width: 0;
    overflow-x: auto;
    overflow-y: hidden;
  }

  .workflow-pipeline > .workflow-pipeline-row {
    padding-bottom: 16px;
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
  /* Right-align when the branch fits; auto margin collapses on overflow so
     the shared pipeline-row overflow-x can scroll. */
  .workflow-async-section > :first-child {
    margin-left: auto;
  }

  /* L-turn connector: centered horizontal leg plus vertical rise back to Response */
  .workflow-async-turn {
    flex: 0 0 60px;
    margin-left: 7px;
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
