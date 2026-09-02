<script>
  // API keys table.
  import TableActionButton from "$lib/components/atoms/TableActionButton.svelte";
  import Icon from "$lib/components/atoms/Icon.svelte";
  import { timezone } from "$lib/stores/timezone.svelte.js";
  import { formatDateUTC, formatTimestampUTC } from "$lib/utils/format.js";
  import { displayModelSelector } from "$lib/utils/modelSelectors.js";
  import { authKeyDeactivated, authKeyExpired, labelChipStyle } from "./authKeysLogic.js";
  import { authKeysStore as store } from "./authKeys.svelte.js";
  import { Boxes, Info, Pencil, Power, ShieldCheck, ShieldOff, TriangleAlert } from "lucide";
  import * as m from "$lib/paraglide/messages.js";
</script>

<div class="table-wrapper">
  <table class="data-table">
    <thead>
      <tr>
        <th>{m.api_keys_name()}</th>
        <th>{m.api_keys_column_description()}</th>
        <th>{m.api_keys_column_user_path()}</th>
        <th>{m.api_keys_column_labels()}</th>
        <th>{m.api_keys_column_allowed_models()}</th>
        <th>
          <span class="auth-key-th-help" title={m.api_keys_effective_models_help()}>
            {m.api_keys_column_effective_models()}
            <Icon icon={Info} width="13" height="13" />
          </span>
        </th>
        <th>{m.api_keys_column_token()}</th>
        <th>
          <span
            class="auth-key-th-help"
            title={m.api_keys_dashboard_access_help()}
          >
            {m.api_keys_dashboard_access()}
            <Icon icon={Info} width="13" height="13" />
          </span>
        </th>
        <th>{m.api_keys_expires()}</th>
        <th>{m.api_keys_column_created()}</th>
        <th class="col-actions" aria-label={m.api_keys_actions()}></th>
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
          <td>
            {#if (key.allowed_models || []).length > 0}
              <div class="auth-key-selector-list">
                {#each key.allowed_models || [] as selector (selector)}
                  <code class="auth-key-selector">{displayModelSelector(selector)}</code>
                {/each}
              </div>
            {:else}
              <span class="auth-key-unrestricted">{m.api_keys_allowed_models_all()}</span>
            {/if}
          </td>
          <td>
            {#if !key.restricted}
              <span class="auth-key-unrestricted">{m.api_keys_effective_all()}</span>
            {:else if !Array.isArray(key.effective_models)}
              <span class="auth-key-unrestricted">&mdash;</span>
            {:else if key.effective_models.length === 0}
              <span class="auth-key-effective-none">
                <Icon icon={TriangleAlert} class="table-icon-svg" />
                {m.api_keys_effective_none()}
              </span>
            {:else}
              <span class="auth-key-effective" title={key.effective_models.join("\n")}>
                {m.api_keys_effective_count({ count: key.effective_models.length })}
              </span>
            {/if}
          </td>
          <td><code class="auth-key-redacted">{key.redacted_value}</code></td>
          <td>
            <span
              class="auth-key-status-badge"
              class:auth-key-status-active={key.dashboard_access}
              class:auth-key-status-inactive={!key.dashboard_access}
            >{key.dashboard_access ? m.api_keys_allowed() : m.api_keys_denied()}</span>
          </td>
          <td title={key.expires_at ? formatTimestampUTC(key.expires_at) : ""}>
            {#if key.expires_at}
              <span class="auth-key-expiry">
                <span>{formatDateUTC(key.expires_at)}</span>
                {#if authKeyExpired(key)}
                  <span class="auth-key-status-badge auth-key-status-inactive">{m.api_keys_expired()}</span>
                {/if}
              </span>
            {:else}
              &mdash;
            {/if}
          </td>
          <td>{timezone.formatTimestamp(key.created_at)}</td>
          <td class="auth-key-actions-cell col-actions">
            <div class="auth-key-row-actions">
              {#if key.active}
                <TableActionButton
                  label={key.dashboard_access
                    ? m.api_keys_revoke_access_action({ name: key.name })
                    : m.api_keys_grant_access_action({ name: key.name })}
                  class="table-icon-btn"
                  onclick={() => store.toggleDashboardAccess(key)}
                  disabled={Boolean(store.dashboardAccessID)}
                >
                  <Icon icon={key.dashboard_access ? ShieldOff : ShieldCheck} class="table-icon-svg" />
                </TableActionButton>
                <TableActionButton
                  label={m.api_keys_edit_labels_action({ name: key.name })}
                  class="table-icon-btn"
                  onclick={() => store.openLabelsEditor(key)}
                >
                  <Icon icon={Pencil} class="table-icon-svg" />
                </TableActionButton>
                <TableActionButton
                  label={m.api_keys_edit_allowed_models_action({ name: key.name })}
                  class="table-icon-btn"
                  onclick={() => store.openAllowedModelsEditor(key)}
                >
                  <Icon icon={Boxes} class="table-icon-svg" />
                </TableActionButton>
                <TableActionButton
                  label={store.deactivatingID === key.id
                    ? m.api_keys_deactivating_action({ name: key.name })
                    : m.api_keys_deactivate_action({ name: key.name })}
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
                    ? m.api_keys_deactivated_on({ date: formatTimestampUTC(key.deactivated_at) })
                    : m.api_keys_deactivated()}
                >{m.api_keys_deactivated()}</span>
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

  .auth-key-selector-list {
    display: flex;
    flex-wrap: wrap;
    gap: 4px;
  }

  .auth-key-selector {
    font-size: 12px;
    padding: 2px 6px;
    border-radius: var(--radius);
    background: color-mix(in srgb, var(--accent) 10%, var(--bg));
    white-space: nowrap;
  }

  .auth-key-unrestricted {
    color: var(--text-muted);
    font-size: 13px;
  }

  .auth-key-effective {
    cursor: help;
    text-decoration: underline dotted;
    text-underline-offset: 3px;
  }

  .auth-key-effective-none {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    color: var(--danger, #c0392b);
    font-weight: 600;
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
