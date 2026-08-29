<script>
  // Registry tree table: the group hierarchy with each group's member users
  // beneath it, indented by depth. Every row's derived user path is shown
  // alongside the name.
  import TableActionButton from "$lib/components/atoms/TableActionButton.svelte";
  import Icon from "$lib/components/atoms/Icon.svelte";
  import { labelChipStyle } from "$lib/utils/chartTheme.js";
  import { usersStore as store } from "./users.svelte.js";
  import { CornerDownRight, KeyRound, Pencil, ShieldCheck, Trash2, UserPlus, UsersRound } from "lucide";
  import * as m from "$lib/paraglide/messages.js";
</script>

<div class="table-wrapper">
  <table class="data-table">
    <thead>
      <tr>
        <th>{m.users_column_entry()}</th>
        <th>{m.users_column_members()}</th>
        <th>{m.users_column_keys()}</th>
        <th aria-label={m.users_actions()}></th>
      </tr>
    </thead>
    <tbody>
      {#each store.treeRows as row (row.kind + row.path)}
        <tr class:users-row-group={row.kind === "group"}>
          <td>
            <div class="users-path-cell" style={`--depth: ${row.depth}`}>
              {#if row.depth > 0}
                <Icon icon={CornerDownRight} class="users-branch-icon" width="14" height="14" />
              {/if}
              <div class="users-path-text">
                {#if row.kind === "group"}
                  <span class="usage-label-chip usage-label-chip-static" style={labelChipStyle(row.group.name)}>
                    <Icon icon={UsersRound} width="12" height="12" />
                    {row.group.name}
                  </span>
                  <code class="users-path">{row.path}</code>
                {:else}
                  <span class="users-name">{row.user.name}</span>
                  <code class="users-path">{row.path}</code>
                {/if}
              </div>
            </div>
          </td>
          <td>
            {#if row.kind === "group"}
              {store.memberCounts.get(row.group.name) || 0}
            {:else}
              <span>&mdash;</span>
            {/if}
          </td>
          <td>
            {#if row.kind === "user" && store.keyCounts.get(row.path)}
              <span class="users-key-count">
                <Icon icon={KeyRound} width="13" height="13" />
                {store.keyCounts.get(row.path)}
              </span>
            {:else}
              <span>&mdash;</span>
            {/if}
          </td>
          <td class="users-actions-cell">
            {#if row.kind === "group"}
              <TableActionButton
                label={m.users_add_member()}
                onclick={() => store.openUserEditorForGroup(row.group.name)}
              >
                <Icon icon={UserPlus} class="table-icon-svg" />
              </TableActionButton>
              <TableActionButton
                label={m.users_grant_access()}
                onclick={() =>
                  store.openAccessEditor({
                    kind: "group",
                    name: row.group.name,
                    path: row.path,
                    label: row.group.name,
                  })}
              >
                <Icon icon={ShieldCheck} class="table-icon-svg" />
              </TableActionButton>
              <TableActionButton label={m.groups_edit()} onclick={() => store.openGroupEditor(row.group)}>
                <Icon icon={Pencil} class="table-icon-svg" />
              </TableActionButton>
              <TableActionButton
                label={m.groups_delete()}
                disabled={store.deletingGroup === row.group.name}
                onclick={() => store.deleteGroup(row.group)}
              >
                <Icon icon={Trash2} class="table-icon-svg" />
              </TableActionButton>
            {:else}
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

  .users-path-text :global(.usage-label-chip) {
    display: inline-flex;
    align-items: center;
    gap: 4px;
  }

  .users-name {
    font-weight: 600;
  }

  .users-path {
    font-size: 12px;
    color: var(--text-secondary);
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
