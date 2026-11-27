<script>
  // Users table: one row per tree node, indented by depth.
  import TableActionButton from "$lib/components/atoms/TableActionButton.svelte";
  import Icon from "$lib/components/atoms/Icon.svelte";
  import { displayModelSelector } from "$lib/utils/modelSelectors.js";
  import HighlightText from "$lib/components/atoms/HighlightText.svelte";
  import { authKeysStore } from "$pages/auth-keys/authKeys.svelte.js";
  import { usersStore as store } from "./users.svelte.js";
  import { userNodeKind, userPathDepth, userPathLeaf, userRequestPolicyItems } from "./usersLogic.js";
  import { CircleCheck, Copy, Folder, KeyRound, Pencil, Trash2, TriangleAlert, User } from "lucide";
  import * as m from "$lib/paraglide/messages.js";
</script>

<div class="table-wrapper">
  <table class="data-table">
    <thead>
      <tr>
        <th>{m.users_column_path()}</th>
        <th>{m.users_column_allowed_models()}</th>
        <th>{m.users_column_effective()}</th>
        <th>{m.users_column_request_policy()}</th>
        <th>{m.users_column_keys()}</th>
        <th>{m.users_column_description()}</th>
        <th class="col-actions" aria-label={m.users_actions()}></th>
      </tr>
    </thead>
    <tbody>
      {#each store.visibleNodes as node (node.user_path)}
        {@const kind = userNodeKind(node, store.nodes)}
        {@const copied = store.copiedPath === node.user_path}
        <tr class:user-row-implied={!node.configured}>
          <td>
            <span
              class="user-path-cell"
              style={`--user-depth: ${userPathDepth(node.user_path)}`}
              title={node.user_path}
            >
              <Icon
                icon={kind === "user" ? User : Folder}
                class="table-icon-svg user-kind-icon"
              />
              <span class="user-path-leaf"><HighlightText text={userPathLeaf(node.user_path)} needle={store.filter} /></span>
              {#if node.managed}
                <span class="auth-key-status-badge auth-key-status-inactive">{m.users_managed()}</span>
              {/if}
              <TableActionButton
                label={copied ? m.users_path_copied() : m.users_copy_path({ path: node.user_path })}
                class="table-icon-btn user-copy-btn"
                onclick={() => store.copyPath(node)}
              >
                <Icon icon={copied ? CircleCheck : Copy} class="table-icon-svg" />
              </TableActionButton>
            </span>
          </td>
          <td>
            {#if (node.allowed_models || []).length > 0}
              <div class="user-selector-list">
                {#each node.allowed_models as selector (selector)}
                  <code class="user-selector"><HighlightText text={displayModelSelector(selector)} needle={store.filter} /></code>
                {/each}
              </div>
            {:else}
              <span class="user-muted">{m.users_no_restriction()}</span>
            {/if}
          </td>
          <td>
            {#if !node.restricted}
              <span class="user-muted">{m.users_effective_all()}</span>
            {:else if !Array.isArray(node.effective_models)}
              <span class="user-muted">&mdash;</span>
            {:else if node.effective_models.length === 0}
              <span class="user-effective-none">
                <Icon icon={TriangleAlert} class="table-icon-svg" />
                {m.users_effective_none()}
              </span>
            {:else}
              <span class="user-effective" title={node.effective_models.join("\n")}>
                {m.users_effective_count({ count: node.effective_models.length })}
              </span>
            {/if}
            {#if (node.inherited_from || []).length > 0}
              <span class="user-effective-via" title={node.inherited_from.join("\n")}>
                {m.users_effective_via({ path: node.inherited_from[node.inherited_from.length - 1] })}
              </span>
            {/if}
          </td>
          <td>
            {#each userRequestPolicyItems(node) as item (item.key)}
              <span class="user-policy-item">
                {item.label}
                {#if item.from && item.from !== node.user_path}
                  <span class="user-effective-via">{m.users_effective_via({ path: item.from })}</span>
                {/if}
              </span>
            {:else}
              <span class="user-muted">{m.users_policy_none()}</span>
            {/each}
          </td>
          <td>
            {#if node.key_count > 0}
              <button
                type="button"
                class="user-keys-link"
                title={m.users_view_keys_action({ path: node.user_path })}
                onclick={() => authKeysStore.showForUserPath(node.user_path)}
              >{node.key_count}</button>
            {:else}
              0
            {/if}
          </td>
          <td class="user-description"><HighlightText text={node.description || "—"} needle={store.filter} /></td>
          <td class="user-actions-cell col-actions">
            <div class="user-row-actions">
              <TableActionButton
                label={m.users_add_key_action({ path: node.user_path })}
                class="table-icon-btn"
                onclick={() => authKeysStore.openFormForUserPath(node.user_path)}
              >
                <Icon icon={KeyRound} class="table-icon-svg" />
              </TableActionButton>
              {#if !node.managed}
                <TableActionButton
                  label={m.users_edit_action({ path: node.user_path })}
                  class="table-icon-btn"
                  onclick={() => store.openForm(node)}
                >
                  <Icon icon={Pencil} class="table-icon-svg" />
                </TableActionButton>
              {/if}
              {#if node.configured && !node.managed}
                <TableActionButton
                  label={m.users_delete_action({ path: node.user_path })}
                  class="table-action-btn-danger table-icon-btn"
                  onclick={() => store.deleteUser(node)}
                  disabled={store.deletingPath === node.user_path}
                >
                  <Icon icon={Trash2} class="table-icon-svg" />
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
  .user-path-cell {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    padding-left: calc(var(--user-depth, 0) * 18px);
    white-space: nowrap;
  }

  .user-path-cell :global(.user-kind-icon) {
    color: var(--text-muted);
    flex-shrink: 0;
  }

  .user-path-leaf {
    font-weight: 600;
  }

  /* The copy affordance stays quiet until the row is hovered or focused. */
  .user-path-cell :global(.user-copy-btn) {
    opacity: 0;
    transition: opacity 0.15s ease;
  }

  tr:hover .user-path-cell :global(.user-copy-btn),
  .user-path-cell :global(.user-copy-btn:focus-visible) {
    opacity: 1;
  }

  /* Implied nodes exist only because a key or a descendant refers to them. */
  .user-row-implied .user-path-leaf {
    font-weight: 500;
  }

  .user-selector-list {
    display: flex;
    flex-wrap: wrap;
    gap: 4px;
  }

  .user-selector {
    font-size: 12px;
    padding: 2px 6px;
    border-radius: var(--radius);
    background: color-mix(in srgb, var(--accent) 10%, var(--bg));
    white-space: nowrap;
  }

  .user-effective {
    cursor: help;
    text-decoration: underline dotted;
    text-underline-offset: 3px;
  }

  .user-effective-none {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    color: var(--danger, #c0392b);
    font-weight: 600;
  }

  .user-effective-via {
    display: block;
    color: var(--text-muted);
    font-size: 12px;
    cursor: help;
  }

  .user-policy-item {
    display: block;
    font-size: 13px;
    white-space: nowrap;
  }

  .user-muted {
    color: var(--text-muted);
    font-size: 13px;
  }

  .user-actions-cell {
    white-space: nowrap;
  }

  .user-keys-link {
    padding: 0;
    border: 0;
    background: none;
    color: var(--accent);
    font: inherit;
    cursor: pointer;
    text-decoration: underline;
    text-underline-offset: 3px;
  }

  .user-row-actions {
    display: inline-flex;
    align-items: center;
    gap: 6px;
  }
</style>
