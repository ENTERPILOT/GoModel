<script>
  // API keys table.
  import TableActionButton from "$lib/components/atoms/TableActionButton.svelte";
  import Icon from "$lib/components/atoms/Icon.svelte";
  import { timezone } from "$lib/stores/timezone.svelte.js";
  import { formatDateUTC, formatTimestampUTC } from "$lib/utils/format.js";
  import { authKeyDeactivated, authKeyExpired, labelChipStyle } from "./authKeysLogic.js";
  import { authKeysStore as store } from "./authKeys.svelte.js";
  import { Info, Pencil, Power, ShieldCheck, ShieldOff } from "lucide";
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
        <th>
          <span
            class="auth-key-th-help"
            title="Keys without dashboard access are denied the dashboard and every /admin API endpoint. Model endpoints and GET /v1/usage stay available to all keys."
          >
            Dashboard Access
            <Icon icon={Info} width="13" height="13" />
          </span>
        </th>
        <th>Expires</th>
        <th>Created</th>
        <th aria-label="Actions"></th>
      </tr>
    </thead>
    <tbody>
      {#each store.visibleKeys as key (key.id)}
        <tr class:auth-key-row-deactivated={authKeyDeactivated(key)}>
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
              class:auth-key-status-active={key.dashboard_access}
              class:auth-key-status-inactive={!key.dashboard_access}
            >{key.dashboard_access ? "Allowed" : "Denied"}</span>
          </td>
          <td title={key.expires_at ? formatTimestampUTC(key.expires_at) : ""}>
            {#if key.expires_at}
              <span class="auth-key-expiry">
                <span>{formatDateUTC(key.expires_at)}</span>
                {#if authKeyExpired(key)}
                  <span class="auth-key-status-badge auth-key-status-inactive">Expired</span>
                {/if}
              </span>
            {:else}
              &mdash;
            {/if}
          </td>
          <td>{timezone.formatTimestamp(key.created_at)}</td>
          <td class="auth-key-actions-cell">
            <div class="auth-key-row-actions">
              {#if key.active}
                <TableActionButton
                  label={(key.dashboard_access ? "Revoke dashboard access for API key " : "Grant dashboard access to API key ") + key.name}
                  class="table-icon-btn"
                  onclick={() => store.toggleDashboardAccess(key)}
                  disabled={Boolean(store.dashboardAccessID)}
                >
                  <Icon icon={key.dashboard_access ? ShieldOff : ShieldCheck} class="table-icon-svg" />
                </TableActionButton>
                <TableActionButton
                  label={"Edit labels for API key " + key.name}
                  class="table-icon-btn"
                  onclick={() => store.openLabelsEditor(key)}
                >
                  <Icon icon={Pencil} class="table-icon-svg" />
                </TableActionButton>
                <TableActionButton
                  label={(store.deactivatingID === key.id ? "Deactivating API key " : "Deactivate API key ") + key.name}
                  class="table-action-btn-danger table-icon-btn"
                  onclick={() => store.deactivateKey(key)}
                  disabled={store.deactivatingID === key.id}
                >
                  <Icon icon={Power} class="table-icon-svg" />
                </TableActionButton>
              {:else if authKeyDeactivated(key)}
                <span
                  class="auth-key-status-badge auth-key-status-inactive"
                  title={key.deactivated_at
                    ? "Deactivated on " + formatTimestampUTC(key.deactivated_at)
                    : "Deactivated"}
                >Deactivated</span>
              {/if}
            </div>
          </td>
        </tr>
      {/each}
    </tbody>
  </table>
</div>

<style>
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

  /* Deactivated keys are kept for the record only — recede them, but leave
     the actions cell at full strength so its "Deactivated" pill stays legible. */
  .auth-key-row-deactivated td:not(.auth-key-actions-cell) {
    opacity: 0.55;
  }

  .auth-key-expiry {
    display: inline-flex;
    align-items: center;
    gap: 8px;
    white-space: nowrap;
  }

  .auth-key-th-help {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    cursor: help;
  }

  .auth-key-row-actions {
    display: inline-flex;
    align-items: center;
    gap: 6px;
  }
</style>
