<script>
  // Users tree table: registered users indented by user-path depth, with
  // plain segment rows for unregistered intermediate paths.
  import TableActionButton from "$lib/components/atoms/TableActionButton.svelte";
  import Icon from "$lib/components/atoms/Icon.svelte";
  import { labelChipStyle } from "$lib/utils/chartTheme.js";
  import { usersStore as store } from "./users.svelte.js";
  import { CornerDownRight, KeyRound, Pencil, ShieldCheck, Trash2, UserPlus } from "lucide";
  import * as m from "$lib/paraglide/messages.js";
</script>

<div class="table-wrapper">
  <table class="data-table">
    <thead>
      <tr>
        <th>{m.users_column_user()}</th>
        <th>{m.users_column_groups()}</th>
        <th>{m.users_column_keys()}</th>
        <th aria-label={m.users_actions()}></th>
      </tr>
    </thead>
    <tbody>
      {#each store.treeRows as row (row.path)}
        <tr class:users-row-segment={!row.user}>
          <td>
            <div class="users-path-cell" style={`--depth: ${row.depth}`}>
              {#if row.depth > 0}
                <Icon icon={CornerDownRight} class="users-branch-icon" width="14" height="14" />
              {/if}
              <div class="users-path-text">
                {#if row.user}
                  <span class="users-name">{row.user.name || row.segment}</span>
                  <code class="users-path">{row.path}</code>
                {:else}
                  <code class="users-path users-path-unregistered">{row.path}</code>
                {/if}
              </div>
            </div>
          </td>
          <td>
            {#if row.user && (row.user.groups || []).length > 0}
              <div class="usage-label-chips">
                {#each row.user.groups as group (group)}
                  <span class="usage-label-chip usage-label-chip-static" style={labelChipStyle(group)}>{group}</span>
                {/each}
              </div>
            {:else}
              <span>&mdash;</span>
            {/if}
          </td>
          <td>
            {#if store.keyCounts.get(row.path)}
              <span class="users-key-count">
                <Icon icon={KeyRound} width="13" height="13" />
                {store.keyCounts.get(row.path)}
              </span>
            {:else}
              <span>&mdash;</span>
            {/if}
          </td>
          <td class="users-actions-cell">
            {#if row.user}
              <TableActionButton
                label={m.users_grant_access()}
                onclick={() =>
                  store.openAccessEditor({
                    kind: "user",
                    path: row.path,
                    label: row.user.name || row.path,
                  })}
              >
                <Icon icon={ShieldCheck} class="table-icon-svg" />
              </TableActionButton>
              <TableActionButton label={m.users_edit()} onclick={() => store.openUserEditor(row.user)}>
                <Icon icon={Pencil} class="table-icon-svg" />
              </TableActionButton>
              <TableActionButton
                label={m.users_delete()}
                disabled={store.deletingID === row.user.id}
                onclick={() => store.deleteUser(row.user)}
              >
                <Icon icon={Trash2} class="table-icon-svg" />
              </TableActionButton>
            {:else}
              <TableActionButton
                label={m.users_register_path()}
                onclick={() => store.openUserEditorForPath(row.path)}
              >
                <Icon icon={UserPlus} class="table-icon-svg" />
              </TableActionButton>
            {/if}
          </td>
        </tr>
      {/each}
    </tbody>
  </table>
</div>

<style>
  .users-path-cell {
    display: flex;
    align-items: center;
    gap: 6px;
    padding-left: calc(var(--depth, 0) * 20px);
  }

  .users-path-cell :global(.users-branch-icon) {
    color: var(--text-secondary);
    flex-shrink: 0;
  }

  .users-path-text {
    display: flex;
    align-items: baseline;
    gap: 8px;
    flex-wrap: wrap;
  }

  .users-name {
    font-weight: 600;
  }

  .users-path {
    font-size: 12px;
    color: var(--text-secondary);
  }

  .users-row-segment td {
    color: var(--text-secondary);
  }

  .users-path-unregistered {
    font-style: italic;
  }

  .users-key-count {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    font-variant-numeric: tabular-nums;
  }

  .users-actions-cell {
    text-align: right;
    white-space: nowrap;
  }
</style>
