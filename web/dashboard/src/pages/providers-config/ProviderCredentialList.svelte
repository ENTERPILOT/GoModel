<script>
  import * as m from "$lib/paraglide/messages.js";
  // Provider credential table. Managed rows (declared in config.yaml or env
  // vars) show a Config badge and expose no edit/delete actions.
  import TableActionButton from "$lib/components/atoms/TableActionButton.svelte";
  import Icon from "$lib/components/atoms/Icon.svelte";
  import { timezone } from "$lib/stores/timezone.svelte.js";
  import { providersConfig } from "./providersConfig.svelte.js";
  import {
    providerCredentialAuthLabel,
    providerCredentialModelsLabel,
    providerRowsHaveActions,
  } from "./providersConfigLogic.js";
  import { Pencil, X } from "lucide";

  const showActions = $derived(providerRowsHaveActions(providersConfig.filteredRows));
</script>

<div class="table-wrapper">
  <table class="data-table">
    <thead>
      <tr>
        <th>{m.providers_name()}</th>
        <th>{m.providers_type()}</th>
        <th>{m.overview_base_url()}</th>
        <th>{m.providers_auth()}</th>
        <th>{m.providers_models()}</th>
        <th>{m.providers_enabled()}</th>
        <th>{m.providers_updated()}</th>
        {#if showActions}
          <th class="col-actions">{m.providers_actions()}</th>
        {/if}
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
                title={m.providers_managed()}
                >{m.common_config()}</span>
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
              >{row.enabled ? m.common_enabled() : m.common_disabled()}</span>
          </td>
          <td>{timezone.formatTimestamp(row.updated_at)}</td>
          {#if showActions}
            <td class="col-actions">
              <div class="alias-actions-cell model-list-actions">
                {#if !row.managed}
                  <TableActionButton
                    label={m.providers_edit_action({ name: row.name })}
                    class="table-icon-btn"
                    onclick={() => providersConfig.openEdit(row)}
                  >
                    <Icon icon={Pencil} class="table-icon-svg" />
                  </TableActionButton>
                  <TableActionButton
                    label={providersConfig.deletingName === row.name
                      ? m.providers_deleting_action({ name: row.name })
                      : m.providers_delete_action({ name: row.name })}
                    class="table-action-btn-danger table-icon-btn"
                    onclick={() => providersConfig.requestDelete(row.name)}
                    disabled={providersConfig.deletingName === row.name}
                  >
                    <Icon icon={X} class="table-icon-svg" />
                  </TableActionButton>
                {/if}
              </div>
            </td>
          {/if}
        </tr>
      {/each}
    </tbody>
  </table>
</div>
