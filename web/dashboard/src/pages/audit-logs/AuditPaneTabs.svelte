<script>
  // Request / Response(s) tab strip plus the panels. Inactive panels stay in
  // the DOM (hidden, not unmounted) so audio playback and scroll position
  // survive tab switches.
  import Icon from "$lib/components/atoms/Icon.svelte";
  import AuditPane from "./AuditPane.svelte";
  import { auditEffectiveTab, auditTabKeydownTarget, statusCodeClass } from "./audit-logic.js";
  import { ArrowLeft, ArrowRight } from "lucide";
  import * as m from "$lib/paraglide/messages.js";

  let { entry, panes = [] } = $props();

  // Active tab; null falls back to the default tab until the user picks one.
  let activeTab = $state(null);
  const effectiveTab = $derived(auditEffectiveTab(activeTab, entry, panes));

  const tabId = (paneId) => "audit-tab-" + entry.id + "-" + paneId;
  const panelId = (paneId) => "audit-tabpanel-" + entry.id + "-" + paneId;

  function onTabKeydown(event, currentId) {
    const ids = panes.map((p) => p.id);
    const next = auditTabKeydownTarget(event.key, ids, currentId);
    if (next == null) return;
    event.preventDefault();
    const tablist = event.currentTarget?.closest?.(".audit-pane-tablist");
    const target = tablist?.querySelectorAll(".audit-pane-tab")[ids.indexOf(next)];
    target?.focus?.();
    target?.scrollIntoView?.({ block: "nearest", inline: "nearest" });
    activeTab = next;
  }
</script>

<div class="audit-request-response">
  <div class="audit-pane-tablist" role="tablist" aria-label={m.audit_request_response_label()}>
    {#each panes as p (p.id)}
      <button
        type="button"
        class="audit-pane-tab"
        class:audit-pane-tab-active={effectiveTab === p.id}
        role="tab"
        aria-selected={effectiveTab === p.id}
        id={tabId(p.id)}
        aria-controls={panelId(p.id)}
        tabindex={effectiveTab === p.id ? 0 : -1}
        onkeydown={(event) => onTabKeydown(event, p.id)}
        onclick={() => (activeTab = p.id)}
      >
        <span
          class={["audit-pane-icon", p.pane.direction && `audit-pane-icon-${p.pane.direction}`]}
          aria-hidden="true"
        >
          {#if p.pane.direction === "request"}
            <Icon icon={ArrowRight} />
          {:else if p.pane.direction === "response"}
            <Icon icon={ArrowLeft} />
          {/if}
        </span>
        <span class="audit-pane-tab-label">{p.pane.title}</span>
        {#if p.pane.seq}
          <span class="audit-pane-seq mono">#{p.pane.seq}</span>
        {/if}
        {#if p.pane.kind}
          <span class="provider-badge audit-pane-kind audit-pane-kind-{p.pane.kind}"
            >{p.pane.kind}</span
          >
        {/if}
        {#each p.pane.noChangeSteps || [] as step (step.id)}
          <span class="audit-step-pill" title={step.title}>{step.label}</span>
        {/each}
        {#if p.pane.savingsLabel}
          <span
            class="audit-savings-pill mono"
            title={m.audit_rewrite_savings_help()}
            >{p.pane.savingsLabel}</span
          >
        {/if}
        {#if p.pane.statusCode}
          <span class="audit-status-badge {statusCodeClass(p.pane.statusCode)}"
            >{p.pane.statusCode}</span
          >
        {/if}
      </button>
    {/each}
  </div>
  {#each panes as p (p.id)}
    <div
      class="audit-pane-tabpanel"
      style:display={effectiveTab === p.id ? null : "none"}
      role="tabpanel"
      id={panelId(p.id)}
      aria-labelledby={tabId(p.id)}
    >
      <AuditPane pane={p.pane} />
    </div>
  {/each}
</div>

<style>
  .audit-request-response {
    margin-top: 4px;
  }

  /* The tab strip carries the direction icon, title, attempt type and status;
     the active panel shows that pane's content. */
  .audit-pane-tablist {
    display: flex;
    gap: 2px;
    overflow-x: auto;
    overflow-y: hidden;
    border-bottom: 1px solid var(--border);
  }

  .audit-pane-tab {
    display: inline-flex;
    flex: 0 0 auto;
    align-items: center;
    gap: 8px;
    white-space: nowrap;
    padding: 8px 12px;
    border: 1px solid var(--border);
    border-bottom-color: transparent;
    border-radius: 6px 6px 0 0;
    margin-bottom: -1px;
    margin-right: 12px;
    background: transparent;
    color: var(--text-muted);
    font-family: inherit;
    font-size: 13px;
    cursor: pointer;
    transition:
      color 0.15s,
      border-color 0.15s,
      background-color 0.15s;
  }

  .audit-pane-tab:not(.audit-pane-tab-active):hover {
    color: var(--text);
    background: color-mix(in srgb, var(--text) 5%, transparent);
  }

  /* Active tab reads as a card: bordered top + sides with slightly rounded top
     corners, and a background-colored bottom edge that blends into the content
     area (covering the tablist underline). */
  .audit-pane-tab-active {
    color: var(--text);
    border-color: var(--border);
    border-bottom-color: var(--bg);
    background: var(--bg);
  }

  .audit-pane-tab-label {
    font-weight: 600;
  }

  .audit-pane-tabpanel {
    min-width: 0;
  }

  /* Direction icon: request (→) vs response (←), distinct hues so the two panes
     are scannable at a glance without relying on success/error color. The hue
     class is composed from pane.direction, so the compiler cannot see it —
     hence :global, kept in a compound so it still outranks the base rule. */
  .audit-pane-icon {
    display: inline-flex;
    align-items: center;
    flex: 0 0 auto;
    color: var(--text-muted);
  }

  .audit-pane-icon:global(.audit-pane-icon-request) {
    color: var(--accent);
  }

  .audit-pane-icon:global(.audit-pane-icon-response) {
    color: var(--info);
  }

  .audit-pane-icon :global(svg) {
    width: 16px;
    height: 16px;
  }

  .audit-pane-seq {
    color: var(--text-muted);
    font-size: 12px;
  }

  /* Attempt type rendered as a pill, accented so failover/retry stand out.
     Same dynamic-class caveat as the direction icon above. */
  .audit-pane-kind {
    text-transform: uppercase;
    letter-spacing: 0.04em;
    font-size: 10px;
    font-weight: 600;
  }

  .audit-pane-kind:global(.audit-pane-kind-primary) {
    color: var(--text-muted);
  }

  .audit-pane-kind:global(.audit-pane-kind-failover) {
    color: var(--accent);
    background: color-mix(in srgb, var(--accent) 14%, var(--bg));
    border-color: color-mix(in srgb, var(--accent) 30%, var(--border));
  }

  .audit-pane-kind:global(.audit-pane-kind-retry) {
    color: var(--warning);
    background: color-mix(in srgb, var(--warning) 14%, var(--bg));
    border-color: color-mix(in srgb, var(--warning) 30%, var(--border));
  }

  /* Share of the request body removed by a rewrite (e.g. token compression),
     shown on the Rewritten tab next to the rewriter pill. Same blue family as
     the cached tokens in the charts (both derive from --info); the prompt-cache
     variables carry the per-theme tone mix that keeps the text readable. */
  .audit-savings-pill {
    display: inline-flex;
    align-items: center;
    padding: 1px 7px;
    border: 1px solid color-mix(in srgb, var(--prompt-cache-color) 45%, var(--border));
    border-radius: 999px;
    background: var(--prompt-cache-color-bg);
    color: var(--prompt-cache-color);
    font-size: 11px;
    font-weight: 700;
    letter-spacing: 0.02em;
  }

  /* An ingress rewriter that ran and changed nothing. Deliberately quieter
     than the savings pill: it reports a step that happened, not a result. */
  .audit-step-pill {
    display: inline-flex;
    align-items: center;
    padding: 1px 7px;
    border: 1px dashed var(--border);
    border-radius: 999px;
    color: var(--text-muted);
    font-size: 11px;
    letter-spacing: 0.02em;
  }
</style>
