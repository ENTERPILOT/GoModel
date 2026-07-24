<script>
  // API keys table.
  import TableActionButton from "$lib/components/atoms/TableActionButton.svelte";
  import Icon from "$lib/components/atoms/Icon.svelte";
  import { timezone } from "$lib/stores/timezone.svelte.js";
  import { formatDateUTC, formatTimestampUTC } from "$lib/utils/format.js";
  import { labelChipStyle } from "./authKeysLogic.js";
  import { authKeysStore as store } from "./authKeys.svelte.js";
</script>

<div class="table-wrapper">
  <table class="data-table">
    <thead>
      <tr>
        <th>Name</th>
        <th>Description</th>
        <th>User Path</th>
        <th>Labels</th>
        <th>Token</th>
        <th>Status</th>
        <th>Expires</th>
        <th>Created</th>
        <th aria-label="Actions"></th>
      </tr>
    </thead>
    <tbody>
      {#each store.keys as key (key.id)}
        <tr>
          <td>{key.name}</td>
          <td class="auth-key-description">{key.description || "—"}</td>
          <td>{key.user_path || "—"}</td>
          <td>
            {#if (key.labels || []).length > 0}
              <div class="usage-label-chips">
                {#each key.labels || [] as label (label)}
                  <span
                    class="usage-label-chip usage-label-chip-static"
                    style={labelChipStyle(label)}
                  >{label}</span>
                {/each}
              </div>
            {:else}
              <span>&mdash;</span>
            {/if}
          </td>
          <td><code class="auth-key-redacted">{key.redacted_value}</code></td>
          <td>
            <span
              class="auth-key-status-badge"
              class:auth-key-status-active={key.active}
              class:auth-key-status-inactive={!key.active}
            >{key.active ? "Active" : "Inactive"}</span>
          </td>
          <td title={key.expires_at ? formatTimestampUTC(key.expires_at) : ""}>
            {key.expires_at ? formatDateUTC(key.expires_at) : "—"}
          </td>
          <td>{timezone.formatTimestamp(key.created_at)}</td>
          <td class="auth-key-actions-cell">
            <div class="auth-key-row-actions">
              {#if key.active}
                <TableActionButton
                  label={"Edit labels for API key " + key.name}
                  class="table-icon-btn"
                  onclick={() => store.openLabelsEditor(key)}
                >
                  <Icon name="pencil" class="table-icon-svg" />
                </TableActionButton>
                <TableActionButton
                  label={(store.deactivatingID === key.id ? "Deactivating API key " : "Deactivate API key ") + key.name}
                  class="table-action-btn-danger table-icon-btn"
                  onclick={() => store.deactivateKey(key)}
                  disabled={store.deactivatingID === key.id}
                >
                  <Icon name="power" class="table-icon-svg" />
                </TableActionButton>
              {/if}
            </div>
          </td>
        </tr>
      {/each}
    </tbody>
  </table>
</div>

<style>
  /* Styles owned by this component (moved from dashboard.css). */
  /* Read-only chip variant (e.g. API key labels) — same look, no affordance. */
  .usage-label-chip-static, .usage-label-chip-static:hover {
    cursor: default;
    background: color-mix(in srgb, var(--label-color, var(--accent)) 14%, var(--bg));
  }

  .auth-key-redacted {
    color: var(--text-muted);
    font-size: 13px;
  }

  .auth-key-actions-cell {
    white-space: nowrap;
  }

  .auth-key-row-actions {
    display: inline-flex;
    align-items: center;
    gap: 6px;
  }
</style>
