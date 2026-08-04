<script>
  // Metadata badge strip under an expanded audit entry.
  import { providerDisplayValue, qualifiedResolvedModelDisplay } from "$lib/utils/format.js";
  import { workflowFailoverTarget } from "./audit-logic.js";

  let { entry } = $props();

  // Badges in render order. `mono` marks machine values; entries with a falsy
  // text are dropped, so optional fields simply disappear.
  const badges = $derived(
    [
      { key: "provider", text: providerDisplayValue(entry) || "-" },
      { key: "model", text: entry.requested_model || entry.model || "-", mono: true },
      { key: "user_path", text: entry.user_path, mono: true },
      { key: "request_id", text: "request_id: " + (entry.request_id || "-"), mono: true },
      { key: "ip", text: entry.client_ip && "ip: " + entry.client_ip, mono: true },
      {
        key: "auth_key_id",
        text: entry.auth_key_id && "auth_key_id: " + entry.auth_key_id,
        mono: true,
      },
      { key: "alias", text: entry.alias_used && "alias", class: "audit-alias-badge" },
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
      { key: "error_type", text: entry.error_type },
    ].filter((b) => !!b.text),
  );
</script>

<div class="audit-entry-metadata">
  <span class="audit-entry-metadata-label">Metadata:</span>
  <div class="audit-entry-context">
    {#each badges as badge (badge.key)}
      <span class={["provider-badge", badge.class, { mono: badge.mono }]}
        >{badge.text}</span
      >
    {/each}
  </div>
</div>

<style>
  .audit-entry-metadata {
    display: flex;
    align-items: center;
    gap: 10px;
    margin-top: 12px;
    padding-top: 12px;
    border-top: 1px solid var(--border);
  }

  .audit-entry-metadata-label {
    flex: 0 0 auto;
    color: var(--text-muted);
    font-size: 12px;
    font-weight: 700;
    letter-spacing: 0.08em;
    text-transform: uppercase;
  }

  .audit-entry-context {
    display: flex;
    flex: 1 1 auto;
    flex-wrap: wrap;
    gap: 8px;
  }

  .audit-alias-badge {
    background: color-mix(in srgb, var(--accent) 14%, var(--bg));
    border-color: color-mix(in srgb, var(--accent) 28%, var(--border));
    color: var(--accent-strong, var(--accent));
  }

  @media (max-width: 768px) {
    .audit-entry-metadata {
        flex-direction: column;
        align-items: flex-start;
        gap: 8px;
      }
  }
</style>
