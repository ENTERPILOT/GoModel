<script>
  // Groups table: name, description, direct member count, and actions.
  import TableActionButton from "$lib/components/atoms/TableActionButton.svelte";
  import Icon from "$lib/components/atoms/Icon.svelte";
  import { labelChipStyle } from "$lib/utils/chartTheme.js";
  import { usersStore as store } from "./users.svelte.js";
  import { Pencil, ShieldCheck, Trash2 } from "lucide";
  import * as m from "$lib/paraglide/messages.js";
</script>

<div class="table-wrapper">
  <table class="data-table">
    <thead>
      <tr>
        <th>{m.groups_column_name()}</th>
        <th>{m.groups_column_description()}</th>
        <th>{m.groups_column_members()}</th>
        <th aria-label={m.users_actions()}></th>
      </tr>
    </thead>
    <tbody>
      {#each store.groups as group (group.name)}
        <tr>
          <td>
            <span class="usage-label-chip usage-label-chip-static" style={labelChipStyle(group.name)}>{group.name}</span>
          </td>
          <td class="groups-description">{group.description || "—"}</td>
          <td>{store.memberCounts.get(group.name) || 0}</td>
          <td class="groups-actions-cell">
            <TableActionButton
              label={m.users_grant_access()}
              onclick={() => store.openAccessEditor({ kind: "group", name: group.name, label: group.name })}
            >
              <Icon icon={ShieldCheck} class="table-icon-svg" />
            </TableActionButton>
            <TableActionButton label={m.groups_edit()} onclick={() => store.openGroupEditor(group)}>
              <Icon icon={Pencil} class="table-icon-svg" />
            </TableActionButton>
            <TableActionButton
              label={m.groups_delete()}
              disabled={store.deletingGroup === group.name}
              onclick={() => store.deleteGroup(group)}
            >
              <Icon icon={Trash2} class="table-icon-svg" />
            </TableActionButton>
          </td>
        </tr>
      {/each}
    </tbody>
  </table>
</div>

<style>
  .groups-description {
    color: var(--text-secondary);
  }

  .groups-actions-cell {
    text-align: right;
    white-space: nowrap;
  }
</style>
