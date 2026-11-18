<script>
  // Request / Response(s) tab strip plus the panels. Inactive panels stay in
  // the DOM (hidden, not unmounted) so audio playback and scroll position
  // survive tab switches.
  import Icon from "$lib/components/atoms/Icon.svelte";
  import RequestGuardrailsPane from "./RequestGuardrailsPane.svelte";
  import RequestLogPane from "./RequestLogPane.svelte";
  import { requestLogEffectiveTab, requestLogTabKeydownTarget, statusCodeClass } from "./request-log-logic.js";
  import { ArrowLeft, ArrowRight, Shield } from "lucide";
  import * as m from "$lib/paraglide/messages.js";

  let { entry, panes = [] } = $props();

  // Active tab; null falls back to the default tab until the user picks one.
  let activeTab = $state(null);
  const effectiveTab = $derived(requestLogEffectiveTab(activeTab, entry, panes));

  const tabId = (paneId) => "request-log-tab-" + entry.id + "-" + paneId;
  const panelId = (paneId) => "request-log-tabpanel-" + entry.id + "-" + paneId;

  function onTabKeydown(event, currentId) {
    const ids = panes.map((p) => p.id);
    const next = requestLogTabKeydownTarget(event.key, ids, currentId);
    if (next == null) return;
    event.preventDefault();
    const tablist = event.currentTarget?.closest?.(".request-log-pane-tablist");
    const target = tablist?.querySelectorAll(".request-log-pane-tab")[ids.indexOf(next)];
    target?.focus?.();
    target?.scrollIntoView?.({ block: "nearest", inline: "nearest" });
    activeTab = next;
  }
</script>

<div class="request-log-request-response">
  <div class="request-log-pane-tablist" role="tablist" aria-label={m.requests_request_response_label()}>
    {#each panes as p (p.id)}
      <button
        type="button"
        class="request-log-pane-tab"
        class:request-log-pane-tab-active={effectiveTab === p.id}
        role="tab"
        aria-selected={effectiveTab === p.id}
        id={tabId(p.id)}
        aria-controls={panelId(p.id)}
        tabindex={effectiveTab === p.id ? 0 : -1}
        onkeydown={(event) => onTabKeydown(event, p.id)}
        onclick={() => (activeTab = p.id)}
      >
        <span
          class={["request-log-pane-icon", p.pane.direction && `request-log-pane-icon-${p.pane.direction}`]}
          aria-hidden="true"
        >
          {#if p.pane.direction === "request"}
            <Icon icon={ArrowRight} />
          {:else if p.pane.direction === "response"}
            <Icon icon={ArrowLeft} />
          {:else if p.pane.type === "guardrails"}
            <Icon icon={Shield} />
          {/if}
        </span>
        <span class="request-log-pane-tab-label">{p.pane.title}</span>
        {#if p.pane.seq}
          <span class="request-log-pane-seq mono">#{p.pane.seq}</span>
        {/if}
        {#if p.pane.kind}
          <span class="provider-badge request-log-pane-kind request-log-pane-kind-{p.pane.kind}"
            >{p.pane.kind}</span
          >
        {/if}
        {#each p.pane.noChangeSteps || [] as step (step.id)}
          <span class="request-log-step-pill" title={step.title}>{step.label}</span>
        {/each}
        {#if p.pane.savingsLabel}
          <span
            class="request-log-savings-pill mono"
            title={m.requests_rewrite_savings_help()}
            >{p.pane.savingsLabel}</span
          >
        {/if}
        {#if p.pane.statusCode}
          <span class="request-log-status-badge {statusCodeClass(p.pane.statusCode)}"
            >{p.pane.statusCode}</span
          >
        {/if}
        {#if p.pane.badge}
          <span class="request-log-status-badge {p.pane.badge.class}">{p.pane.badge.text}</span>
        {/if}
      </button>
    {/each}
  </div>
  {#each panes as p (p.id)}
    <div
      class="request-log-pane-tabpanel"
      style:display={effectiveTab === p.id ? null : "none"}
      role="tabpanel"
      id={panelId(p.id)}
      aria-labelledby={tabId(p.id)}
    >
      {#if p.pane.type === "guardrails"}
        <RequestGuardrailsPane pane={p.pane} />
      {:else}
        <RequestLogPane pane={p.pane} />
      {/if}
    </div>
  {/each}
</div>

<style>
  .request-log-request-response {
    margin-top: 4px;
  }

  /* The tab strip carries the direction icon, title, attempt type and status;
     the active panel shows that pane's content. */
  .request-log-pane-tablist {
    display: flex;
    gap: 2px;
    overflow-x: auto;
    overflow-y: hidden;
    border-bottom: 1px solid var(--border);
  }

  .request-log-pane-tab {
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

  .request-log-pane-tab:not(.request-log-pane-tab-active):hover {
    color: var(--text);
    background: color-mix(in srgb, var(--text) 5%, transparent);
  }

  /* Active tab reads as a card: bordered top + sides with slightly rounded top
     corners, and a background-colored bottom edge that blends into the content
     area (covering the tablist underline). */
  .request-log-pane-tab-active {
    color: var(--text);
    border-color: var(--border);
    border-bottom-color: var(--bg);
    background: var(--bg);
  }

  .request-log-pane-tab-label {
    font-weight: 600;
  }

  .request-log-pane-tabpanel {
    min-width: 0;
  }

  /* Direction icon: request (→) vs response (←), distinct hues so the two panes
     are scannable at a glance without relying on success/error color. The hue
     class is composed from pane.direction, so the compiler cannot see it —
     hence :global, kept in a compound so it still outranks the base rule. */
  .request-log-pane-icon {
    display: inline-flex;
    align-items: center;
    flex: 0 0 auto;
    color: var(--text-muted);
  }

  .request-log-pane-icon:global(.request-log-pane-icon-request) {
    color: var(--accent);
  }

  .request-log-pane-icon:global(.request-log-pane-icon-response) {
    color: var(--info);
  }

  .request-log-pane-icon :global(svg) {
    width: 16px;
    height: 16px;
  }

  .request-log-pane-seq {
    color: var(--text-muted);
    font-size: 12px;
  }

  /* Attempt type rendered as a pill, accented so failover/retry stand out.
     Same dynamic-class caveat as the direction icon above. */
  .request-log-pane-kind {
    text-transform: uppercase;
    letter-spacing: 0.04em;
    font-size: 10px;
    font-weight: 600;
  }

  .request-log-pane-kind:global(.request-log-pane-kind-primary) {
    color: var(--text-muted);
  }

  .request-log-pane-kind:global(.request-log-pane-kind-failover) {
    color: var(--accent);
    background: color-mix(in srgb, var(--accent) 14%, var(--bg));
    border-color: color-mix(in srgb, var(--accent) 30%, var(--border));
  }

  .request-log-pane-kind:global(.request-log-pane-kind-retry) {
    color: var(--warning);
    background: color-mix(in srgb, var(--warning) 14%, var(--bg));
    border-color: color-mix(in srgb, var(--warning) 30%, var(--border));
  }

  /* Share of the request body removed by a rewrite (e.g. token compression),
     shown on the Rewritten tab next to the rewriter pill. Same blue family as
     the cached tokens in the charts (both derive from --info); the prompt-cache
     variables carry the per-theme tone mix that keeps the text readable. */
  .request-log-savings-pill {
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
  .request-log-step-pill {
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
