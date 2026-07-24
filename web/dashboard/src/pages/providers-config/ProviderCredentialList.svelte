<script>
  // Provider credential table. Managed rows (declared in config.yaml or env
  // vars) show a Config badge and expose no edit/delete actions.
  import TableActionButton from "$lib/components/atoms/TableActionButton.svelte";
  import Icon from "$lib/components/atoms/Icon.svelte";
  import { timezone } from "$lib/stores/timezone.svelte.js";
  import { providersConfig } from "./providersConfig.svelte.js";
  import {
    providerCredentialAuthLabel,
    providerCredentialModelsLabel,
  } from "./providersConfigLogic.js";
</script>

<div class="table-wrapper">
  <table class="data-table">
    <thead>
      <tr>
        <th>Name</th>
        <th>Type</th>
        <th>Base URL</th>
        <th>Auth</th>
        <th>Models</th>
        <th>Enabled</th>
        <th>Updated</th>
        <th class="col-actions">Actions</th>
      </tr>
    </thead>
    <tbody>
      {#each providersConfig.filteredRows as row (row.name)}
        <tr>
          <td>
            <span class="font-size-md">{row.name}</span>
            {#if row.managed}
              <span
                class="alias-kind-badge"
                title="Declared in configuration (config.yaml or env vars); read-only in the dashboard"
                >Config</span>
            {/if}
          </td>
          <td><span class="budget-source mono">{row.type}</span></td>
          <td class="mono font-size-md" title={row.base_url || ""}>{row.base_url || "—"}</td>
          <td>{providerCredentialAuthLabel(row)}</td>
          <td>{providerCredentialModelsLabel(row)}</td>
          <td>
            <span
              class="auth-key-status-badge"
              class:auth-key-status-active={row.enabled}
              class:auth-key-status-inactive={!row.enabled}
              >{row.enabled ? "Enabled" : "Disabled"}</span>
          </td>
          <td>{timezone.formatTimestamp(row.updated_at)}</td>
          <td class="col-actions">
            <div class="alias-actions-cell">
              {#if !row.managed}
                <TableActionButton
                  label={"Edit provider " + row.name}
                  class="table-icon-btn"
                  onclick={() => providersConfig.openEdit(row)}
                >
                  <Icon name="pencil" class="table-icon-svg" />
                </TableActionButton>
                <TableActionButton
                  label={(providersConfig.deletingName === row.name ? "Deleting provider " : "Delete provider ") + row.name}
                  class="table-action-btn-danger table-icon-btn"
                  onclick={() => providersConfig.requestDelete(row.name)}
                  disabled={providersConfig.deletingName === row.name}
                >
                  <Icon name="x" class="table-icon-svg" />
                </TableActionButton>
              {/if}
            </div>
          </td>
        </tr>
      {/each}
    </tbody>
  </table>
</div>
