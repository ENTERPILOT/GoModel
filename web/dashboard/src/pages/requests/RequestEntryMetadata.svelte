<script>
  // Metadata badge strip under an expanded request log entry.
  import { providerDisplayValue, qualifiedResolvedModelDisplay } from "$lib/utils/format.js";
  import { requestGuardrailVerdict, workflowFailoverTarget } from "./request-log-logic.js";
  import { usagePage } from "../usage/usage.svelte.js";
  import * as m from "$lib/paraglide/messages.js";

  let { entry } = $props();

  // The guardrail that stopped the request (or the first that warned).
  const guardrail = $derived(requestGuardrailVerdict(entry));

  // Badges in render order. `mono` marks machine values; entries with a falsy
  // text are dropped, so optional fields simply disappear.
  const badges = $derived(
    [
      { key: "provider", text: providerDisplayValue(entry) || "-" },
      { key: "model", text: entry.requested_model || entry.model || "-", mono: true },
      { key: "user_path", text: entry.user_path, mono: true },
      { key: "session_id", text: entry.session_id && "session: " + entry.session_id, mono: true },
      { key: "request_id", text: "request_id: " + (entry.request_id || "-"), mono: true },
      { key: "ip", text: entry.client_ip && "ip: " + entry.client_ip, mono: true },
      {
        key: "auth_key_id",
        text: entry.auth_key_id && "auth_key_id: " + entry.auth_key_id,
        mono: true,
      },
      { key: "alias", text: entry.alias_used && "alias", class: "request-log-alias-badge" },
      {
        key: "resolved",
        text:
          entry.alias_used &&
          entry.resolved_model &&
          "resolved: " + qualifiedResolvedModelDisplay(entry),
        mono: true,
      },
      {
        key: "failover",
        text: workflowFailoverTarget(entry) && "failover: " + workflowFailoverTarget(entry),
        mono: true,
      },
      { key: "stream", text: entry.stream && "stream" },
      {
        key: "guardrail",
        text: guardrail && guardrail.text,
        class:
          guardrail && guardrail.tone === "danger"
            ? "request-log-guardrail-badge-danger"
            : "request-log-guardrail-badge-warning",
      },
      { key: "error_type", text: entry.error_type },
    ].filter((b) => !!b.text),
  );
</script>

<div class="request-log-entry-metadata">
  <span class="request-log-entry-metadata-label">{m.requests_metadata_label()}</span>
  <div class="request-log-entry-context">
    {#each badges as badge (badge.key)}
      {#if badge.key === "session_id"}
        <button
          type="button"
          class="provider-badge request-log-session-usage-link mono"
          title={m.requests_view_session_usage()}
          onclick={() => usagePage.filterBySession(entry.session_id, entry.user_path)}
        >{badge.text}</button>
      {:else}
        <span class={["provider-badge", badge.class, { mono: badge.mono }]}
          >{badge.text}</span
        >
      {/if}
    {/each}
  </div>
</div>

<style>
  .request-log-entry-metadata {
    display: flex;
    align-items: center;
    gap: 10px;
    margin-top: 12px;
    padding-top: 12px;
    border-top: 1px solid var(--border);
  }

  .request-log-entry-metadata-label {
    flex: 0 0 auto;
    color: var(--text-muted);
    font-size: 12px;
    font-weight: 700;
    letter-spacing: 0.08em;
    text-transform: uppercase;
  }

  .request-log-entry-context {
    display: flex;
    flex: 1 1 auto;
    flex-wrap: wrap;
    gap: 8px;
  }

  .request-log-alias-badge {
    background: color-mix(in srgb, var(--accent) 14%, var(--bg));
    border-color: color-mix(in srgb, var(--accent) 28%, var(--border));
    color: var(--accent-strong, var(--accent));
  }

  /* Guardrail verdict badges; the class is picked in script, so the compound
     :global keeps the rule visible to the compiler at the base's specificity. */
  .provider-badge:global(.request-log-guardrail-badge-danger) {
    background: color-mix(in srgb, var(--danger) 12%, var(--bg));
    border-color: color-mix(in srgb, var(--danger) 40%, var(--border));
    color: var(--danger);
  }

  .provider-badge:global(.request-log-guardrail-badge-warning) {
    background: color-mix(in srgb, var(--warning) 12%, var(--bg));
    border-color: color-mix(in srgb, var(--warning) 40%, var(--border));
    color: var(--warning);
  }

  .request-log-session-usage-link {
    background: color-mix(in srgb, var(--accent) 12%, var(--bg));
    border-color: color-mix(in srgb, var(--accent) 35%, var(--border));
    color: var(--accent-strong, var(--accent));
    cursor: pointer;
  }

  .request-log-session-usage-link:hover {
    background: color-mix(in srgb, var(--accent) 22%, var(--bg));
    border-color: var(--accent);
  }

  @media (max-width: 768px) {
    .request-log-entry-metadata {
        flex-direction: column;
        align-items: flex-start;
        gap: 8px;
      }
  }
</style>
